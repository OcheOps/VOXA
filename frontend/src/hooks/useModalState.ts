import { useState, useEffect, useCallback } from 'react';
import { toast } from 'sonner';
import { TranscriptModelProps } from '@/components/TranscriptSettings';
import { useVoxaClient } from '@/clients';

export type ModalType =
  | 'modelSettings'
  | 'deviceSettings'
  | 'languageSettings'
  | 'modelSelector'
  | 'errorAlert'
  | 'chunkDropWarning';

interface ModalState {
  modelSettings: boolean;
  deviceSettings: boolean;
  languageSettings: boolean;
  modelSelector: boolean;
  errorAlert: boolean;
  chunkDropWarning: boolean;
}

interface ModalMessages {
  errorAlert: string;
  chunkDropWarning: string;
  modelSelector: string;
}

interface UseModalStateReturn {
  modals: ModalState;
  messages: ModalMessages;
  showModal: (name: ModalType, message?: string) => void;
  hideModal: (name: ModalType) => void;
  hideAllModals: () => void;
}

/**
 * Custom hook for managing all modal state and event listeners.
 * Consolidates 9 useState calls and 3 event listeners from page.tsx.
 *
 * Features:
 * - Unified modal state management
 * - Event listeners for chunk drops, transcription errors, model downloads
 * - Auto-close on model download completion
 */
export function useModalState(transcriptModelConfig?: TranscriptModelProps): UseModalStateReturn {
  const { recording: recordingClient, transcription: transcriptionClient } = useVoxaClient();
  // Modal visibility state
  const [modals, setModals] = useState<ModalState>({
    modelSettings: false,
    deviceSettings: false,
    languageSettings: false,
    modelSelector: false,
    errorAlert: false,
    chunkDropWarning: false,
  });

  // Modal messages
  const [messages, setMessages] = useState<ModalMessages>({
    errorAlert: '',
    chunkDropWarning: '',
    modelSelector: '',
  });

  // Show modal with optional message
  const showModal = useCallback((name: ModalType, message?: string) => {
    setModals(prev => ({ ...prev, [name]: true }));

    // Set message if provided
    if (message && (name === 'errorAlert' || name === 'chunkDropWarning' || name === 'modelSelector')) {
      setMessages(prev => ({ ...prev, [name]: message }));
    }
  }, []);

  // Hide modal and clear its message
  const hideModal = useCallback((name: ModalType) => {
    setModals(prev => ({ ...prev, [name]: false }));

    // Clear message when closing
    if (name === 'errorAlert' || name === 'chunkDropWarning' || name === 'modelSelector') {
      setMessages(prev => ({ ...prev, [name]: '' }));
    }
  }, []);

  // Hide all modals
  const hideAllModals = useCallback(() => {
    setModals({
      modelSettings: false,
      deviceSettings: false,
      languageSettings: false,
      modelSelector: false,
      errorAlert: false,
      chunkDropWarning: false,
    });
    setMessages({
      errorAlert: '',
      chunkDropWarning: '',
      modelSelector: '',
    });
  }, []);

  // Set up chunk drop warning listener
  useEffect(() => {
    console.log('Setting up chunk-drop-warning listener...');
    const unlistenFn = recordingClient.onChunkDropWarning(warning => {
      console.log('Chunk drop warning received:', warning);
      showModal('chunkDropWarning', warning);
    });

    return () => {
      console.log('Cleaning up chunk drop warning listener...');
      unlistenFn();
    };
  }, [showModal, recordingClient]);

  // Set up transcription error listener for model loading failures
  useEffect(() => {
    console.log('Setting up transcription-error listener...');
    const unlistenFn = transcriptionClient.onError(error => {
          console.log('Transcription error received:', error);
          const { userMessage, actionable } = error;

          if (actionable) {
            // This is a model-related error that requires user action
            showModal('modelSelector', userMessage);
          } else {
            // Show toast instead of modal for non-actionable errors (consistent with sidebar)
            toast.error('', {
              description: userMessage,
              duration: 5000,
            });
          }
    });
    console.log('Transcription error listener setup complete');

    return () => {
      console.log('Cleaning up transcription error listener...');
      unlistenFn();
    };
  }, [showModal, transcriptionClient]);

  // Listen for model download completion to auto-close modal
  useEffect(() => {
    const unsubscribe = transcriptionClient.onModelDownloadComplete(event => {
        const { modelName, provider } = event;
        if (provider !== 'localWhisper') return;
        console.log('[useModalState] Whisper model download complete:', modelName);

        // Auto-close modal if the downloaded model matches the selected one
        if (transcriptModelConfig?.provider === 'localWhisper' && transcriptModelConfig?.model === modelName) {
          toast.success('Model ready! Closing window...', { duration: 1500 });
          setTimeout(() => hideModal('modelSelector'), 1500);
        }
    });

    return unsubscribe;
  }, [transcriptModelConfig, hideModal, transcriptionClient]);

  return {
    modals,
    messages,
    showModal,
    hideModal,
    hideAllModals,
  };
}
