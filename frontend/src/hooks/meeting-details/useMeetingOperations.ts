import { useCallback } from 'react';
import { toast } from 'sonner';
import { useVoxaClient } from '@/clients';

interface UseMeetingOperationsProps {
  meeting: any;
}

export function useMeetingOperations({
  meeting,
}: UseMeetingOperationsProps) {
  const { meetings: meetingClient } = useVoxaClient();

  // Open meeting folder in file explorer
  const handleOpenMeetingFolder = useCallback(async () => {
    try {
      await meetingClient.openRecordingFolder(meeting.id);
    } catch (error) {
      console.error('Failed to open meeting folder:', error);
      toast.error(error as string || 'Failed to open recording folder');
    }
  }, [meeting.id, meetingClient]);

  return {
    handleOpenMeetingFolder,
  };
}
