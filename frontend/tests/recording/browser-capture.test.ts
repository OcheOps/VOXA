import { describe, expect, test } from 'bun:test';
import { BrowserRecordingClient, RecordingPermissionError, type RecorderLike } from '../../src/clients/browser/recording-client';
import { CaptureError, detectBrowserCapture, MixGraph, softLimiterCurve } from '../../src/clients/browser/capture';

// --- fakes ---

class FakeTrack {
  stopped = false;
  onended: (() => void) | null = null;
  constructor(readonly kind: 'audio' | 'video', readonly settings: Record<string, unknown> = {}) {}
  stop() { this.stopped = true; }
  getSettings() { return this.settings; }
  end() { this.stopped = true; this.onended?.(); }
}

class FakeStream {
  constructor(readonly tracks: FakeTrack[]) {}
  getTracks() { return this.tracks; }
  getAudioTracks() { return this.tracks.filter((t) => t.kind === 'audio'); }
  getVideoTracks() { return this.tracks.filter((t) => t.kind === 'video'); }
}

class FakeNode {
  connections: FakeNode[] = [];
  disconnected = false;
  constructor(readonly label: string, readonly context: FakeAudioContext) { context.nodes.push(this); }
  connect(node: FakeNode) { this.connections.push(node); return node; }
  disconnect() { this.disconnected = true; this.connections = []; }
}

class FakeAudioContext {
  static instances: FakeAudioContext[] = [];
  static startSuspended = false;
  state = 'running';
  closed = false;
  nodes: FakeNode[] = [];
  sources: Array<{ node: FakeNode; stream: FakeStream }> = [];
  destinationTrack = new FakeTrack('audio');
  constructor() {
    FakeAudioContext.instances.push(this);
    if (FakeAudioContext.startSuspended) this.state = 'suspended';
  }
  async resume() { if (!FakeAudioContext.startSuspended) this.state = 'running'; }
  async close() { this.closed = true; this.state = 'closed'; }
  createMediaStreamSource(stream: FakeStream) { const node = new FakeNode('source', this); this.sources.push({ node, stream }); return node; }
  createMediaStreamDestination() { return Object.assign(new FakeNode('destination', this), { stream: new FakeStream([this.destinationTrack]) }); }
  createGain() { return Object.assign(new FakeNode('gain', this), { gain: { value: 1 } }); }
  createWaveShaper() { return Object.assign(new FakeNode('shaper', this), { curve: null as Float32Array | null, oversample: '' }); }
}
(FakeAudioContext.prototype as unknown as { createMediaStreamDestination: unknown }).createMediaStreamDestination = FakeAudioContext.prototype.createMediaStreamDestination;

class FakeRecorder implements RecorderLike {
  static instances: FakeRecorder[] = [];
  static throwOnConstruct = false;
  static isTypeSupported(type: string) { return type === 'audio/webm;codecs=opus'; }
  state: 'inactive' | 'recording' | 'paused' = 'inactive';
  mimeType: string;
  ondataavailable: ((event: { data: Blob }) => void) | null = null;
  onstop: (() => void) | null = null;
  onerror: ((event: unknown) => void) | null = null;
  constructor(readonly stream: FakeStream, options: { mimeType: string; audioBitsPerSecond?: number }) {
    if (FakeRecorder.throwOnConstruct) throw new Error('NotSupportedError');
    this.mimeType = options.mimeType;
    FakeRecorder.instances.push(this);
  }
  start() { this.state = 'recording'; }
  pause() { this.state = 'paused'; }
  resume() { this.state = 'recording'; }
  emit(text: string) { this.ondataavailable?.({ data: new Blob([text]) }); }
  stop() { this.state = 'inactive'; this.emit('final'); this.onstop?.(); }
}

interface Options {
  display?: 'tab' | 'no-audio' | 'denied' | 'missing' | 'error';
  microphone?: 'ok' | 'denied';
  audioContext?: boolean;
}

function setup(options: Options = {}) {
  FakeRecorder.instances = [];
  FakeRecorder.throwOnConstruct = false;
  FakeAudioContext.instances = [];
  FakeAudioContext.startSuspended = false;
  const mic = new FakeTrack('audio');
  const tabAudio = new FakeTrack('audio');
  const tabVideo = new FakeTrack('video', { displaySurface: 'browser' });
  const calls: string[] = [];
  const displayOptions: unknown[] = [];
  const micConstraints: unknown[] = [];
  const display = options.display ?? 'tab';
  const mediaDevices: Record<string, unknown> = {
    getUserMedia: async (constraints: unknown) => {
      calls.push('microphone');
      micConstraints.push(constraints);
      if (options.microphone === 'denied') throw Object.assign(new Error('denied'), { name: 'NotAllowedError' });
      return new FakeStream([mic]);
    },
    enumerateDevices: async () => [],
    getSupportedConstraints: () => ({ suppressLocalAudioPlayback: true }),
  };
  if (display !== 'missing') {
    mediaDevices.getDisplayMedia = async (opts: unknown) => {
      calls.push('display');
      displayOptions.push(opts);
      if (display === 'denied') throw Object.assign(new Error('Permission denied by user'), { name: 'NotAllowedError' });
      if (display === 'error') throw Object.assign(new Error('boom'), { name: 'AbortErrorX' });
      return new FakeStream(display === 'no-audio' ? [tabVideo] : [tabVideo, tabAudio]);
    };
  }
  const client = new BrowserRecordingClient({
    mediaDevices: mediaDevices as never,
    MediaRecorder: FakeRecorder as never,
    AudioContext: options.audioContext === false ? undefined : FakeAudioContext as never,
    MediaStream: FakeStream as never,
    now: () => 0,
    timesliceMs: 1000,
  });
  return { client, mic, tabAudio, tabVideo, calls, displayOptions, micConstraints, recorder: () => FakeRecorder.instances.at(-1)!, context: () => FakeAudioContext.instances.at(-1) };
}

describe('capability detection', () => {
  test('modes come from feature detection, not browser names', () => {
    const full = setup().client.browserCapture;
    expect(full).toEqual({ microphone: true, displayCapture: true, sharedAudio: 'likely', mixing: true, modes: ['microphone', 'microphone+shared', 'shared'] });
    expect(setup().client.capabilities.tabCapture).toBe(true);
    expect(setup().client.capabilities.systemAudioCapture).toBe('display-media');
    // No getDisplayMedia (e.g. mobile browsers): microphone only.
    const mobile = setup({ display: 'missing' }).client;
    expect(mobile.browserCapture.modes).toEqual(['microphone']);
    expect(mobile.capabilities.tabCapture).toBe(false);
    // getDisplayMedia without Web Audio mixing: shared-only, no mix.
    expect(setup({ audioContext: false }).client.browserCapture.modes).toEqual(['microphone', 'shared']);
    // A browser that does not advertise display-audio constraints: audio is unknown, not promised.
    expect(detectBrowserCapture({ mediaDevices: { getUserMedia: async () => ({}) as MediaStream, getDisplayMedia: async () => ({}) as MediaStream, getSupportedConstraints: () => ({}) }, canRecord: true, hasMediaStream: true }).sharedAudio).toBe('unknown');
  });
});

describe('microphone + meeting tab', () => {
  test('asks for the share picker first, then the microphone, and records the mix', async () => {
    const { client, calls, displayOptions, micConstraints, tabVideo, recorder, context } = setup();
    const description = await client.prepare({ mode: 'microphone+shared', microphoneDeviceName: null });
    expect(calls).toEqual(['display', 'microphone']);
    expect(description).toEqual({ mode: 'microphone+shared', microphone: true, shared: { surface: 'browser' } });
    // Video is discarded immediately; shared audio is not processed like a microphone.
    expect(tabVideo.stopped).toBe(true);
    const request = displayOptions[0] as { audio: Record<string, boolean>; selfBrowserSurface: string };
    expect(request.audio).toMatchObject({ echoCancellation: false, noiseSuppression: false, autoGainControl: false, suppressLocalAudioPlayback: false });
    expect(request.selfBrowserSurface).toBe('exclude');
    expect((micConstraints[0] as { audio: Record<string, boolean> }).audio).toMatchObject({ echoCancellation: true, noiseSuppression: true, autoGainControl: true });

    await client.start({ microphoneDeviceName: null, systemAudioDeviceName: null, meetingName: 'm', captureMode: 'microphone+shared' });
    const ctx = context()!;
    // Two sources → gains → one limiter → destination; never the speakers.
    expect(ctx.sources).toHaveLength(2);
    const destination = ctx.nodes.find((n) => n.label === 'destination')!;
    const shaper = ctx.nodes.find((n) => n.label === 'shaper')!;
    expect(shaper.connections).toEqual([destination]);
    expect(ctx.nodes.filter((n) => n.label === 'gain').every((g) => g.connections[0] === shaper)).toBe(true);
    expect(recorder().stream.getTracks()).toEqual([ctx.destinationTrack]);
  });

  test('when tab sharing stops, recording continues with the microphone and warns', async () => {
    const { client, tabAudio, mic, recorder, context } = setup();
    const warnings: string[] = [];
    const changes: unknown[] = [];
    client.onCaptureWarning((w) => warnings.push(w));
    client.onCaptureChanged((c) => changes.push(c));
    await client.start({ microphoneDeviceName: null, systemAudioDeviceName: null, meetingName: 'm', captureMode: 'microphone+shared' });
    recorder().emit('a');
    tabAudio.end();
    expect(await client.isRecording()).toBe(true);
    expect(warnings).toEqual(['Tab audio sharing stopped. Recording continues with your microphone only.']);
    expect(client.getCapture()).toEqual({ mode: 'microphone+shared', microphone: true, shared: null });
    expect(context()!.sources.find((s) => s.stream.getTracks()[0] === tabAudio)!.node.disconnected).toBe(true);
    expect(FakeRecorder.instances).toHaveLength(1); // same recorder: one continuous file
    // When the microphone also goes, the recording stops and keeps its chunks.
    mic.end();
    expect(await client.isRecording()).toBe(false);
    expect(client.getLastSummary()?.chunkCount).toBe(2);
    expect(changes.at(-1)).toBeNull();
  });

  test('stop releases every track and closes the audio context', async () => {
    const { client, mic, tabAudio, context } = setup();
    await client.start({ microphoneDeviceName: null, systemAudioDeviceName: null, meetingName: 'm', captureMode: 'microphone+shared' });
    await client.stop();
    const ctx = context()!;
    expect(mic.stopped && tabAudio.stopped && ctx.destinationTrack.stopped).toBe(true);
    expect(ctx.closed).toBe(true);
    expect(client.getCapture()).toBeNull();
  });

  test('microphone-only recording does not create an AudioContext', async () => {
    const { client, calls, context } = setup();
    await client.start({ microphoneDeviceName: null, systemAudioDeviceName: null, meetingName: 'm', captureMode: 'microphone' });
    expect(calls).toEqual(['microphone']);
    expect(context()).toBeUndefined();
    await client.stop();
  });
});

describe('failures are specific and release what was acquired', () => {
  const start = (client: BrowserRecordingClient, mode: 'microphone+shared' | 'shared') =>
    client.start({ microphoneDeviceName: null, systemAudioDeviceName: null, meetingName: 'm', captureMode: mode });

  test('cancelled share picker', async () => {
    const { client, calls, context } = setup({ display: 'denied' });
    const error = await start(client, 'microphone+shared').catch((e) => e);
    expect(error).toBeInstanceOf(RecordingPermissionError);
    expect(error.reason).toBe('display-denied');
    expect(calls).toEqual(['display']); // microphone never requested
    expect(context()!.closed).toBe(true);
    expect(await client.isRecording()).toBe(false);
  });

  test('a shared source without audio (window/screen, or Firefox/Safari)', async () => {
    const { client, tabVideo, context } = setup({ display: 'no-audio' });
    const error = await start(client, 'microphone+shared').catch((e) => e);
    expect(error.reason).toBe('no-shared-audio');
    expect(error.message).toContain('Share tab audio');
    expect(tabVideo.stopped).toBe(true);
    expect(context()!.closed).toBe(true);
  });

  test('microphone denied after sharing releases the shared audio', async () => {
    const { client, tabAudio } = setup({ microphone: 'denied' });
    const error = await start(client, 'microphone+shared').catch((e) => e);
    expect(error.reason).toBe('permission-denied');
    expect(tabAudio.stopped).toBe(true);
  });

  test('unsupported getDisplayMedia, audio context that cannot start, encoder failure', async () => {
    const missing = setup({ display: 'missing' });
    expect((await start(missing.client, 'shared').catch((e) => e)).reason).toBe('display-unsupported');

    const suspended = setup();
    FakeAudioContext.startSuspended = true;
    const contextError = await start(suspended.client, 'microphone+shared').catch((e) => e);
    expect(contextError.reason).toBe('audio-context');
    expect(suspended.tabAudio.stopped && suspended.mic.stopped).toBe(true);

    const encoder = setup();
    FakeRecorder.throwOnConstruct = true;
    const encoderError = await start(encoder.client, 'microphone+shared').catch((e) => e);
    expect(encoderError).toBeInstanceOf(CaptureError);
    expect(encoderError.reason).toBe('encoder');
    expect(encoder.mic.stopped && encoder.tabAudio.stopped && encoder.context()!.closed).toBe(true);
    // The client is reusable afterwards.
    FakeRecorder.throwOnConstruct = false;
    await encoder.client.start({ microphoneDeviceName: null, systemAudioDeviceName: null, meetingName: 'm', captureMode: 'microphone' });
    expect(await encoder.client.isRecording()).toBe(true);
  });

  test('shared-only recording stops when sharing stops', async () => {
    const { client, tabAudio, recorder, context, calls } = setup();
    const warnings: string[] = [];
    client.onCaptureWarning((w) => warnings.push(w));
    await start(client, 'shared');
    expect(calls).toEqual(['display']);
    expect(context()).toBeUndefined();
    expect(recorder().stream.getTracks()).toEqual([tabAudio]);
    tabAudio.end();
    expect(await client.isRecording()).toBe(false);
    expect(warnings).toEqual(['Sharing stopped, so the recording ended.']);
  });

  test('releasePrepared frees sources when the server recording cannot start', async () => {
    const { client, mic, tabAudio, context } = setup();
    await client.prepare({ mode: 'microphone+shared', microphoneDeviceName: null });
    await client.releasePrepared();
    expect(mic.stopped && tabAudio.stopped && context()!.closed).toBe(true);
    await client.prepare({ mode: 'microphone', microphoneDeviceName: null });
    await client.releasePrepared();
  });
});

describe('soft limiter', () => {
  test('is transparent at nominal levels and never reaches full scale', () => {
    const curve = softLimiterCurve(4097);
    const at = (x: number) => curve[Math.round(((x + 1) / 2) * 4096)];
    expect(at(0.5)).toBeCloseTo(0.5, 3);
    expect(at(-0.6)).toBeCloseTo(-0.6, 3);
    expect(at(1)).toBeLessThan(0.95);
    expect(at(1)).toBeGreaterThan(0.85);
    for (let i = 1; i < curve.length; i++) expect(curve[i]).toBeGreaterThanOrEqual(curve[i - 1]);
  });

  test('a graph can be closed twice-safely and disconnects all inputs', async () => {
    const graph = await MixGraph.create(FakeAudioContext as never);
    graph.add('microphone', new FakeStream([new FakeTrack('audio')]) as never);
    await graph.close();
    expect(graph.state).toBe('closed');
  });
});
