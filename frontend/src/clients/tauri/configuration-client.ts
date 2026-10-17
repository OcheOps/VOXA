import { invoke } from '@tauri-apps/api/core';
import { Store } from '@tauri-apps/plugin-store';
import type {
  ConfigurationCapabilities,
  ConfigurationClient,
  CustomOpenAIConfiguration,
  RecordingPreferences,
  NotificationPreferences,
  SummaryPreferences,
  TranscriptionPreferences,
} from '../contracts';

const PREFERENCES_STORE = 'preferences.json';
const RECORDING_NOTIFICATION_KEY = 'show_recording_notification';
const TRANSCRIPTION_LANGUAGE_KEY = 'primaryLanguage';
const AUTO_SUMMARY_KEY = 'isAutoSummary';

interface NativeRecordingPreferences {
  save_folder: string;
  auto_save: boolean;
  file_format: string;
  preferred_mic_device: string | null;
  preferred_system_device: string | null;
  system_audio_backend?: string | null;
}

interface NativeNotificationPreferences {
  recording_notifications: boolean;
  time_based_reminders: boolean;
  meeting_reminders: boolean;
  respect_do_not_disturb: boolean;
  notification_sound: boolean;
  system_permission_granted: boolean;
  consent_given: boolean;
  manual_dnd_mode: boolean;
  notification_preferences: {
    show_recording_started: boolean;
    show_recording_stopped: boolean;
    show_recording_paused: boolean;
    show_recording_resumed: boolean;
    show_transcription_complete: boolean;
    show_meeting_reminders: boolean;
    show_system_errors: boolean;
    meeting_reminder_minutes: number[];
  };
}

export class TauriConfigurationClient implements ConfigurationClient {
  readonly capabilities: ConfigurationCapabilities = {
    remoteCredentialStorage: false,
    desktopStorageLocations: true,
    recordingDeviceDefaults: true,
    nativeNotificationPreferences: true,
  };

  getTranscriptionPreferences(): Promise<TranscriptionPreferences> {
    return invoke('api_get_transcript_config');
  }

  async saveTranscriptionPreferences(preferences: TranscriptionPreferences): Promise<void> {
    await invoke('api_save_transcript_config', {
      provider: preferences.provider,
      model: preferences.model,
      apiKey: preferences.apiKey,
    });
  }

  getTranscriptionApiKey(provider: string): Promise<string> {
    return invoke('api_get_transcript_api_key', { provider });
  }

  async getTranscriptionLanguage(): Promise<string> {
    return typeof localStorage === 'undefined'
      ? 'auto'
      : localStorage.getItem(TRANSCRIPTION_LANGUAGE_KEY) ?? 'auto';
  }

  async saveTranscriptionLanguage(language: string): Promise<void> {
    if (typeof localStorage !== 'undefined') localStorage.setItem(TRANSCRIPTION_LANGUAGE_KEY, language);
  }

  getSummaryPreferences(): Promise<SummaryPreferences | null> {
    return invoke('api_get_model_config');
  }

  async saveSummaryPreferences(preferences: SummaryPreferences): Promise<void> {
    await invoke('api_save_model_config', {
      provider: preferences.provider,
      model: preferences.model,
      whisperModel: preferences.whisperModel,
      apiKey: preferences.apiKey ?? null,
      ollamaEndpoint: preferences.ollamaEndpoint ?? null,
    });
  }

  getSummaryApiKey(provider: string): Promise<string> {
    return invoke('api_get_api_key', { provider });
  }

  getCustomOpenAIConfiguration(): Promise<CustomOpenAIConfiguration | null> {
    return invoke('api_get_custom_openai_config');
  }

  async saveCustomOpenAIConfiguration(configuration: CustomOpenAIConfiguration): Promise<void> {
    await invoke('api_save_custom_openai_config', {
      endpoint: configuration.endpoint,
      apiKey: configuration.apiKey,
      model: configuration.model,
      maxTokens: configuration.maxTokens,
      temperature: configuration.temperature,
      topP: configuration.topP,
    });
  }

  async getAutoSummaryEnabled(): Promise<boolean> {
    if (typeof localStorage === 'undefined') return false;
    return localStorage.getItem(AUTO_SUMMARY_KEY) === 'true';
  }

  async setAutoSummaryEnabled(enabled: boolean): Promise<void> {
    if (typeof localStorage !== 'undefined') localStorage.setItem(AUTO_SUMMARY_KEY, String(enabled));
  }

  async getRecordingPreferences(): Promise<RecordingPreferences> {
    const value = await invoke<NativeRecordingPreferences>('get_recording_preferences');
    return {
      saveFolder: value.save_folder,
      autoSave: value.auto_save,
      fileFormat: value.file_format,
      preferredMicrophoneDevice: value.preferred_mic_device,
      preferredSystemAudioDevice: value.preferred_system_device,
      systemAudioBackend: value.system_audio_backend,
    };
  }

  async saveRecordingPreferences(preferences: RecordingPreferences): Promise<void> {
    await invoke('set_recording_preferences', { preferences: {
      save_folder: preferences.saveFolder,
      auto_save: preferences.autoSave,
      file_format: preferences.fileFormat,
      preferred_mic_device: preferences.preferredMicrophoneDevice,
      preferred_system_device: preferences.preferredSystemAudioDevice,
      system_audio_backend: preferences.systemAudioBackend,
    } });
  }

  getDefaultRecordingsFolder(): Promise<string> {
    return invoke('get_default_recordings_folder_path');
  }

  async getRecordingNotificationEnabled(): Promise<boolean> {
    const store = await Store.load(PREFERENCES_STORE);
    return (await store.get<boolean>(RECORDING_NOTIFICATION_KEY)) ?? true;
  }

  async setRecordingNotificationEnabled(enabled: boolean): Promise<void> {
    const store = await Store.load(PREFERENCES_STORE);
    await store.set(RECORDING_NOTIFICATION_KEY, enabled);
    await store.save();
  }

  async getNotificationPreferences(): Promise<NotificationPreferences> {
    const value = await invoke<NativeNotificationPreferences>('get_notification_settings');
    return {
      recordingNotifications: value.recording_notifications,
      timeBasedReminders: value.time_based_reminders,
      meetingReminders: value.meeting_reminders,
      respectDoNotDisturb: value.respect_do_not_disturb,
      notificationSound: value.notification_sound,
      systemPermissionGranted: value.system_permission_granted,
      consentGiven: value.consent_given,
      manualDndMode: value.manual_dnd_mode,
      notifications: {
        showRecordingStarted: value.notification_preferences.show_recording_started,
        showRecordingStopped: value.notification_preferences.show_recording_stopped,
        showRecordingPaused: value.notification_preferences.show_recording_paused,
        showRecordingResumed: value.notification_preferences.show_recording_resumed,
        showTranscriptionComplete: value.notification_preferences.show_transcription_complete,
        showMeetingReminders: value.notification_preferences.show_meeting_reminders,
        showSystemErrors: value.notification_preferences.show_system_errors,
        meetingReminderMinutes: value.notification_preferences.meeting_reminder_minutes,
      },
    };
  }

  async saveNotificationPreferences(preferences: NotificationPreferences): Promise<void> {
    await invoke('set_notification_settings', { settings: {
      recording_notifications: preferences.recordingNotifications,
      time_based_reminders: preferences.timeBasedReminders,
      meeting_reminders: preferences.meetingReminders,
      respect_do_not_disturb: preferences.respectDoNotDisturb,
      notification_sound: preferences.notificationSound,
      system_permission_granted: preferences.systemPermissionGranted,
      consent_given: preferences.consentGiven,
      manual_dnd_mode: preferences.manualDndMode,
      notification_preferences: {
        show_recording_started: preferences.notifications.showRecordingStarted,
        show_recording_stopped: preferences.notifications.showRecordingStopped,
        show_recording_paused: preferences.notifications.showRecordingPaused,
        show_recording_resumed: preferences.notifications.showRecordingResumed,
        show_transcription_complete: preferences.notifications.showTranscriptionComplete,
        show_meeting_reminders: preferences.notifications.showMeetingReminders,
        show_system_errors: preferences.notifications.showSystemErrors,
        meeting_reminder_minutes: preferences.notifications.meetingReminderMinutes,
      },
    } });
  }
}
