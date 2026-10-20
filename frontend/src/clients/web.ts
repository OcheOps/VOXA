import type { VoxaClient } from './contracts';
import { HttpConfigurationClient, HttpCredentialClient, HttpWorkspaceSettingsClient } from './http/configuration-client';
import { BrowserRecordingClient } from './browser/recording-client';
import { HttpMediaClient } from './http/media-client';
import { HttpMeetingClient } from './http/meeting-client';
import { HttpTranscriptionClient } from './http/transcription-client';
import type { HttpTransport } from './http/transport';
import { UnavailableSummaryClient } from './unavailable';

/**
 * Web composition: server-backed meetings, configuration, credentials,
 * media and transcription; browser microphone recording; summaries remain
 * explicitly unavailable until server summary jobs exist. Nothing here imports Tauri.
 */
export function createWebVoxaClient(transport: HttpTransport, apiBaseUrl: string): VoxaClient {
  const media = new HttpMediaClient(transport, apiBaseUrl);
  const transcription = new HttpTranscriptionClient(transport);
  return {
    meetings: new HttpMeetingClient(transport),
    recording: new BrowserRecordingClient(),
    transcription,
    summaries: new UnavailableSummaryClient(),
    media,
    serverMedia: media,
    serverTranscripts: transcription,
    configuration: new HttpConfigurationClient(transport),
    credentials: new HttpCredentialClient(transport),
    workspaceSettings: new HttpWorkspaceSettingsClient(transport),
  };
}
