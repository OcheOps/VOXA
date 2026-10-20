import type { VoxaClient } from './contracts';
import { HttpConfigurationClient, HttpCredentialClient, HttpWorkspaceSettingsClient } from './http/configuration-client';
import { BrowserRecordingClient } from './browser/recording-client';
import { HttpMediaClient } from './http/media-client';
import { HttpMeetingClient } from './http/meeting-client';
import { HttpSummaryClient } from './http/summary-client';
import { HttpTranscriptionClient } from './http/transcription-client';
import type { HttpTransport } from './http/transport';

/**
 * Web composition: server-backed meetings, configuration, credentials,
 * media, transcription and summaries; browser microphone recording.
 * Nothing here imports Tauri.
 */
export function createWebVoxaClient(transport: HttpTransport, apiBaseUrl: string): VoxaClient {
  const media = new HttpMediaClient(transport, apiBaseUrl);
  const transcription = new HttpTranscriptionClient(transport);
  const summaries = new HttpSummaryClient(transport);
  return {
    meetings: new HttpMeetingClient(transport),
    recording: new BrowserRecordingClient(),
    transcription,
    summaries,
    media,
    serverMedia: media,
    serverTranscripts: transcription,
    serverSummaries: summaries,
    configuration: new HttpConfigurationClient(transport),
    credentials: new HttpCredentialClient(transport),
    workspaceSettings: new HttpWorkspaceSettingsClient(transport),
  };
}
