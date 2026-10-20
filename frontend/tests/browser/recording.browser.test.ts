import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { Subprocess } from 'bun';
import { chromeBinary, launchChrome, type ChromePage } from './cdp';

/*
 * Real browser + real server: headless Chrome (fake microphone) runs the
 * production recording, IndexedDB and upload code against the Voxa Go
 * server in development auth mode with filesystem media storage.
 *
 *   VOXA_BROWSER_E2E_SERVER_BIN=/path/to/voxa bun test tests/browser
 */
const SERVER_BIN = process.env.VOXA_BROWSER_E2E_SERVER_BIN;
const enabled = Boolean(SERVER_BIN && chromeBinary());
const TIMESLICE_MS = 400;

let pagesServer: ReturnType<typeof Bun.serve>;
let voxa: Subprocess | undefined;
let chrome: ChromePage;
let apiUrl = '';
let pageUrl = '';
const temp = mkdtempSync(join(tmpdir(), 'voxa-browser-e2e-'));

async function waitHttp(url: string) {
  for (let i = 0; i < 100; i++) {
    try { if ((await fetch(url)).ok) return; } catch { /* starting */ }
    await Bun.sleep(100);
  }
  throw new Error(`timeout waiting for ${url}`);
}

async function openHarness() {
  await chrome.send('Page.navigate', { url: pageUrl });
  for (let i = 0; i < 100; i++) {
    if (await chrome.evaluate<boolean>('typeof window.harness === "object"').catch(() => false)) break;
    await Bun.sleep(100);
  }
  return chrome.evaluate<{ mimeType: string; capabilities: Record<string, unknown> }>(`harness.setup(${JSON.stringify(apiUrl)}, ${TIMESLICE_MS})`);
}

/** Blocks only the Voxa API origin (the page itself still loads), simulating an API outage. */
const blockApi = (blocked: boolean) => chrome.send('Network.setBlockedURLs', { urls: blocked ? [`${apiUrl}/*`] : [] });
const setOffline = (offline: boolean) => chrome.send('Network.emulateNetworkConditions', { offline, latency: 0, downloadThroughput: -1, uploadThroughput: -1 });

async function serverMedia(meetingId: string) {
  return (await (await fetch(`${apiUrl}/api/v1/meetings/${meetingId}/media`)).json()).items as Array<{ id: string; status: string; sizeBytes: number; durationSeconds: number | null }>;
}

async function decodes(bytes: Uint8Array): Promise<boolean | null> {
  if (!Bun.which('ffmpeg')) return null;
  const file = join(temp, `check-${crypto.randomUUID()}.webm`);
  await Bun.write(file, bytes);
  const result = Bun.spawnSync(['ffmpeg', '-v', 'error', '-i', file, '-f', 'null', '-']);
  // A recording cut off mid-capture may end with a truncated final block; that is reported but decodes.
  const errors = result.stderr.toString().split('\n').filter((line) => line && !line.includes('File ended prematurely'));
  return result.exitCode === 0 && errors.length === 0;
}

describe.skipIf(!enabled)('Voxa Web recording in a real browser', () => {
  beforeAll(async () => {
    // Bundled in a subprocess: Bun.build inside the test runner resolves differently.
    const outfile = join(temp, 'harness.js');
    const build = Bun.spawnSync([process.execPath, 'build', join(import.meta.dir, 'harness-entry.ts'), '--target', 'browser', '--outfile', outfile], { cwd: join(import.meta.dir, '../..') });
    if (build.exitCode !== 0) throw new Error(build.stderr.toString());
    const script = await Bun.file(outfile).text();
    pagesServer = Bun.serve({
      port: 0,
      fetch: (request) => new URL(request.url).pathname === '/harness.js'
        ? new Response(script, { headers: { 'Content-Type': 'text/javascript' } })
        : new Response('<!doctype html><title>Voxa recording harness</title><script src="/harness.js"></script>', { headers: { 'Content-Type': 'text/html' } }),
    });
    pageUrl = `http://localhost:${pagesServer.port}/`;
    const port = 21000 + Math.floor(Math.random() * 20000);
    apiUrl = `http://127.0.0.1:${port}`;
    voxa = Bun.spawn([SERVER_BIN!], {
      env: { PATH: process.env.PATH ?? '', VOXA_AUTH_MODE: 'development', VOXA_LISTEN_ADDR: `127.0.0.1:${port}`, VOXA_ALLOWED_ORIGINS: `http://localhost:${pagesServer.port}`, VOXA_MEDIA_DIR: join(temp, 'media') },
      stdout: 'ignore', stderr: 'ignore',
    });
    await waitHttp(`${apiUrl}/readyz`);
    chrome = await launchChrome(join(temp, 'chrome'));
    await chrome.send('Network.enable');
  }, 60_000);

  afterAll(() => {
    chrome?.close();
    voxa?.kill();
    pagesServer?.stop(true);
    rmSync(temp, { recursive: true, force: true });
  });

  test('records, uploads, finalizes, survives reload, streams exact bytes with ranges', async () => {
    const { mimeType, capabilities } = await openHarness();
    expect(mimeType).toBe('audio/webm;codecs=opus');
    expect(capabilities.microphoneCapture).toBe(true);
    expect(capabilities.tabCapture).toBe(true); // Chromium can share tab audio (see capture.browser.test.ts)

    const meetingId = await chrome.evaluate<string>(`harness.createMeeting('Browser E2E')`);
    const started = await chrome.evaluate<{ mediaAssetId: string; phase: string }>(`harness.start(${JSON.stringify(meetingId)})`);
    expect(started.phase).toBe('recording');
    await Bun.sleep(2_200);
    await chrome.evaluate(`harness.stop()`);
    const job = await chrome.evaluate<{ uploadedChunks: number; asset: { durationSeconds: number } }>(`harness.waitFor(${JSON.stringify(started.mediaAssetId)}, 'ready', 15000)`);
    expect(job.uploadedChunks).toBeGreaterThanOrEqual(4);
    expect(job.asset.durationSeconds).toBeGreaterThan(1.5);
    expect(await chrome.evaluate<number>(`harness.pendingLocal(${JSON.stringify(started.mediaAssetId)})`)).toBe(0);

    // "Reload the app": a fresh page still sees the recording on the server.
    await openHarness();
    const media = await serverMedia(meetingId);
    expect(media).toHaveLength(1);
    expect(media[0].status).toBe('ready');

    const expected = Buffer.from(await chrome.evaluate<string>(`harness.copiedBytes(${JSON.stringify(started.mediaAssetId)}, ${job.uploadedChunks})`), 'base64');
    const content = new Uint8Array(await (await fetch(`${apiUrl}/api/v1/media/${started.mediaAssetId}/content`)).arrayBuffer());
    expect(Buffer.compare(Buffer.from(content), expected)).toBe(0);
    expect(media[0].sizeBytes).toBe(expected.length);
    expect(await decodes(content)).not.toBe(false);

    const ranged = await fetch(`${apiUrl}/api/v1/media/${started.mediaAssetId}/content`, { headers: { Range: 'bytes=10-99' } });
    expect(ranged.status).toBe(206);
    expect(ranged.headers.get('content-range')).toBe(`bytes 10-99/${expected.length}`);
    expect(Buffer.compare(Buffer.from(await ranged.arrayBuffer()), expected.subarray(10, 100))).toBe(0);

    const playback = await (await fetch(`${apiUrl}/api/v1/media/${started.mediaAssetId}/playback`)).json();
    const played = await chrome.evaluate<{ error: number | null; currentTime: number }>(`harness.play(${JSON.stringify(new URL(playback.url, apiUrl).toString())})`);
    expect(played.error).toBeNull();
  }, 60_000);

  test('keeps recording through a network outage and uploads when back online', async () => {
    await openHarness();
    const meetingId = await chrome.evaluate<string>(`harness.createMeeting('Offline E2E')`);
    const started = await chrome.evaluate<{ mediaAssetId: string }>(`harness.start(${JSON.stringify(meetingId)})`);
    await Bun.sleep(900);
    await setOffline(true);
    await Bun.sleep(1_600);
    const offlineJob = await chrome.evaluate<{ phase: string; waitingForNetwork: boolean; pendingChunks: number }>(`harness.job(${JSON.stringify(started.mediaAssetId)})`);
    expect(offlineJob.phase).toBe('recording');
    expect(offlineJob.waitingForNetwork).toBe(true);
    expect(await chrome.evaluate<number>(`harness.pendingLocal(${JSON.stringify(started.mediaAssetId)})`)).toBeGreaterThanOrEqual(2);
    await chrome.evaluate(`harness.stop()`);
    expect((await chrome.evaluate<{ phase: string }>(`harness.job(${JSON.stringify(started.mediaAssetId)})`)).phase).toBe('waiting');
    await setOffline(false);
    const job = await chrome.evaluate<{ uploadedChunks: number }>(`harness.waitFor(${JSON.stringify(started.mediaAssetId)}, 'ready', 20000)`);
    const expected = Buffer.from(await chrome.evaluate<string>(`harness.copiedBytes(${JSON.stringify(started.mediaAssetId)}, ${job.uploadedChunks})`), 'base64');
    const content = Buffer.from(await (await fetch(`${apiUrl}/api/v1/media/${started.mediaAssetId}/content`)).arrayBuffer());
    expect(Buffer.compare(content, expected)).toBe(0);
  }, 60_000);

  test('a page reloaded mid-recording recovers its chunks from IndexedDB', async () => {
    await openHarness();
    const meetingId = await chrome.evaluate<string>(`harness.createMeeting('Reload E2E')`);
    const started = await chrome.evaluate<{ mediaAssetId: string }>(`harness.start(${JSON.stringify(meetingId)})`);
    await Bun.sleep(700);
    await blockApi(true);
    await Bun.sleep(1_300);
    expect(await chrome.evaluate<number>(`harness.pendingLocal(${JSON.stringify(started.mediaAssetId)})`)).toBeGreaterThanOrEqual(2);
    // The tab reloads while recording and the API is unreachable: recorder and in-memory state are gone.
    await openHarness();
    await blockApi(false);
    const resumed = await chrome.evaluate<string[]>(`harness.recover()`);
    expect(resumed).toContain(started.mediaAssetId);
    const job = await chrome.evaluate<{ recovered: boolean; uploadedChunks: number; asset: { status: string; durationSeconds: number | null } }>(`harness.waitFor(${JSON.stringify(started.mediaAssetId)}, 'ready', 20000)`);
    expect(job.recovered).toBe(true);
    expect(job.asset.status).toBe('ready');
    expect(job.uploadedChunks).toBeGreaterThanOrEqual(3);
    const expected = Buffer.from(await chrome.evaluate<string>(`harness.copiedBytes(${JSON.stringify(started.mediaAssetId)}, ${job.uploadedChunks})`), 'base64');
    const content = Buffer.from(await (await fetch(`${apiUrl}/api/v1/media/${started.mediaAssetId}/content`)).arrayBuffer());
    expect(Buffer.compare(content, expected)).toBe(0);
    expect(await decodes(new Uint8Array(content))).not.toBe(false);
    expect(await chrome.evaluate<number>(`harness.pendingLocal(${JSON.stringify(started.mediaAssetId)})`)).toBe(0);
  }, 60_000);
});
