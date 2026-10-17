import type {
  MeetingMetadata,
  PaginatedTranscriptsResponse,
  Transcript,
  TranscriptUpdate,
} from '@/types';

export interface MeetingListItem {
  id: string;
  title: string;
}

export interface MeetingDetails extends MeetingMetadata {
  transcripts: Transcript[];
}

export interface TranscriptSearchResult {
  id: string;
  title: string;
  matchContext: string;
  timestamp: string;
}

export interface CreateMeetingInput {
  title: string;
  transcripts: Transcript[];
  folderPath: string | null;
}

export interface CreateMeetingResult {
  meeting_id: string;
}

export interface MeetingClient {
  list(): Promise<MeetingListItem[]>;
  searchTranscripts(query: string): Promise<TranscriptSearchResult[]>;
  get(meetingId: string): Promise<MeetingDetails>;
  getMetadata(meetingId: string): Promise<MeetingMetadata>;
  getTranscripts(
    meetingId: string,
    page: { limit: number; offset: number },
  ): Promise<PaginatedTranscriptsResponse>;
  create(input: CreateMeetingInput): Promise<CreateMeetingResult>;
  rename(meetingId: string, title: string): Promise<void>;
  delete(meetingId: string): Promise<void>;
}

export interface RecordingState {
  isRecording: boolean;
  isPaused: boolean;
  isActive: boolean;
  recordingDuration: number | null;
  activeDuration: number | null;
}

export interface RecordingCapabilities {
  systemAudioCapture: 'native' | 'display-media' | 'unavailable';
  deviceEnumeration: 'input-and-output' | 'input-only' | 'unavailable';
  microphoneHotSwap: 'automatic' | 'manual' | 'unavailable';
  audioLevelMonitoring: boolean;
  pauseResume: boolean;
  tabCapture: boolean;
}

export interface RecordingDevice {
  name: string;
  type: 'input' | 'output';
}

export interface RecordingStartOptions {
  microphoneDeviceName: string | null;
  systemAudioDeviceName: string | null;
  meetingName: string;
}

export interface RecordingStoppedEvent {
  message: string;
  folderPath?: string;
  meetingName?: string;
}

export interface AudioLevelData {
  deviceName: string;
  deviceType: string;
  rmsLevel: number;
  peakLevel: number;
  isActive: boolean;
}

export interface AudioLevelUpdate {
  timestamp: number;
  levels: AudioLevelData[];
}

export interface MicrophoneSwitchedEvent {
  deviceName: string;
}

export interface MicrophoneSwapFailedEvent extends MicrophoneSwitchedEvent {
  error: string;
}

export interface RecordingClient {
  readonly capabilities: RecordingCapabilities;
  listDevices(): Promise<RecordingDevice[]>;
  isRecording(): Promise<boolean>;
  getState(): Promise<RecordingState>;
  getMeetingName(): Promise<string | null>;
  getSessionFolderPath(): Promise<string | null>;
  start(input: RecordingStartOptions): Promise<void>;
  stop(): Promise<void>;
  pause(): Promise<void>;
  resume(): Promise<void>;
  startAudioLevelMonitoring(deviceNames: string[]): Promise<void>;
  stopAudioLevelMonitoring(): Promise<void>;
  onStarting(callback: () => void): () => void;
  onStarted(callback: () => void): () => void;
  onStopped(callback: (event: RecordingStoppedEvent) => void): () => void;
  onPaused(callback: () => void): () => void;
  onResumed(callback: () => void): () => void;
  onStopComplete(callback: (shouldPersist: boolean) => void): () => void;
  onSpeechDetected(callback: () => void): () => void;
  onChunkDropWarning(callback: (warning: string) => void): () => void;
  onAudioLevels(callback: (update: AudioLevelUpdate) => void): () => void;
  onMicrophoneSwitched(callback: (event: MicrophoneSwitchedEvent) => void): () => void;
  onMicrophoneUnavailable(callback: () => void): () => void;
  onMicrophoneSwapFailed(callback: (event: MicrophoneSwapFailedEvent) => void): () => void;
  onMicrophoneRecoveryExhausted(callback: (event: MicrophoneSwitchedEvent) => void): () => void;
}

export interface TranscriptionClient {
  readonly capabilities: TranscriptionCapabilities;
  getSelectedProvider(): Promise<TranscriptionProvider>;
  ensureReady(): Promise<boolean>;
  initializeProvider(provider: TranscriptionProvider): Promise<void>;
  hasAvailableModels(provider: TranscriptionProvider): Promise<boolean>;
  listModels(provider: TranscriptionProvider): Promise<TranscriptionModel[]>;
  getHistory(): Promise<Transcript[]>;
  getStatus(): Promise<TranscriptionStatus>;
  startRetranscription(options: RetranscriptionOptions): Promise<void>;
  cancelRetranscription(): Promise<void>;
  startImport(options: ImportTranscriptionOptions): Promise<void>;
  cancelImport(): Promise<void>;
  onUpdate(
    callback: (update: TranscriptEvent) => void,
    onRegistrationError?: (error: unknown) => void,
  ): () => void;
  onComplete(callback: () => void): () => void;
  onError(callback: (error: TranscriptionError) => void): () => void;
  onLegacyError(callback: (error: string) => void): () => void;
  onModelDownloadProgress(
    callback: (event: ModelDownloadProgress) => void,
    onRegistrationError?: (error: unknown) => void,
  ): () => void;
  onModelDownloadComplete(
    callback: (event: ModelDownloadComplete) => void,
    onRegistrationError?: (error: unknown) => void,
  ): () => void;
  onModelDownloadError(
    callback: (event: ModelDownloadError) => void,
    onRegistrationError?: (error: unknown) => void,
  ): () => void;
  onRetranscriptionProgress(callback: (event: RetranscriptionProgress) => void): () => void;
  onRetranscriptionComplete(callback: (event: RetranscriptionResult) => void): () => void;
  onRetranscriptionError(callback: (event: RetranscriptionError) => void): () => void;
  onImportProgress(callback: (event: ImportTranscriptionProgress) => void): () => void;
  onImportComplete(callback: (event: ImportTranscriptionResult) => void): () => void;
  onImportError(callback: (event: ImportTranscriptionError) => void): () => void;
}

export type TranscriptionProvider = 'localWhisper' | 'parakeet';

export interface TranscriptionCapabilities {
  execution: 'local' | 'remote';
  modelManagement: boolean;
  modelDownload: boolean;
  retranscription: boolean;
  importedAudio: boolean;
  translationToEnglish: boolean;
}

export type TranscriptionModelStatus =
  | 'available'
  | 'missing'
  | { downloading: { progress: number } }
  | { error: string }
  | { corrupted: { fileSize: number; expectedMinSize: number } };

export interface TranscriptionModel {
  name: string;
  sizeMb: number;
  status: TranscriptionModelStatus;
}

export interface TranscriptionStatus {
  chunksInQueue: number;
  isProcessing: boolean;
  lastActivityMs: number;
}

export interface TranscriptEvent {
  text: string;
  timestamp: string;
  source: string;
  sequenceId: number;
  chunkStartTime: number;
  isPartial: boolean;
  confidence: number;
  audioStartTime: number;
  audioEndTime: number;
  duration: number;
}

export interface ModelDownloadProgress {
  provider: TranscriptionProvider;
  modelName: string;
  progress: number;
  status?: 'downloading' | 'completed' | 'cancelled';
  downloadedBytes?: number;
  totalBytes?: number;
  downloadedMb?: number;
  totalMb?: number;
  speedMbps?: number;
}

export interface ModelDownloadComplete {
  provider: TranscriptionProvider;
  modelName: string;
}

export interface ModelDownloadError extends ModelDownloadComplete { error: string; }

export interface RetranscriptionOptions {
  meetingId: string;
  source: MediaSource;
  language: string | null;
  model: string | null;
  provider: string | null;
}

export interface RetranscriptionProgress {
  meetingId: string;
  stage: string;
  progressPercentage: number;
  message: string;
}

export interface RetranscriptionResult {
  meetingId: string;
  segmentsCount: number;
  durationSeconds: number;
  language: string | null;
}

export interface RetranscriptionError { meetingId: string; error: string; }

export interface ImportTranscriptionOptions {
  source: MediaSource;
  title: string;
  language: string | null;
  model: string | null;
  provider: string | null;
}

export interface ImportTranscriptionProgress {
  stage: string;
  progressPercentage: number;
  message: string;
}

export interface ImportTranscriptionResult {
  meetingId: string;
  title: string;
  segmentsCount: number;
  durationSeconds: number;
}

export interface ImportTranscriptionError { error: string; }

export interface TranscriptionError {
  error: string;
  userMessage: string;
  actionable: boolean;
  phase: 'startup' | 'active';
}

export interface SummaryClient {
  readonly capabilities: SummaryCapabilities;
  getState(meetingId: string): Promise<SummaryState>;
  generate(request: SummaryRequest): Promise<SummaryGenerationResult>;
  cancel(meetingId: string, processId: string): Promise<SummaryCancellationResult>;
  save(meetingId: string, summary: unknown): Promise<void>;
  listTemplates(): Promise<SummaryTemplate[]>;
  checkRuntime(input: SummaryRuntimeInput): Promise<SummaryRuntimeReadiness>;
}

export type SummaryProvider =
  | 'builtin-ai'
  | 'ollama'
  | 'openai'
  | 'claude'
  | 'groq'
  | 'openrouter'
  | 'custom-openai';

export interface SummaryCapabilities {
  execution: 'local' | 'remote' | 'local-and-remote';
  asynchronousJobs: boolean;
  polling: boolean;
  cancellation: boolean;
  templates: boolean;
  localRuntimePreflight: boolean;
  providers: SummaryProvider[];
}

export type SummaryStatus =
  | 'pending'
  | 'processing'
  | 'completed'
  | 'failed'
  | 'cancelled'
  | 'error'
  | 'idle';

export interface SummaryState {
  status: SummaryStatus;
  meetingName: string | null;
  meetingId: string;
  processId: string | null;
  endedAt: string | null;
  data: unknown | null;
  error: string | null;
}

export interface SummaryRequest {
  text: string;
  provider: SummaryProvider;
  modelName: string;
  meetingId: string;
  chunkSize: number;
  overlap: number;
  customPrompt: string;
  templateId: string;
  summaryLanguage: string | null;
}

export interface SummaryGenerationResult { message: string; processId: string; }
export interface SummaryCancellationResult {
  cancelled: boolean;
  message: string;
  meetingId: string;
}
export interface SummaryTemplate { id: string; name: string; description: string; }
export interface SummaryRuntimeInput {
  provider: SummaryProvider;
  modelName: string;
  ollamaEndpoint: string | null;
}
export type SummaryRuntimeReadiness =
  | { ready: true }
  | { ready: false; reason: 'no-model-selected' | 'no-models-available' | 'model-not-ready' };

export interface MediaClient {
  readonly capabilities: MediaCapabilities;
  getMeetingSource(meetingId: string): Promise<MediaSource | null>;
  getPlaybackSource(source: MediaSource): Promise<MediaPlaybackSource>;
  selectImportSource(): Promise<MediaImportSelection | null>;
  validateDroppedImport(nativeReference: string): Promise<MediaImportSelection>;
  openMeetingFolder(meetingId: string): Promise<void>;
  openRecordingsFolder(): Promise<void>;
}

export interface MediaCapabilities {
  nativeFilePicker: boolean;
  filesystemReveal: boolean;
  localByteReads: boolean;
  urlPlayback: boolean;
  rangedStreaming: boolean;
  import: boolean;
  exportDownload: boolean;
}

declare const mediaSourceBrand: unique symbol;

/** Opaque transport-owned media locator. */
export type MediaSource = { readonly [mediaSourceBrand]: true };

export type MediaPlaybackSource =
  | { kind: 'bytes'; bytes: number[] }
  | { kind: 'url'; url: string };

export interface MediaImportSelection {
  source: MediaSource;
  fileName: string;
  durationSeconds: number;
  sizeBytes: number;
  format: string;
}

export interface VoxaClient {
  meetings: MeetingClient;
  recording: RecordingClient;
  transcription: TranscriptionClient;
  summaries: SummaryClient;
  media: MediaClient;
}
