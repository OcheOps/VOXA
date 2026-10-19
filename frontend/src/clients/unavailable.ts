import type {
  ImportTranscriptionOptions,
  MediaCapabilities,
  MediaClient,
  MediaImportSelection,
  MediaPlaybackSource,
  MediaSource,
  RecordingCapabilities,
  RecordingClient,
  RecordingDevice,
  RecordingState,
  RetranscriptionOptions,
  SummaryCancellationResult,
  SummaryCapabilities,
  SummaryClient,
  SummaryGenerationResult,
  SummaryRuntimeReadiness,
  SummaryState,
  SummaryTemplate,
  TranscriptionCapabilities,
  TranscriptionClient,
  TranscriptionModel,
  TranscriptionProvider,
  TranscriptionStatus,
} from './contracts';
import type { Transcript } from '@/types';
import { UnsupportedOperationError } from './errors';

/*
 * Placeholder clients for runtimes where a capability does not exist yet
 * (the web runtime has no browser recording, media storage, transcription
 * or summary jobs). They report capabilities truthfully, answer queries
 * with empty/idle results so shared providers can mount, register no-op
 * subscriptions, and reject every action with UnsupportedOperationError.
 * They never pretend an operation succeeded.
 */

const noop = () => () => {};
const unsupported = (operation: string) => () => Promise.reject(new UnsupportedOperationError(operation));

export class UnavailableRecordingClient implements RecordingClient {
  readonly capabilities: RecordingCapabilities = {
    systemAudioCapture: 'unavailable',
    deviceEnumeration: 'unavailable',
    microphoneHotSwap: 'unavailable',
    audioLevelMonitoring: false,
    pauseResume: false,
    tabCapture: false,
  };

  async listDevices(): Promise<RecordingDevice[]> { return []; }
  async isRecording(): Promise<boolean> { return false; }
  async getState(): Promise<RecordingState> {
    return { isRecording: false, isPaused: false, isActive: false, recordingDuration: null, activeDuration: null };
  }
  async getMeetingName(): Promise<string | null> { return null; }
  async getSessionFolderPath(): Promise<string | null> { return null; }
  start = unsupported('Recording');
  stop = unsupported('Recording');
  pause = unsupported('Recording');
  resume = unsupported('Recording');
  startAudioLevelMonitoring = unsupported('Audio level monitoring');
  async stopAudioLevelMonitoring(): Promise<void> {}
  onStarting = noop;
  onStarted = noop;
  onStopped = noop;
  onPaused = noop;
  onResumed = noop;
  onStopComplete = noop;
  onSpeechDetected = noop;
  onChunkDropWarning = noop;
  onAudioLevels = noop;
  onMicrophoneSwitched = noop;
  onMicrophoneUnavailable = noop;
  onMicrophoneSwapFailed = noop;
  onMicrophoneRecoveryExhausted = noop;
}

export class UnavailableTranscriptionClient implements TranscriptionClient {
  readonly capabilities: TranscriptionCapabilities = {
    execution: 'unavailable',
    modelManagement: false,
    modelDownload: false,
    retranscription: false,
    importedAudio: false,
    translationToEnglish: false,
  };

  async getSelectedProvider(): Promise<TranscriptionProvider> {
    throw new UnsupportedOperationError('Transcription');
  }
  async ensureReady(): Promise<boolean> { return false; }
  initializeProvider = unsupported('Transcription');
  setLanguage = unsupported('Transcription');
  async hasAvailableModels(): Promise<boolean> { return false; }
  async listModels(): Promise<TranscriptionModel[]> { return []; }
  async getHistory(): Promise<Transcript[]> { return []; }
  async getStatus(): Promise<TranscriptionStatus> { return { chunksInQueue: 0, isProcessing: false, lastActivityMs: 0 }; }
  startRetranscription: (options: RetranscriptionOptions) => Promise<void> = unsupported('Retranscription');
  async cancelRetranscription(): Promise<void> {}
  startImport: (options: ImportTranscriptionOptions) => Promise<void> = unsupported('Audio import');
  async cancelImport(): Promise<void> {}
  onUpdate = noop;
  onComplete = noop;
  onError = noop;
  onLegacyError = noop;
  onModelDownloadProgress = noop;
  onModelDownloadComplete = noop;
  onModelDownloadError = noop;
  onRetranscriptionProgress = noop;
  onRetranscriptionComplete = noop;
  onRetranscriptionError = noop;
  onImportProgress = noop;
  onImportComplete = noop;
  onImportError = noop;
}

export class UnavailableSummaryClient implements SummaryClient {
  readonly capabilities: SummaryCapabilities = {
    execution: 'unavailable',
    asynchronousJobs: false,
    polling: false,
    cancellation: false,
    templates: false,
    localRuntimePreflight: false,
    providers: [],
  };

  async getState(meetingId: string): Promise<SummaryState> {
    return { status: 'idle', meetingName: null, meetingId, processId: null, endedAt: null, data: null, error: null };
  }
  generate: () => Promise<SummaryGenerationResult> = unsupported('Summary generation');
  cancel: () => Promise<SummaryCancellationResult> = unsupported('Summary generation');
  save: () => Promise<void> = unsupported('Saving summaries');
  async listTemplates(): Promise<SummaryTemplate[]> { return []; }
  async checkRuntime(): Promise<SummaryRuntimeReadiness> { return { ready: false, reason: 'no-models-available' }; }
}

export class UnavailableMediaClient implements MediaClient {
  readonly capabilities: MediaCapabilities = {
    nativeFilePicker: false,
    filesystemReveal: false,
    localByteReads: false,
    urlPlayback: false,
    rangedStreaming: false,
    import: false,
    exportDownload: false,
  };

  async getMeetingSource(): Promise<MediaSource | null> { return null; }
  getPlaybackSource: (source: MediaSource) => Promise<MediaPlaybackSource> = unsupported('Media playback');
  selectImportSource: () => Promise<MediaImportSelection | null> = unsupported('Media import');
  validateDroppedImport: (reference: string) => Promise<MediaImportSelection> = unsupported('Media import');
  openMeetingFolder = unsupported('Revealing files');
  openRecordingsFolder = unsupported('Revealing files');
}
