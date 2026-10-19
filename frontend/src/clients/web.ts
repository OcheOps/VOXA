import type { VoxaClient } from './contracts';
import { HttpConfigurationClient, HttpCredentialClient, HttpWorkspaceSettingsClient } from './http/configuration-client';
import { HttpMeetingClient } from './http/meeting-client';
import type { HttpTransport } from './http/transport';
import {
  UnavailableMediaClient,
  UnavailableRecordingClient,
  UnavailableSummaryClient,
  UnavailableTranscriptionClient,
} from './unavailable';

/**
 * Web composition: server-backed meetings, configuration and credentials;
 * recording, media, transcription and summaries are explicitly unavailable
 * until browser capture and server jobs exist. Nothing here imports Tauri.
 */
export function createWebVoxaClient(transport: HttpTransport): VoxaClient {
  return {
    meetings: new HttpMeetingClient(transport),
    recording: new UnavailableRecordingClient(),
    transcription: new UnavailableTranscriptionClient(),
    summaries: new UnavailableSummaryClient(),
    media: new UnavailableMediaClient(),
    configuration: new HttpConfigurationClient(transport),
    credentials: new HttpCredentialClient(transport),
    workspaceSettings: new HttpWorkspaceSettingsClient(transport),
  };
}
