import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { Subprocess } from 'bun';
import { chromeBinary, launchChrome, type ChromePage } from './cdp';

/*
 * Real browser meeting capture: headless Chrome performs a genuine tab
 * capture (getDisplayMedia, auto-selected by tab title, as if the user
 * picked the meeting tab and enabled "Share tab audio") of a local "meeting"
 * page, plus a fake microphone fed from a generated WAV. The production
 * recording, mixing, IndexedDB and upload code record into a real Voxa
 * server; the stored file is decoded with ffmpeg and analysed.
 *
 *   VOXA_BROWSER_E2E_SERVER_BIN=/path/to/voxa bun test tests/browser/capture.browser.test.ts
 *
 * With VOXA_E2E_WHISPER_BIN, VOXA_E2E_WHISPER_MODEL and VOXA_E2E_SPEECH (and
 * VOXA_E2E_WORK_DIR visible to the whisper binary) it also transcribes
 * speech played in the meeting tab. VOXA_E2E_LONG_SECONDS sets the extended
 * session length (default 40).
 */
const SERVER_BIN = process.env.VOXA_BROWSER_E2E_SERVER_BIN;
const WHISPER_BIN = process.env.VOXA_E2E_WHISPER_BIN;
const WHISPER_MODEL = process.env.VOXA_E2E_WHISPER_MODEL;
const SPEECH = process.env.VOXA_E2E_SPEECH;
const enabled = Boolean(SERVER_BIN && chromeBinary() && Bun.which('ffmpeg'));
const withWhisper = Boolean(WHISPER_BIN && WHISPER_MODEL && SPEECH);
const LONG_SECONDS = Number(process.env.VOXA_E2E_LONG_SECONDS ?? 40);
const TIMESLICE_MS = 500;
const MEETING_TITLE = 'VoxaMeetingFixture';
const RATE = 48_000;
const MIC_HZ = 440;
const TAB_HZ = 1000;

let pagesServer: ReturnType<typeof Bun.serve>;
let voxa: Subprocess | undefined;
let chrome: ChromePage;
let apiUrl = '';
let pageUrl = '';
const workRoot = process.env.VOXA_E2E_WORK_DIR ?? tmpdir();
mkdirSync(workRoot, { recursive: true });
const temp = mkdtempSync(join(workRoot, 'voxa-capture-e2e-'));

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
    await Bun.sleep(200);
  }
  throw new Error(`timed out waiting for ${what}`);
}

/** Meeting pages stand in for Meet/Teams/Zoom tabs: they just play audio. */
function meetingPage(kind: string, gain: number): string {
  const source = kind === 'speech'
    ? `const a = new Audio('/speech.wav'); a.loop = true; a.volume = ${gain}; a.play();`
    : `const ctx = new AudioContext(); const o = ctx.createOscillator(); const g = ctx.createGain(); o.frequency.value = ${TAB_HZ}; g.gain.value = ${gain}; o.connect(g); g.connect(ctx.destination); o.start();`;
  return `<!doctype html><title>${MEETING_TITLE}</title><body>meeting<script>${source}</script></body>`;
}

/**
 * Decodes the stored recording's first channel to PCM at 48 kHz. (A plain
 * -ac 1 downmix of the stereo mix would add both channels at 0.707 and read
 * levels 3 dB high.)
 */
function decode(bytes: Uint8Array): Float32Array {
  const file = join(temp, `decode-${crypto.randomUUID()}.webm`);
  Bun.write(file, bytes);
  const result = Bun.spawnSync(['ffmpeg', '-v', 'error', '-i', file, '-af', 'pan=mono|c0=c0', '-ar', String(RATE), '-f', 'f32le', 'pipe:1']);
  if (result.exitCode !== 0) throw new Error(result.stderr.toString());
  const buffer = result.stdout;
  return new Float32Array(buffer.buffer.slice(buffer.byteOffset, buffer.byteOffset + buffer.byteLength));
}

/** Amplitude of one frequency in a window (Goertzel). */
function amplitude(samples: Float32Array, start: number, length: number, hz: number): number {
  const k = 2 * Math.cos((2 * Math.PI * hz) / RATE);
  let s1 = 0, s2 = 0;
  for (let i = start; i < start + length; i++) {
    const s0 = samples[i] + k * s1 - s2;
    s2 = s1;
    s1 = s0;
  }
  const power = s1 * s1 + s2 * s2 - k * s1 * s2;
  return (2 * Math.sqrt(Math.max(power, 0))) / length;
}

interface Analysis { seconds: number; peak: number; clipped: number; mic: number[]; tab: number[] }

/** Per-0.5 s amplitudes of the microphone and tab tones, plus peak/clipping. */
function analyze(samples: Float32Array): Analysis {
  const window = RATE / 2;
  const result: Analysis = { seconds: samples.length / RATE, peak: 0, clipped: 0, mic: [], tab: [] };
  for (const x of samples) {
    const magnitude = Math.abs(x);
    result.peak = Math.max(result.peak, magnitude);
    if (magnitude >= 0.999) result.clipped++;
  }
  for (let start = 0; start + window <= samples.length; start += window) {
    result.mic.push(amplitude(samples, start, window, MIC_HZ));
    result.tab.push(amplitude(samples, start, window, TAB_HZ));
  }
  return result;
}

const present = (values: number[], threshold = 0.02) => values.filter((v) => v > threshold).length;
/** Per-second maximum of 0.5 s windows (the microphone tone is pulsed). */
const perSecond = (values: number[]) => values.reduce<number[]>((out, v, i) => { if (i % 2 === 0) out.push(v); else out[out.length - 1] = Math.max(out[out.length - 1], v); return out; }, []);

async function serverContent(assetId: string): Promise<Uint8Array> {
  return new Uint8Array(await (await fetch(`${apiUrl}/api/v1/media/${assetId}/content`)).arrayBuffer());
}

async function openHarness(synthetic?: { micHz: number; micAmplitude: number; tabHz: number; tabAmplitude: number }) {
  await chrome.send('Page.navigate', { url: pageUrl });
  await waitFor('harness', 10_000, () => chrome.evaluate<boolean>('typeof window.harness === "object"').catch(() => false));
  return chrome.evaluate<{ mimeType: string }>(`harness.setup(${JSON.stringify(apiUrl)}, ${TIMESLICE_MS}${synthetic ? `, ${JSON.stringify(synthetic)}` : ''})`);
}

async function record(mode: string, title: string, seconds: number, during?: (elapsed: number) => Promise<void>) {
  const meetingId = await chrome.evaluate<string>(`harness.createMeeting(${JSON.stringify(title)})`);
  const started = await chrome.evaluate<{ mediaAssetId: string; phase: string; error: string | null; capture: unknown }>(`harness.start(${JSON.stringify(meetingId)}, ${JSON.stringify(mode)})`);
  if (started.phase !== 'recording') throw new Error(`did not start: ${JSON.stringify(started)}`);
  const begin = Date.now();
  while (Date.now() - begin < seconds * 1000) {
    await Bun.sleep(250);
    await during?.((Date.now() - begin) / 1000);
  }
  const state = await chrome.evaluate<{ capture: unknown; warning: string | null; phase: string }>('harness.state()');
  if (state.phase === 'recording' || state.phase === 'paused') await chrome.evaluate('harness.stop()');
  const job = await chrome.evaluate<{ uploadedChunks: number; asset: { durationSeconds: number | null } }>(`harness.waitFor(${JSON.stringify(started.mediaAssetId)}, 'ready', 30000)`);
  return { meetingId, mediaAssetId: started.mediaAssetId, started, state, job };
}

describe.skipIf(!enabled)('browser meeting capture (real tab capture + fake microphone)', () => {
  beforeAll(async () => {
    const mic = join(temp, 'mic.wav');
    // A 440 Hz tone at -10 dBFS, pulsed 0.6 s on / 0.4 s off like speech
    // bursts, stands in for the user's voice (a steady tone is treated as
    // noise and removed by the browser's microphone noise suppression).
    const made = Bun.spawnSync(['ffmpeg', '-v', 'error', '-y', '-f', 'lavfi', '-i', `sine=frequency=${MIC_HZ}:sample_rate=${RATE}:duration=600`,
      '-af', "volume='0.3*lt(mod(t,1),0.6)':eval=frame", '-ac', '1', mic]);
    if (made.exitCode !== 0) throw new Error(made.stderr.toString());
    const outfile = join(temp, 'harness.js');
    const build = Bun.spawnSync([process.execPath, 'build', join(import.meta.dir, 'harness-entry.ts'), '--target', 'browser', '--outfile', outfile], { cwd: join(import.meta.dir, '../..') });
    if (build.exitCode !== 0) throw new Error(build.stderr.toString());
    const script = await Bun.file(outfile).text();
    pagesServer = Bun.serve({
      port: 0,
      fetch: (request) => {
        const url = new URL(request.url);
        const html = (body: string) => new Response(body, { headers: { 'Content-Type': 'text/html' } });
        if (url.pathname === '/harness.js') return new Response(script, { headers: { 'Content-Type': 'text/javascript' } });
        if (url.pathname === '/meeting') return html(meetingPage(url.searchParams.get('kind') ?? 'tone', Number(url.searchParams.get('gain') ?? '0.3')));
        if (url.pathname === '/speech.wav' && SPEECH) return new Response(Bun.file(SPEECH));
        return html('<!doctype html><title>Voxa capture harness</title><script src="/harness.js"></script>');
      },
    });
    pageUrl = `http://localhost:${pagesServer.port}/`;
    const port = 21000 + Math.floor(Math.random() * 20000);
    apiUrl = `http://127.0.0.1:${port}`;
    const env: Record<string, string> = {
      PATH: process.env.PATH ?? '', HOME: process.env.HOME ?? '', VOXA_AUTH_MODE: 'development', VOXA_LISTEN_ADDR: `127.0.0.1:${port}`,
      VOXA_ALLOWED_ORIGINS: `http://localhost:${pagesServer.port}`, VOXA_MEDIA_DIR: join(temp, 'media'),
    };
    if (withWhisper) Object.assign(env, { VOXA_WHISPER_BIN: WHISPER_BIN!, VOXA_WHISPER_MODEL: WHISPER_MODEL! });
    voxa = Bun.spawn([SERVER_BIN!], { env, stdout: 'ignore', stderr: 'ignore' });
    await waitHttp(`${apiUrl}/readyz`);
    chrome = await launchChrome(join(temp, 'chrome'), {
      fakeUi: false,
      args: [`--auto-select-tab-capture-source-by-title=${MEETING_TITLE}`, `--use-file-for-fake-audio-capture=${mic}`],
    });
    await chrome.send('Browser.grantPermissions', { permissions: ['audioCapture'], origin: `http://localhost:${pagesServer.port}` });
    await chrome.send('Network.enable');
  }, 60_000);

  afterAll(() => {
    chrome?.close();
    voxa?.kill();
    pagesServer?.stop(true);
    rmSync(temp, { recursive: true, force: true });
  });

  test('capabilities are feature-detected in Chromium', async () => {
    await openHarness();
    const capture = await chrome.evaluate<Record<string, unknown>>('harness.browserCapture()');
    expect(capture).toEqual({ microphone: true, displayCapture: true, sharedAudio: 'likely', mixing: true, modes: ['microphone', 'microphone+shared', 'shared'] });
  });

  test('deterministic mix levels: both sources keep their level, the sum does not clip, a stopped source leaves the other intact', async () => {
    // Production mixer + MediaRecorder + upload; oscillators replace the devices.
    await openHarness({ micHz: MIC_HZ, micAmplitude: 0.5, tabHz: TAB_HZ, tabAmplitude: 0.5 });
    let ended = false;
    const { started, state, mediaAssetId } = await record('microphone+shared', 'Levels', 6, async (elapsed) => {
      if (!ended && elapsed >= 3) {
        ended = true;
        await chrome.evaluate('harness.endSynthetic(0)'); // the share picker is requested first: stream 0 is the tab
      }
    });
    expect(started.capture).toEqual({ mode: 'microphone+shared', microphone: true, shared: { surface: 'unknown' } });
    expect(state.warning).toContain('Tab audio sharing stopped');
    const result = analyze(decode(await serverContent(mediaAssetId)));
    const both = { mic: median(result.mic.slice(1, 5)), tab: median(result.tab.slice(1, 5)) };
    const after = { mic: median(result.mic.slice(-4)), tab: median(result.tab.slice(-4)) };
    console.log(`levels: both ${JSON.stringify(both)} after ${JSON.stringify(after)} peak ${result.peak.toFixed(3)} clipped ${result.clipped}`);
    // 0.5 + 0.5 sums to full scale: the soft limiter keeps it below clipping,
    // while each tone stays near its own level.
    expect(both.mic).toBeGreaterThan(0.4);
    expect(both.tab).toBeGreaterThan(0.4);
    expect(result.peak).toBeLessThan(0.97);
    expect(result.clipped).toBe(0);
    // After the tab stops: the microphone continues at full level, the tab is gone.
    expect(after.mic).toBeGreaterThan(0.45);
    expect(after.mic).toBeLessThan(0.55);
    expect(after.tab).toBeLessThan(0.01);
  }, 60_000);

  test('deterministic nominal levels pass through unchanged', async () => {
    await openHarness({ micHz: MIC_HZ, micAmplitude: 0.25, tabHz: TAB_HZ, tabAmplitude: 0.2 });
    const { mediaAssetId } = await record('microphone+shared', 'Nominal', 3);
    const result = analyze(decode(await serverContent(mediaAssetId)));
    const mic = median(result.mic.slice(1, -1));
    const tab = median(result.tab.slice(1, -1));
    console.log(`nominal: mic ${mic.toFixed(3)} tab ${tab.toFixed(3)} peak ${result.peak.toFixed(3)}`);
    expect(Math.abs(mic - 0.25)).toBeLessThan(0.03);
    expect(Math.abs(tab - 0.2)).toBeLessThan(0.03);
    expect(result.peak).toBeLessThan(0.5);
  }, 60_000);

  test('microphone + meeting tab: both sources in one file, no clipping; tab closed mid-way keeps the microphone going', async () => {
    const meetingTab = await chrome.openTab(`${pageUrl}meeting?kind=tone&gain=0.3`);
    await Bun.sleep(800);
    await openHarness();
    let closedAt = 0;
    const { started, state, job, mediaAssetId } = await record('microphone+shared', 'Mixed', 6, async (elapsed) => {
      if (!closedAt && elapsed >= 3) {
        // The user leaves the meeting (or the shared tab closes): its audio track ends.
        closedAt = elapsed;
        await chrome.closeTab(meetingTab);
      }
    });
    expect(started.capture).toEqual({ mode: 'microphone+shared', microphone: true, shared: { surface: 'browser' } });
    expect(state.warning).toBe('Tab audio sharing stopped. Recording continues with your microphone only.');
    expect(state.capture).toEqual({ mode: 'microphone+shared', microphone: true, shared: null });
    expect(job.uploadedChunks).toBeGreaterThanOrEqual(8);

    const result = analyze(decode(await serverContent(mediaAssetId)));
    const mic = perSecond(result.mic);
    console.log(`mixed: ${result.seconds.toFixed(1)} s, peak ${result.peak.toFixed(3)}, mic/s ${mic.map((v) => v.toFixed(3)).join(' ')}, tab ${result.tab.map((v) => v.toFixed(3)).join(" ")}`);
    expect(result.seconds).toBeGreaterThan(5);
    // The microphone is present throughout; the tab only until it was closed.
    expect(present(mic)).toBeGreaterThanOrEqual(mic.length - 1);
    // Headless Chrome renders a background tab's audio unevenly (levels vary
    // even without Voxa in the path; see the tab-only test), so presence is
    // checked with a low threshold here and levels in the deterministic tests.
    expect(present(result.tab.slice(1, 5), 0.01)).toBeGreaterThanOrEqual(3);
    expect(present(result.tab.slice(-3), 0.002)).toBe(0);
    expect(result.clipped).toBe(0);
    expect(result.peak).toBeLessThan(0.99);
  }, 90_000);

  test('meeting tab only: records the shared audio and stops when sharing ends', async () => {
    const meetingTab = await chrome.openTab(`${pageUrl}meeting?kind=tone&gain=0.3`);
    await Bun.sleep(800);
    await openHarness();
    const { started, state, mediaAssetId } = await record('shared', 'Tab only', 5, async (elapsed) => {
      if (elapsed >= 4.5) await chrome.closeTab(meetingTab);
    });
    expect(started.capture).toEqual({ mode: 'shared', microphone: false, shared: { surface: 'browser' } });
    expect(state.phase).toBe('idle'); // stopped by itself
    const result = analyze(decode(await serverContent(mediaAssetId)));
    console.log(`tab only: tab ${result.tab.map((v) => v.toFixed(3)).join(' ')}`);
    expect(present(result.tab, 0.004)).toBeGreaterThanOrEqual(result.tab.length - 2);
    expect(present(result.mic, 0.002)).toBe(0);
  }, 60_000);

  // A cancelled picker or a source without audio cannot be produced here:
  // headless Chrome waits forever on an unanswered picker, and tab captures
  // always carry an audio track. Those paths are covered with fakes in
  // tests/recording/browser-capture.test.ts.

  test('mixed recording survives a network drop and a reload (IndexedDB recovery)', async () => {
    const meetingTab = await chrome.openTab(`${pageUrl}meeting?kind=tone&gain=0.3`);
    await Bun.sleep(800);
    await openHarness();
    const meetingId = await chrome.evaluate<string>(`harness.createMeeting('Recovery')`);
    const started = await chrome.evaluate<{ mediaAssetId: string }>(`harness.start(${JSON.stringify(meetingId)}, 'microphone+shared')`);
    await Bun.sleep(1200);
    await chrome.send('Network.setBlockedURLs', { urls: [`${apiUrl}/*`] });
    await Bun.sleep(1500);
    expect(await chrome.evaluate<number>(`harness.pendingLocal(${JSON.stringify(started.mediaAssetId)})`)).toBeGreaterThanOrEqual(2);
    // Reload while recording with the API unreachable.
    await openHarness();
    await chrome.send('Network.setBlockedURLs', { urls: [] });
    expect(await chrome.evaluate<string[]>('harness.recover()')).toContain(started.mediaAssetId);
    const job = await chrome.evaluate<{ recovered: boolean; uploadedChunks: number }>(`harness.waitFor(${JSON.stringify(started.mediaAssetId)}, 'ready', 20000)`);
    expect(job.recovered).toBe(true);
    const expected = Buffer.from(await chrome.evaluate<string>(`harness.copiedBytes(${JSON.stringify(started.mediaAssetId)}, ${job.uploadedChunks})`), 'base64');
    const stored = Buffer.from(await serverContent(started.mediaAssetId));
    expect(Buffer.compare(stored, expected)).toBe(0);
    const result = analyze(decode(stored));
    expect(present(result.mic)).toBeGreaterThan(0);
    expect(present(result.tab, 0.004)).toBeGreaterThan(0);
    await chrome.closeTab(meetingTab);
  }, 60_000);

  test('extended mixed session: steady chunks, bounded memory, resources released', async () => {
    const meetingTab = await chrome.openTab(`${pageUrl}meeting?kind=tone&gain=0.3`);
    await Bun.sleep(800);
    await openHarness();
    const heap: number[] = [];
    const { job, mediaAssetId } = await record('microphone+shared', 'Long', LONG_SECONDS, async (elapsed) => {
      if (Math.floor(elapsed) % 5 === 0) {
        const bytes = await chrome.evaluate<number | null>('harness.heapBytes()');
        if (bytes) heap.push(bytes);
      }
    });
    await chrome.closeTab(meetingTab);
    const chunksPerSecond = job.uploadedChunks / LONG_SECONDS;
    const growthMb = (heap.at(-1)! - heap[0]) / 1024 / 1024;
    console.log(`extended: ${LONG_SECONDS} s, ${job.uploadedChunks} chunks, heap ${(heap[0] / 1048576).toFixed(1)} → ${(heap.at(-1)! / 1048576).toFixed(1)} MB`);
    expect(chunksPerSecond).toBeGreaterThan(1.5);
    expect(growthMb).toBeLessThan(20);
    const result = analyze(decode(await serverContent(mediaAssetId)));
    expect(result.seconds).toBeGreaterThan(LONG_SECONDS - 2);
    const mic = perSecond(result.mic);
    console.log(`extended mic/s min ${Math.min(...mic).toFixed(3)} median ${median(mic).toFixed(3)}`);
    expect(present(result.tab, 0.004)).toBeGreaterThanOrEqual(result.tab.length - 6);
    expect(present(mic)).toBeGreaterThanOrEqual(mic.length - 2);
  }, (LONG_SECONDS + 60) * 1000);

  test.skipIf(!withWhisper)('speech in the meeting tab reaches remux and the transcript', async () => {
    const meetingTab = await chrome.openTab(`${pageUrl}meeting?kind=speech&gain=1`);
    await Bun.sleep(800);
    await openHarness();
    const { mediaAssetId } = await record('microphone+shared', 'Transcribed', 13);
    await chrome.closeTab(meetingTab);
    const transcript = await waitFor('transcript', 180_000, async () => {
      const response = await (await fetch(`${apiUrl}/api/v1/media/${mediaAssetId}/transcript`)).json();
      if (response.state === 'failed') throw new Error(JSON.stringify(response.error));
      return response.state === 'ready' && response.transcript;
    });
    const text = transcript.segments.map((s: { text: string }) => s.text).join(' ').toLowerCase();
    console.log(`transcript: ${text}`);
    expect(text).toContain('your country');
    const media = (await (await fetch(`${apiUrl}/api/v1/media/${mediaAssetId}`)).json()).mediaAsset;
    expect(media.durationSeconds).toBeGreaterThan(11);
  }, 240_000);
});

function median(values: number[]): number {
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.floor(sorted.length / 2)] ?? 0;
}
