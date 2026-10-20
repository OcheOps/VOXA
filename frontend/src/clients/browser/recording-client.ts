import type {
  AudioLevelUpdate,
  MicrophoneSwapFailedEvent,
  MicrophoneSwitchedEvent,
  RecordingCapabilities,
  RecordingClient,
  RecordingDevice,
  RecordingStartOptions,
  RecordingState,
  RecordingStoppedEvent,
} from '../contracts';
import { UnsupportedOperationError } from '../errors';
import {
  CaptureError,
  DISPLAY_MEDIA_OPTIONS,
  MAX_CAPTURE_DIAGNOSTICS,
  noSharedAudioMessage,
  type CaptureDiagnostic,
  type CaptureDiagnosticType,
  MixGraph,
  classifyDisplayError,
  classifyMicrophoneError,
  detectBrowserCapture,
  microphoneConstraints,
  surfaceOf,
  type AudioContextConstructor,
  type BrowserCaptureCapabilities,
  type CaptureDescription,
  type CaptureMediaDevices,
  type CaptureMode,
} from './capture';

export type { BrowserCaptureCapabilities, CaptureDescription, CaptureDiagnostic, CaptureMode } from './capture';

/**
 * Chunk cadence. MediaRecorder emits one encoded piece per timeslice; each
 * piece is persisted locally and uploaded as one chunk. 15 s keeps at most
 * ~15 s of audio at risk on a crash, produces ~60–240 KB chunks for Opus
 * speech, and keeps request overhead negligible.
 */
export const RECORDING_TIMESLICE_MS = 15_000;

/**
 * Preferred formats, in order. All are single-stream containers whose
 * MediaRecorder timeslices concatenate into one playable file (verified
 * for WebM/Opus in Chrome); the server accepts the same set.
 */
export const RECORDING_MIME_PREFERENCES = ['audio/webm;codecs=opus', 'audio/ogg;codecs=opus', 'audio/webm', 'audio/mp4'] as const;

export function selectRecordingMimeType(isTypeSupported: (type: string) => boolean): string | null {
  return RECORDING_MIME_PREFERENCES.find((type) => {
    try {
      return isTypeSupported(type);
    } catch {
      return false;
    }
  }) ?? null;
}

/** Capture could not start; `reason` says which step failed (see CaptureFailure). */
export const RecordingPermissionError = CaptureError;
export type RecordingPermissionError = CaptureError;

export interface RecordedChunk {
  index: number;
  data: Blob;
  mimeType: string;
}

export interface RecordingSummary {
  chunkCount: number;
  /** Recorded (non-paused) seconds. */
  durationSeconds: number;
  mimeType: string;
}

/** Structural subset of MediaRecorder used here (replaced in tests). */
export interface RecorderLike {
  readonly mimeType: string;
  readonly state: 'inactive' | 'recording' | 'paused';
  ondataavailable: ((event: { data: Blob }) => void) | null;
  onstop: (() => void) | null;
  onerror: ((event: unknown) => void) | null;
  start(timeslice?: number): void;
  stop(): void;
  pause(): void;
  resume(): void;
}

export interface RecorderConstructor {
  new (stream: MediaStream, options: { mimeType: string; audioBitsPerSecond?: number }): RecorderLike;
  isTypeSupported(type: string): boolean;
}

export interface BrowserMediaEnvironment {
  mediaDevices?: CaptureMediaDevices & Pick<MediaDevices, 'enumerateDevices'>;
  MediaRecorder?: RecorderConstructor;
  /** Web Audio, used only to mix microphone + shared audio. */
  AudioContext?: AudioContextConstructor;
  MediaStream?: new (tracks: MediaStreamTrack[]) => MediaStream;
  now: () => number;
  timesliceMs: number;
}

export function defaultBrowserEnvironment(): BrowserMediaEnvironment {
  const scope = globalThis as unknown as {
    navigator?: Navigator; MediaRecorder?: RecorderConstructor; AudioContext?: AudioContextConstructor;
    webkitAudioContext?: AudioContextConstructor; MediaStream?: new (tracks: MediaStreamTrack[]) => MediaStream;
  };
  return {
    mediaDevices: scope.navigator?.mediaDevices as BrowserMediaEnvironment['mediaDevices'],
    MediaRecorder: scope.MediaRecorder,
    AudioContext: scope.AudioContext ?? scope.webkitAudioContext,
    MediaStream: scope.MediaStream,
    now: () => Date.now(),
    timesliceMs: RECORDING_TIMESLICE_MS,
  };
}

/** Streams acquired for one recording (before or while recording). */
interface CaptureSession {
  mode: CaptureMode;
  /** The stream MediaRecorder records: the microphone, the shared audio, or the mix. */
  output: MediaStream;
  microphone: MediaStream | null;
  shared: MediaStream | null;
  graph: MixGraph | null;
  description: CaptureDescription;
}

export interface CapturePreparation {
  mode: CaptureMode;
  microphoneDeviceName: string | null;
}

type Phase = 'idle' | 'starting' | 'recording' | 'paused' | 'stopping';
type Listener<T> = (value: T) => void;

class Emitter<T> {
  private readonly listeners = new Set<Listener<T>>();
  on(listener: Listener<T>): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }
  emit(value: T): void {
    this.listeners.forEach((listener) => listener(value));
  }
  get size(): number {
    return this.listeners.size;
  }
}

/**
 * Microphone recording in the browser. It owns device access, the
 * MediaRecorder lifecycle and chunk production; it knows nothing about
 * uploads or server storage (see RecordingUploadCoordinator).
 */
export class BrowserRecordingClient implements RecordingClient {
  readonly capabilities: RecordingCapabilities;
  private readonly env: BrowserMediaEnvironment;
  private phase: Phase = 'idle';
  private capture: CaptureSession | null = null;
  private recorder: RecorderLike | null = null;
  private selectedMime: string | null;
  private chunkIndex = 0;
  private startedAt = 0;
  private pausedAt: number | null = null;
  private pausedTotal = 0;
  private lastSummary: RecordingSummary | null = null;
  private meetingName: string | null = null;
  private stopWaiters: Array<() => void> = [];
  private readonly deviceIds = new Map<string, string>();
  private readonly events = {
    starting: new Emitter<void>(), started: new Emitter<void>(), stopped: new Emitter<RecordingStoppedEvent>(),
    paused: new Emitter<void>(), resumed: new Emitter<void>(), chunk: new Emitter<RecordedChunk>(),
    microphoneUnavailable: new Emitter<void>(), error: new Emitter<string>(),
    captureChanged: new Emitter<CaptureDescription | null>(), warning: new Emitter<string>(), diagnostic: new Emitter<CaptureDiagnostic>(),
  };
  private readonly diagnostics: CaptureDiagnostic[] = [];
  /** Feature-detected browser capture support (also in capabilities.browserCapture). */
  readonly browserCapture: BrowserCaptureCapabilities;

  constructor(env: BrowserMediaEnvironment = defaultBrowserEnvironment()) {
    this.env = env;
    this.selectedMime = env.MediaRecorder ? selectRecordingMimeType((type) => env.MediaRecorder!.isTypeSupported(type)) : null;
    const canRecord = Boolean(env.mediaDevices?.getUserMedia && env.MediaRecorder && this.selectedMime);
    const pauseSupported = Boolean(env.MediaRecorder && typeof env.MediaRecorder.prototype?.pause === 'function');
    this.browserCapture = detectBrowserCapture({
      mediaDevices: env.mediaDevices, canRecord: Boolean(env.MediaRecorder && this.selectedMime), AudioContext: env.AudioContext,
      hasMediaStream: Boolean(env.MediaStream),
    });
    const shared = this.browserCapture.modes.some((mode) => mode !== 'microphone');
    this.capabilities = {
      microphoneCapture: canRecord,
      systemAudioCapture: shared ? 'display-media' : 'unavailable',
      deviceEnumeration: canRecord && env.mediaDevices?.enumerateDevices ? 'input-only' : 'unavailable',
      microphoneHotSwap: 'unavailable',
      audioLevelMonitoring: false,
      pauseResume: canRecord && pauseSupported,
      tabCapture: shared,
      browserCapture: this.browserCapture,
    };
  }

  /** The negotiated container/codec, or null when recording is unsupported. */
  get mimeType(): string | null {
    return this.selectedMime;
  }

  /** Subscribe to encoded chunks; indices start at 0 per recording. */
  onChunk(callback: (chunk: RecordedChunk) => void): () => void {
    return this.events.chunk.on(callback);
  }

  onRecorderError(callback: (message: string) => void): () => void {
    return this.events.error.on(callback);
  }

  /** Sources being captured; null when idle. Changes when a source stops. */
  onCaptureChanged(callback: (capture: CaptureDescription | null) => void): () => void {
    return this.events.captureChanged.on(callback);
  }

  /** Non-fatal problems during a recording (e.g. tab sharing stopped). */
  onCaptureWarning(callback: (message: string) => void): () => void {
    return this.events.warning.on(callback);
  }

  /** Recent capture events (bounded; no media or content). */
  getDiagnostics(): CaptureDiagnostic[] {
    return [...this.diagnostics];
  }

  onDiagnostic(callback: (event: CaptureDiagnostic) => void): () => void {
    return this.events.diagnostic.on(callback);
  }

  private diagnose(type: CaptureDiagnosticType, detail?: CaptureDiagnostic['detail']): void {
    const event: CaptureDiagnostic = { at: Math.round(globalThis.performance?.now?.() ?? this.env.now()), type, ...(detail ? { detail } : {}) };
    this.diagnostics.push(event);
    if (this.diagnostics.length > MAX_CAPTURE_DIAGNOSTICS) this.diagnostics.splice(0, this.diagnostics.length - MAX_CAPTURE_DIAGNOSTICS);
    this.events.diagnostic.emit(event);
  }

  getCapture(): CaptureDescription | null {
    return this.capture ? { ...this.capture.description } : null;
  }

  /** Summary of the last completed recording (after stop). */
  getLastSummary(): RecordingSummary | null {
    return this.lastSummary;
  }

  async listDevices(): Promise<RecordingDevice[]> {
    if (this.capabilities.deviceEnumeration === 'unavailable') return [];
    const devices = await this.env.mediaDevices!.enumerateDevices();
    this.deviceIds.clear();
    let unnamed = 0;
    return devices
      .filter((device) => device.kind === 'audioinput')
      .map((device) => {
        // Labels are empty until microphone permission has been granted.
        const name = device.label || (device.deviceId === 'default' ? 'Default microphone' : `Microphone ${++unnamed}`);
        this.deviceIds.set(name, device.deviceId);
        return { name, type: 'input' as const };
      });
  }

  async isRecording(): Promise<boolean> {
    return this.phase === 'recording' || this.phase === 'paused';
  }

  private activeMs(now: number): number {
    const paused = this.pausedTotal + (this.pausedAt !== null ? now - this.pausedAt : 0);
    return Math.max(0, now - this.startedAt - paused);
  }

  async getState(): Promise<RecordingState> {
    const active = this.phase === 'recording' || this.phase === 'paused';
    const now = this.env.now();
    return {
      isRecording: active,
      isPaused: this.phase === 'paused',
      isActive: this.phase === 'recording',
      recordingDuration: active ? (now - this.startedAt) / 1000 : null,
      activeDuration: active ? this.activeMs(now) / 1000 : null,
    };
  }

  async getMeetingName(): Promise<string | null> {
    return this.meetingName;
  }

  async getSessionFolderPath(): Promise<string | null> {
    return null;
  }

  /**
   * Acquires the sources for a mode. Call it directly from the user's click:
   * the share picker is requested first, before anything else is awaited,
   * because browsers only allow getDisplayMedia (and starting audio) during
   * a user gesture. Then the microphone, then the mix. On failure everything
   * acquired so far is released.
   */
  async prepare(options: CapturePreparation): Promise<CaptureDescription> {
    if (this.phase !== 'idle' || this.capture) throw new Error('A recording is already in progress');
    return this.acquire(options);
  }

  private async acquire(options: CapturePreparation): Promise<CaptureDescription> {
    const { mode } = options;
    if (!this.browserCapture.modes.includes(mode)) {
      if (mode === 'microphone') throw new UnsupportedOperationError('Microphone recording');
      throw new CaptureError(this.browserCapture.displayCapture ? 'audio-context' : 'display-unsupported');
    }
    const devices = this.env.mediaDevices!;
    const wantsMicrophone = mode !== 'shared';
    // Both started synchronously, inside the gesture.
    const displayRequest = mode !== 'microphone' ? Promise.resolve().then(() => devices.getDisplayMedia!(DISPLAY_MEDIA_OPTIONS)) : null;
    const graphRequest = mode === 'microphone+shared' ? MixGraph.create(this.env.AudioContext!) : null;
    // Avoid unhandled rejections while the other request is awaited.
    graphRequest?.catch(() => {});
    displayRequest?.catch(() => {});

    let shared: MediaStream | null = null;
    let microphone: MediaStream | null = null;
    let graph: MixGraph | null = null;
    const description: CaptureDescription = { mode, microphone: false, shared: null };
    this.diagnose('capture-requested', { mode });
    try {
      if (displayRequest) {
        try {
          shared = await displayRequest;
        } catch (error) {
          throw new CaptureError(classifyDisplayError(error));
        }
        description.shared = surfaceOf(shared);
        const audio = shared.getAudioTracks();
        // Voxa needs audio only: stop the video right away (browsers keep the
        // audio track running; verified in Chromium).
        shared.getVideoTracks().forEach((track) => track.stop());
        if (audio.length === 0) {
          this.diagnose('shared-audio-missing', { surface: description.shared?.surface ?? 'unknown' });
          throw new CaptureError('no-shared-audio', noSharedAudioMessage(description.shared?.surface ?? 'unknown'));
        }
        this.diagnose('shared-track-started', { surface: description.shared?.surface ?? 'unknown', tracks: audio.length });
        shared = new this.env.MediaStream!(audio);
      }
      if (wantsMicrophone) {
        const deviceId = options.microphoneDeviceName ? this.deviceIds.get(options.microphoneDeviceName) : undefined;
        try {
          microphone = await devices.getUserMedia({ audio: microphoneConstraints(deviceId), video: false });
        } catch (error) {
          throw new CaptureError(classifyMicrophoneError(error));
        }
        description.microphone = true;
        this.diagnose('microphone-track-started');
      }
      if (graphRequest) {
        graph = await graphRequest;
        this.diagnose('audio-context', { state: graph.state });
        graph.onStateChange = (state) => this.diagnose('audio-context', { state });
        graph.add('microphone', microphone!);
        graph.add('shared', shared!);
      }
    } catch (error) {
      this.diagnose('capture-failed', { reason: error instanceof CaptureError ? error.reason : 'unexpected' });
      shared?.getTracks().forEach((track) => track.stop());
      microphone?.getTracks().forEach((track) => track.stop());
      const pending = graph ?? (graphRequest ? await graphRequest.catch(() => null) : null);
      await pending?.close();
      throw error;
    }
    const output = graph ? graph.stream : mode === 'shared' ? shared! : microphone!;
    this.capture = { mode, output, microphone, shared, graph, description };
    return { ...description };
  }

  /** Releases sources acquired by prepare() when the recording does not start. */
  async releasePrepared(): Promise<void> {
    if (this.phase !== 'idle') return;
    await this.releaseCapture();
  }

  async start(input: RecordingStartOptions): Promise<void> {
    if (this.phase !== 'idle') throw new Error('A recording is already in progress');
    if (!this.capabilities.microphoneCapture) throw new UnsupportedOperationError('Microphone recording');
    if (input.systemAudioDeviceName) throw new UnsupportedOperationError('System audio device selection (use a capture mode)');
    this.phase = 'starting';
    this.events.starting.emit();
    if (!this.capture) {
      try {
        await this.acquire({ mode: input.captureMode ?? 'microphone', microphoneDeviceName: input.microphoneDeviceName });
      } catch (error) {
        this.phase = 'idle';
        throw error;
      }
    }
    const capture = this.capture!;

    let recorder: RecorderLike;
    try {
      recorder = new this.env.MediaRecorder!(capture.output, { mimeType: this.selectedMime!, audioBitsPerSecond: capture.mode === 'microphone' ? 64_000 : 96_000 });
    } catch {
      await this.releaseCapture();
      this.phase = 'idle';
      if (capture.mode !== 'microphone') throw new CaptureError('encoder');
      throw new UnsupportedOperationError(`Recording as ${this.selectedMime}`);
    }
    this.recorder = recorder;
    this.selectedMime = recorder.mimeType || this.selectedMime;
    this.chunkIndex = 0;
    this.pausedTotal = 0;
    this.pausedAt = null;
    this.lastSummary = null;
    this.meetingName = input.meetingName;

    recorder.ondataavailable = (event) => {
      if (!event.data || event.data.size === 0) return;
      this.events.chunk.emit({ index: this.chunkIndex++, data: event.data, mimeType: this.selectedMime! });
    };
    recorder.onerror = () => {
      this.diagnose('recorder-error');
      this.events.error.emit('The recorder stopped unexpectedly.');
      void this.stop();
    };
    recorder.onstop = () => this.finish();
    this.watchSources(capture);
    try {
      recorder.start(this.env.timesliceMs);
    } catch {
      this.detachRecorder();
      await this.releaseCapture();
      this.phase = 'idle';
      throw capture.mode === 'microphone' ? new CaptureError('microphone-busy') : new CaptureError('encoder');
    }
    this.startedAt = this.env.now();
    this.phase = 'recording';
    this.diagnose('recorder-started', { mode: capture.mode, mimeType: this.selectedMime, timesliceMs: this.env.timesliceMs });
    this.events.started.emit();
    this.events.captureChanged.emit({ ...capture.description });
  }

  /**
   * Source loss policy (predictable, documented):
   * - microphone mode: a lost microphone stops the recording (chunks kept);
   * - microphone + shared: whichever source ends is removed from the mix and
   *   the recording continues with the other, with a warning; when both are
   *   gone it stops;
   * - shared mode: when sharing stops, the recording stops.
   */
  private watchSources(capture: CaptureSession): void {
    const onEnded = (kind: 'microphone' | 'shared') => {
      if (this.capture !== capture || (this.phase !== 'recording' && this.phase !== 'paused')) return;
      if (kind === 'microphone' && !capture.description.microphone) return;
      if (kind === 'shared' && !capture.description.shared) return;
      this.diagnose(kind === 'microphone' ? 'microphone-track-ended' : 'shared-track-ended');
      if (kind === 'microphone') {
        capture.description.microphone = false;
        this.events.microphoneUnavailable.emit();
      } else {
        capture.description.shared = null;
      }
      const remaining = capture.description.microphone || capture.description.shared !== null;
      if (capture.graph && remaining) {
        capture.graph.remove(kind);
        this.diagnose('fallback', { remaining: kind === 'shared' ? 'microphone' : 'shared' });
        this.events.warning.emit(kind === 'shared'
          ? 'Tab audio sharing stopped. Recording continues with your microphone only.'
          : 'Your microphone stopped. Recording continues with the shared audio only.');
        this.events.captureChanged.emit({ ...capture.description });
        return;
      }
      if (kind === 'shared') this.events.warning.emit('Sharing stopped, so the recording ended.');
      this.events.captureChanged.emit({ ...capture.description });
      void this.stop();
    };
    capture.microphone?.getAudioTracks().forEach((track) => { track.onended = () => onEnded('microphone'); });
    capture.shared?.getAudioTracks().forEach((track) => { track.onended = () => onEnded('shared'); });
  }

  /** Stops recording; resolves after the final chunk has been emitted. */
  async stop(): Promise<void> {
    if (this.phase === 'idle') return;
    if (this.phase === 'stopping') return new Promise((resolve) => this.stopWaiters.push(resolve));
    const now = this.env.now();
    this.lastSummary = { chunkCount: 0, durationSeconds: this.activeMs(now) / 1000, mimeType: this.selectedMime! };
    this.phase = 'stopping';
    const done = new Promise<void>((resolve) => this.stopWaiters.push(resolve));
    if (this.recorder && this.recorder.state !== 'inactive') this.recorder.stop();
    else this.finish();
    return done;
  }

  private finish(): void {
    if (this.lastSummary === null) {
      this.lastSummary = { chunkCount: 0, durationSeconds: this.activeMs(this.env.now()) / 1000, mimeType: this.selectedMime ?? '' };
    }
    this.lastSummary.chunkCount = this.chunkIndex;
    this.diagnose('recorder-stopped', { chunks: this.chunkIndex });
    this.releaseDevices();
    this.phase = 'idle';
    this.events.stopped.emit({ message: 'stopped', meetingName: this.meetingName ?? undefined });
    const waiters = this.stopWaiters;
    this.stopWaiters = [];
    waiters.forEach((resolve) => resolve());
  }

  private detachRecorder(): void {
    if (this.recorder) {
      this.recorder.ondataavailable = null;
      this.recorder.onerror = null;
      this.recorder.onstop = null;
    }
    this.recorder = null;
  }

  /** Stops every captured track and closes the mixing graph. */
  private async releaseCapture(): Promise<void> {
    const capture = this.capture;
    this.capture = null;
    if (!capture) return;
    for (const stream of [capture.microphone, capture.shared, capture.output]) {
      stream?.getTracks().forEach((track) => {
        track.onended = null;
        track.stop();
      });
    }
    await capture.graph?.close();
  }

  private releaseDevices(): void {
    this.detachRecorder();
    void this.releaseCapture();
    this.events.captureChanged.emit(null);
  }

  async pause(): Promise<void> {
    if (this.phase !== 'recording' || !this.capabilities.pauseResume) return;
    this.recorder!.pause();
    this.pausedAt = this.env.now();
    this.phase = 'paused';
    this.events.paused.emit();
  }

  async resume(): Promise<void> {
    if (this.phase !== 'paused') return;
    this.recorder!.resume();
    this.pausedTotal += this.env.now() - (this.pausedAt ?? this.env.now());
    this.pausedAt = null;
    this.phase = 'recording';
    this.events.resumed.emit();
  }

  async startAudioLevelMonitoring(): Promise<void> {
    throw new UnsupportedOperationError('Audio level monitoring');
  }

  async stopAudioLevelMonitoring(): Promise<void> {}

  onStarting(callback: () => void) { return this.events.starting.on(callback); }
  onStarted(callback: () => void) { return this.events.started.on(callback); }
  onStopped(callback: (event: RecordingStoppedEvent) => void) { return this.events.stopped.on(callback); }
  onPaused(callback: () => void) { return this.events.paused.on(callback); }
  onResumed(callback: () => void) { return this.events.resumed.on(callback); }
  onMicrophoneUnavailable(callback: () => void) { return this.events.microphoneUnavailable.on(callback); }
  onStopComplete(_callback: (shouldPersist: boolean) => void) { return () => {}; }
  onSpeechDetected(_callback: () => void) { return () => {}; }
  onChunkDropWarning(_callback: (warning: string) => void) { return () => {}; }
  onAudioLevels(_callback: (update: AudioLevelUpdate) => void) { return () => {}; }
  onMicrophoneSwitched(_callback: (event: MicrophoneSwitchedEvent) => void) { return () => {}; }
  onMicrophoneSwapFailed(_callback: (event: MicrophoneSwapFailedEvent) => void) { return () => {}; }
  onMicrophoneRecoveryExhausted(_callback: (event: MicrophoneSwitchedEvent) => void) { return () => {}; }

  /** Number of active listeners (tests verify cleanup). */
  listenerCount(): number {
    return Object.values(this.events).reduce((sum, emitter) => sum + emitter.size, 0);
  }
}
