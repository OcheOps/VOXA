'use client';

import { createContext, useContext } from 'react';
import type { VoxaClient } from './contracts';
import { TauriMediaClient } from './tauri/media-client';
import { TauriMeetingClient } from './tauri/meeting-client';
import { TauriRecordingClient } from './tauri/recording-client';
import { TauriSummaryClient } from './tauri/summary-client';
import { TauriTranscriptionClient } from './tauri/transcription-client';
import { TauriConfigurationClient } from './tauri/configuration-client';

export type * from './contracts';

export const voxaClient: VoxaClient = {
  meetings: new TauriMeetingClient(),
  recording: new TauriRecordingClient(),
  transcription: new TauriTranscriptionClient(),
  summaries: new TauriSummaryClient(),
  media: new TauriMediaClient(),
  configuration: new TauriConfigurationClient(),
};

const VoxaClientContext = createContext<VoxaClient>(voxaClient);

export const VoxaClientProvider = VoxaClientContext.Provider;

export function useVoxaClient(): VoxaClient {
  return useContext(VoxaClientContext);
}
