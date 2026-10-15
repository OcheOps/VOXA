'use client';

import React, { useEffect } from 'react';
import { useRecordingStop } from '@/hooks/useRecordingStop';
import { useVoxaClient } from '@/clients';

/**
 * RecordingPostProcessingProvider
 *
 * This provider handles post-processing when recording stops from any source:
 * - Tray menu stop
 * - Global keyboard shortcut
 * - Overlay stop button
 * - Main UI stop button
 *
 * It listens for the 'recording-stop-complete' event from Rust backend
 * and triggers the full post-processing flow (save to database, navigate, analytics)
 * regardless of which page the user is currently on.
 */
export function RecordingPostProcessingProvider({ children }: { children: React.ReactNode }) {
  const { recording: recordingClient } = useVoxaClient();
  // No-op functions since the global RecordingStateContext already handles state updates
  // These are only needed for the hook's local component state management
  const setIsRecording = () => { };
  const setIsRecordingDisabled = () => { };

  const {
    handleRecordingStop,
  } = useRecordingStop(setIsRecording, setIsRecordingDisabled);

  useEffect(() => {
    const unlistenFn = recordingClient.onStopComplete(shouldPersist => {
          console.log('[RecordingPostProcessing] Received recording-stop-complete event:', shouldPersist);

          // Call the post-processing handler
          // event.payload is the callApi boolean (true for normal stops)
          handleRecordingStop(shouldPersist);
    });
    console.log('[RecordingPostProcessing] Event listener set up successfully');

    return () => {
      console.log('[RecordingPostProcessing] Cleaning up event listener');
      unlistenFn();
    };
  }, [handleRecordingStop, recordingClient]);

  return <>{children}</>;
}
