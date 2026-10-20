import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { Subprocess } from 'bun';
import { HttpMediaClient } from '../../src/clients/http/media-client';
import { HttpSummaryClient } from '../../src/clients/http/summary-client';
import { HttpTranscriptionClient } from '../../src/clients/http/transcription-client';
import { HttpTransport } from '../../src/clients/http/transport';
import { chromeBinary, launchChrome, type ChromePage } from './cdp';

/*
 * Opt-in end to end: record → transcript → summary. Real Voxa server
 * (development auth, filesystem media, in-process workers), real ffmpeg and
 * whisper.cpp, a fake local OpenAI-compatible summary provider (no paid
 * API), and the production Voxa Web transcript and summary views in
 * headless Chrome.
 *
 *   VOXA_BROWSER_E2E_SERVER_BIN=/path/to/voxa \
 *   VOXA_E2E_WHISPER_BIN=/path/to/whisper-cli VOXA_E2E_WHISPER_MODEL=/path/to/ggml-tiny.en.bin \
 *   VOXA_E2E_SPEECH=/path/to/jfk.wav [VOXA_E2E_WORK_DIR=dir-visible-to-the-whisper-binary] \
 *   bun test tests/browser/summary.browser.test.ts
 */
const SERVER_BIN = process.env.VOXA_BROWSER_E2E_SERVER_BIN;
const WHISPER_BIN = process.env.VOXA_E2E_WHISPER_BIN;
const WHISPER_MODEL = process.env.VOXA_E2E_WHISPER_MODEL;
const SPEECH = process.env.VOXA_E2E_SPEECH;
const enabled = Boolean(SERVER_BIN && WHISPER_BIN && WHISPER_MODEL && SPEECH && chromeBinary() && Bun.which('ffmpeg') && Bun.which('ffprobe'));
const SECRET = 'sk-e2e-summary-SECRET-6f1d2c9a8b7e';

let pagesServer: ReturnType<typeof Bun.serve>;
let llm: ReturnType<typeof Bun.serve>;
let voxa: Subprocess<'ignore', 'pipe', 'pipe'> | undefined;
let chrome: ChromePage;
let apiUrl = '';
let serverLogs = '';
const llmRequests: Array<{ authorization: string | null; system: string; user: string }> = [];
const workRoot = process.env.VOXA_E2E_WORK_DIR ?? tmpdir();
mkdirSync(workRoot, { recursive: true });
const temp = mkdtempSync(join(workRoot, 'voxa-summary-e2e-'));

async function waitHttp(url: string) {
  for (let i = 0; i < 100; i++) {
    try { if ((await fetch(url)).ok) return; } catch { /* starting */ }
    await Bun.sleep(100);
  }
  throw new Error(`timeout waiting for ${url}`);
}

async function waitFor<T>(what: string, timeoutMs: number, poll: () => Promise<T | null | false>): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const value = await poll();
    if (value) return value;
    await Bun.sleep(250);
  }
  throw new Error(`timed out waiting for ${what}`);
}

async function collect(stream: ReadableStream<Uint8Array>) {
  const decoder = new TextDecoder();
  const reader = stream.getReader();
  for (;;) {
    const { done, value } = await reader.read();
    if (done) return;
    serverLogs += decoder.decode(value, { stream: true });
  }
}

const api = (path: string, init?: RequestInit) => fetch(`${apiUrl}/api/v1${path}`, init);
const jsonInit = (method: string, body: unknown): RequestInit => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });

function streamedWebm(): Uint8Array<ArrayBuffer> {
  const result = Bun.spawnSync(['ffmpeg', '-v', 'error', '-i', SPEECH!, '-c:a', 'libopus', '-b:a', '32k', '-f', 'webm', 'pipe:1']);
  if (result.exitCode !== 0) throw new Error(result.stderr.toString());
  return new Uint8Array(result.stdout.buffer.slice(result.stdout.byteOffset, result.stdout.byteOffset + result.stdout.byteLength) as ArrayBuffer);
}

describe.skipIf(!enabled)('Voxa record → transcript → summary end to end', () => {
  beforeAll(async () => {
    // Fake OpenAI-compatible provider: requires the workspace key, answers
    // with a template-shaped report that echoes whether it saw the speech.
    let generation = 0;
    llm = Bun.serve({
      port: 0,
      async fetch(request) {
        const body = await request.json() as { messages: Array<{ role: string; content: string }> };
        const system = body.messages.find((m) => m.role === 'system')?.content ?? '';
        const user = body.messages.find((m) => m.role === 'user')?.content ?? '';
        llmRequests.push({ authorization: request.headers.get('authorization'), system, user });
        if (request.headers.get('authorization') !== `Bearer ${SECRET}`) return Response.json({ error: 'bad key' }, { status: 401 });
        generation += 1;
        const heard = user.toLowerCase().includes('fellow americans') ? 'The speaker addressed fellow Americans.' : 'Nothing recognized.';
        return Response.json({
          choices: [{ message: { content: `# Inaugural address v${generation}\n\n**Summary**\n\n${heard}\n\n**Action Items**\n\n| **Owner** | Task |\n| --- | --- |\n| Citizens | Ask what you can do |` } }],
          usage: { prompt_tokens: 200, completion_tokens: 40 },
        });
      },
    });
    const outfile = join(temp, 'harness.js');
    const build = Bun.spawnSync([process.execPath, 'build', join(import.meta.dir, 'transcript-harness-entry.tsx'), '--target', 'browser', '--outfile', outfile], { cwd: join(import.meta.dir, '../..') });
    if (build.exitCode !== 0) throw new Error(build.stderr.toString());
    const script = await Bun.file(outfile).text();
    pagesServer = Bun.serve({
      port: 0,
      fetch: (request) => new URL(request.url).pathname === '/harness.js'
        ? new Response(script, { headers: { 'Content-Type': 'text/javascript' } })
        : new Response('<!doctype html><title>Voxa summary harness</title><script src="/harness.js"></script>', { headers: { 'Content-Type': 'text/html' } }),
    });
    const port = 21000 + Math.floor(Math.random() * 20000);
    apiUrl = `http://127.0.0.1:${port}`;
    voxa = Bun.spawn([SERVER_BIN!], {
      env: {
        PATH: process.env.PATH ?? '', HOME: process.env.HOME ?? '', VOXA_AUTH_MODE: 'development', VOXA_LISTEN_ADDR: `127.0.0.1:${port}`,
        VOXA_ALLOWED_ORIGINS: `http://localhost:${pagesServer.port}`, VOXA_MEDIA_DIR: join(temp, 'media'),
        VOXA_WHISPER_BIN: WHISPER_BIN!, VOXA_WHISPER_MODEL: WHISPER_MODEL!,
        VOXA_SUMMARY_PROVIDERS: 'custom-openai', VOXA_CUSTOM_OPENAI_URL: `http://127.0.0.1:${llm.port}/v1`,
      },
      stdin: 'ignore', stdout: 'pipe', stderr: 'pipe',
    });
    void collect(voxa.stdout);
    void collect(voxa.stderr);
    await waitHttp(`${apiUrl}/readyz`);
    chrome = await launchChrome(join(temp, 'chrome'));
  }, 120_000);

  afterAll(() => {
    chrome?.close();
    voxa?.kill();
    pagesServer?.stop(true);
    llm?.stop(true);
    rmSync(temp, { recursive: true, force: true });
  });

  test('summary is generated from the transcript, shown in Voxa Web, regenerated, and secrets never leak', async () => {
    const capabilities = await (await api('/capabilities')).json();
    expect(capabilities.summary).toEqual({ available: true, providers: ['custom-openai'] });

    // Workspace configuration: credential (write-only) and auto-summary.
    expect((await api('/credentials/custom-openai', jsonInit('PUT', { value: SECRET }))).status).toBe(200);
    const configured = await api('/configuration', jsonInit('PATCH', { summary: { provider: 'custom-openai', model: 'fake-llm', autoGenerate: true } }));
    expect(configured.status).toBe(200);

    const meetingId = (await (await api('/meetings', jsonInit('POST', { title: 'Summary E2E' }))).json()).meeting.id as string;
    const transport = new HttpTransport({ baseUrl: apiUrl, getAccessToken: () => null });
    const media = new HttpMediaClient(transport, apiUrl);
    const target = await media.createRecording(meetingId, 'audio/webm;codecs=opus');
    await media.uploadChunk(target.asset.id, 0, new Blob([streamedWebm()]));
    await media.finalize(target.asset.id, 1, null);

    const transcripts = new HttpTranscriptionClient(transport);
    const transcript = await waitFor('transcript', 180_000, async () => {
      const current = await transcripts.getRecordingTranscript(target.asset.id);
      if (current.state === 'failed') throw new Error(`transcription failed: ${JSON.stringify(current.error)}`);
      return current.state === 'ready' && current.transcript;
    });

    // Auto-summary after the transcript is final.
    const summaries = new HttpSummaryClient(transport);
    const ready = await waitFor('summary', 60_000, async () => {
      const view = await summaries.getMeetingSummary(meetingId);
      if (view.state === 'failed') throw new Error(`summary failed: ${JSON.stringify(view.error)}`);
      return view.state === 'ready' && view;
    });
    expect(ready.summary?.title).toBe('Inaugural address v1');
    expect(ready.summary?.markdown).toContain('The speaker addressed fellow Americans.');
    expect(ready.summary?.transcriptId).toBe(transcript.id);
    expect(ready.summary?.provider).toBe('custom-openai');
    expect(ready.summary?.model).toBe('fake-llm');
    expect(ready.stale).toBe(false);
    // The provider received the workspace key, the desktop template prompt and the timestamped transcript.
    const first = llmRequests[0];
    expect(first.authorization).toBe(`Bearer ${SECRET}`);
    expect(first.system).toContain('**SECTION-SPECIFIC INSTRUCTIONS:**');
    expect(first.user).toMatch(/<transcript_chunks>\n\[00:0\d\] /);

    // Voxa Web renders the summary; Regenerate replaces it.
    await chrome.send('Page.navigate', { url: `http://localhost:${pagesServer.port}/` });
    await waitFor('harness', 10_000, () => chrome.evaluate<boolean>('typeof window.harness === "object"').catch(() => false));
    await chrome.evaluate(`harness.render(${JSON.stringify(apiUrl)}, ${JSON.stringify(meetingId)})`);
    const rendered = await waitFor('rendered summary', 15_000, async () => {
      const text = await chrome.evaluate<string>('harness.summaryText()');
      return text.includes('Inaugural address v1') && text;
    });
    expect(rendered).toContain('The speaker addressed fellow Americans.');
    expect(rendered).toContain('Generated by custom-openai (fake-llm)');
    expect(await chrome.evaluate<boolean>(`harness.clickButton('Regenerate')`)).toBe(true);
    await waitFor('regenerated summary in page', 30_000, async () => (await chrome.evaluate<string>('harness.summaryText()')).includes('Inaugural address v2'));
    const regenerated = await summaries.getMeetingSummary(meetingId);
    expect(regenerated.state).toBe('ready');
    expect(regenerated.summary?.id).not.toBe(ready.summary?.id);

    // Secrets never reach clients, the page, or server logs.
    const html = await chrome.evaluate<string>('harness.html()');
    const responses = await Promise.all(['/credentials', '/configuration', `/meetings/${meetingId}/summary`, `/meetings/${meetingId}/jobs`, '/capabilities']
      .map(async (path) => (await api(path)).text()));
    for (const surface of [html, serverLogs, ...responses]) {
      expect(surface).not.toContain(SECRET);
      expect(surface).not.toContain('SECRET-6f1d');
    }
    expect(serverLogs).not.toContain('fellow Americans');
    expect(serverLogs).toContain('summaries enabled');
  }, 300_000);
});
