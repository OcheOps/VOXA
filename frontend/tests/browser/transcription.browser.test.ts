import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { Subprocess } from 'bun';
import { HttpMediaClient } from '../../src/clients/http/media-client';
import { HttpTranscriptionClient } from '../../src/clients/http/transcription-client';
import { HttpTransport } from '../../src/clients/http/transport';
import { chromeBinary, launchChrome, type ChromePage } from './cdp';

/*
 * Opt-in end to end: real Voxa server (development auth, filesystem media,
 * in-process workers), real ffmpeg remux, real whisper.cpp, and the
 * production Voxa Web media/transcript view in headless Chrome.
 *
 *   VOXA_BROWSER_E2E_SERVER_BIN=/path/to/voxa \
 *   VOXA_E2E_WHISPER_BIN=/path/to/whisper-cli VOXA_E2E_WHISPER_MODEL=/path/to/ggml-tiny.en.bin \
 *   VOXA_E2E_SPEECH=/path/to/jfk.wav [VOXA_E2E_WORK_DIR=dir-visible-to-the-whisper-binary] \
 *   bun test tests/browser/transcription.browser.test.ts
 */
const SERVER_BIN = process.env.VOXA_BROWSER_E2E_SERVER_BIN;
const WHISPER_BIN = process.env.VOXA_E2E_WHISPER_BIN;
const WHISPER_MODEL = process.env.VOXA_E2E_WHISPER_MODEL;
const SPEECH = process.env.VOXA_E2E_SPEECH;
const enabled = Boolean(SERVER_BIN && WHISPER_BIN && WHISPER_MODEL && SPEECH && chromeBinary() && Bun.which('ffmpeg') && Bun.which('ffprobe'));

let pagesServer: ReturnType<typeof Bun.serve>;
let voxa: Subprocess | undefined;
let chrome: ChromePage;
let apiUrl = '';
const workRoot = process.env.VOXA_E2E_WORK_DIR ?? tmpdir();
mkdirSync(workRoot, { recursive: true });
const temp = mkdtempSync(join(workRoot, 'voxa-transcription-e2e-'));

async function waitHttp(url: string) {
  for (let i = 0; i < 100; i++) {
    try { if ((await fetch(url)).ok) return; } catch { /* starting */ }
    await Bun.sleep(100);
  }
  throw new Error(`timeout waiting for ${url}`);
}

const api = (path: string, init?: RequestInit) => fetch(`${apiUrl}/api/v1${path}`, init);

/** A browser-like WebM/Opus stream: written non-seekably, so it has no duration header or cues. */
function streamedWebm(): Uint8Array<ArrayBuffer> {
  const result = Bun.spawnSync(['ffmpeg', '-v', 'error', '-i', SPEECH!, '-c:a', 'libopus', '-b:a', '32k', '-f', 'webm', 'pipe:1']);
  if (result.exitCode !== 0) throw new Error(result.stderr.toString());
  return new Uint8Array(result.stdout.buffer.slice(result.stdout.byteOffset, result.stdout.byteOffset + result.stdout.byteLength) as ArrayBuffer);
}

async function waitFor<T>(what: string, timeoutMs: number, poll: () => Promise<T | null>): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = await poll();
    if (value) return value;
    await Bun.sleep(250);
  }
  throw new Error(`timed out waiting for ${what}`);
}

describe.skipIf(!enabled)('Voxa transcription end to end', () => {
  beforeAll(async () => {
    const outfile = join(temp, 'harness.js');
    const build = Bun.spawnSync([process.execPath, 'build', join(import.meta.dir, 'transcript-harness-entry.tsx'), '--target', 'browser', '--outfile', outfile], { cwd: join(import.meta.dir, '../..') });
    if (build.exitCode !== 0) throw new Error(build.stderr.toString());
    const script = await Bun.file(outfile).text();
    pagesServer = Bun.serve({
      port: 0,
      fetch: (request) => new URL(request.url).pathname === '/harness.js'
        ? new Response(script, { headers: { 'Content-Type': 'text/javascript' } })
        : new Response('<!doctype html><title>Voxa transcript harness</title><script src="/harness.js"></script>', { headers: { 'Content-Type': 'text/html' } }),
    });
    const port = 21000 + Math.floor(Math.random() * 20000);
    apiUrl = `http://127.0.0.1:${port}`;
    voxa = Bun.spawn([SERVER_BIN!], {
      env: {
        PATH: process.env.PATH ?? '', HOME: process.env.HOME ?? '', VOXA_AUTH_MODE: 'development', VOXA_LISTEN_ADDR: `127.0.0.1:${port}`,
        VOXA_ALLOWED_ORIGINS: `http://localhost:${pagesServer.port}`, VOXA_MEDIA_DIR: join(temp, 'media'),
        VOXA_WHISPER_BIN: WHISPER_BIN!, VOXA_WHISPER_MODEL: WHISPER_MODEL!,
      },
      stdout: 'ignore', stderr: 'pipe',
    });
    await waitHttp(`${apiUrl}/readyz`);
    chrome = await launchChrome(join(temp, 'chrome'));
  }, 120_000);

  afterAll(() => {
    chrome?.close();
    voxa?.kill();
    pagesServer?.stop(true);
    rmSync(temp, { recursive: true, force: true });
  });

  test('upload → remux → whisper transcript → API, HTTP client and web view with click-to-seek', async () => {
    const capabilities = await (await api('/capabilities')).json();
    expect(capabilities.transcription).toEqual({ available: true, providers: ['whisper.cpp'] });
    expect(capabilities.summary.available).toBe(false);

    const meetingId = (await (await api('/meetings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title: 'Transcription E2E' }) })).json()).meeting.id as string;
    const transport = new HttpTransport({ baseUrl: apiUrl, getAccessToken: () => null });
    const media = new HttpMediaClient(transport, apiUrl);
    const target = await media.createRecording(meetingId, 'audio/webm;codecs=opus');
    const assetId = target.asset.id;
    await media.uploadChunk(assetId, 0, new Blob([streamedWebm()]));
    expect((await media.finalize(assetId, 1, null)).status).toBe('ready');

    // Worker remuxes (duration becomes known) and then transcribes.
    const asset = await waitFor('remux', 60_000, async () => {
      const found = (await media.listMeetingMedia(meetingId)).find((item) => item.id === assetId);
      return found?.durationSeconds ? found : null;
    });
    expect(asset.durationSeconds!).toBeGreaterThan(10);
    expect(asset.durationSeconds!).toBeLessThan(12);

    const client = new HttpTranscriptionClient(transport);
    const result = await waitFor('transcript', 180_000, async () => {
      const current = await client.getRecordingTranscript(assetId);
      if (current.state === 'failed') throw new Error(`transcription failed: ${JSON.stringify(current.error)}`);
      return current.state === 'ready' ? current : null;
    });
    const text = result.transcript!.segments.map((segment) => segment.text).join(' ').toLowerCase();
    expect(text).toContain('fellow americans');
    expect(text).toContain('your country');
    expect(result.transcript!.asrEngine).toBe('whisper.cpp');
    for (const segment of result.transcript!.segments) {
      expect(segment.startSeconds).toBeGreaterThanOrEqual(0);
      expect(segment.endSeconds).toBeLessThanOrEqual(asset.durationSeconds! + 0.001);
    }
    const raw = await (await api(`/media/${assetId}/transcript`)).json();
    expect(raw.transcript.segments[0].timingProvenance).toBe('asr');
    expect(raw.transcript.segments[0].confidence).toBeNull();
    const jobs = (await (await api(`/meetings/${meetingId}/jobs`)).json()).items as Array<{ kind: string; status: string }>;
    expect(jobs.map((job) => `${job.kind}:${job.status}`)).toEqual(['remux:completed', 'transcription:completed']);

    // The production web view renders the transcript; clicking a segment seeks the player.
    await chrome.send('Page.navigate', { url: `http://localhost:${pagesServer.port}/` });
    await waitFor('harness', 10_000, () => chrome.evaluate<boolean>('typeof window.harness === "object"').catch(() => false));
    await chrome.evaluate(`harness.render(${JSON.stringify(apiUrl)}, ${JSON.stringify(meetingId)})`);
    const rendered = await waitFor('rendered segments', 15_000, async () => {
      const segments = await chrome.evaluate<string[]>('harness.segments()');
      return segments.length === result.transcript!.segments.length ? segments : null;
    });
    expect(rendered.join(' ').toLowerCase()).toContain('fellow americans');
    const last = result.transcript!.segments.length - 1;
    const seekTo = result.transcript!.segments[last].startSeconds;
    const seeked = await chrome.evaluate<{ currentTime: number; duration: number; error: number | null }>(`harness.clickSegment(${last})`);
    expect(seeked.error).toBeNull();
    expect(seeked.currentTime).toBeGreaterThanOrEqual(seekTo - 0.05);
    expect(seeked.currentTime).toBeLessThan(seekTo + 1.5);
    expect(seeked.duration).toBeCloseTo(asset.durationSeconds!, 0);
  }, 300_000);
});
