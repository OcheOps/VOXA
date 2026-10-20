import type { CaptureClient, CaptureMode, IsoDateTime, JsonValue, MediaAsset, Meeting, ProcessingJob, RecordingSession, SharedSurface, Summary, Transcript, WorkspaceRole } from './domain.js';
import type { CredentialStatus, CredentialWriteRequest } from './credentials.js';
import type { MediaAssetId, MeetingId, ProcessingJobId, TranscriptId, UserId, WorkspaceId } from './ids.js';

export const API_V1 = {
  health: '/api/v1/health', capabilities: '/api/v1/capabilities', me: '/api/v1/me',
  meetings: '/api/v1/meetings', meeting: '/api/v1/meetings/:meetingId',
  configuration: '/api/v1/configuration', credentials: '/api/v1/credentials',
  credential: '/api/v1/credentials/:provider',
  meetingRecordings: '/api/v1/meetings/:meetingId/recordings',
  meetingMedia: '/api/v1/meetings/:meetingId/media',
  mediaAsset: '/api/v1/media/:mediaAssetId',
  mediaUpload: '/api/v1/media/:mediaAssetId/upload',
  mediaChunk: '/api/v1/media/:mediaAssetId/chunks/:index',
  mediaFinalize: '/api/v1/media/:mediaAssetId/finalize',
  mediaPlayback: '/api/v1/media/:mediaAssetId/playback',
  mediaContent: '/api/v1/media/:mediaAssetId/content',
  meetingJobs: '/api/v1/meetings/:meetingId/jobs',
  meetingTranscript: '/api/v1/meetings/:meetingId/transcript',
  mediaTranscript: '/api/v1/media/:mediaAssetId/transcript',
  mediaTranscription: '/api/v1/media/:mediaAssetId/transcription',
  jobCancel: '/api/v1/jobs/:jobId/cancel',
  meetingSummary: '/api/v1/meetings/:meetingId/summary',
  meetingSummaryCancel: '/api/v1/meetings/:meetingId/summary/cancel',
  summaryTemplates: '/api/v1/summary-templates',
} as const;

export interface HealthResponse { status: 'ok'; apiVersion: 'v1'; serverVersion: string; }
export interface CapabilitiesResponse {
  apiVersion: 'v1';
  mediaStorage: { available: boolean };
  transcription: { available: boolean; providers: string[] };
  summary: { available: boolean; providers: string[] };
  configuration: { serverManaged: boolean; credentialManagement: boolean };
}
/**
 * The authenticated caller and the workspace the request is scoped to.
 * Authentication itself (bearer tokens) is HTTP transport, not protocol data:
 * no token, issuer, subject or claim appears here.
 */
export interface CurrentUser { id: UserId; email: string | null; displayName: string | null; }
export interface CurrentWorkspace { id: WorkspaceId; name: string; }
export interface MeResponse { user: CurrentUser; workspace: CurrentWorkspace; role: WorkspaceRole; }
/** `details.reason` of a 409 `conflict` when a caller belongs to several workspaces and selection is not yet supported. */
export const WORKSPACE_SELECTION_REQUIRED = 'workspace_selection_required' as const;

/**
 * Chunked, resumable recording upload. A recording is one continuous
 * encoder stream split into ordered chunks: chunk 0 carries the container
 * header and chunks are assembled strictly in index order. PUT
 * `application/octet-stream` bytes to `mediaChunk`; identical retries are
 * no-ops, different bytes for a stored index are a 409 `chunk_mismatch`.
 * Finalize requires exactly chunks 0..chunkCount-1 (409 `missing_chunks` /
 * `unexpected_chunks`) and is idempotent.
 */
/** `capture` is optional: omitted (older clients) is stored as unknown. */
export interface CreateRecordingRequest { mimeType: string; capture?: { mode: CaptureMode; client: CaptureClient; sharedSurface?: SharedSurface | null }; }
export interface MediaUpload {
  mediaAssetId: MediaAssetId;
  status: MediaAsset['status'];
  receivedChunks: number[];
  receivedBytes: number;
  maxChunkBytes: number;
  maxTotalBytes: number;
}
export interface CreateRecordingResponse { recordingSession: RecordingSession; mediaAsset: MediaAsset; upload: MediaUpload; }
export interface MediaUploadResponse { upload: MediaUpload; }
export interface FinalizeMediaRequest { chunkCount: number; durationSeconds?: number | null; }
export interface MediaAssetResponse { mediaAsset: MediaAsset; }
export interface MeetingMediaResponse { items: MediaAsset[]; }
/**
 * Short-lived signed playback URL (relative to the API origin) for media
 * elements, which cannot send Authorization headers. It supports HTTP range
 * requests; request a new one when it expires.
 */
export interface MediaPlaybackResponse { url: string; expiresAt: IsoDateTime; }

/**
 * User-facing processing state of a recording (no queue internals):
 * preparing = making the recording seekable; transcribing; ready = transcript
 * available; failed/cancelled (see `error`); unavailable = this server cannot
 * transcribe; none = nothing to do (e.g. upload not finished).
 */
export type ProcessingState = 'none' | 'preparing' | 'transcribing' | 'ready' | 'failed' | 'cancelled' | 'unavailable';
export interface ProcessingError { code: string; message: string; }
/** `transcript` is null until one exists; it is never faked as empty text. */
export interface MediaTranscriptResponse { mediaAssetId: MediaAssetId | null; state: ProcessingState; error: ProcessingError | null; transcript: Transcript | null; }
export interface ProcessingJobListResponse { items: ProcessingJob[]; }
export interface ProcessingJobResponse { job: ProcessingJob; }

/**
 * Summary state of a meeting. queued/generating: a job is active (any
 * previous summary is still returned); failed/cancelled: the latest job did
 * not finish (any previous summary is kept); unavailable: the server or the
 * workspace cannot generate summaries (`reason` says why).
 */
export type SummaryState = 'none' | 'queued' | 'generating' | 'ready' | 'failed' | 'cancelled' | 'unavailable';
export type SummaryStateReason = 'disabled' | 'not_configured' | 'provider_not_enabled' | 'model_missing' | 'credential_missing' | 'no_transcript';
export interface MeetingSummaryResponse {
  meetingId: MeetingId;
  state: SummaryState;
  reason: SummaryStateReason | null;
  /** True when the summary was produced from a transcript that has since been replaced. */
  stale: boolean;
  currentTranscriptId: TranscriptId | null;
  error: ProcessingError | null;
  job: ProcessingJob | null;
  summary: Summary | null;
}
/** Optional overrides; provider, model and endpoint always come from server configuration. */
export interface GenerateSummaryRequest { templateId?: string; customPrompt?: string; }
export interface SummaryTemplateInfo { id: string; name: string; description: string; }
export interface SummaryTemplateListResponse { items: SummaryTemplateInfo[]; }

export interface CursorPage { nextCursor: string | null; hasMore: boolean; }
export interface MeetingListQuery { cursor?: string; limit?: number; }
export interface MeetingListResponse { items: Meeting[]; page: CursorPage; }
export interface CreateMeetingRequest { title: string; }
export interface CreateMeetingResponse { meeting: Meeting; }
export interface GetMeetingResponse { meeting: Meeting; }
export interface UpdateMeetingRequest { title?: string; }
export interface UpdateMeetingResponse { meeting: Meeting; }

export interface TranscriptionDefaults { provider: string; model: string; language: string; translateToEnglish: boolean; }
export interface SummaryDefaults { provider: string; model: string; outputLanguage: string; templateId: string | null; autoGenerate: boolean; }
export interface ServerRecordingDefaults { retainMedia: boolean; }
export interface ServerPrivacyPreferences { analyticsEnabled: boolean; }
export interface ServerNotificationPreferences { processingComplete: boolean; }
export interface ConfigurationResponse {
  workspaceId: WorkspaceId;
  transcription: TranscriptionDefaults;
  summary: SummaryDefaults;
  recording: ServerRecordingDefaults;
  privacy: ServerPrivacyPreferences;
  notifications: ServerNotificationPreferences;
  credentials: CredentialStatus[];
}
export interface UpdateConfigurationRequest {
  transcription?: Partial<TranscriptionDefaults>;
  summary?: Partial<SummaryDefaults>;
  recording?: Partial<ServerRecordingDefaults>;
  privacy?: Partial<ServerPrivacyPreferences>;
  notifications?: Partial<ServerNotificationPreferences>;
}
export interface UpdateConfigurationResponse { configuration: ConfigurationResponse; }
export interface CredentialListResponse { credentials: CredentialStatus[]; }
export interface CredentialUpdateResponse { credential: CredentialStatus; }

export type ApiErrorCode = 'validation' | 'not_found' | 'unauthorized' | 'forbidden' | 'conflict' | 'unavailable' | 'internal';
export interface ApiErrorBody { code: ApiErrorCode; message: string; details?: { [key: string]: JsonValue }; requestId?: string; }
export interface ApiErrorResponse { error: ApiErrorBody; }
const SAFE_ERROR_MESSAGES: Record<ApiErrorCode, string> = {
  validation: 'Request validation failed', not_found: 'Resource not found',
  unauthorized: 'Authentication required', forbidden: 'Access forbidden',
  conflict: 'Resource conflict', unavailable: 'Service unavailable',
  internal: 'Internal server error',
};
export function apiError(code: ApiErrorCode, requestId?: string): ApiErrorResponse {
  return { error: { code, message: SAFE_ERROR_MESSAGES[code], ...(requestId ? { requestId } : {}) } };
}

interface Endpoint<Method extends string, Path extends string, Request, Response> { method: Method; path: Path; request: Request; response: Response; }
export interface ApiV1Contract {
  health: Endpoint<'GET', typeof API_V1.health, never, HealthResponse>;
  capabilities: Endpoint<'GET', typeof API_V1.capabilities, never, CapabilitiesResponse>;
  me: Endpoint<'GET', typeof API_V1.me, never, MeResponse>;
  listMeetings: Endpoint<'GET', typeof API_V1.meetings, MeetingListQuery, MeetingListResponse>;
  createMeeting: Endpoint<'POST', typeof API_V1.meetings, CreateMeetingRequest, CreateMeetingResponse>;
  getMeeting: Endpoint<'GET', typeof API_V1.meeting, { meetingId: MeetingId }, GetMeetingResponse>;
  updateMeeting: Endpoint<'PATCH', typeof API_V1.meeting, { meetingId: MeetingId; body: UpdateMeetingRequest }, UpdateMeetingResponse>;
  deleteMeeting: Endpoint<'DELETE', typeof API_V1.meeting, { meetingId: MeetingId }, void>;
  getConfiguration: Endpoint<'GET', typeof API_V1.configuration, never, ConfigurationResponse>;
  updateConfiguration: Endpoint<'PATCH', typeof API_V1.configuration, UpdateConfigurationRequest, UpdateConfigurationResponse>;
  listCredentials: Endpoint<'GET', typeof API_V1.credentials, never, CredentialListResponse>;
  updateCredential: Endpoint<'PUT', typeof API_V1.credential, { provider: string; body: CredentialWriteRequest }, CredentialUpdateResponse>;
  deleteCredential: Endpoint<'DELETE', typeof API_V1.credential, { provider: string }, void>;
  createRecording: Endpoint<'POST', typeof API_V1.meetingRecordings, { meetingId: MeetingId; body: CreateRecordingRequest }, CreateRecordingResponse>;
  listMeetingMedia: Endpoint<'GET', typeof API_V1.meetingMedia, { meetingId: MeetingId }, MeetingMediaResponse>;
  getMediaAsset: Endpoint<'GET', typeof API_V1.mediaAsset, { mediaAssetId: MediaAssetId }, MediaAssetResponse>;
  deleteMediaAsset: Endpoint<'DELETE', typeof API_V1.mediaAsset, { mediaAssetId: MediaAssetId }, void>;
  getMediaUpload: Endpoint<'GET', typeof API_V1.mediaUpload, { mediaAssetId: MediaAssetId }, MediaUploadResponse>;
  uploadMediaChunk: Endpoint<'PUT', typeof API_V1.mediaChunk, { mediaAssetId: MediaAssetId; index: number; body: Blob | ArrayBuffer }, MediaUploadResponse>;
  finalizeMedia: Endpoint<'POST', typeof API_V1.mediaFinalize, { mediaAssetId: MediaAssetId; body: FinalizeMediaRequest }, MediaAssetResponse>;
  getMediaPlayback: Endpoint<'GET', typeof API_V1.mediaPlayback, { mediaAssetId: MediaAssetId }, MediaPlaybackResponse>;
  getMediaContent: Endpoint<'GET', typeof API_V1.mediaContent, { mediaAssetId: MediaAssetId; range?: string }, Blob>;
  listMeetingJobs: Endpoint<'GET', typeof API_V1.meetingJobs, { meetingId: MeetingId }, ProcessingJobListResponse>;
  getMeetingTranscript: Endpoint<'GET', typeof API_V1.meetingTranscript, { meetingId: MeetingId }, MediaTranscriptResponse>;
  getMediaTranscript: Endpoint<'GET', typeof API_V1.mediaTranscript, { mediaAssetId: MediaAssetId }, MediaTranscriptResponse>;
  requestTranscription: Endpoint<'POST', typeof API_V1.mediaTranscription, { mediaAssetId: MediaAssetId }, ProcessingJobResponse>;
  cancelJob: Endpoint<'POST', typeof API_V1.jobCancel, { jobId: ProcessingJobId }, ProcessingJobResponse>;
  getMeetingSummary: Endpoint<'GET', typeof API_V1.meetingSummary, { meetingId: MeetingId }, MeetingSummaryResponse>;
  generateSummary: Endpoint<'POST', typeof API_V1.meetingSummary, { meetingId: MeetingId; body?: GenerateSummaryRequest }, ProcessingJobResponse>;
  cancelSummary: Endpoint<'POST', typeof API_V1.meetingSummaryCancel, { meetingId: MeetingId }, ProcessingJobResponse>;
  listSummaryTemplates: Endpoint<'GET', typeof API_V1.summaryTemplates, Record<string, never>, SummaryTemplateListResponse>;
}
