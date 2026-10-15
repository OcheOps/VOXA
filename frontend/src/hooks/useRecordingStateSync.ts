import { useState, useEffect } from 'react';
import { useVoxaClient } from '@/clients';

interface UseRecordingStateSyncReturn {
  isBackendRecording: boolean;
  isRecordingDisabled: boolean;
  setIsRecordingDisabled: (value: boolean) => void;
}

/**
 * Custom hook for synchronizing frontend recording state with backend.
 * Polls backend every 1 second to detect recording state changes.
 *
 * Features:
 * - Backend state synchronization (1-second polling)
 * - Recording disabled flag management (prevents re-recording during processing)
 */
export function useRecordingStateSync(
  isRecording: boolean,
  setIsRecording: (value: boolean) => void,
  setIsMeetingActive: (value: boolean) => void
): UseRecordingStateSyncReturn {
  const { recording: recordingClient } = useVoxaClient();
  const [isRecordingDisabled, setIsRecordingDisabled] = useState(false);

  useEffect(() => {
    console.log('Setting up recording state check effect, current isRecording:', isRecording);

    const checkRecordingState = async () => {
      try {
        console.log('checkRecordingState called');
        console.log('About to call is_recording command');
        const isCurrentlyRecording = await recordingClient.isRecording();
        console.log('checkRecordingState: backend recording =', isCurrentlyRecording, 'UI recording =', isRecording);

        if (isCurrentlyRecording && !isRecording) {
          console.log('Recording is active in backend but not in UI, synchronizing state...');
          setIsRecording(true);
          setIsMeetingActive(true);
        } else if (!isCurrentlyRecording && isRecording) {
          console.log('Recording is inactive in backend but active in UI, synchronizing state...');
          setIsRecording(false);
        }
      } catch (error) {
        console.error('Failed to check recording state:', error);
      }
    };

    checkRecordingState();

    // The selected transport decides how recording state is retrieved.
    const interval = setInterval(checkRecordingState, 1000);

    return () => {
      console.log('Cleaning up recording state check interval');
      clearInterval(interval);
    };
  }, [isRecording, setIsRecording, setIsMeetingActive, recordingClient]);

  return {
    isBackendRecording: isRecording,
    isRecordingDisabled,
    setIsRecordingDisabled,
  };
}
