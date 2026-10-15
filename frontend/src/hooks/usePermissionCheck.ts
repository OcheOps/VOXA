import { useState, useEffect } from 'react';
import { useVoxaClient } from '@/clients';

export interface PermissionStatus {
  hasMicrophone: boolean;
  hasSystemAudio: boolean;
  isChecking: boolean;
  error: string | null;
}

export function usePermissionCheck() {
  const { recording: recordingClient } = useVoxaClient();
  const [status, setStatus] = useState<PermissionStatus>({
    hasMicrophone: false,
    hasSystemAudio: false,
    isChecking: true,
    error: null,
  });

  const checkPermissions = async () => {
    setStatus(prev => ({ ...prev, isChecking: true, error: null }));

    try {
      // Get audio devices to check for microphone and system audio availability
      const devices = await recordingClient.listDevices();

      // Check for microphone devices (Input)
      const inputDevices = devices.filter(d => d.type === 'input');
      const hasMicrophone = inputDevices.length > 0;

      // Check for system audio devices (Output)
      // On macOS, we need ScreenCaptureKit devices for system audio
      const outputDevices = devices.filter(d => d.type === 'output');
      const hasSystemAudio = outputDevices.length > 0;

      console.log('Permission check:', {
        hasMicrophone,
        hasSystemAudio,
        inputDevices: inputDevices.length,
        outputDevices: outputDevices.length
      });

      setStatus({
        hasMicrophone,
        hasSystemAudio,
        isChecking: false,
        error: null,
      });

      return { hasMicrophone, hasSystemAudio };
    } catch (error) {
      console.error('Failed to check audio permissions:', error);
      setStatus({
        hasMicrophone: false,
        hasSystemAudio: false,
        isChecking: false,
        error: error instanceof Error ? error.message : 'Failed to check permissions',
      });
      return { hasMicrophone: false, hasSystemAudio: false };
    }
  };

  const requestPermissions = async () => {
    try {
      // Trigger audio permission by trying to access devices
      await recordingClient.listDevices();

      // Recheck after triggering
      setTimeout(() => {
        checkPermissions();
      }, 1000);
    } catch (error) {
      console.error('Failed to request permissions:', error);
    }
  };

  // Check permissions on mount
  useEffect(() => {
    checkPermissions();
  }, []);

  return {
    ...status,
    checkPermissions,
    requestPermissions,
  };
}
