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

export class RecordingPermissionError extends Error {
  readonly reason: 'permission-denied' | 'no-microphone' | 'microphone-busy';

  constructor(reason: RecordingPermissionError['reason']) {
    super({
      'permission-denied': 'Microphone access was denied.',
      'no-microphone': 'No microphone was found.',
      'microphone-busy': 'The microphone could not be started.',
    }[reason]);
    this.name = 'RecordingPermissionError';
    this.reason = reason;
  }
}

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
  mediaDevices?: Pick<MediaDevices, 'getUserMedia' | 'enumerateDevices'>;
  MediaRecorder?: RecorderConstructor;
  now: () => number;
  timesliceMs: number;
}

export function defaultBrowserEnvironment(): BrowserMediaEnvironment {
  const scope = globalThis as unknown as { navigator?: Navigator; MediaRecorder?: RecorderConstructor };
  return {
    mediaDevices: scope.navigator?.mediaDevices,
    MediaRecorder: scope.MediaRecorder,
    now: () => Date.now(),
    timesliceMs: RECORDING_TIMESLICE_MS,
  };
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
  private stream: MediaStream | null = null;
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
  };

  constructor(env: BrowserMediaEnvironment = defaultBrowserEnvironment()) {
    this.env = env;
    this.selectedMime = env.MediaRecorder ? selectRecordingMimeType((type) => env.MediaRecorder!.isTypeSupported(type)) : null;
    const canRecord = Boolean(env.mediaDevices?.getUserMedia && env.MediaRecorder && this.selectedMime);
    const pauseSupported = Boolean(env.MediaRecorder && typeof env.MediaRecorder.prototype?.pause === 'function');
    this.capabilities = {
      microphoneCapture: canRecord,
      systemAudioCapture: 'unavailable',
      deviceEnumeration: canRecord && env.mediaDevices?.enumerateDevices ? 'input-only' : 'unavailable',
      microphoneHotSwap: 'unavailable',
      audioLevelMonitoring: false,
      pauseResume: canRecord && pauseSupported,
      tabCapture: false,
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

  async start(input: RecordingStartOptions): Promise<void> {
    if (this.phase !== 'idle') throw new Error('A recording is already in progress');
    if (!this.capabilities.microphoneCapture) throw new UnsupportedOperationError('Microphone recording');
    if (input.systemAudioDeviceName) throw new UnsupportedOperationError('System audio capture');
    this.phase = 'starting';
    this.events.starting.emit();
    let stream: MediaStream;
    try {
      const deviceId = input.microphoneDeviceName ? this.deviceIds.get(input.microphoneDeviceName) : undefined;
      stream = await this.env.mediaDevices!.getUserMedia({
        audio: deviceId ? { deviceId: { exact: deviceId }, echoCancellation: true, noiseSuppression: true } : { echoCancellation: true, noiseSuppression: true },
        video: false,
      });
    } catch (error) {
      this.phase = 'idle';
      const name = (error as { name?: string })?.name;
      if (name === 'NotAllowedError' || name === 'SecurityError') throw new RecordingPermissionError('permission-denied');
      if (name === 'NotFoundError' || name === 'OverconstrainedError') throw new RecordingPermissionError('no-microphone');
      throw new RecordingPermissionError('microphone-busy');
    }

    let recorder: RecorderLike;
    try {
      recorder = new this.env.MediaRecorder!(stream, { mimeType: this.selectedMime!, audioBitsPerSecond: 64_000 });
    } catch {
      stream.getTracks().forEach((track) => track.stop());
      this.phase = 'idle';
      throw new UnsupportedOperationError(`Recording as ${this.selectedMime}`);
    }
    this.stream = stream;
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
      this.events.error.emit('The recorder stopped unexpectedly.');
      void this.stop();
    };
    recorder.onstop = () => this.finish();
    for (const track of stream.getAudioTracks()) {
      track.onended = () => {
        // Unplugged or revoked microphone: keep what was recorded.
        this.events.microphoneUnavailable.emit();
        void this.stop();
      };
    }
    try {
      recorder.start(this.env.timesliceMs);
    } catch {
      this.releaseDevices();
      this.phase = 'idle';
      throw new RecordingPermissionError('microphone-busy');
    }
    this.startedAt = this.env.now();
    this.phase = 'recording';
    this.events.started.emit();
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
    this.releaseDevices();
    this.phase = 'idle';
    this.events.stopped.emit({ message: 'stopped', meetingName: this.meetingName ?? undefined });
    const waiters = this.stopWaiters;
    this.stopWaiters = [];
    waiters.forEach((resolve) => resolve());
  }

  private releaseDevices(): void {
    if (this.recorder) {
      this.recorder.ondataavailable = null;
      this.recorder.onerror = null;
      this.recorder.onstop = null;
    }
    this.stream?.getTracks().forEach((track) => {
      track.onended = null;
      track.stop();
    });
    this.stream = null;
    this.recorder = null;
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
