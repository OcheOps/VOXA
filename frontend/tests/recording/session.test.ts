import { describe, expect, test } from 'bun:test';
import { BrowserRecordingClient, type RecorderLike } from '../../src/clients/browser/recording-client';
import type { ServerMediaAsset, ServerMediaClient } from '../../src/clients/contracts';
import { VoxaTransportError } from '../../src/clients/http/transport';
import { MemoryPendingStore } from '../../src/web/recording/pending-store';
import { RecordingSessionController } from '../../src/web/recording/session';
import { RecordingUploadCoordinator } from '../../src/web/recording/upload-coordinator';

class Recorder implements RecorderLike {
  static last: Recorder;
  static isTypeSupported = (type: string) => type === 'audio/webm;codecs=opus';
  state: 'inactive' | 'recording' | 'paused' = 'inactive';
  mimeType = 'audio/webm;codecs=opus';
  ondataavailable: ((event: { data: Blob }) => void) | null = null;
  onstop: (() => void) | null = null;
  onerror: ((event: unknown) => void) | null = null;
  constructor() { Recorder.last = this; }
  start() { this.state = 'recording'; }
  pause() { this.state = 'paused'; }
  resume() { this.state = 'recording'; }
  stop() { this.state = 'inactive'; this.ondataavailable?.({ data: new Blob(['tail']) }); this.onstop?.(); }
}

function server(options: { failCreate?: boolean } = {}) {
  const deleted: string[] = [];
  const finalized: Array<{ count: number; duration: number | null }> = [];
  const captures: unknown[] = [];
  const chunks = new Map<number, string>();
  const asset: ServerMediaAsset = { id: 'med_1', meetingId: 'mtg_1', mimeType: 'audio/webm;codecs=opus', status: 'uploading', durationSeconds: null, sizeBytes: 0, createdAt: '', updatedAt: '', capture: null };
  const media: ServerMediaClient = {
    createRecording: async (_meetingId, _mime, capture) => {
      captures.push(capture);
      if (options.failCreate) throw new VoxaTransportError('network', 'offline');
      return { asset, recordingSessionId: 'rec_1', maxChunkBytes: 1 << 20, maxTotalBytes: 1 << 30 };
    },
    uploadChunk: async (_id, index, data) => { chunks.set(index, await data.text()); return { status: 'uploading', receivedChunks: [...chunks.keys()], receivedBytes: 0 }; },
    getUploadStatus: async () => ({ status: 'uploading', receivedChunks: [...chunks.keys()], receivedBytes: 0 }),
    finalize: async (_id, count, duration) => { finalized.push({ count, duration }); return { ...asset, status: 'ready', durationSeconds: duration }; },
    listMeetingMedia: async () => [],
    getPlaybackUrl: async () => ({ url: '', expiresAt: '' }),
    deleteMedia: async (id) => void deleted.push(id),
  };
  return { media, deleted, finalized, chunks, captures };
}

function setup(getUserMedia: () => Promise<MediaStream>, options: { failCreate?: boolean } = {}) {
  const fake = server(options);
  const store = new MemoryPendingStore();
  const uploads = new RecordingUploadCoordinator({ media: fake.media, store, isOnline: () => true, onOnline: () => () => {} });
  const recorder = new BrowserRecordingClient({
    mediaDevices: { getUserMedia: getUserMedia as never, enumerateDevices: async () => [] },
    MediaRecorder: Recorder as never, now: () => 0, timesliceMs: 1000,
  });
  return { ...fake, store, uploads, session: new RecordingSessionController(recorder, uploads) };
}

const track = () => ({ stop() {}, onended: null as null | (() => void) });
const stream = (t = track()) => ({ getTracks: () => [t], getAudioTracks: () => [t] }) as unknown as MediaStream;
const settle = async () => { for (let i = 0; i < 30; i++) await new Promise((r) => setTimeout(r, 0)); };

describe('RecordingSessionController', () => {
  test('a full recording ends with a finalized upload', async () => {
    const { session, finalized, chunks, uploads } = setup(async () => stream());
    await session.start('mtg_1', null);
    expect(session.getState().phase).toBe('recording');
    Recorder.last.ondataavailable?.({ data: new Blob(['head']) });
    await session.stop();
    await settle();
    expect(session.getState().phase).toBe('idle');
    expect([...chunks.values()]).toEqual(['head', 'tail']);
    expect(finalized).toEqual([{ count: 2, duration: 0 }]);
    expect(uploads.getJob('med_1')?.phase).toBe('ready');
  });

  test('denied microphone fails before any server recording or local state exists', async () => {
    let created = 0;
    const { session, deleted, store, media } = setup(async () => { throw Object.assign(new Error('denied'), { name: 'NotAllowedError' }); });
    const create = media.createRecording;
    media.createRecording = async (...args) => { created++; return create(...args); };
    await session.start('mtg_1', null);
    expect(session.getState()).toMatchObject({ phase: 'idle', error: 'Microphone access was denied.' });
    expect(created).toBe(0);
    expect(deleted).toEqual([]);
    expect(await store.listRecordings()).toEqual([]);
  });

  test('a recorder that stops on its own (lost microphone) still completes the upload', async () => {
    const t = track();
    const { session, finalized } = setup(async () => stream(t));
    await session.start('mtg_1', null);
    t.onended?.();
    await settle();
    expect(session.getState().phase).toBe('idle');
    expect(finalized).toEqual([{ count: 1, duration: 0 }]);
  });

  test('cannot start without reaching the server, and releases the microphone', async () => {
    let stopped = false;
    const t = { stop() { stopped = true; }, onended: null as null | (() => void) };
    const { session } = setup(async () => stream(t), { failCreate: true });
    await session.start('mtg_1', null);
    expect(session.getState().phase).toBe('idle');
    expect(session.getState().error).toContain('Could not start a recording');
    expect(stopped).toBe(true);
  });

  test('reports what is captured while recording, and sends capture provenance at creation', async () => {
    const { session, captures } = setup(async () => stream());
    await session.start('mtg_1', null, 'microphone');
    expect(session.getState().capture).toEqual({ mode: 'microphone', microphone: true, shared: null });
    expect(captures).toEqual([{ mode: 'microphone', client: 'web', sharedSurface: null }]);
    await session.stop();
    await settle();
    expect(session.getState().capture).toBeNull();
  });
});

describe('capture provenance', () => {
  test('maps capture descriptions to the server model without anything identifying', async () => {
    const { captureProvenance } = await import('../../src/web/recording/session');
    expect(captureProvenance({ mode: 'microphone+shared', microphone: true, shared: { surface: 'browser' } })).toEqual({ mode: 'microphone_and_shared_audio', client: 'web', sharedSurface: 'browser' });
    expect(captureProvenance({ mode: 'shared', microphone: false, shared: { surface: 'window' } })).toEqual({ mode: 'shared_audio', client: 'web', sharedSurface: 'window' });
    expect(captureProvenance({ mode: 'microphone', microphone: true, shared: null })).toEqual({ mode: 'microphone', client: 'web', sharedSurface: null });
  });
});
