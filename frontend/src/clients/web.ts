import type { VoxaClient } from './contracts';
import { HttpConfigurationClient, HttpCredentialClient, HttpWorkspaceSettingsClient } from './http/configuration-client';
import { BrowserRecordingClient } from './browser/recording-client';
import { HttpMediaClient } from './http/media-client';
import { HttpMeetingClient } from './http/meeting-client';
import type { HttpTransport } from './http/transport';
import { UnavailableSummaryClient, UnavailableTranscriptionClient } from './unavailable';

/**
 * Web composition: server-backed meetings, configuration, credentials and
 * media; browser microphone recording; transcription and summaries remain
 * explicitly unavailable until server jobs exist. Nothing here imports Tauri.
 */
export function createWebVoxaClient(transport: HttpTransport, apiBaseUrl: string): VoxaClient {
  const media = new HttpMediaClient(transport, apiBaseUrl);
  return {
    meetings: new HttpMeetingClient(transport),
    recording: new BrowserRecordingClient(),
    transcription: new UnavailableTranscriptionClient(),
    summaries: new UnavailableSummaryClient(),
    media,
    serverMedia: media,
    configuration: new HttpConfigurationClient(transport),
    credentials: new HttpCredentialClient(transport),
    workspaceSettings: new HttpWorkspaceSettingsClient(transport),
  };
}
