import type {
  CapabilitiesResponse,
  MediaTranscriptResponse,
  ProcessingJobListResponse,
} from '@voxa/protocol';
import type { Transcript } from '@/types';
import type {
  ImportTranscriptionOptions,
  MediaSource,
  RecordingTranscript,
  RetranscriptionOptions,
  ServerTranscriptClient,
  TranscriptionCapabilities,
  TranscriptionClient,
  TranscriptionModel,
  TranscriptionProvider,
  TranscriptionStatus,
} from '../contracts';
import { UnsupportedOperationError } from '../errors';
import type { HttpTransport } from './transport';

const noop = () => () => {};

function toRecordingTranscript(response: MediaTranscriptResponse): RecordingTranscript {
  const transcript = response.transcript;
  return {
    mediaAssetId: response.mediaAssetId,
    state: response.state,
    error: response.error,
    transcript: transcript && {
      id: transcript.id,
      language: transcript.language,
      asrEngine: transcript.asrEngine,
      asrModel: transcript.asrModel,
      segments: transcript.segments.map((segment) => ({
        id: segment.id, text: segment.text, startSeconds: segment.startTimeSeconds, endSeconds: segment.endTimeSeconds, sequence: segment.sequence,
      })),
    },
  };
}

function serverMediaId(source: MediaSource): string {
  const candidate = source as unknown as { kind?: string; mediaAssetId?: string };
  if (candidate?.kind !== 'voxa-server-media' || !candidate.mediaAssetId) throw new UnsupportedOperationError('Transcribing media from another runtime');
  return candidate.mediaAssetId;
}

/**
 * Transcription performed by the Voxa server (whisper.cpp) on stored
 * recordings. There is no browser model management, download or local
 * engine initialization; the server decides models and availability.
 */
export class HttpTranscriptionClient implements TranscriptionClient, ServerTranscriptClient {
  readonly capabilities: TranscriptionCapabilities = {
    execution: 'remote',
    modelManagement: false,
    modelDownload: false,
    retranscription: true,
    importedAudio: false,
    translationToEnglish: true,
  };

  constructor(private readonly transport: HttpTransport) {}

  // --- ServerTranscriptClient ---

  async getRecordingTranscript(mediaAssetId: string, signal?: AbortSignal): Promise<RecordingTranscript> {
    return toRecordingTranscript(await this.transport.get<MediaTranscriptResponse>(`/api/v1/media/${encodeURIComponent(mediaAssetId)}/transcript`, { signal }));
  }

  async getMeetingTranscript(meetingId: string, signal?: AbortSignal): Promise<RecordingTranscript> {
    return toRecordingTranscript(await this.transport.get<MediaTranscriptResponse>(`/api/v1/meetings/${encodeURIComponent(meetingId)}/transcript`, { signal }));
  }

  async requestTranscription(mediaAssetId: string): Promise<void> {
    await this.transport.post(`/api/v1/media/${encodeURIComponent(mediaAssetId)}/transcription`, {});
  }

  async cancelProcessing(meetingId: string, mediaAssetId: string): Promise<boolean> {
    const jobs = await this.transport.get<ProcessingJobListResponse>(`/api/v1/meetings/${encodeURIComponent(meetingId)}/jobs`);
    const active = jobs.items.filter((job) => job.mediaAssetId === mediaAssetId && (job.status === 'queued' || job.status === 'running'));
    for (const job of active) await this.transport.post(`/api/v1/jobs/${encodeURIComponent(job.id)}/cancel`, {});
    return active.length > 0;
  }

  // --- TranscriptionClient (shared contract) ---

  async getSelectedProvider(): Promise<TranscriptionProvider> {
    throw new UnsupportedOperationError('Selecting a local transcription engine');
  }

  /** Ready when the server advertises a usable transcription engine. */
  async ensureReady(): Promise<boolean> {
    const capabilities = await this.transport.get<CapabilitiesResponse>('/api/v1/capabilities');
    return capabilities.transcription.available;
  }

  async initializeProvider(_provider: TranscriptionProvider): Promise<void> {
    throw new UnsupportedOperationError('Initializing a local transcription engine');
  }

  async setLanguage(): Promise<void> {
    throw new UnsupportedOperationError('Setting the live transcription language (use workspace settings)');
  }

  async hasAvailableModels(): Promise<boolean> { return false; }
  async listModels(): Promise<TranscriptionModel[]> { return []; }
  async getHistory(): Promise<Transcript[]> { return []; }
  async getStatus(): Promise<TranscriptionStatus> { return { chunksInQueue: 0, isProcessing: false, lastActivityMs: 0 }; }

  async startRetranscription(options: RetranscriptionOptions): Promise<void> {
    await this.requestTranscription(serverMediaId(options.source));
  }

  async cancelRetranscription(): Promise<void> {
    throw new UnsupportedOperationError('Cancelling without a recording (use cancelProcessing)');
  }

  async startImport(_options: ImportTranscriptionOptions): Promise<void> {
    throw new UnsupportedOperationError('Audio import');
  }

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
