/*
 * Browser test harness: the production recording/upload modules wired to a
 * real Voxa server, exposed on window for the CDP-driven test. Not shipped.
 */
import { BrowserRecordingClient, defaultBrowserEnvironment } from '../../src/clients/browser/recording-client';
import { HttpMediaClient } from '../../src/clients/http/media-client';
import { HttpTransport } from '../../src/clients/http/transport';
import { IndexedDbPendingStore } from '../../src/web/recording/pending-store';
import { RecordingSessionController } from '../../src/web/recording/session';
import { browserRecordingLocks, RecordingUploadCoordinator } from '../../src/web/recording/upload-coordinator';

const COPY_DB = 'voxa-test-copies';

function copies(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(COPY_DB, 1);
    request.onupgradeneeded = () => request.result.createObjectStore('chunks', { keyPath: ['mediaAssetId', 'index'] });
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

async function keepCopy(mediaAssetId: string, index: number, data: Blob) {
  const db = await copies();
  const bytes = new Uint8Array(await data.arrayBuffer());
  await new Promise<void>((resolve, reject) => {
    const tx = db.transaction('chunks', 'readwrite');
    tx.objectStore('chunks').put({ mediaAssetId, index, bytes });
    tx.oncomplete = () => resolve();
    tx.onerror = () => reject(tx.error);
  });
}

function toBase64(bytes: Uint8Array): string {
  let binary = '';
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

/** Deterministic sources: oscillators stand in for the microphone and the shared tab. */
interface Synthetic { micHz: number; micAmplitude: number; tabHz: number; tabAmplitude: number }
const syntheticTracks: MediaStreamTrack[] = [];

function oscillatorStream(context: AudioContext, hz: number, amplitude: number): MediaStream {
  const oscillator = context.createOscillator();
  const gain = context.createGain();
  const destination = context.createMediaStreamDestination();
  oscillator.frequency.value = hz;
  gain.gain.value = amplitude;
  oscillator.connect(gain).connect(destination);
  oscillator.start();
  syntheticTracks.push(...destination.stream.getAudioTracks());
  return destination.stream;
}

function syntheticDevices(synthetic: Synthetic) {
  const context = new AudioContext();
  return {
    getUserMedia: async () => oscillatorStream(context, synthetic.micHz, synthetic.micAmplitude),
    getDisplayMedia: async () => {
      const stream = oscillatorStream(context, synthetic.tabHz, synthetic.tabAmplitude);
      const canvas = document.createElement('canvas');
      stream.addTrack(canvas.captureStream(1).getVideoTracks()[0]);
      return stream;
    },
    enumerateDevices: async () => [],
    getSupportedConstraints: () => navigator.mediaDevices.getSupportedConstraints(),
  };
}

let coordinator: RecordingUploadCoordinator;
let session: RecordingSessionController;
let recorder: BrowserRecordingClient;
let apiUrl = '';

(window as any).harness = {
  setup(api: string, timesliceMs: number, synthetic?: Synthetic) {
    apiUrl = api;
    const transport = new HttpTransport({ baseUrl: api, getAccessToken: () => null });
    const media = new HttpMediaClient(transport, api);
    coordinator = new RecordingUploadCoordinator({
      media, store: new IndexedDbPendingStore(indexedDB), locks: browserRecordingLocks(),
      isOnline: () => navigator.onLine,
      onOnline: (callback) => { window.addEventListener('online', callback); return () => window.removeEventListener('online', callback); },
      backoffMs: [200, 400, 800],
    });
    const env = defaultBrowserEnvironment();
    recorder = new BrowserRecordingClient({ ...env, ...(synthetic ? { mediaDevices: syntheticDevices(synthetic) as never } : {}), timesliceMs });
    recorder.onChunk((chunk) => {
      const id = session.getState().mediaAssetId ?? (coordinator.getJobs().at(-1)?.mediaAssetId ?? '');
      void keepCopy(id, chunk.index, chunk.data);
    });
    session = new RecordingSessionController(recorder, coordinator);
    return { mimeType: recorder.mimeType, capabilities: recorder.capabilities };
  },
  async createMeeting(title: string) {
    const response = await fetch(`${apiUrl}/api/v1/meetings`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ title }) });
    return (await response.json()).meeting.id as string;
  },
  async start(meetingId: string, mode: 'microphone' | 'microphone+shared' | 'shared' = 'microphone') {
    await session.start(meetingId, null, mode);
    return session.getState();
  },
  state() { return session.getState(); },
  /** Simulates the browser ending a synthetic source (e.g. "Stop sharing"). */
  endSynthetic(index: number) { syntheticTracks[index]?.dispatchEvent(new Event('ended')); },
  browserCapture() { return recorder.browserCapture; },
  heapBytes() { return (performance as unknown as { memory?: { usedJSHeapSize: number } }).memory?.usedJSHeapSize ?? null; },
  async stop() {
    await session.stop();
    return session.getState();
  },
  job(id: string) { return coordinator.getJob(id); },
  jobs() { return coordinator.getJobs(); },
  async pendingLocal(id: string) { return (await new IndexedDbPendingStore(indexedDB).listChunkIndexes(id)).length; },
  async recover() { return coordinator.recoverPending(); },
  async waitFor(id: string, phase: string, timeoutMs: number) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      const job = coordinator.getJob(id);
      if (job?.phase === phase) return job;
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    throw new Error(`timed out waiting for ${phase}: ${JSON.stringify(coordinator.getJob(id))}`);
  },
  /** Concatenated copies of the first `count` chunks the recorder produced. */
  async copiedBytes(id: string, count: number) {
    const db = await copies();
    const parts: Uint8Array[] = [];
    for (let index = 0; index < count; index++) {
      const row = await new Promise<any>((resolve) => {
        const request = db.transaction('chunks').objectStore('chunks').get([id, index]);
        request.onsuccess = () => resolve(request.result);
      });
      if (!row) throw new Error(`missing copy of chunk ${index}`);
      parts.push(row.bytes);
    }
    const total = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
    let offset = 0;
    for (const part of parts) { total.set(part, offset); offset += part.length; }
    return toBase64(total);
  },
  async play(url: string) {
    const audio = new Audio(url);
    await new Promise((resolve, reject) => { audio.onloadedmetadata = resolve; audio.onerror = () => reject(new Error(`media error ${audio.error?.code}`)); });
    audio.currentTime = 0.5;
    await new Promise((resolve) => { audio.onseeked = resolve; setTimeout(resolve, 3000); });
    return { currentTime: audio.currentTime, error: audio.error?.code ?? null };
  },
};
