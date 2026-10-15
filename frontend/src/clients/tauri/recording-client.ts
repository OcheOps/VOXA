import { invoke } from '@tauri-apps/api/core';
import { appDataDir } from '@tauri-apps/api/path';
import { subscribeToTauriEvent } from './subscribe';
import type {
  AudioLevelUpdate,
  MicrophoneSwapFailedEvent,
  MicrophoneSwitchedEvent,
  RecordingCapabilities,
  RecordingClient,
  RecordingDevice,
  RecordingStartOptions,
  RecordingState,
  RecordingStoppedEvent,
} from '../contracts';

interface NativeRecordingState {
  is_recording: boolean;
  is_paused: boolean;
  is_active: boolean;
  recording_duration: number | null;
  active_duration: number | null;
}

interface NativeRecordingDevice {
  name: string;
  device_type: 'Input' | 'Output';
}

interface NativeRecordingStoppedEvent {
  message: string;
  folder_path?: string;
  meeting_name?: string;
}

interface NativeAudioLevelUpdate {
  timestamp: number;
  levels: Array<{
    device_name: string;
    device_type: string;
    rms_level: number;
    peak_level: number;
    is_active: boolean;
  }>;
}

const START_TIMEOUT_MS = 120_000;

function withStartTimeout<T>(promise: Promise<T>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error('Recording start timed out after 120s')),
      START_TIMEOUT_MS,
    );
    promise.then(
      value => {
        clearTimeout(timer);
        resolve(value);
      },
      error => {
        clearTimeout(timer);
        reject(error);
      },
    );
  });
}

export class TauriRecordingClient implements RecordingClient {
  readonly capabilities: RecordingCapabilities = {
    systemAudioCapture: 'native',
    deviceEnumeration: 'input-and-output',
    microphoneHotSwap: 'automatic',
    audioLevelMonitoring: true,
    pauseResume: true,
    tabCapture: false,
  };

  async listDevices(): Promise<RecordingDevice[]> {
    const devices = await invoke<NativeRecordingDevice[]>('get_audio_devices');
    return devices.map(device => ({
      name: device.name,
      type: device.device_type === 'Input' ? 'input' : 'output',
    }));
  }

  isRecording(): Promise<boolean> {
    return invoke('is_recording');
  }

  async getState(): Promise<RecordingState> {
    const state = await invoke<NativeRecordingState>('get_recording_state');
    return {
      isRecording: state.is_recording,
      isPaused: state.is_paused,
      isActive: state.is_active,
      recordingDuration: state.recording_duration,
      activeDuration: state.active_duration,
    };
  }

  getMeetingName(): Promise<string | null> {
    return invoke('get_recording_meeting_name');
  }

  getSessionFolderPath(): Promise<string | null> {
    return invoke('get_meeting_folder_path');
  }

  start(input: RecordingStartOptions): Promise<void> {
    return withStartTimeout(invoke('start_recording_with_devices_and_meeting', {
      micDeviceName: input.microphoneDeviceName,
      systemDeviceName: input.systemAudioDeviceName,
      meetingName: input.meetingName,
    }));
  }

  async stop(): Promise<void> {
    const dataDir = await appDataDir();
    const timestamp = new Date().toISOString().replace(/[:.]/g, '-');
    const savePath = `${dataDir}/recording-${timestamp}.wav`;
    await invoke('stop_recording', { args: { save_path: savePath } });
  }

  pause(): Promise<void> {
    return invoke('pause_recording');
  }

  resume(): Promise<void> {
    return invoke('resume_recording');
  }

  startAudioLevelMonitoring(deviceNames: string[]): Promise<void> {
    return invoke('start_audio_level_monitoring', { deviceNames });
  }

  stopAudioLevelMonitoring(): Promise<void> {
    return invoke('stop_audio_level_monitoring');
  }

  onStarting(callback: () => void): () => void {
    return subscribeToTauriEvent('recording-starting', callback);
  }

  onStarted(callback: () => void): () => void {
    return subscribeToTauriEvent('recording-started', callback);
  }

  onStopped(callback: (event: RecordingStoppedEvent) => void): () => void {
    return subscribeToTauriEvent<NativeRecordingStoppedEvent, RecordingStoppedEvent>(
      'recording-stopped', callback, payload => ({
        message: payload.message,
        folderPath: payload.folder_path,
        meetingName: payload.meeting_name,
      }),
    );
  }

  onPaused(callback: () => void): () => void {
    return subscribeToTauriEvent('recording-paused', callback);
  }

  onResumed(callback: () => void): () => void {
    return subscribeToTauriEvent('recording-resumed', callback);
  }

  onStopComplete(callback: (shouldPersist: boolean) => void): () => void {
    return subscribeToTauriEvent('recording-stop-complete', callback);
  }

  onSpeechDetected(callback: () => void): () => void {
    return subscribeToTauriEvent('speech-detected', callback);
  }

  onChunkDropWarning(callback: (warning: string) => void): () => void {
    return subscribeToTauriEvent('chunk-drop-warning', callback);
  }

  onAudioLevels(callback: (update: AudioLevelUpdate) => void): () => void {
    return subscribeToTauriEvent<NativeAudioLevelUpdate, AudioLevelUpdate>(
      'audio-levels', callback, payload => ({
        timestamp: payload.timestamp,
        levels: payload.levels.map(level => ({
        deviceName: level.device_name,
        deviceType: level.device_type,
        rmsLevel: level.rms_level,
        peakLevel: level.peak_level,
        isActive: level.is_active,
        })),
      }),
    );
  }

  onMicrophoneSwitched(callback: (event: MicrophoneSwitchedEvent) => void): () => void {
    return subscribeToTauriEvent<{ device_name: string }, MicrophoneSwitchedEvent>(
      'mic-device-switched', callback, payload => ({ deviceName: payload.device_name }),
    );
  }

  onMicrophoneUnavailable(callback: () => void): () => void {
    return subscribeToTauriEvent('mic-unavailable', callback);
  }

  onMicrophoneSwapFailed(callback: (event: MicrophoneSwapFailedEvent) => void): () => void {
    return subscribeToTauriEvent<
      { error: string; device_name: string }, MicrophoneSwapFailedEvent
    >('mic-swap-failed', callback, payload => ({
      error: payload.error, deviceName: payload.device_name,
    }));
  }

  onMicrophoneRecoveryExhausted(callback: (event: MicrophoneSwitchedEvent) => void): () => void {
    return subscribeToTauriEvent<{ device_name: string }, MicrophoneSwitchedEvent>(
      'mic-recovery-exhausted', callback, payload => ({ deviceName: payload.device_name }),
    );
  }
}
