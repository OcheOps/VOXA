'use client';

import { useVoxaClient } from '@/clients';
import { useServerCapabilities } from './context';

/**
 * Explains which meeting features are unavailable. A feature needs both a
 * browser client implementation and server support; neither is assumed.
 */
export function CapabilityNotice() {
  const client = useVoxaClient();
  const server = useServerCapabilities();
  const missing: string[] = [];
  // Recording needs both a browser that can capture and a server that stores media.
  const mediaStorage = Boolean(server?.mediaStorage.available && client.media.capabilities.urlPlayback);
  if (!client.recording.capabilities.microphoneCapture || !mediaStorage) missing.push('recording');
  if (!mediaStorage) missing.push('audio storage and playback');
  if (client.recording.capabilities.microphoneCapture && !client.recording.capabilities.tabCapture) missing.push('meeting tab audio capture (this browser cannot share tab audio)');
  if (!server?.transcription.available || client.transcription.capabilities.execution === 'unavailable') missing.push('transcription');
  if (!server?.summary.available || client.summaries.capabilities.execution === 'unavailable') missing.push('summaries');
  if (missing.length === 0) return null;
  return (
    <p className="rounded-md border border-gray-200 bg-gray-50 px-3 py-2 text-sm text-gray-600" role="note">
      Not available in Voxa Web yet: {missing.join(', ')}. Use the Voxa desktop app for these.
    </p>
  );
}
