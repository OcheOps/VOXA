import { invoke } from '@tauri-apps/api/core';
import type { Transcript, TranscriptUpdate } from '@/types';
import type {
  ImportTranscriptionError, ImportTranscriptionOptions, ImportTranscriptionProgress,
  ImportTranscriptionResult, ModelDownloadComplete, ModelDownloadError,
  ModelDownloadProgress, RetranscriptionError, RetranscriptionOptions,
  RetranscriptionProgress, RetranscriptionResult, TranscriptEvent,
  TranscriptionCapabilities, TranscriptionClient, TranscriptionError,
  TranscriptionModel, TranscriptionModelStatus, TranscriptionProvider,
  TranscriptionStatus,
} from '../contracts';
import { combineSubscriptions, subscribeToTauriEvent } from './subscribe';
import { getTauriMediaReference } from './media-source';

interface NativeTranscriptConfig { provider?: string; }
interface NativeTranscriptionStatus {
  chunks_in_queue: number; is_processing: boolean; last_activity_ms: number;
}
interface NativeModel {
  name: string;
  size_mb: number;
  status: 'Available' | 'Missing'
    | { Downloading: { progress: number } }
    | { Error: string }
    | { Corrupted: { file_size: number; expected_min_size: number } };
}
interface NativeModelProgress {
  modelName: string;
  progress: number;
  status?: 'downloading' | 'completed' | 'cancelled';
  downloaded_bytes?: number;
  total_bytes?: number;
  downloaded_mb?: number;
  total_mb?: number;
  speed_mbps?: number;
}
interface NativeRetranscriptionProgress {
  meeting_id: string; stage: string; progress_percentage: number; message: string;
}
interface NativeRetranscriptionResult {
  meeting_id: string; segments_count: number; duration_seconds: number; language: string | null;
}
interface NativeRetranscriptionError { meeting_id: string; error: string; }
interface NativeImportProgress { stage: string; progress_percentage: number; message: string; }
interface NativeImportResult {
  meeting_id: string; title: string; segments_count: number; duration_seconds: number;
}

const PROVIDER_COMMANDS = {
  localWhisper: {
    initialize: 'whisper_init',
    hasAvailableModels: 'whisper_has_available_models',
    getAvailableModels: 'whisper_get_available_models',
  },
  parakeet: {
    initialize: 'parakeet_init',
    hasAvailableModels: 'parakeet_has_available_models',
    getAvailableModels: 'parakeet_get_available_models',
  },
} as const;

function normalizeProvider(provider?: string): TranscriptionProvider {
  if (!provider || provider === 'parakeet') return 'parakeet';
  if (provider === 'localWhisper') return 'localWhisper';
  throw new Error(`Unsupported transcription provider: ${provider}`);
}

function normalizeModelStatus(status: NativeModel['status']): TranscriptionModelStatus {
  if (status === 'Available') return 'available';
  if (status === 'Missing') return 'missing';
  if ('Downloading' in status) return { downloading: { progress: status.Downloading.progress } };
  if ('Error' in status) return { error: status.Error };
  return { corrupted: {
    fileSize: status.Corrupted.file_size,
    expectedMinSize: status.Corrupted.expected_min_size,
  } };
}

function mapTranscript(update: TranscriptUpdate): TranscriptEvent {
  return {
    text: update.text, timestamp: update.timestamp, source: update.source,
    sequenceId: update.sequence_id, chunkStartTime: update.chunk_start_time,
    isPartial: update.is_partial, confidence: update.confidence,
    audioStartTime: update.audio_start_time, audioEndTime: update.audio_end_time,
    duration: update.duration,
  };
}

export class TauriTranscriptionClient implements TranscriptionClient {
  readonly capabilities: TranscriptionCapabilities = {
    execution: 'local', modelManagement: true, modelDownload: true,
    retranscription: true, importedAudio: true, translationToEnglish: true,
  };

  async getSelectedProvider(): Promise<TranscriptionProvider> {
    const config = await invoke<NativeTranscriptConfig | null>('api_get_transcript_config');
    return normalizeProvider(config?.provider);
  }

  async ensureReady(): Promise<boolean> {
    const provider = await this.getSelectedProvider();
    await this.initializeProvider(provider);
    return this.hasAvailableModels(provider);
  }

  initializeProvider(provider: TranscriptionProvider): Promise<void> {
    return invoke(PROVIDER_COMMANDS[provider].initialize);
  }

  hasAvailableModels(provider: TranscriptionProvider): Promise<boolean> {
    return invoke(PROVIDER_COMMANDS[provider].hasAvailableModels);
  }

  async listModels(provider: TranscriptionProvider): Promise<TranscriptionModel[]> {
    const models = await invoke<NativeModel[]>(PROVIDER_COMMANDS[provider].getAvailableModels);
    return models.map(model => ({
      name: model.name, sizeMb: model.size_mb, status: normalizeModelStatus(model.status),
    }));
  }

  getHistory(): Promise<Transcript[]> { return invoke('get_transcript_history'); }

  async getStatus(): Promise<TranscriptionStatus> {
    const status = await invoke<NativeTranscriptionStatus>('get_transcription_status');
    return {
      chunksInQueue: status.chunks_in_queue,
      isProcessing: status.is_processing,
      lastActivityMs: status.last_activity_ms,
    };
  }

  startRetranscription(options: RetranscriptionOptions): Promise<void> {
    const { source, ...nativeOptions } = options;
    return invoke('start_retranscription_command', {
      ...nativeOptions,
      meetingFolderPath: getTauriMediaReference(source),
    });
  }
  cancelRetranscription(): Promise<void> { return invoke('cancel_retranscription_command'); }
  startImport(options: ImportTranscriptionOptions): Promise<void> {
    const { source, ...nativeOptions } = options;
    return invoke('start_import_audio_command', {
      ...nativeOptions,
      sourcePath: getTauriMediaReference(source),
    });
  }
  cancelImport(): Promise<void> { return invoke('cancel_import_command'); }

  onUpdate(
    callback: (update: TranscriptEvent) => void,
    onRegistrationError?: (error: unknown) => void,
  ): () => void {
    return subscribeToTauriEvent<TranscriptUpdate, TranscriptEvent>(
      'transcript-update', callback, mapTranscript, onRegistrationError,
    );
  }
  onComplete(callback: () => void): () => void {
    return subscribeToTauriEvent('transcription-complete', callback);
  }
  onError(callback: (error: TranscriptionError) => void): () => void {
    return subscribeToTauriEvent('transcription-error', callback);
  }
  onLegacyError(callback: (error: string) => void): () => void {
    return subscribeToTauriEvent('transcript-error', callback);
  }

  onModelDownloadProgress(
    callback: (event: ModelDownloadProgress) => void,
    onRegistrationError?: (error: unknown) => void,
  ): () => void {
    return combineSubscriptions(
      subscribeToTauriEvent<NativeModelProgress, ModelDownloadProgress>(
        'model-download-progress', callback,
        event => ({ provider: 'localWhisper', modelName: event.modelName, progress: event.progress }),
        onRegistrationError,
      ),
      subscribeToTauriEvent<NativeModelProgress, ModelDownloadProgress>(
        'parakeet-model-download-progress', callback, event => ({
          provider: 'parakeet', modelName: event.modelName, progress: event.progress,
          status: event.status, downloadedBytes: event.downloaded_bytes,
          totalBytes: event.total_bytes, downloadedMb: event.downloaded_mb,
          totalMb: event.total_mb, speedMbps: event.speed_mbps,
        }), onRegistrationError,
      ),
    );
  }

  onModelDownloadComplete(
    callback: (event: ModelDownloadComplete) => void,
    onRegistrationError?: (error: unknown) => void,
  ): () => void {
    return combineSubscriptions(
      subscribeToTauriEvent<{ modelName: string }, ModelDownloadComplete>(
        'model-download-complete', callback,
        event => ({ provider: 'localWhisper', modelName: event.modelName }),
        onRegistrationError,
      ),
      subscribeToTauriEvent<{ modelName: string }, ModelDownloadComplete>(
        'parakeet-model-download-complete', callback,
        event => ({ provider: 'parakeet', modelName: event.modelName }),
        onRegistrationError,
      ),
    );
  }

  onModelDownloadError(
    callback: (event: ModelDownloadError) => void,
    onRegistrationError?: (error: unknown) => void,
  ): () => void {
    return combineSubscriptions(
      subscribeToTauriEvent<{ modelName: string; error: string }, ModelDownloadError>(
        'model-download-error', callback,
        event => ({ provider: 'localWhisper', modelName: event.modelName, error: event.error }),
        onRegistrationError,
      ),
      subscribeToTauriEvent<{ modelName: string; error: string }, ModelDownloadError>(
        'parakeet-model-download-error', callback,
        event => ({ provider: 'parakeet', modelName: event.modelName, error: event.error }),
        onRegistrationError,
      ),
    );
  }

  onRetranscriptionProgress(callback: (event: RetranscriptionProgress) => void): () => void {
    return subscribeToTauriEvent<NativeRetranscriptionProgress, RetranscriptionProgress>('retranscription-progress', callback, event => ({
      meetingId: event.meeting_id, stage: event.stage,
      progressPercentage: event.progress_percentage, message: event.message,
    }));
  }
  onRetranscriptionComplete(callback: (event: RetranscriptionResult) => void): () => void {
    return subscribeToTauriEvent<NativeRetranscriptionResult, RetranscriptionResult>('retranscription-complete', callback, event => ({
      meetingId: event.meeting_id, segmentsCount: event.segments_count,
      durationSeconds: event.duration_seconds, language: event.language,
    }));
  }
  onRetranscriptionError(callback: (event: RetranscriptionError) => void): () => void {
    return subscribeToTauriEvent<NativeRetranscriptionError, RetranscriptionError>('retranscription-error', callback, event => ({
      meetingId: event.meeting_id, error: event.error,
    }));
  }
  onImportProgress(callback: (event: ImportTranscriptionProgress) => void): () => void {
    return subscribeToTauriEvent<NativeImportProgress, ImportTranscriptionProgress>('import-progress', callback, event => ({
      stage: event.stage, progressPercentage: event.progress_percentage, message: event.message,
    }));
  }
  onImportComplete(callback: (event: ImportTranscriptionResult) => void): () => void {
    return subscribeToTauriEvent<NativeImportResult, ImportTranscriptionResult>('import-complete', callback, event => ({
      meetingId: event.meeting_id, title: event.title,
      segmentsCount: event.segments_count, durationSeconds: event.duration_seconds,
    }));
  }
  onImportError(callback: (event: ImportTranscriptionError) => void): () => void {
    return subscribeToTauriEvent('import-error', callback);
  }
}
