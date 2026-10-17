import { beforeEach, describe, expect, mock, test } from 'bun:test';

type InvokeCall = { command: string; args?: Record<string, unknown> };
const calls: InvokeCall[] = [];
const storeValues = new Map<string, unknown>();
const storeCalls: unknown[][] = [];

const invoke = mock(async (command: string, args?: Record<string, unknown>): Promise<unknown> => {
  calls.push({ command, args });
  if (command === 'api_get_transcript_config') return { provider: 'parakeet', model: 'p-v3', apiKey: null };
  if (command === 'api_get_model_config') return { provider: 'ollama', model: 'qwen', whisperModel: 'large-v3' };
  if (command === 'api_get_api_key' || command === 'api_get_transcript_api_key') return 'secret';
  if (command === 'api_get_custom_openai_config') return null;
  if (command === 'get_recording_preferences') return {
    save_folder: '/recordings', auto_save: true, file_format: 'mp4',
    preferred_mic_device: null, preferred_system_device: null,
  };
  if (command === 'get_default_recordings_folder_path') return '/recordings';
  return undefined;
});

const store = {
  get: mock(async (key: string) => { storeCalls.push(['get', key]); return storeValues.get(key); }),
  set: mock(async (key: string, value: unknown) => { storeCalls.push(['set', key, value]); storeValues.set(key, value); }),
  save: mock(async () => { storeCalls.push(['save']); }),
};
const load = mock(async (name: string) => { storeCalls.push(['load', name]); return store; });

mock.module('@tauri-apps/api/core', () => ({ invoke }));
mock.module('@tauri-apps/plugin-store', () => ({ Store: { load } }));
const { TauriConfigurationClient } = await import('../../src/clients/tauri/configuration-client');

beforeEach(() => {
  calls.length = 0;
  storeCalls.length = 0;
  storeValues.clear();
  invoke.mockClear();
});

describe('TauriConfigurationClient', () => {
  test('reports factual desktop configuration capabilities', () => {
    expect(new TauriConfigurationClient().capabilities).toEqual({
      remoteCredentialStorage: false, desktopStorageLocations: true,
      recordingDeviceDefaults: true, nativeNotificationPreferences: true,
    });
  });

  test('preserves transcription preference commands and payloads', async () => {
    const client = new TauriConfigurationClient();
    expect(await client.getTranscriptionPreferences()).toEqual({ provider: 'parakeet', model: 'p-v3', apiKey: null });
    await client.saveTranscriptionPreferences({ provider: 'localWhisper', model: 'small', apiKey: null });
    expect(await client.getTranscriptionApiKey('cloud')).toBe('secret');
    expect(calls).toEqual([
      { command: 'api_get_transcript_config', args: undefined },
      { command: 'api_save_transcript_config', args: { provider: 'localWhisper', model: 'small', apiKey: null } },
      { command: 'api_get_transcript_api_key', args: { provider: 'cloud' } },
    ]);
  });

  test('preserves summary preferences, credentials, and custom endpoint payloads', async () => {
    const client = new TauriConfigurationClient();
    expect((await client.getSummaryPreferences())?.model).toBe('qwen');
    await client.saveSummaryPreferences({ provider: 'openai', model: 'gpt-4o', whisperModel: 'large-v3', apiKey: 'key' });
    expect(calls.at(-1)).toEqual({ command: 'api_save_model_config', args: {
      provider: 'openai', model: 'gpt-4o', whisperModel: 'large-v3',
      apiKey: 'key', ollamaEndpoint: null,
    } });
    expect(await client.getSummaryApiKey('openai')).toBe('secret');
    expect(await client.getCustomOpenAIConfiguration()).toBeNull();
    await client.saveCustomOpenAIConfiguration({ endpoint: 'http://llm', apiKey: null, model: 'm', maxTokens: 10, temperature: 0.2, topP: 0.9 });
    expect(calls.at(-1)).toEqual({ command: 'api_save_custom_openai_config', args: {
      endpoint: 'http://llm', apiKey: null, model: 'm', maxTokens: 10, temperature: 0.2, topP: 0.9,
    } });
  });

  test('preserves recording and storage commands', async () => {
    const client = new TauriConfigurationClient();
    const preferences = await client.getRecordingPreferences();
    await client.saveRecordingPreferences(preferences);
    expect(await client.getDefaultRecordingsFolder()).toBe('/recordings');
    expect(calls).toEqual([
      { command: 'get_recording_preferences', args: undefined },
      { command: 'set_recording_preferences', args: { preferences: {
        save_folder: '/recordings', auto_save: true, file_format: 'mp4',
        preferred_mic_device: null, preferred_system_device: null,
        system_audio_backend: undefined,
      } } },
      { command: 'get_default_recordings_folder_path', args: undefined },
    ]);
  });

  test('uses the existing store key and defaults missing notification preference to true', async () => {
    const client = new TauriConfigurationClient();
    expect(await client.getRecordingNotificationEnabled()).toBe(true);
    await client.setRecordingNotificationEnabled(false);
    expect(storeCalls).toEqual([
      ['load', 'preferences.json'], ['get', 'show_recording_notification'],
      ['load', 'preferences.json'], ['set', 'show_recording_notification', false], ['save'],
    ]);
  });

  test('preserves native notification preference commands and payloads', async () => {
    const client = new TauriConfigurationClient();
    const nativePreferences = {
      recording_notifications: true, time_based_reminders: false,
      meeting_reminders: false, respect_do_not_disturb: true,
      notification_sound: true, system_permission_granted: true,
      consent_given: true, manual_dnd_mode: false,
      notification_preferences: {
        show_recording_started: true, show_recording_stopped: true,
        show_recording_paused: true, show_recording_resumed: true,
        show_transcription_complete: true, show_meeting_reminders: false,
        show_system_errors: true, meeting_reminder_minutes: [5],
      },
    };
    invoke.mockImplementationOnce(async (command, args) => {
      calls.push({ command, args });
      return nativePreferences;
    });
    const preferences = await client.getNotificationPreferences();
    expect(preferences.recordingNotifications).toBe(true);
    expect(preferences.notifications.meetingReminderMinutes).toEqual([5]);
    await client.saveNotificationPreferences(preferences);
    expect(calls).toEqual([
      { command: 'get_notification_settings', args: undefined },
      { command: 'set_notification_settings', args: { settings: nativePreferences } },
    ]);
  });

  test('propagates native errors', async () => {
    invoke.mockImplementationOnce(async () => { throw new Error('database unavailable'); });
    await expect(new TauriConfigurationClient().getSummaryPreferences()).rejects.toThrow('database unavailable');
  });
});
