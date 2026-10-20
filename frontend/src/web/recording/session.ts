import type { BrowserRecordingClient, CaptureDescription, CaptureMode } from '@/clients/browser/recording-client';
import type { RecordingCaptureProvenance } from '@/clients/contracts';
import type { RecordingUploadCoordinator } from './upload-coordinator';

const PROVENANCE_MODES: Record<CaptureMode, RecordingCaptureProvenance['mode']> = {
  microphone: 'microphone', 'microphone+shared': 'microphone_and_shared_audio', shared: 'shared_audio',
};

/** Capture provenance reported to the server: mode, client kind and shared surface only. */
export function captureProvenance(capture: CaptureDescription): RecordingCaptureProvenance {
  return { mode: PROVENANCE_MODES[capture.mode], client: 'web', sharedSurface: capture.shared?.surface ?? null };
}

export type SessionPhase = 'idle' | 'starting' | 'recording' | 'paused' | 'stopping';

export interface SessionState {
  phase: SessionPhase;
  meetingId: string | null;
  mediaAssetId: string | null;
  error: string | null;
  /** What is being captured right now (null when idle). */
  capture: CaptureDescription | null;
  /** Non-fatal problem during recording, e.g. tab sharing stopped. */
  warning: string | null;
}

/**
 * Joins one browser recording to one durable upload:
 *
 *   start: sources (share picker first, inside the click) -> server RecordingSession
 *          + MediaAsset -> chunks -> coordinator
 *   stop (by the user, a lost microphone or a recorder error): summary -> coordinator.complete
 *
 * Capture stays in BrowserRecordingClient; persistence and upload stay in
 * RecordingUploadCoordinator.
 */
export class RecordingSessionController {
  private state: SessionState = { phase: 'idle', meetingId: null, mediaAssetId: null, error: null, capture: null, warning: null };
  private readonly listeners = new Set<() => void>();
  private detach: Array<() => void> = [];
  private completion: Promise<void> | null = null;

  constructor(private readonly recorder: BrowserRecordingClient, private readonly uploads: RecordingUploadCoordinator) {}

  getState = (): SessionState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  private set(patch: Partial<SessionState>): void {
    this.state = { ...this.state, ...patch };
    this.listeners.forEach((listener) => listener());
  }

  /**
   * Call directly from the user's click: the share picker (for meeting-tab
   * modes) must be requested before anything else is awaited.
   */
  async start(meetingId: string, microphoneDeviceName: string | null, captureMode: CaptureMode = 'microphone'): Promise<void> {
    if (this.state.phase !== 'idle') return;
    const mimeType = this.recorder.mimeType;
    if (!mimeType) {
      this.set({ error: 'This browser cannot record audio.' });
      return;
    }
    this.set({ phase: 'starting', meetingId, mediaAssetId: null, error: null, warning: null, capture: null });
    let prepared: CaptureDescription;
    try {
      prepared = await this.recorder.prepare({ mode: captureMode, microphoneDeviceName });
    } catch (error) {
      this.set({ phase: 'idle', error: error instanceof Error ? error.message : 'Recording could not start.' });
      return;
    }
    let mediaAssetId: string;
    try {
      mediaAssetId = (await this.uploads.begin(meetingId, mimeType, captureProvenance(prepared))).mediaAssetId;
    } catch {
      await this.recorder.releasePrepared();
      this.set({ phase: 'idle', error: 'Could not start a recording on the server. Check your connection and try again.' });
      return;
    }
    this.detach = [
      this.recorder.onChunk((chunk) => {
        void this.uploads.addChunk(mediaAssetId, chunk.index, chunk.data).catch(() => {
          this.set({ error: 'A part of the recording could not be saved on this device.' });
        });
      }),
      this.recorder.onPaused(() => this.set({ phase: 'paused' })),
      this.recorder.onResumed(() => this.set({ phase: 'recording' })),
      this.recorder.onStopped(() => { this.completion = this.completeUpload(mediaAssetId); }),
      this.recorder.onCaptureChanged((capture) => { if (capture) this.set({ capture }); }),
      this.recorder.onCaptureWarning((warning) => this.set({ warning })),
    ];
    try {
      await this.recorder.start({ microphoneDeviceName, systemAudioDeviceName: null, meetingName: meetingId, captureMode });
      this.set({ phase: 'recording', mediaAssetId, capture: this.recorder.getCapture() });
    } catch (error) {
      this.release();
      await this.recorder.releasePrepared();
      await this.uploads.discard(mediaAssetId);
      this.set({ phase: 'idle', mediaAssetId: null, error: error instanceof Error ? error.message : 'Recording could not start.' });
    }
  }

  /** Resolves once the recording is stopped and its upload marked complete. */
  async stop(): Promise<void> {
    if (this.state.phase !== 'recording' && this.state.phase !== 'paused') return;
    this.set({ phase: 'stopping' });
    await this.recorder.stop();
    await this.completion;
  }

  async pause(): Promise<void> {
    await this.recorder.pause();
  }

  async resume(): Promise<void> {
    await this.recorder.resume();
  }

  /** Runs once per recording, however it stopped. */
  private async completeUpload(mediaAssetId: string): Promise<void> {
    this.release();
    const summary = this.recorder.getLastSummary();
    this.set({ phase: 'idle', capture: null });
    if (summary && summary.chunkCount > 0) {
      await this.uploads.complete(mediaAssetId, summary.chunkCount, summary.durationSeconds);
    } else {
      await this.uploads.discard(mediaAssetId);
    }
  }

  private release(): void {
    this.detach.forEach((detach) => detach());
    this.detach = [];
  }
}
