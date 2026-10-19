import { describe, expect, test } from 'bun:test';
import type { ServerMediaAsset, ServerMediaClient, UploadProgress } from '../../src/clients/contracts';
import { VoxaApiError, VoxaTransportError } from '../../src/clients/http/transport';
import { MemoryPendingStore } from '../../src/web/recording/pending-store';
import { ABANDONED_AFTER_MS, RecordingUploadCoordinator, type RecordingLocks, type Scheduler } from '../../src/web/recording/upload-coordinator';

/** In-memory server with the Go server's semantics and injectable faults. */
class FakeServer implements ServerMediaClient {
  offline = false;
  failNext: Array<'503' | 'network' | 'lost-response' | 'conflict'> = [];
  failFinalize: Array<'network'> = [];
  readonly chunks = new Map<string, Map<number, string>>();
  readonly assets = new Map<string, ServerMediaAsset>();
  finalizeCalls = 0;
  uploadCalls = 0;
  private next = 0;

  private guard() {
    if (this.offline) throw new VoxaTransportError('network', 'offline');
  }

  async createRecording(meetingId: string, mimeType: string) {
    this.guard();
    const id = `med_${++this.next}`;
    const asset: ServerMediaAsset = { id, meetingId, mimeType, status: 'uploading', durationSeconds: null, sizeBytes: 0, createdAt: 't', updatedAt: 't' };
    this.assets.set(id, asset);
    this.chunks.set(id, new Map());
    return { asset, recordingSessionId: `rec_${this.next}`, maxChunkBytes: 1 << 20, maxTotalBytes: 1 << 30 };
  }

  private progress(id: string): UploadProgress {
    const stored = this.chunks.get(id)!;
    return { status: this.assets.get(id)!.status, receivedChunks: [...stored.keys()].sort((a, b) => a - b), receivedBytes: 0 };
  }

  async uploadChunk(id: string, index: number, data: Blob) {
    this.uploadCalls++;
    this.guard();
    const fault = this.failNext.shift();
    if (fault === '503') throw new VoxaApiError(503, 'unavailable', 'Service unavailable', 'r', null);
    if (fault === 'network') throw new VoxaTransportError('network', 'offline');
    if (fault === 'conflict') throw new VoxaApiError(409, 'conflict', 'Resource conflict', 'r', { reason: 'chunk_mismatch' });
    const text = await data.text();
    const stored = this.chunks.get(id)!;
    if (stored.has(index) && stored.get(index) !== text) throw new VoxaApiError(409, 'conflict', 'Resource conflict', 'r', { reason: 'chunk_mismatch' });
    stored.set(index, text);
    if (fault === 'lost-response') throw new VoxaTransportError('network', 'response lost');
    return this.progress(id);
  }

  async getUploadStatus(id: string) {
    this.guard();
    return this.progress(id);
  }

  async finalize(id: string, count: number, durationSeconds: number | null) {
    this.finalizeCalls++;
    this.guard();
    if (this.failFinalize.shift()) throw new VoxaTransportError('network', 'offline');
    const asset = this.assets.get(id)!;
    if (asset.status === 'ready') return asset;
    const stored = this.chunks.get(id)!;
    for (let i = 0; i < count; i++) {
      if (!stored.has(i)) throw new VoxaApiError(409, 'conflict', 'Resource conflict', 'r', { reason: 'missing_chunks' });
    }
    const ready: ServerMediaAsset = { ...asset, status: 'ready', durationSeconds, sizeBytes: [...stored.values()].join('').length };
    this.assets.set(id, ready);
    return ready;
  }

  content(id: string) {
    const stored = this.chunks.get(id)!;
    return [...stored.keys()].sort((a, b) => a - b).map((i) => stored.get(i)).join('');
  }

  async listMeetingMedia() { return [...this.assets.values()]; }
  async getPlaybackUrl(id: string) { return { url: `https://api/${id}`, expiresAt: '' }; }
  async deleteMedia(id: string) { this.assets.delete(id); }
}

class ManualScheduler implements Scheduler {
  tasks = new Map<number, { callback: () => void; ms: number }>();
  private id = 0;
  set(callback: () => void, ms: number) { this.tasks.set(++this.id, { callback, ms }); return this.id; }
  clear(handle: unknown) { this.tasks.delete(handle as number); }
  /** Runs due retry timers (not heartbeats). */
  runRetries() {
    for (const [id, task] of [...this.tasks]) {
      if (task.ms < 20_000) {
        this.tasks.delete(id);
        task.callback();
      }
    }
  }
  retryDelays() { return [...this.tasks.values()].map((t) => t.ms).filter((ms) => ms < 20_000); }
}

async function settle() {
  for (let i = 0; i < 50; i++) await new Promise((resolve) => setTimeout(resolve, 0));
}

function setup(options: { store?: MemoryPendingStore; server?: FakeServer; locks?: RecordingLocks; now?: () => number } = {}) {
  const server = options.server ?? new FakeServer();
  const store = options.store ?? new MemoryPendingStore();
  const scheduler = new ManualScheduler();
  let online = true;
  let onlineListener: (() => void) | null = null;
  const coordinator = new RecordingUploadCoordinator({
    media: server, store, scheduler, locks: options.locks, now: options.now,
    isOnline: () => online,
    onOnline: (callback) => { onlineListener = callback; return () => { onlineListener = null; }; },
    backoffMs: [1_000, 2_000, 5_000],
  });
  return {
    server, store, scheduler, coordinator,
    goOffline: () => { online = false; server.offline = true; },
    goOnline: () => { online = true; server.offline = false; onlineListener?.(); },
  };
}

const blob = (text: string) => new Blob([text]);

describe('RecordingUploadCoordinator', () => {
  test('uploads chunks in order and finalizes after stop', async () => {
    const { coordinator, server, store } = setup();
    const { mediaAssetId } = await coordinator.begin('mtg_1', 'audio/webm;codecs=opus');
    expect(coordinator.getJob(mediaAssetId)?.phase).toBe('recording');
    for (const [index, text] of ['A', 'B', 'C'].entries()) await coordinator.addChunk(mediaAssetId, index, blob(text));
    await settle();
    expect(coordinator.getJob(mediaAssetId)?.uploadedChunks).toBe(3);
    await coordinator.complete(mediaAssetId, 3, 12.5);
    await settle();
    const job = coordinator.getJob(mediaAssetId)!;
    expect(job.phase).toBe('ready');
    expect(job.asset?.durationSeconds).toBe(12.5);
    expect(server.content(mediaAssetId)).toBe('ABC');
    expect(server.finalizeCalls).toBe(1);
    expect(await store.listRecordings()).toEqual([]);
    expect(await store.listChunkIndexes(mediaAssetId)).toEqual([]);
  });

  test('keeps recording offline, holds chunks locally, and resumes when online', async () => {
    const { coordinator, server, store, goOffline, goOnline } = setup();
    const { mediaAssetId } = await coordinator.begin('mtg_1', 'audio/webm');
    goOffline();
    await coordinator.addChunk(mediaAssetId, 0, blob('A'));
    await coordinator.addChunk(mediaAssetId, 1, blob('B'));
    await settle();
    let job = coordinator.getJob(mediaAssetId)!;
    expect(job.phase).toBe('recording');
    expect(job.waitingForNetwork).toBe(true);
    expect(job.pendingChunks).toBe(2);
    // Nothing was deleted locally without acknowledgement.
    expect(await store.listChunkIndexes(mediaAssetId)).toEqual([0, 1]);
    await coordinator.complete(mediaAssetId, 2, 4);
    await settle();
    expect(coordinator.getJob(mediaAssetId)?.phase).toBe('waiting');
    goOnline();
    await settle();
    job = coordinator.getJob(mediaAssetId)!;
    expect(job.phase).toBe('ready');
    expect(server.content(mediaAssetId)).toBe('AB');
  });

  test('retries transient server errors with backoff', async () => {
    const { coordinator, server, scheduler } = setup();
    const { mediaAssetId } = await coordinator.begin('mtg_1', 'audio/webm');
    server.failNext = ['503', '503'];
    await coordinator.addChunk(mediaAssetId, 0, blob('A'));
    await settle();
    expect(scheduler.retryDelays()).toEqual([1_000]);
    scheduler.runRetries();
    await settle();
    expect(scheduler.retryDelays()).toEqual([2_000]);
    scheduler.runRetries();
    await settle();
    expect(scheduler.retryDelays()).toEqual([]);
    expect(server.content(mediaAssetId)).toBe('A');
  });

  test('a lost response is retried without duplicating media', async () => {
    const { coordinator, server, scheduler } = setup();
    const { mediaAssetId } = await coordinator.begin('mtg_1', 'audio/webm');
    server.failNext = ['lost-response'];
    await coordinator.addChunk(mediaAssetId, 0, blob('A'));
    await settle();
    scheduler.runRetries();
    await settle();
    await coordinator.complete(mediaAssetId, 1, null);
    await settle();
    expect(server.uploadCalls).toBe(2);
    expect(server.content(mediaAssetId)).toBe('A');
    expect(coordinator.getJob(mediaAssetId)?.phase).toBe('ready');
  });

  test('finalization is retried until it succeeds', async () => {
    const { coordinator, server, scheduler } = setup();
    const { mediaAssetId } = await coordinator.begin('mtg_1', 'audio/webm');
    await coordinator.addChunk(mediaAssetId, 0, blob('A'));
    server.failFinalize = ['network'];
    await coordinator.complete(mediaAssetId, 1, 1);
    await settle();
    expect(coordinator.getJob(mediaAssetId)?.phase).toBe('waiting');
    scheduler.runRetries();
    await settle();
    expect(coordinator.getJob(mediaAssetId)?.phase).toBe('ready');
    expect(server.finalizeCalls).toBe(2);
  });

  test('non-retryable conflicts surface an error and can be retried manually', async () => {
    const { coordinator, server, scheduler, store } = setup();
    const { mediaAssetId } = await coordinator.begin('mtg_1', 'audio/webm');
    server.failNext = ['conflict'];
    await coordinator.addChunk(mediaAssetId, 0, blob('A'));
    await settle();
    const job = coordinator.getJob(mediaAssetId)!;
    expect(job.phase).toBe('error');
    expect(job.error).toContain('different data');
    expect(scheduler.retryDelays()).toEqual([]);
    expect(await store.listChunkIndexes(mediaAssetId)).toEqual([0]);
    coordinator.retry(mediaAssetId);
    await settle();
    expect(server.content(mediaAssetId)).toBe('A');
  });

  test('a reloaded page resumes pending chunks and finalizes the recovered recording', async () => {
    const store = new MemoryPendingStore();
    const server = new FakeServer();
    const first = setup({ store, server });
    const { mediaAssetId } = await first.coordinator.begin('mtg_1', 'audio/webm');
    await first.coordinator.addChunk(mediaAssetId, 0, blob('A'));
    await settle();
    first.goOffline();
    await first.coordinator.addChunk(mediaAssetId, 1, blob('B'));
    await first.coordinator.addChunk(mediaAssetId, 2, blob('C'));
    await settle();
    // The page dies mid-recording: the coordinator is gone, IndexedDB remains.
    first.coordinator.dispose();
    server.offline = false;

    const later = Date.now() + ABANDONED_AFTER_MS + 1;
    const second = setup({ store, server, now: () => later });
    expect(await second.coordinator.recoverPending()).toEqual([mediaAssetId]);
    await settle();
    const job = second.coordinator.getJob(mediaAssetId)!;
    expect(job.recovered).toBe(true);
    expect(job.phase).toBe('ready');
    expect(server.content(mediaAssetId)).toBe('ABC');
    expect(job.asset?.durationSeconds).toBeNull();
    expect(await store.listRecordings()).toEqual([]);
  });

  test('chunks the server already has are not uploaded again on recovery', async () => {
    const store = new MemoryPendingStore();
    const server = new FakeServer();
    const first = setup({ store, server });
    const { mediaAssetId } = await first.coordinator.begin('mtg_1', 'audio/webm');
    await first.coordinator.addChunk(mediaAssetId, 0, blob('A'));
    await settle();
    // Server got chunk 0, recording stopped, but the page died before the local copy was deleted.
    first.coordinator.dispose();
    await store.putChunk(mediaAssetId, 0, blob('A'));
    await store.saveRecording({ ...(await store.getRecording(mediaAssetId))!, state: 'stopped', chunkCount: 1, durationSeconds: 2 });
    const uploadsBefore = server.uploadCalls;
    const second = setup({ store, server });
    await second.coordinator.recoverPending();
    await settle();
    expect(server.uploadCalls).toBe(uploadsBefore);
    expect(second.coordinator.getJob(mediaAssetId)?.phase).toBe('ready');
  });

  test('recordings owned by another live tab are not taken over', async () => {
    const store = new MemoryPendingStore();
    const server = new FakeServer();
    const owner = setup({ store, server });
    const { mediaAssetId } = await owner.coordinator.begin('mtg_1', 'audio/webm');
    await owner.coordinator.addChunk(mediaAssetId, 0, blob('A'));
    // With Web Locks: the lock is held elsewhere.
    const heldElsewhere: RecordingLocks = { supported: true, acquire: async () => null };
    expect(await setup({ store, server, locks: heldElsewhere }).coordinator.recoverPending()).toEqual([]);
    // Without Web Locks: a recently active recording is presumed live.
    expect(await setup({ store, server, now: () => Date.now() }).coordinator.recoverPending()).toEqual([]);
    expect((await store.getRecording(mediaAssetId))?.state).toBe('recording');
  });

  test('discard removes local data and the server recording', async () => {
    const { coordinator, server, store } = setup();
    const { mediaAssetId } = await coordinator.begin('mtg_1', 'audio/webm');
    await coordinator.addChunk(mediaAssetId, 0, blob('A'));
    await coordinator.discard(mediaAssetId);
    expect(await store.listRecordings()).toEqual([]);
    expect(server.assets.has(mediaAssetId)).toBe(false);
    expect(coordinator.getJob(mediaAssetId)).toBeNull();
  });
});
