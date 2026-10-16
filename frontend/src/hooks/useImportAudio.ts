import { useState, useEffect, useCallback, useRef } from 'react';
import Analytics from '@/lib/analytics';
import { applyPinnedSummaryLanguageToMeeting } from '@/lib/summary-language-preferences';
import { toast } from 'sonner';
import { type MediaImportSelection, useVoxaClient } from '@/clients';

export type AudioFileInfo = MediaImportSelection;

export interface ImportProgress {
  stage: string;
  progress_percentage: number;
  message: string;
}

export interface ImportResult {
  meeting_id: string;
  title: string;
  segments_count: number;
  duration_seconds: number;
}

export interface ImportError {
  error: string;
}

export type ImportStatus = 'idle' | 'validating' | 'processing' | 'complete' | 'error';

export interface UseImportAudioOptions {
  onComplete?: (result: ImportResult) => void;
  onError?: (error: string) => void;
}

export interface UseImportAudioReturn {
  status: ImportStatus;
  fileInfo: AudioFileInfo | null;
  progress: ImportProgress | null;
  error: string | null;
  isProcessing: boolean;
  isBusy: boolean;
  selectFile: () => Promise<AudioFileInfo | null>;
  acceptSelection: (selection: AudioFileInfo) => void;
  startImport: (
    title: string,
    language?: string | null,
    model?: string | null,
    provider?: string | null
  ) => Promise<void>;
  cancelImport: () => Promise<void>;
  reset: () => void;
}

export function useImportAudio({
  onComplete,
  onError,
}: UseImportAudioOptions = {}): UseImportAudioReturn {
  const { media: mediaClient, transcription: transcriptionClient } = useVoxaClient();
  const [status, setStatus] = useState<ImportStatus>('idle');
  const [fileInfo, setFileInfo] = useState<AudioFileInfo | null>(null);
  const [progress, setProgress] = useState<ImportProgress | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Stable refs for callbacks to avoid listener re-registration on every render
  const onCompleteRef = useRef(onComplete);
  const onErrorRef = useRef(onError);
  useEffect(() => { onCompleteRef.current = onComplete; }, [onComplete]);
  useEffect(() => { onErrorRef.current = onError; }, [onError]);

  // Cancellation guard: prevents late events from updating state after cancel
  const isCancelledRef = useRef(false);

  // Set up event listeners (registered once, use refs for callbacks)
  useEffect(() => {
    const unlisteners = [
      transcriptionClient.onImportProgress(event => {
          if (isCancelledRef.current) return;
          setProgress({
            stage: event.stage,
            progress_percentage: event.progressPercentage,
            message: event.message,
          });
          setStatus('processing');
      }),
      transcriptionClient.onImportComplete(async event => {
          if (isCancelledRef.current) return;

          const result: ImportResult = {
            meeting_id: event.meetingId,
            title: event.title,
            segments_count: event.segmentsCount,
            duration_seconds: event.durationSeconds,
          };

          await Analytics.track('import_audio_completed', {
            success: 'true',
            duration_seconds: result.duration_seconds.toString(),
            segments_count: result.segments_count.toString()
          });

          setStatus('complete');
          setProgress(null);
          try {
            await applyPinnedSummaryLanguageToMeeting(result.meeting_id);
          } catch (error) {
            console.warn('Failed to apply pinned summary language to imported meeting:', error);
            toast.warning('Could not apply default summary language', {
              description: 'The imported meeting was saved, but the default summary language was not applied.',
            });
          }
          onCompleteRef.current?.(result);
      }),
      transcriptionClient.onImportError(async event => {
          if (isCancelledRef.current) return;

          await Analytics.trackError('import_audio_failed', event.error);

          setStatus('error');
          setError(event.error);
          onErrorRef.current?.(event.error);
      }),
    ];

    return () => {
      unlisteners.forEach((unlisten) => unlisten());
    };
  }, [transcriptionClient]);

  // Select file using native file dialog
  const selectFile = useCallback(async (): Promise<AudioFileInfo | null> => {
    setStatus('validating');
    setError(null);

    try {
      const result = await mediaClient.selectImportSource();
      if (result) {
        setFileInfo(result);
        setStatus('idle');
        return result;
      } else {
        // User cancelled
        setStatus('idle');
        return null;
      }
    } catch (err: any) {
      setStatus('error');
      const errorMsg = typeof err === 'string' ? err : (err?.message || String(err) || 'Failed to validate file');
      setError(errorMsg);
      onErrorRef.current?.(errorMsg);
      return null;
    }
  }, [mediaClient]);

  const acceptSelection = useCallback((selection: AudioFileInfo) => {
    setFileInfo(selection);
    setStatus('idle');
    setError(null);
  }, []);

  // Start the import process
  const startImport = useCallback(
    async (
      title: string,
      language?: string | null,
      model?: string | null,
      provider?: string | null
    ) => {
      isCancelledRef.current = false;
      setStatus('processing');
      setError(null);
      setProgress(null);

      try {
        if (fileInfo) {
          await Analytics.track('import_audio_started', {
            file_size_bytes: fileInfo.sizeBytes.toString(),
            duration_seconds: fileInfo.durationSeconds.toString(),
            language: language || 'auto',
            model_provider: provider || '',
            model_name: model || ''
          });
        }

        if (!fileInfo) {
          throw new Error('No validated audio source selected');
        }

        await transcriptionClient.startImport({
          source: fileInfo.source,
          title,
          language: language || null,
          model: model || null,
          provider: provider || null,
        });
      } catch (err: any) {
        setStatus('error');
        const errorMsg = typeof err === 'string' ? err : (err?.message || String(err) || 'Failed to start import');
        setError(errorMsg);

        await Analytics.trackError('import_audio_failed', errorMsg);

        onErrorRef.current?.(errorMsg);
      }
    },
    [fileInfo, transcriptionClient]
  );

  // Cancel ongoing import
  const cancelImport = useCallback(async () => {
    isCancelledRef.current = true;
    try {
      await transcriptionClient.cancelImport();
      setStatus('idle');
      setProgress(null);
    } catch (err: any) {
      console.error('Failed to cancel import:', err);
    }
  }, [transcriptionClient]);

  // Reset all state
  const reset = useCallback(() => {
    isCancelledRef.current = false;
    setStatus('idle');
    setFileInfo(null);
    setProgress(null);
    setError(null);
  }, []);

  return {
    status,
    fileInfo,
    progress,
    error,
    isProcessing: status === 'processing',
    isBusy: status === 'processing' || status === 'validating',
    selectFile,
    acceptSelection,
    startImport,
    cancelImport,
    reset,
  };
}
