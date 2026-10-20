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

export interface MeetingCapabilities {
  /** Meetings carry transcript segments readable through this client. */
  transcripts: boolean;
  /** Full-text transcript search is available. */
  transcriptSearch: boolean;
  /** `create` accepts transcripts and a native folder path, not only a title. */
  createWithTranscripts: boolean;
}

export interface MeetingClient {
  readonly capabilities: MeetingCapabilities;
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
  /** The runtime can record from a microphone. */
  microphoneCapture: boolean;
  systemAudioCapture: 'native' | 'display-media' | 'unavailable';
  deviceEnumeration: 'input-and-output' | 'input-only' | 'unavailable';
  microphoneHotSwap: 'automatic' | 'manual' | 'unavailable';
  audioLevelMonitoring: boolean;
  pauseResume: boolean;
  /** The runtime can capture audio from a user-selected tab/window/screen. */
  tabCapture: boolean;
  /** Browser runtimes: feature-detected microphone/shared-audio capture modes. */
  browserCapture?: import('./browser/capture').BrowserCaptureCapabilities;
}

export interface RecordingDevice {
  name: string;
  type: 'input' | 'output';
}

export interface RecordingStartOptions {
  microphoneDeviceName: string | null;
  systemAudioDeviceName: string | null;
  meetingName: string;
  /** Browser runtimes: what to capture (default: microphone). Ignored by the desktop app. */
  captureMode?: import('./browser/capture').CaptureMode;
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
  setLanguage(language: string): Promise<void>;
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
  execution: 'local' | 'remote' | 'unavailable';
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
  execution: 'local' | 'remote' | 'local-and-remote' | 'unavailable';
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

export type SummaryPreferenceProvider = SummaryProvider;

export interface TranscriptionPreferences {
  provider: 'localWhisper' | 'parakeet' | 'deepgram' | 'elevenLabs' | 'groq' | 'openai';
  model: string;
  apiKey: string | null;
}

export interface SummaryPreferences {
  provider: SummaryPreferenceProvider;
  model: string;
  whisperModel: string;
  apiKey?: string | null;
  ollamaEndpoint?: string | null;
  customOpenAIDisplayName?: string | null;
  customOpenAIEndpoint?: string | null;
  customOpenAIModel?: string | null;
  customOpenAIApiKey?: string | null;
  maxTokens?: number | null;
  temperature?: number | null;
  topP?: number | null;
}

export interface CustomOpenAIConfiguration {
  displayName?: string | null;
  endpoint: string;
  apiKey: string | null;
  model: string;
  maxTokens: number | null;
  temperature: number | null;
  topP: number | null;
}

export interface RecordingPreferences {
  saveFolder: string;
  autoSave: boolean;
  fileFormat: string;
  preferredMicrophoneDevice: string | null;
  preferredSystemAudioDevice: string | null;
  systemAudioBackend?: string | null;
}

export interface ConfigurationCapabilities {
  remoteCredentialStorage: boolean;
  desktopStorageLocations: boolean;
  recordingDeviceDefaults: boolean;
  nativeNotificationPreferences: boolean;
}

export interface NotificationPreferences {
  recordingNotifications: boolean;
  timeBasedReminders: boolean;
  meetingReminders: boolean;
  respectDoNotDisturb: boolean;
  notificationSound: boolean;
  systemPermissionGranted: boolean;
  consentGiven: boolean;
  manualDndMode: boolean;
  notifications: {
    showRecordingStarted: boolean;
    showRecordingStopped: boolean;
    showRecordingPaused: boolean;
    showRecordingResumed: boolean;
    showTranscriptionComplete: boolean;
    showMeetingReminders: boolean;
    showSystemErrors: boolean;
    meetingReminderMinutes: number[];
  };
}

export interface ConfigurationClient {
  readonly capabilities: ConfigurationCapabilities;
  getTranscriptionPreferences(): Promise<TranscriptionPreferences>;
  saveTranscriptionPreferences(preferences: TranscriptionPreferences): Promise<void>;
  getTranscriptionApiKey(provider: string): Promise<string>;
  getTranscriptionLanguage(): Promise<string>;
  saveTranscriptionLanguage(language: string): Promise<void>;
  getSummaryPreferences(): Promise<SummaryPreferences | null>;
  saveSummaryPreferences(preferences: SummaryPreferences): Promise<void>;
  getSummaryApiKey(provider: string): Promise<string>;
  getCustomOpenAIConfiguration(): Promise<CustomOpenAIConfiguration | null>;
  saveCustomOpenAIConfiguration(configuration: CustomOpenAIConfiguration): Promise<void>;
  getAutoSummaryEnabled(): Promise<boolean>;
  setAutoSummaryEnabled(enabled: boolean): Promise<void>;
  getRecordingPreferences(): Promise<RecordingPreferences>;
  saveRecordingPreferences(preferences: RecordingPreferences): Promise<void>;
  getDefaultRecordingsFolder(): Promise<string>;
  getRecordingNotificationEnabled(): Promise<boolean>;
  setRecordingNotificationEnabled(enabled: boolean): Promise<void>;
  getNotificationPreferences(): Promise<NotificationPreferences>;
  saveNotificationPreferences(preferences: NotificationPreferences): Promise<void>;
}

/** Server-stored media (web). Capture itself stays in RecordingClient. */
export type ServerMediaStatus = 'uploading' | 'ready' | 'failed';

export interface ServerMediaAsset {
  id: string;
  meetingId: string;
  mimeType: string;
  status: ServerMediaStatus;
  durationSeconds: number | null;
  sizeBytes: number;
  createdAt: string;
  updatedAt: string;
}

export interface RecordingUploadTarget {
  asset: ServerMediaAsset;
  recordingSessionId: string;
  maxChunkBytes: number;
  maxTotalBytes: number;
}

export interface UploadProgress {
  status: ServerMediaStatus;
  receivedChunks: number[];
  receivedBytes: number;
}

export interface PlaybackUrl {
  url: string;
  expiresAt: string;
}

/**
 * Chunked, resumable recording storage on the Voxa server. Chunks are
 * ordered pieces of one encoder stream; retries of identical chunks are
 * safe and finalization requires exactly chunks 0..n-1.
 */
export interface ServerMediaClient {
  createRecording(meetingId: string, mimeType: string): Promise<RecordingUploadTarget>;
  uploadChunk(mediaAssetId: string, index: number, data: Blob, signal?: AbortSignal): Promise<UploadProgress>;
  getUploadStatus(mediaAssetId: string): Promise<UploadProgress>;
  finalize(mediaAssetId: string, chunkCount: number, durationSeconds: number | null): Promise<ServerMediaAsset>;
  listMeetingMedia(meetingId: string): Promise<ServerMediaAsset[]>;
  getPlaybackUrl(mediaAssetId: string): Promise<PlaybackUrl>;
  deleteMedia(mediaAssetId: string): Promise<void>;
}

/** User-facing processing state of one server recording. */
export type RecordingProcessingState = 'none' | 'preparing' | 'transcribing' | 'ready' | 'failed' | 'cancelled' | 'unavailable';

export interface TranscriptSegmentView {
  id: string;
  text: string;
  /** Seconds from the start of the recording. */
  startSeconds: number;
  endSeconds: number;
  sequence: number;
}

export interface RecordingTranscript {
  mediaAssetId: string | null;
  state: RecordingProcessingState;
  error: { code: string; message: string } | null;
  transcript: {
    id: string;
    language: string | null;
    asrEngine: string | null;
    asrModel: string | null;
    segments: TranscriptSegmentView[];
  } | null;
}

/** Server-side transcription of stored recordings (web). */
export interface ServerTranscriptClient {
  getRecordingTranscript(mediaAssetId: string, signal?: AbortSignal): Promise<RecordingTranscript>;
  getMeetingTranscript(meetingId: string, signal?: AbortSignal): Promise<RecordingTranscript>;
  requestTranscription(mediaAssetId: string): Promise<void>;
  /** Cancels the recording's active processing, if any. */
  cancelProcessing(meetingId: string, mediaAssetId: string): Promise<boolean>;
}

export type MeetingSummaryState = 'none' | 'queued' | 'generating' | 'ready' | 'failed' | 'cancelled' | 'unavailable';

/** A server-generated meeting summary and its processing state (web). */
export interface MeetingSummaryView {
  meetingId: string;
  state: MeetingSummaryState;
  /** Why summaries cannot run (unavailable) or are not possible yet (none). */
  reason: string | null;
  /** The summary describes a transcript that has since been replaced. */
  stale: boolean;
  error: { code: string; message: string } | null;
  jobId: string | null;
  progress: number | null;
  /** Kept while a regeneration runs and after a failed one. */
  summary: {
    id: string;
    markdown: string;
    title: string | null;
    provider: string | null;
    model: string | null;
    templateId: string | null;
    outputLanguage: string | null;
    transcriptId: string | null;
    updatedAt: string;
  } | null;
}

/** Server-side summaries (web). Provider, model and credentials are server configuration. */
export interface ServerSummaryClient {
  getMeetingSummary(meetingId: string, signal?: AbortSignal): Promise<MeetingSummaryView>;
  generateSummary(meetingId: string, options?: { templateId?: string; customPrompt?: string }): Promise<void>;
  /** Cancels the meeting's active summary job; false when none was running. */
  cancelSummary(meetingId: string): Promise<boolean>;
  listSummaryTemplates(): Promise<SummaryTemplate[]>;
}

/** Provider credential metadata. Secrets are write-only and never read back. */
export interface CredentialStatus {
  provider: string;
  configured: boolean;
}

/** Server-held, write-only provider credentials (web runtime). */
export interface CredentialClient {
  list(): Promise<CredentialStatus[]>;
  set(provider: string, secret: string): Promise<CredentialStatus>;
  remove(provider: string): Promise<void>;
}

/** Workspace defaults stored by the Voxa server. */
export interface WorkspaceSettings {
  transcription: { provider: string; model: string; language: string; translateToEnglish: boolean };
  summary: { provider: string; model: string; outputLanguage: string; templateId: string | null; autoGenerate: boolean };
  retainMedia: boolean;
  analyticsEnabled: boolean;
  processingCompleteNotifications: boolean;
}

export interface WorkspaceSettingsPatch {
  transcription?: Partial<WorkspaceSettings['transcription']>;
  summary?: Partial<WorkspaceSettings['summary']>;
  retainMedia?: boolean;
  analyticsEnabled?: boolean;
  processingCompleteNotifications?: boolean;
}

export interface WorkspaceSettingsClient {
  get(): Promise<WorkspaceSettings>;
  update(patch: WorkspaceSettingsPatch): Promise<WorkspaceSettings>;
}

export interface VoxaClient {
  meetings: MeetingClient;
  recording: RecordingClient;
  transcription: TranscriptionClient;
  summaries: SummaryClient;
  media: MediaClient;
  configuration: ConfigurationClient;
  /** Present only where a server stores credentials (web). Desktop keeps keys in its own preferences. */
  credentials?: CredentialClient;
  /** Present only where workspace defaults live on a server (web). */
  workspaceSettings?: WorkspaceSettingsClient;
  /** Present only where media is stored on a server (web). */
  serverMedia?: ServerMediaClient;
  /** Present only where recordings are transcribed on a server (web). */
  serverTranscripts?: ServerTranscriptClient;
  serverSummaries?: ServerSummaryClient;
}
