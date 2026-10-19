import type { ServerMediaAsset, ServerMediaClient } from '@/clients/contracts';
import { VoxaApiError, VoxaTransportError } from '@/clients/http/transport';
import type { PendingChunkStore, PendingRecording } from './pending-store';

export type UploadPhase = 'recording' | 'waiting' | 'uploading' | 'finalizing' | 'ready' | 'error';

export interface UploadJobState {
  mediaAssetId: string;
  meetingId: string;
  phase: UploadPhase;
  /** The recorder is still producing chunks. */
  recording: boolean;
  /** Chunks stored on this device and not yet acknowledged by the server. */
  pendingChunks: number;
  /** Chunks acknowledged by the server. */
  uploadedChunks: number;
  /** The last upload attempt failed for a retryable reason (offline, server down). */
  waitingForNetwork: boolean;
  /** Resumed from local storage after a reload or crash. */
  recovered: boolean;
  error: string | null;
  asset: ServerMediaAsset | null;
}

export interface Scheduler {
  set(callback: () => void, ms: number): unknown;
  clear(handle: unknown): void;
}

/** Cross-tab ownership of a live recording. acquire() resolves null when another tab holds it. */
export interface RecordingLocks {
  readonly supported: boolean;
  acquire(name: string): Promise<(() => void) | null>;
}

export interface CoordinatorOptions {
  media: ServerMediaClient;
  store: PendingChunkStore;
  isOnline: () => boolean;
  onOnline: (callback: () => void) => () => void;
  scheduler?: Scheduler;
  locks?: RecordingLocks;
  now?: () => number;
  /** Retry delays in ms; the last value repeats. */
  backoffMs?: number[];
}

/** Without Web Locks, a recording untouched for this long is considered abandoned. */
export const ABANDONED_AFTER_MS = 90_000;
const HEARTBEAT_MS = 20_000;
const DEFAULT_BACKOFF = [1_000, 2_000, 5_000, 10_000, 30_000];

const timers: Scheduler = { set: (callback, ms) => setTimeout(callback, ms), clear: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>) };

type Failure = { retry: boolean; message: string };

function classify(error: unknown): Failure {
  if (error instanceof VoxaTransportError) return { retry: true, message: 'Waiting for the network…' };
  if (error instanceof VoxaApiError) {
    if (error.status >= 500 || error.status === 401 || error.status === 408 || error.status === 429) {
      return { retry: true, message: error.status === 401 ? 'Waiting for sign-in…' : 'The server is unavailable; retrying…' };
    }
    const reason = typeof error.details?.reason === 'string' ? error.details.reason : null;
    const messages: Record<string, string> = {
      chunk_mismatch: 'The server holds different data for part of this recording.',
      upload_closed: 'This upload was already completed or closed.',
      upload_failed: 'The server abandoned this upload.',
      missing_chunks: 'Parts of this recording are missing and cannot be recovered.',
      unexpected_chunks: 'The server has recording parts beyond a missing one.',
    };
    if (error.status === 404) return { retry: false, message: 'The recording no longer exists on the server.' };
    return { retry: false, message: (reason && messages[reason]) || error.message };
  }
  if (error instanceof DOMException && error.name === 'AbortError') return { retry: true, message: 'Upload interrupted; retrying…' };
  return { retry: false, message: 'Unexpected upload error.' };
}

class UploadJob {
  state: UploadJobState;
  record: PendingRecording;
  private running = false;
  private rerun = false;
  private attempt = 0;
  private timer: unknown = null;
  private heartbeat: unknown = null;
  release: (() => void) | null = null;

  constructor(private readonly owner: RecordingUploadCoordinator, record: PendingRecording, recovered: boolean) {
    this.record = record;
    this.state = {
      mediaAssetId: record.mediaAssetId, meetingId: record.meetingId, phase: record.state === 'recording' ? 'recording' : 'uploading',
      recording: record.state === 'recording' && !recovered, pendingChunks: 0, uploadedChunks: 0, waitingForNetwork: false, recovered, error: null, asset: null,
    };
  }

  private get o() { return this.owner.options; }

  update(patch: Partial<UploadJobState>): void {
    this.state = { ...this.state, ...patch };
    const s = this.state;
    let phase: UploadPhase;
    if (s.error) phase = 'error';
    else if (s.asset?.status === 'ready') phase = 'ready';
    else if (s.recording) phase = 'recording';
    else if (s.waitingForNetwork) phase = 'waiting';
    else if (s.pendingChunks > 0 || this.record.state === 'recording') phase = 'uploading';
    else phase = 'finalizing';
    this.state = { ...this.state, phase };
    this.owner.notify();
  }

  startHeartbeat(): void {
    const beat = () => {
      this.heartbeat = this.o.scheduler!.set(async () => {
        if (this.record.state !== 'recording' || !this.state.recording) return;
        this.record = { ...this.record, updatedAt: this.o.now!() };
        await this.o.store.saveRecording(this.record).catch(() => {});
        beat();
      }, HEARTBEAT_MS);
    };
    beat();
  }

  async refreshPending(): Promise<void> {
    this.update({ pendingChunks: (await this.o.store.listChunkIndexes(this.record.mediaAssetId)).length });
  }

  schedule(now = false): void {
    if (this.timer !== null) {
      this.o.scheduler!.clear(this.timer);
      this.timer = null;
    }
    if (now) {
      this.attempt = 0;
      void this.pump();
      return;
    }
    const backoff = this.o.backoffMs!;
    const delay = backoff[Math.min(this.attempt, backoff.length - 1)];
    this.attempt++;
    this.timer = this.o.scheduler!.set(() => {
      this.timer = null;
      void this.pump();
    }, delay);
  }

  /** Uploads pending chunks in order, then finalizes once recording has stopped. Single-flight. */
  async pump(): Promise<void> {
    if (this.state.error || this.state.phase === 'ready') return;
    if (this.running) {
      this.rerun = true;
      return;
    }
    this.running = true;
    try {
      do {
        this.rerun = false;
        if (!(await this.uploadPending())) return;
        if (this.record.state === 'stopped') {
          // Finalizing is the last step; a finalized job never pumps again.
          await this.finalize();
          return;
        }
      } while (this.rerun);
    } finally {
      this.running = false;
    }
  }

  private fail(error: unknown): false {
    const failure = classify(error);
    if (failure.retry) {
      this.update({ waitingForNetwork: true });
      this.schedule();
    } else {
      this.update({ error: failure.message, waitingForNetwork: false });
    }
    return false;
  }

  private async uploadPending(): Promise<boolean> {
    const id = this.record.mediaAssetId;
    for (;;) {
      const indexes = await this.o.store.listChunkIndexes(id);
      this.update({ pendingChunks: indexes.length });
      if (indexes.length === 0) return true;
      const index = indexes[0];
      const data = await this.o.store.getChunk(id, index);
      if (data) {
        try {
          const progress = await this.o.media.uploadChunk(id, index, data);
          this.update({ uploadedChunks: progress.receivedChunks.length, waitingForNetwork: false });
          this.attempt = 0;
        } catch (error) {
          return this.fail(error);
        }
      }
      // Deleted only after the server acknowledged the chunk.
      await this.o.store.deleteChunk(id, index);
    }
  }

  private async finalize(): Promise<boolean> {
    const id = this.record.mediaAssetId;
    this.update({ waitingForNetwork: false });
    try {
      let count = this.record.chunkCount;
      if (count === null) {
        // Recovered mid-recording: finalize the contiguous prefix the server holds.
        const status = await this.o.media.getUploadStatus(id);
        const received = new Set(status.receivedChunks);
        count = 0;
        while (received.has(count)) count++;
        if (count === 0) {
          this.update({ error: 'Nothing from this recording reached the server.' });
          return false;
        }
      }
      const asset = await this.o.media.finalize(id, count, this.record.durationSeconds);
      await this.o.store.deleteRecording(id);
      this.release?.();
      this.release = null;
      this.update({ asset, pendingChunks: 0, uploadedChunks: count });
      this.owner.finished(this);
      return true;
    } catch (error) {
      return this.fail(error);
    }
  }

  dispose(): void {
    if (this.timer !== null) this.o.scheduler!.clear(this.timer);
    if (this.heartbeat !== null) this.o.scheduler!.clear(this.heartbeat);
    this.release?.();
    this.release = null;
  }
}

/**
 * Coordinates durable, resumable recording uploads:
 *
 *   BrowserRecordingClient --chunk--> coordinator --(IndexedDB first)--> HttpMediaClient
 *
 * Every chunk is committed locally before upload and deleted only after the
 * server acknowledges it. Uploads are ordered and single-flight per
 * recording, retried with backoff and immediately when the browser comes
 * back online. Once recording stops and nothing is pending, the upload is
 * finalized (idempotent on the server). Foreground only: uploads continue
 * while a Voxa tab is open, not after the browser is closed.
 */
export class RecordingUploadCoordinator {
  readonly options: Required<Omit<CoordinatorOptions, 'locks'>> & { locks?: RecordingLocks };
  private readonly jobs = new Map<string, UploadJob>();
  private readonly listeners = new Set<() => void>();
  private readonly unsubscribeOnline: () => void;

  constructor(options: CoordinatorOptions) {
    this.options = {
      ...options,
      scheduler: options.scheduler ?? timers,
      now: options.now ?? (() => Date.now()),
      backoffMs: options.backoffMs ?? DEFAULT_BACKOFF,
    };
    this.unsubscribeOnline = options.onOnline(() => this.jobs.forEach((job) => job.schedule(true)));
  }

  subscribe(listener: () => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  notify(): void {
    this.listeners.forEach((listener) => listener());
  }

  getJob(mediaAssetId: string): UploadJobState | null {
    return this.jobs.get(mediaAssetId)?.state ?? null;
  }

  getJobs(): UploadJobState[] {
    return [...this.jobs.values()].map((job) => job.state);
  }

  finished(_job: UploadJob): void {
    this.notify();
  }

  /** Creates the server recording and local record; requires connectivity. */
  async begin(meetingId: string, mimeType: string): Promise<{ mediaAssetId: string; maxChunkBytes: number }> {
    const target = await this.options.media.createRecording(meetingId, mimeType);
    const now = this.options.now();
    const record: PendingRecording = {
      mediaAssetId: target.asset.id, meetingId, mimeType: target.asset.mimeType, state: 'recording',
      chunkCount: null, durationSeconds: null, createdAt: now, updatedAt: now,
    };
    await this.options.store.saveRecording(record);
    const job = new UploadJob(this, record, false);
    job.release = (await this.options.locks?.acquire(lockName(record.mediaAssetId))) ?? null;
    this.jobs.set(record.mediaAssetId, job);
    job.startHeartbeat();
    job.update({});
    return { mediaAssetId: record.mediaAssetId, maxChunkBytes: target.maxChunkBytes };
  }

  /** Persists a chunk locally, then uploads it when possible. */
  async addChunk(mediaAssetId: string, index: number, data: Blob): Promise<void> {
    const job = this.requireJob(mediaAssetId);
    await this.options.store.putChunk(mediaAssetId, index, data);
    job.record = { ...job.record, updatedAt: this.options.now() };
    await this.options.store.saveRecording(job.record);
    await job.refreshPending();
    void job.pump();
  }

  /** Marks recording complete; the upload finalizes once all chunks are acknowledged. */
  async complete(mediaAssetId: string, chunkCount: number, durationSeconds: number | null): Promise<void> {
    const job = this.requireJob(mediaAssetId);
    job.record = { ...job.record, state: 'stopped', chunkCount, durationSeconds, updatedAt: this.options.now() };
    await this.options.store.saveRecording(job.record);
    job.update({ recording: false });
    void job.pump();
  }

  retry(mediaAssetId: string): void {
    const job = this.jobs.get(mediaAssetId);
    if (!job) return;
    job.update({ error: null });
    job.schedule(true);
  }

  /** Discards a recording locally and on the server. */
  async discard(mediaAssetId: string): Promise<void> {
    const job = this.jobs.get(mediaAssetId);
    job?.dispose();
    this.jobs.delete(mediaAssetId);
    await this.options.store.deleteRecording(mediaAssetId);
    await this.options.media.deleteMedia(mediaAssetId).catch(() => {});
    this.notify();
  }

  /**
   * Resumes recordings left in local storage by a previous page (reload,
   * crash, network loss). Recordings still owned by another open tab are
   * skipped. Returns the resumed media asset IDs.
   */
  async recoverPending(): Promise<string[]> {
    const resumed: string[] = [];
    for (const record of await this.options.store.listRecordings()) {
      if (this.jobs.has(record.mediaAssetId)) continue;
      let release: (() => void) | null = null;
      if (this.options.locks?.supported) {
        release = await this.options.locks.acquire(lockName(record.mediaAssetId));
        if (!release) continue;
      } else if (record.state === 'recording' && this.options.now() - record.updatedAt < ABANDONED_AFTER_MS) {
        continue;
      }
      // A recording interrupted mid-capture can no longer grow: finalize what exists.
      const recovered: PendingRecording = record.state === 'recording' ? { ...record, state: 'stopped', chunkCount: null, durationSeconds: null } : record;
      await this.options.store.saveRecording(recovered);
      const job = new UploadJob(this, recovered, true);
      job.release = release;
      this.jobs.set(record.mediaAssetId, job);
      try {
        const status = await this.options.media.getUploadStatus(record.mediaAssetId);
        if (status.status === 'ready') {
          await this.options.store.deleteRecording(record.mediaAssetId);
          job.release?.();
          job.release = null;
          job.update({ asset: { id: record.mediaAssetId, meetingId: record.meetingId, mimeType: record.mimeType, status: 'ready', durationSeconds: record.durationSeconds, sizeBytes: status.receivedBytes, createdAt: '', updatedAt: '' } });
          continue;
        }
        // Chunks the server already holds need not be sent again.
        for (const index of status.receivedChunks) await this.options.store.deleteChunk(record.mediaAssetId, index);
        job.update({ uploadedChunks: status.receivedChunks.length });
      } catch {
        // Offline or server unavailable: the pump retries.
      }
      await job.refreshPending();
      resumed.push(record.mediaAssetId);
      void job.pump();
    }
    return resumed;
  }

  private requireJob(mediaAssetId: string): UploadJob {
    const job = this.jobs.get(mediaAssetId);
    if (!job) throw new Error('Unknown recording upload');
    return job;
  }

  dispose(): void {
    this.unsubscribeOnline();
    this.jobs.forEach((job) => job.dispose());
    this.jobs.clear();
  }
}

const lockName = (mediaAssetId: string) => `voxa-recording-${mediaAssetId}`;

/** Web Locks adapter: a held lock marks a recording as owned by a live tab. */
export function browserRecordingLocks(): RecordingLocks {
  const locks = (globalThis as { navigator?: { locks?: LockManager } }).navigator?.locks;
  if (!locks) return { supported: false, acquire: async () => () => {} };
  return {
    supported: true,
    acquire: (name) => new Promise((resolve) => {
      void locks.request(name, { ifAvailable: true }, (lock) => {
        if (!lock) {
          resolve(null);
          return undefined;
        }
        // Hold the lock until released (or the tab closes).
        return new Promise<void>((release) => resolve(() => release()));
      });
    }),
  };
}
