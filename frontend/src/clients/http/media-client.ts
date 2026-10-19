import type {
  CreateRecordingResponse,
  FinalizeMediaRequest,
  MediaAsset,
  MediaAssetResponse,
  MediaPlaybackResponse,
  MediaUploadResponse,
  MeetingMediaResponse,
} from '@voxa/protocol';
import type {
  MediaCapabilities,
  MediaClient,
  MediaImportSelection,
  MediaPlaybackSource,
  MediaSource,
  PlaybackUrl,
  RecordingUploadTarget,
  ServerMediaAsset,
  ServerMediaClient,
  UploadProgress,
} from '../contracts';
import { UnsupportedOperationError } from '../errors';
import type { HttpTransport } from './transport';

function toAsset(asset: MediaAsset): ServerMediaAsset {
  return {
    id: asset.id, meetingId: asset.meetingId, mimeType: asset.mimeType, status: asset.status,
    durationSeconds: asset.durationSeconds, sizeBytes: asset.sizeBytes, createdAt: asset.createdAt, updatedAt: asset.updatedAt,
  };
}

const mediaPath = (id: string) => `/api/v1/media/${encodeURIComponent(id)}`;

/** Opaque MediaSource for server media: only this client can interpret it. */
interface ServerMediaSource { readonly kind: 'voxa-server-media'; readonly mediaAssetId: string }

function asServerSource(source: MediaSource): ServerMediaSource {
  const candidate = source as unknown as ServerMediaSource;
  if (candidate?.kind !== 'voxa-server-media') throw new UnsupportedOperationError('Playing media from another runtime');
  return candidate;
}

/**
 * Server media over protocol v1: recording uploads, meeting media lookup and
 * playback through short-lived signed URLs (range-capable streaming; nothing
 * is decoded into memory). Native file operations are unavailable.
 */
export class HttpMediaClient implements MediaClient, ServerMediaClient {
  readonly capabilities: MediaCapabilities = {
    nativeFilePicker: false,
    filesystemReveal: false,
    localByteReads: false,
    urlPlayback: true,
    rangedStreaming: true,
    import: false,
    exportDownload: false,
  };

  constructor(private readonly transport: HttpTransport, private readonly apiBaseUrl: string) {}

  async createRecording(meetingId: string, mimeType: string): Promise<RecordingUploadTarget> {
    const response = await this.transport.post<CreateRecordingResponse>(`/api/v1/meetings/${encodeURIComponent(meetingId)}/recordings`, { mimeType });
    return {
      asset: toAsset(response.mediaAsset),
      recordingSessionId: response.recordingSession.id,
      maxChunkBytes: response.upload.maxChunkBytes,
      maxTotalBytes: response.upload.maxTotalBytes,
    };
  }

  async uploadChunk(mediaAssetId: string, index: number, data: Blob, signal?: AbortSignal): Promise<UploadProgress> {
    const response = await this.transport.request<MediaUploadResponse>('PUT', `${mediaPath(mediaAssetId)}/chunks/${index}`, {
      binary: { data, contentType: 'application/octet-stream' },
      signal,
    });
    return { status: response.upload.status, receivedChunks: response.upload.receivedChunks, receivedBytes: response.upload.receivedBytes };
  }

  async getUploadStatus(mediaAssetId: string): Promise<UploadProgress> {
    const response = await this.transport.get<MediaUploadResponse>(`${mediaPath(mediaAssetId)}/upload`);
    return { status: response.upload.status, receivedChunks: response.upload.receivedChunks, receivedBytes: response.upload.receivedBytes };
  }

  async finalize(mediaAssetId: string, chunkCount: number, durationSeconds: number | null): Promise<ServerMediaAsset> {
    const body: FinalizeMediaRequest = { chunkCount, durationSeconds };
    return toAsset((await this.transport.post<MediaAssetResponse>(`${mediaPath(mediaAssetId)}/finalize`, body)).mediaAsset);
  }

  async listMeetingMedia(meetingId: string): Promise<ServerMediaAsset[]> {
    const response = await this.transport.get<MeetingMediaResponse>(`/api/v1/meetings/${encodeURIComponent(meetingId)}/media`);
    return response.items.map(toAsset);
  }

  async getPlaybackUrl(mediaAssetId: string): Promise<PlaybackUrl> {
    const response = await this.transport.get<MediaPlaybackResponse>(`${mediaPath(mediaAssetId)}/playback`);
    return { url: new URL(response.url, `${this.apiBaseUrl}/`).toString(), expiresAt: response.expiresAt };
  }

  async deleteMedia(mediaAssetId: string): Promise<void> {
    await this.transport.delete(mediaPath(mediaAssetId));
  }

  // MediaClient contract.
  async getMeetingSource(meetingId: string): Promise<MediaSource | null> {
    const ready = (await this.listMeetingMedia(meetingId)).filter((asset) => asset.status === 'ready');
    const latest = ready.at(-1);
    if (!latest) return null;
    const source: ServerMediaSource = { kind: 'voxa-server-media', mediaAssetId: latest.id };
    return source as unknown as MediaSource;
  }

  async getPlaybackSource(source: MediaSource): Promise<MediaPlaybackSource> {
    return { kind: 'url', url: (await this.getPlaybackUrl(asServerSource(source).mediaAssetId)).url };
  }

  async selectImportSource(): Promise<MediaImportSelection | null> {
    throw new UnsupportedOperationError('Media import');
  }

  async validateDroppedImport(_reference: string): Promise<MediaImportSelection> {
    throw new UnsupportedOperationError('Media import');
  }

  async openMeetingFolder(_meetingId: string): Promise<void> {
    throw new UnsupportedOperationError('Revealing files');
  }

  async openRecordingsFolder(): Promise<void> {
    throw new UnsupportedOperationError('Revealing files');
  }
}
