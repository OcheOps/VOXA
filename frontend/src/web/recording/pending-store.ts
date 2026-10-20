/**
 * Local durability for recording chunks that are not yet confirmed by the
 * server. Chunks are written here before upload and deleted only after the
 * server acknowledges them, so a network outage or page reload loses at most
 * the chunk being encoded.
 */
export interface PendingRecording {
  mediaAssetId: string;
  meetingId: string;
  mimeType: string;
  /** 'recording' while a recorder may still produce chunks; 'stopped' once complete. */
  state: 'recording' | 'stopped';
  chunkCount: number | null;
  durationSeconds: number | null;
  createdAt: number;
  updatedAt: number;
}

export interface PendingChunkStore {
  readonly durable: boolean;
  saveRecording(recording: PendingRecording): Promise<void>;
  getRecording(mediaAssetId: string): Promise<PendingRecording | null>;
  listRecordings(): Promise<PendingRecording[]>;
  /** Deletes the recording and any remaining chunks. */
  deleteRecording(mediaAssetId: string): Promise<void>;
  putChunk(mediaAssetId: string, index: number, data: Blob): Promise<void>;
  getChunk(mediaAssetId: string, index: number): Promise<Blob | null>;
  listChunkIndexes(mediaAssetId: string): Promise<number[]>;
  deleteChunk(mediaAssetId: string, index: number): Promise<void>;
}

/** Non-durable fallback (and test double) when IndexedDB is unavailable. */
export class MemoryPendingStore implements PendingChunkStore {
  readonly durable = false;
  private readonly recordings = new Map<string, PendingRecording>();
  private readonly chunks = new Map<string, Map<number, Blob>>();

  async saveRecording(recording: PendingRecording) { this.recordings.set(recording.mediaAssetId, { ...recording }); }
  async getRecording(id: string) { const r = this.recordings.get(id); return r ? { ...r } : null; }
  async listRecordings() { return [...this.recordings.values()].map((r) => ({ ...r })); }
  async deleteRecording(id: string) { this.recordings.delete(id); this.chunks.delete(id); }
  async putChunk(id: string, index: number, data: Blob) {
    if (!this.chunks.has(id)) this.chunks.set(id, new Map());
    this.chunks.get(id)!.set(index, data);
  }
  async getChunk(id: string, index: number) { return this.chunks.get(id)?.get(index) ?? null; }
  async listChunkIndexes(id: string) { return [...(this.chunks.get(id)?.keys() ?? [])].sort((a, b) => a - b); }
  async deleteChunk(id: string, index: number) { this.chunks.get(id)?.delete(index); }
}

const DB_NAME = 'voxa-recordings';
const DB_VERSION = 1;

function promisify<T>(request: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

function completed(transaction: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    transaction.oncomplete = () => resolve();
    transaction.onerror = () => reject(transaction.error);
    transaction.onabort = () => reject(transaction.error ?? new Error('IndexedDB transaction aborted'));
  });
}

/**
 * IndexedDB-backed store. Writes resolve only after the transaction has
 * committed. Stores: `recordings` (keyed by mediaAssetId) and `chunks`
 * (keyed by [mediaAssetId, index]).
 *
 * Chunk bytes are stored as an ArrayBuffer (`bytes`, plus `type`), not as a
 * Blob. In Chromium, a Blob value that has been read back from IndexedDB
 * keeps its backing file after the record is deleted, so uploading
 * Blob-valued chunks leaked about 0.7 MB of origin storage per recorded
 * minute (measured in the 30-minute endurance test); ArrayBuffer values are
 * reclaimed. Rows written by older builds (`data: Blob`) are still read.
 */
export class IndexedDbPendingStore implements PendingChunkStore {
  readonly durable = true;
  private dbPromise: Promise<IDBDatabase> | null = null;

  constructor(private readonly factory: IDBFactory) {}

  private db(): Promise<IDBDatabase> {
    if (!this.dbPromise) {
      this.dbPromise = new Promise((resolve, reject) => {
        const request = this.factory.open(DB_NAME, DB_VERSION);
        request.onupgradeneeded = () => {
          const db = request.result;
          if (!db.objectStoreNames.contains('recordings')) db.createObjectStore('recordings', { keyPath: 'mediaAssetId' });
          if (!db.objectStoreNames.contains('chunks')) {
            const chunks = db.createObjectStore('chunks', { keyPath: ['mediaAssetId', 'index'] });
            chunks.createIndex('byRecording', 'mediaAssetId');
          }
        };
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
      });
    }
    return this.dbPromise;
  }

  private async write(stores: string[], action: (tx: IDBTransaction) => void): Promise<void> {
    const tx = (await this.db()).transaction(stores, 'readwrite');
    const done = completed(tx);
    action(tx);
    await done;
  }

  async saveRecording(recording: PendingRecording) {
    await this.write(['recordings'], (tx) => tx.objectStore('recordings').put(recording));
  }

  async getRecording(id: string) {
    const tx = (await this.db()).transaction('recordings', 'readonly');
    return ((await promisify(tx.objectStore('recordings').get(id))) as PendingRecording | undefined) ?? null;
  }

  async listRecordings() {
    const tx = (await this.db()).transaction('recordings', 'readonly');
    return (await promisify(tx.objectStore('recordings').getAll())) as PendingRecording[];
  }

  async deleteRecording(id: string) {
    const indexes = await this.listChunkIndexes(id);
    await this.write(['recordings', 'chunks'], (tx) => {
      tx.objectStore('recordings').delete(id);
      for (const index of indexes) tx.objectStore('chunks').delete([id, index]);
    });
  }

  async putChunk(id: string, index: number, data: Blob) {
    // Read before the transaction: IndexedDB transactions cannot span awaits.
    const bytes = await data.arrayBuffer();
    await this.write(['chunks'], (tx) => tx.objectStore('chunks').put({ mediaAssetId: id, index, bytes, type: data.type }));
  }

  async getChunk(id: string, index: number) {
    const tx = (await this.db()).transaction('chunks', 'readonly');
    const row = (await promisify(tx.objectStore('chunks').get([id, index]))) as { bytes?: ArrayBuffer; type?: string; data?: Blob } | undefined;
    if (!row) return null;
    if (row.bytes) return new Blob([row.bytes], { type: row.type ?? '' });
    return row.data ?? null; // written by an older build
  }

  async listChunkIndexes(id: string) {
    const tx = (await this.db()).transaction('chunks', 'readonly');
    const keys = (await promisify(tx.objectStore('chunks').index('byRecording').getAllKeys(IDBKeyRange.only(id)))) as Array<[string, number]>;
    return keys.map(([, index]) => index).sort((a, b) => a - b);
  }

  async deleteChunk(id: string, index: number) {
    await this.write(['chunks'], (tx) => tx.objectStore('chunks').delete([id, index]));
  }
}

export function createPendingStore(): PendingChunkStore {
  const factory = (globalThis as { indexedDB?: IDBFactory }).indexedDB;
  return factory ? new IndexedDbPendingStore(factory) : new MemoryPendingStore();
}
