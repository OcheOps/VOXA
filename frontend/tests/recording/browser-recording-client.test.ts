import { describe, expect, test } from 'bun:test';
import {
  BrowserRecordingClient,
  RecordingPermissionError,
  selectRecordingMimeType,
  type RecorderLike,
} from '../../src/clients/browser/recording-client';
import { UnsupportedOperationError } from '../../src/clients/errors';

class FakeTrack {
  stopped = false;
  onended: (() => void) | null = null;
  stop() { this.stopped = true; }
}

function fakeStream() {
  const track = new FakeTrack();
  return { track, stream: { getTracks: () => [track], getAudioTracks: () => [track] } as unknown as MediaStream };
}

class FakeRecorder implements RecorderLike {
  static supported = new Set(['audio/webm;codecs=opus', 'audio/webm']);
  static instances: FakeRecorder[] = [];
  static throwOnConstruct = false;
  static isTypeSupported(type: string) { return FakeRecorder.supported.has(type); }
  state: 'inactive' | 'recording' | 'paused' = 'inactive';
  mimeType: string;
  timeslice?: number;
  ondataavailable: ((event: { data: Blob }) => void) | null = null;
  onstop: (() => void) | null = null;
  onerror: ((event: unknown) => void) | null = null;
  constructor(_stream: MediaStream, options: { mimeType: string }) {
    if (FakeRecorder.throwOnConstruct) throw new Error('NotSupportedError');
    this.mimeType = options.mimeType;
    FakeRecorder.instances.push(this);
  }
  start(timeslice?: number) { this.state = 'recording'; this.timeslice = timeslice; }
  pause() { this.state = 'paused'; }
  resume() { this.state = 'recording'; }
  emit(text: string) { this.ondataavailable?.({ data: new Blob([text]) }); }
  stop() {
    this.state = 'inactive';
    this.emit('final');
    this.onstop?.();
  }
}

function setup(overrides: { getUserMedia?: () => Promise<MediaStream>; recorder?: unknown; devices?: MediaDeviceInfo[] } = {}) {
  let now = 1_000;
  const { stream, track } = fakeStream();
  const constraints: unknown[] = [];
  FakeRecorder.instances = [];
  FakeRecorder.throwOnConstruct = false;
  const client = new BrowserRecordingClient({
    mediaDevices: {
      getUserMedia: (async (c: unknown) => { constraints.push(c); return overrides.getUserMedia ? overrides.getUserMedia() : stream; }) as never,
      enumerateDevices: async () => overrides.devices ?? [],
    },
    MediaRecorder: (overrides.recorder ?? FakeRecorder) as never,
    now: () => now,
    timesliceMs: 15_000,
  });
  return { client, track, constraints, advance: (ms: number) => { now += ms; }, recorder: () => FakeRecorder.instances.at(-1)! };
}

describe('MIME negotiation', () => {
  test('picks the first supported preference', () => {
    expect(selectRecordingMimeType((t) => t === 'audio/ogg;codecs=opus' || t === 'audio/mp4')).toBe('audio/ogg;codecs=opus');
    expect(selectRecordingMimeType(() => false)).toBeNull();
    expect(selectRecordingMimeType((t) => { if (t.includes('webm')) throw new Error('boom'); return t === 'audio/mp4'; })).toBe('audio/mp4');
  });

  test('reports capabilities honestly', () => {
    const { client } = setup();
    expect(client.mimeType).toBe('audio/webm;codecs=opus');
    expect(client.capabilities).toEqual({
      microphoneCapture: true, systemAudioCapture: 'unavailable', deviceEnumeration: 'input-only', microphoneHotSwap: 'unavailable',
      audioLevelMonitoring: false, pauseResume: true, tabCapture: false,
    });
    const unsupported = new BrowserRecordingClient({ now: () => 0, timesliceMs: 1 });
    expect(unsupported.capabilities.microphoneCapture).toBe(false);
    expect(unsupported.capabilities.deviceEnumeration).toBe('unavailable');
    expect(unsupported.mimeType).toBeNull();
  });
});

describe('BrowserRecordingClient', () => {
  test('records chunks, pauses and resumes, and stops cleanly', async () => {
    const { client, track, advance, recorder } = setup();
    const chunks: Array<{ index: number; text: string }> = [];
    const events: string[] = [];
    const unsubscribes = [
      client.onChunk(async (chunk) => chunks.push({ index: chunk.index, text: await chunk.data.text() })),
      client.onStarted(() => events.push('started')),
      client.onPaused(() => events.push('paused')),
      client.onResumed(() => events.push('resumed')),
      client.onStopped(() => events.push('stopped')),
    ];
    await client.start({ microphoneDeviceName: null, systemAudioDeviceName: null, meetingName: 'Standup' });
    expect(recorder().timeslice).toBe(15_000);
    expect(await client.isRecording()).toBe(true);
    recorder().emit('a');
    advance(10_000);
    await client.pause();
    expect((await client.getState()).isPaused).toBe(true);
    advance(5_000);
    await client.resume();
    advance(10_000);
    recorder().emit('b');
    recorder().ondataavailable?.({ data: new Blob([]) }); // empty slices are ignored
    await client.stop();
    await Promise.resolve();
    expect(events).toEqual(['started', 'paused', 'resumed', 'stopped']);
    expect(chunks.map((c) => c.index)).toEqual([0, 1, 2]);
    expect(chunks.map((c) => c.text)).toEqual(['a', 'b', 'final']);
    expect(client.getLastSummary()).toEqual({ chunkCount: 3, durationSeconds: 20, mimeType: 'audio/webm;codecs=opus' });
    expect(track.stopped).toBe(true);
    expect(recorder().ondataavailable).toBeNull();
    expect((await client.getState()).isRecording).toBe(false);
    unsubscribes.forEach((unsubscribe) => unsubscribe());
    expect(client.listenerCount()).toBe(0);
  });

  test('permission and device errors are typed and leave the client reusable', async () => {
    for (const [name, reason] of [['NotAllowedError', 'permission-denied'], ['NotFoundError', 'no-microphone'], ['NotReadableError', 'microphone-busy']] as const) {
      const { client } = setup({ getUserMedia: async () => { throw Object.assign(new Error(name), { name }); } });
      const error = await client.start({ microphoneDeviceName: null, systemAudioDeviceName: null, meetingName: 'x' }).catch((e: unknown) => e);
      expect(error).toBeInstanceOf(RecordingPermissionError);
      expect((error as RecordingPermissionError).reason).toBe(reason);
      expect(await client.isRecording()).toBe(false);
    }
  });

  test('a recorder that cannot start releases the microphone', async () => {
    const { client, track } = setup();
    FakeRecorder.throwOnConstruct = true;
    await expect(client.start({ microphoneDeviceName: null, systemAudioDeviceName: null, meetingName: 'x' })).rejects.toBeInstanceOf(UnsupportedOperationError);
    expect(track.stopped).toBe(true);
    FakeRecorder.throwOnConstruct = false;
    await client.start({ microphoneDeviceName: null, systemAudioDeviceName: null, meetingName: 'x' });
    expect(await client.isRecording()).toBe(true);
  });

  test('refuses unsupported or overlapping recordings', async () => {
    const { client } = setup();
    await expect(client.start({ microphoneDeviceName: null, systemAudioDeviceName: 'Tab audio', meetingName: 'x' })).rejects.toBeInstanceOf(UnsupportedOperationError);
    await client.start({ microphoneDeviceName: null, systemAudioDeviceName: null, meetingName: 'x' });
    await expect(client.start({ microphoneDeviceName: null, systemAudioDeviceName: null, meetingName: 'x' })).rejects.toThrow('already in progress');
    const unsupported = new BrowserRecordingClient({ now: () => 0, timesliceMs: 1 });
    await expect(unsupported.start({ microphoneDeviceName: null, systemAudioDeviceName: null, meetingName: 'x' })).rejects.toBeInstanceOf(UnsupportedOperationError);
  });

  test('a lost microphone stops the recording and keeps its chunks', async () => {
    const { client, track, recorder } = setup();
    const chunks: number[] = [];
    let unavailable = false;
    client.onChunk((chunk) => chunks.push(chunk.index));
    client.onMicrophoneUnavailable(() => { unavailable = true; });
    await client.start({ microphoneDeviceName: null, systemAudioDeviceName: null, meetingName: 'x' });
    recorder().emit('a');
    track.onended?.();
    await Promise.resolve();
    expect(unavailable).toBe(true);
    expect(chunks).toEqual([0, 1]);
    expect(await client.isRecording()).toBe(false);
  });

  test('device selection maps names to device ids', async () => {
    const { client, constraints } = setup({
      devices: [
        { kind: 'audioinput', label: 'USB Mic', deviceId: 'usb-1' },
        { kind: 'audioinput', label: '', deviceId: 'default' },
        { kind: 'videoinput', label: 'Camera', deviceId: 'cam' },
      ] as MediaDeviceInfo[],
    });
    expect(await client.listDevices()).toEqual([{ name: 'USB Mic', type: 'input' }, { name: 'Default microphone', type: 'input' }]);
    await client.start({ microphoneDeviceName: 'USB Mic', systemAudioDeviceName: null, meetingName: 'x' });
    expect(JSON.stringify(constraints[0])).toContain('"exact":"usb-1"');
  });
});
