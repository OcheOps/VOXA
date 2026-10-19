import type { JsonValue, Meeting, WorkspaceRole } from './domain.js';
import type { CredentialStatus, CredentialWriteRequest } from './credentials.js';
import type { MeetingId, UserId, WorkspaceId } from './ids.js';

export const API_V1 = {
  health: '/api/v1/health', capabilities: '/api/v1/capabilities', me: '/api/v1/me',
  meetings: '/api/v1/meetings', meeting: '/api/v1/meetings/:meetingId',
  configuration: '/api/v1/configuration', credentials: '/api/v1/credentials',
  credential: '/api/v1/credentials/:provider',
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
}
