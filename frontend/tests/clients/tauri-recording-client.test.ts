import { describe, expect, mock, test } from 'bun:test';

type InvokeCall = { command: string; args?: Record<string, unknown> };
type Listener = (event: { payload: unknown }) => void;

const invokeCalls: InvokeCall[] = [];
const listeners = new Map<string, Listener>();
const unlistenCalls: string[] = [];

const invoke = mock(async (command: string, args?: Record<string, unknown>): Promise<unknown> => {
  invokeCalls.push({ command, args });

  if (command === 'is_recording') return true;
  if (command === 'get_recording_state') {
    return {
      is_recording: true,
      is_paused: false,
      is_active: true,
      recording_duration: 12,
      active_duration: 11,
    };
  }
  if (command === 'get_audio_devices') {
    return [
      { name: 'Built-in Mic', device_type: 'Input' },
      { name: 'Desktop Audio', device_type: 'Output' },
    ];
  }
  if (command === 'get_recording_meeting_name') return 'Planning';
  if (command === 'get_meeting_folder_path') return '/recordings/planning';
  return undefined;
});

const listen = mock(async (eventName: string, listener: Listener) => {
  listeners.set(eventName, listener);
  return () => {
    listeners.delete(eventName);
    unlistenCalls.push(eventName);
  };
});

mock.module('@tauri-apps/api/core', () => ({ invoke }));
mock.module('@tauri-apps/api/event', () => ({ listen }));
mock.module('@tauri-apps/api/path', () => ({ appDataDir: async () => '/app-data' }));

const { TauriRecordingClient } = await import('../../src/clients/tauri/recording-client');

describe('TauriRecordingClient commands', () => {
  test('normalizes devices and recording state while preserving query commands', async () => {
    invokeCalls.length = 0;
    const client = new TauriRecordingClient();

    expect(client.capabilities).toEqual({
      systemAudioCapture: 'native',
      deviceEnumeration: 'input-and-output',
      microphoneHotSwap: 'automatic',
      audioLevelMonitoring: true,
      pauseResume: true,
      tabCapture: false,
    });
    expect(await client.listDevices()).toEqual([
      { name: 'Built-in Mic', type: 'input' },
      { name: 'Desktop Audio', type: 'output' },
    ]);
    expect(await client.getState()).toEqual({
      isRecording: true,
      isPaused: false,
      isActive: true,
      recordingDuration: 12,
      activeDuration: 11,
    });
    expect(await client.isRecording()).toBe(true);
    expect(await client.getMeetingName()).toBe('Planning');
    expect(await client.getSessionFolderPath()).toBe('/recordings/planning');

    expect(invokeCalls).toEqual([
      { command: 'get_audio_devices', args: undefined },
      { command: 'get_recording_state', args: undefined },
      { command: 'is_recording', args: undefined },
      { command: 'get_recording_meeting_name', args: undefined },
      { command: 'get_meeting_folder_path', args: undefined },
    ]);
  });

  test('preserves start, stop, pause, resume, and level-monitor command payloads', async () => {
    invokeCalls.length = 0;
    const client = new TauriRecordingClient();

    await client.start({
      microphoneDeviceName: 'Built-in Mic (input)',
      systemAudioDeviceName: 'Desktop Audio (output)',
      meetingName: 'Planning',
    });
    await client.pause();
    await client.resume();
    await client.startAudioLevelMonitoring(['Built-in Mic']);
    await client.stopAudioLevelMonitoring();
    await client.stop();

    expect(invokeCalls.slice(0, 5)).toEqual([
      {
        command: 'start_recording_with_devices_and_meeting',
        args: {
          micDeviceName: 'Built-in Mic (input)',
          systemDeviceName: 'Desktop Audio (output)',
          meetingName: 'Planning',
        },
      },
      { command: 'pause_recording', args: undefined },
      { command: 'resume_recording', args: undefined },
      { command: 'start_audio_level_monitoring', args: { deviceNames: ['Built-in Mic'] } },
      { command: 'stop_audio_level_monitoring', args: undefined },
    ]);
    expect(invokeCalls[5]?.command).toBe('stop_recording');
    expect(invokeCalls[5]?.args).toEqual({
      args: {
        save_path: expect.stringMatching(/^\/app-data\/recording-.*\.wav$/),
      },
    });
  });

  test('represents the absence of an active recording folder as null', async () => {
    invoke.mockImplementationOnce(async (command: string, args?: Record<string, unknown>) => {
      invokeCalls.push({ command, args });
      return null;
    });

    const client = new TauriRecordingClient();
    expect(await client.getSessionFolderPath()).toBeNull();
  });
});

describe('TauriRecordingClient event boundary', () => {
  test('translates native recording payloads and cleans up after registration', async () => {
    listeners.clear();
    unlistenCalls.length = 0;
    const client = new TauriRecordingClient();
    const received: unknown[] = [];

    const unsubscribe = client.onStopped(payload => received.push(payload));
    await Promise.resolve();
    listeners.get('recording-stopped')?.({
      payload: {
        message: 'stopped',
        folder_path: '/recordings/planning',
        meeting_name: 'Planning',
      },
    });

    expect(received).toEqual([{
      message: 'stopped',
      folderPath: '/recordings/planning',
      meetingName: 'Planning',
    }]);

    unsubscribe();
    expect(unlistenCalls).toEqual(['recording-stopped']);
    expect(listeners.has('recording-stopped')).toBe(false);
  });

  test('cancellation before async listen resolves immediately removes the late listener', async () => {
    let resolveRegistration!: (unlisten: () => void) => void;
    let pendingListener!: Listener;
    const lateUnlisten = mock(() => {});
    listen.mockImplementationOnce((_eventName, listener) => new Promise(resolve => {
      pendingListener = listener;
      resolveRegistration = resolve;
    }));

    const client = new TauriRecordingClient();
    const callback = mock(() => {});
    const unsubscribe = client.onStarted(callback);
    unsubscribe();
    pendingListener({ payload: undefined });
    resolveRegistration(lateUnlisten);
    await Promise.resolve();
    await Promise.resolve();
    unsubscribe();

    expect(callback).not.toHaveBeenCalled();
    expect(lateUnlisten).toHaveBeenCalledTimes(1);
  });

  test('translates audio-level and microphone recovery payloads', async () => {
    listeners.clear();
    const client = new TauriRecordingClient();
    const audioLevels: unknown[] = [];
    const swapFailures: unknown[] = [];
    const exhausted: unknown[] = [];

    const unsubscribeLevels = client.onAudioLevels(update => audioLevels.push(update));
    const unsubscribeSwapFailure = client.onMicrophoneSwapFailed(event => swapFailures.push(event));
    const unsubscribeExhausted = client.onMicrophoneRecoveryExhausted(event => exhausted.push(event));
    await Promise.resolve();

    listeners.get('audio-levels')?.({
      payload: {
        timestamp: 42,
        levels: [{
          device_name: 'Built-in Mic',
          device_type: 'Input',
          rms_level: 0.2,
          peak_level: 0.4,
          is_active: true,
        }],
      },
    });
    listeners.get('mic-swap-failed')?.({
      payload: { error: 'unavailable', device_name: 'USB Mic' },
    });
    listeners.get('mic-recovery-exhausted')?.({
      payload: { device_name: 'USB Mic' },
    });

    expect(audioLevels).toEqual([{
      timestamp: 42,
      levels: [{
        deviceName: 'Built-in Mic',
        deviceType: 'Input',
        rmsLevel: 0.2,
        peakLevel: 0.4,
        isActive: true,
      }],
    }]);
    expect(swapFailures).toEqual([{ error: 'unavailable', deviceName: 'USB Mic' }]);
    expect(exhausted).toEqual([{ deviceName: 'USB Mic' }]);

    unsubscribeLevels();
    unsubscribeSwapFailure();
    unsubscribeExhausted();
  });

  test('preserves lifecycle event names and stop-complete payload', async () => {
    listeners.clear();
    const client = new TauriRecordingClient();
    const events: string[] = [];
    let shouldPersist: boolean | undefined;

    const cleanups = [
      client.onStarting(() => events.push('starting')),
      client.onPaused(() => events.push('paused')),
      client.onResumed(() => events.push('resumed')),
      client.onStopComplete(value => { shouldPersist = value; }),
    ];
    await Promise.resolve();

    listeners.get('recording-starting')?.({ payload: undefined });
    listeners.get('recording-paused')?.({ payload: undefined });
    listeners.get('recording-resumed')?.({ payload: undefined });
    listeners.get('recording-stop-complete')?.({ payload: true });

    expect(events).toEqual(['starting', 'paused', 'resumed']);
    expect(shouldPersist).toBe(true);
    cleanups.forEach(cleanup => cleanup());
  });
});
