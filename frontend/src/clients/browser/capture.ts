/*
 * Browser meeting capture: which sources a browser can capture, how they are
 * requested, and how microphone + shared (tab/window/screen) audio are mixed
 * into one stream for MediaRecorder. Everything is feature-detected; nothing
 * depends on browser names or on which meeting site is in the shared tab.
 */

/** What a recording captures. */
export type CaptureMode = 'microphone' | 'microphone+shared' | 'shared';

export interface BrowserCaptureCapabilities {
  /** getUserMedia + MediaRecorder with a supported format. */
  microphone: boolean;
  /** navigator.mediaDevices.getDisplayMedia exists (a share picker can be shown). */
  displayCapture: boolean;
  /**
   * Whether shared audio is likely: browsers only reveal it after the user
   * picks a source. 'likely' when the browser advertises display-audio
   * constraints (Chromium); 'unknown' otherwise (e.g. Firefox and Safari
   * share video without audio). Never a promise.
   */
  sharedAudio: 'likely' | 'unknown' | 'unavailable';
  /** Web Audio can mix microphone and shared audio into one recordable stream. */
  mixing: boolean;
  /** Modes this browser can attempt, in UI order. */
  modes: CaptureMode[];
}

/** Which sources a running recording actually captures. */
export interface CaptureDescription {
  mode: CaptureMode;
  microphone: boolean;
  /** What the user shared, while its audio is still being captured. */
  shared: { surface: 'browser' | 'window' | 'monitor' | 'unknown' } | null;
}

// --- Minimal structural Web Audio types (real classes in browsers, fakes in tests) ---

export interface AudioNodeLike {
  connect(destination: AudioNodeLike): unknown;
  disconnect(): void;
}
export interface AudioParamLike { value: number }
export interface GainNodeLike extends AudioNodeLike { gain: AudioParamLike }
export interface WaveShaperNodeLike extends AudioNodeLike { curve: Float32Array | null; oversample: string }
export interface MediaStreamDestinationLike extends AudioNodeLike { stream: MediaStream }
export interface AudioContextLike {
  readonly state: string;
  resume(): Promise<void>;
  close(): Promise<void>;
  createMediaStreamSource(stream: MediaStream): AudioNodeLike;
  createMediaStreamDestination(): MediaStreamDestinationLike;
  createGain(): GainNodeLike;
  createWaveShaper(): WaveShaperNodeLike;
}
export type AudioContextConstructor = new () => AudioContextLike;

export interface CaptureMediaDevices {
  getUserMedia(constraints: MediaStreamConstraints): Promise<MediaStream>;
  getDisplayMedia?(options?: unknown): Promise<MediaStream>;
  getSupportedConstraints?(): Record<string, boolean | undefined>;
}

export function detectBrowserCapture(input: {
  mediaDevices?: Partial<CaptureMediaDevices>;
  canRecord: boolean;
  AudioContext?: AudioContextConstructor;
  /** MediaStream constructor (needed to record shared audio tracks on their own). */
  hasMediaStream: boolean;
}): BrowserCaptureCapabilities {
  const microphone = input.canRecord && typeof input.mediaDevices?.getUserMedia === 'function';
  const displayCapture = input.canRecord && typeof input.mediaDevices?.getDisplayMedia === 'function';
  const mixing = Boolean(input.AudioContext) && typeof input.AudioContext?.prototype?.createMediaStreamDestination === 'function';
  let supported: Record<string, boolean | undefined> = {};
  try {
    supported = input.mediaDevices?.getSupportedConstraints?.() ?? {};
  } catch {
    supported = {};
  }
  const sharedAudio: BrowserCaptureCapabilities['sharedAudio'] = !displayCapture ? 'unavailable'
    : supported.suppressLocalAudioPlayback || supported.restrictOwnAudio ? 'likely' : 'unknown';
  const modes: CaptureMode[] = [];
  if (microphone) modes.push('microphone');
  if (microphone && displayCapture && mixing) modes.push('microphone+shared');
  if (displayCapture && input.hasMediaStream) modes.push('shared');
  return { microphone, displayCapture, sharedAudio, mixing, modes };
}

/** Why capture could not start. Messages are written for users. */
export type CaptureFailure =
  | 'permission-denied'
  | 'no-microphone'
  | 'microphone-busy'
  | 'display-unsupported'
  | 'display-denied'
  | 'display-failed'
  | 'no-shared-audio'
  | 'audio-context'
  | 'encoder';

export const CAPTURE_FAILURE_MESSAGES: Record<CaptureFailure, string> = {
  'permission-denied': 'Microphone access was denied.',
  'no-microphone': 'No microphone was found.',
  'microphone-busy': 'The microphone could not be started.',
  'display-unsupported': 'This browser cannot share a tab or screen. Use “Microphone only”.',
  'display-denied': 'Sharing was cancelled or not allowed, so nothing was recorded.',
  'display-failed': 'The browser could not start sharing. Try again, or use “Microphone only”.',
  'no-shared-audio': 'The shared source has no audio. In the share dialog, choose the meeting tab and turn on “Share tab audio” (a window or screen usually has no audio).',
  'audio-context': 'The browser could not start audio mixing. Try again, or use “Microphone only”.',
  'encoder': 'This browser could not record the mixed audio. Use “Microphone only”.',
};

/** Microphone processing: on for the microphone only, never for shared audio. */
export function microphoneConstraints(deviceId?: string): MediaTrackConstraints {
  // Echo cancellation removes meeting audio that the speakers play back into
  // the microphone (browsers use their own playback as the reference).
  const processing = { echoCancellation: true, noiseSuppression: true, autoGainControl: true };
  return deviceId ? { deviceId: { exact: deviceId }, ...processing } : processing;
}

/**
 * Display request. Video is requested because browsers require it; it is
 * stopped as soon as audio is confirmed. Shared audio is left unprocessed
 * (it is already the far-end mix) and keeps playing locally. Hints the
 * browser may ignore: prefer a tab, hide Voxa's own tab, allow system audio
 * where the platform offers it.
 */
export const DISPLAY_MEDIA_OPTIONS = {
  video: { displaySurface: 'browser' },
  audio: { echoCancellation: false, noiseSuppression: false, autoGainControl: false, suppressLocalAudioPlayback: false },
  selfBrowserSurface: 'exclude',
  surfaceSwitching: 'include',
  systemAudio: 'include',
  preferCurrentTab: false,
} as const;

export function classifyMicrophoneError(error: unknown): CaptureFailure {
  const name = (error as { name?: string })?.name;
  if (name === 'NotAllowedError' || name === 'SecurityError') return 'permission-denied';
  if (name === 'NotFoundError' || name === 'OverconstrainedError') return 'no-microphone';
  return 'microphone-busy';
}

export function classifyDisplayError(error: unknown): CaptureFailure {
  const name = (error as { name?: string })?.name;
  // Cancelling the picker and policy refusals are both NotAllowedError.
  if (name === 'NotAllowedError' || name === 'SecurityError' || name === 'AbortError') return 'display-denied';
  if (name === 'NotSupportedError' || name === 'TypeError') return 'display-unsupported';
  return 'display-failed';
}

export function surfaceOf(stream: MediaStream): CaptureDescription['shared'] {
  const track = stream.getVideoTracks?.()[0] ?? stream.getAudioTracks()[0];
  const surface = (track?.getSettings?.() as { displaySurface?: string } | undefined)?.displaySurface;
  return { surface: surface === 'browser' || surface === 'window' || surface === 'monitor' ? surface : 'unknown' };
}

/**
 * Linear below 0.65, smooth knee approaching 0.95: transparent at normal
 * levels, no hard clipping when sources add up. The ceiling leaves room for
 * Opus encoding overshoot (measured ~2% above the input peak).
 */
export function softLimiterCurve(size = 4096): Float32Array {
  const curve = new Float32Array(size);
  const knee = 0.65;
  const ceiling = 0.95;
  for (let i = 0; i < size; i++) {
    const x = (i / (size - 1)) * 2 - 1;
    const magnitude = Math.abs(x);
    const shaped = magnitude <= knee ? magnitude : knee + (ceiling - knee) * Math.tanh((magnitude - knee) / (ceiling - knee));
    curve[i] = Math.sign(x) * shaped;
  }
  return curve;
}

/**
 * Mixes the microphone and shared audio into one MediaStream:
 *
 *   mic ─ source ─ gain ┐
 *                       ├─ soft limiter ─ MediaStreamDestination → MediaRecorder
 *   shared ─ source ─ gain ┘
 *
 * The graph is never connected to the speakers, so it adds no local
 * playback or echo. Each source keeps its own node, so one can be removed
 * (shared audio ended) without touching the other or the recorder, and a
 * future per-source recorder can tap them separately.
 */
export class MixGraph {
  readonly stream: MediaStream;
  private readonly inputs = new Map<'microphone' | 'shared', { source: AudioNodeLike; gain: GainNodeLike }>();
  private readonly limiter: WaveShaperNodeLike;
  private readonly destination: MediaStreamDestinationLike;

  constructor(private readonly context: AudioContextLike) {
    this.destination = context.createMediaStreamDestination();
    this.limiter = context.createWaveShaper();
    this.limiter.curve = softLimiterCurve();
    this.limiter.oversample = 'none';
    this.limiter.connect(this.destination);
    this.stream = this.destination.stream;
  }

  /** Created and resumed synchronously inside the user's click where possible. */
  static async create(AudioContext: AudioContextConstructor): Promise<MixGraph> {
    let context: AudioContextLike;
    try {
      context = new AudioContext();
    } catch {
      throw new CaptureError('audio-context');
    }
    try {
      if (context.state !== 'running') await context.resume();
    } catch {
      // Checked below.
    }
    if (context.state !== 'running') {
      await context.close().catch(() => {});
      throw new CaptureError('audio-context');
    }
    return new MixGraph(context);
  }

  add(kind: 'microphone' | 'shared', stream: MediaStream, level = 1): void {
    const source = this.context.createMediaStreamSource(stream);
    const gain = this.context.createGain();
    gain.gain.value = level;
    source.connect(gain);
    gain.connect(this.limiter);
    this.inputs.set(kind, { source, gain });
  }

  remove(kind: 'microphone' | 'shared'): void {
    const input = this.inputs.get(kind);
    if (!input) return;
    input.source.disconnect();
    input.gain.disconnect();
    this.inputs.delete(kind);
  }

  get state(): string {
    return this.context.state;
  }

  async close(): Promise<void> {
    for (const kind of [...this.inputs.keys()]) this.remove(kind);
    this.limiter.disconnect();
    this.destination.stream.getTracks().forEach((track) => track.stop());
    await this.context.close().catch(() => {});
  }
}

export class CaptureError extends Error {
  readonly reason: CaptureFailure;

  constructor(reason: CaptureFailure) {
    super(CAPTURE_FAILURE_MESSAGES[reason]);
    this.name = 'RecordingPermissionError';
    this.reason = reason;
  }
}
