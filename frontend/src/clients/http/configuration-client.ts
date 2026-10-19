import type {
  ConfigurationResponse,
  CredentialListResponse,
  CredentialUpdateResponse,
  UpdateConfigurationRequest,
  UpdateConfigurationResponse,
} from '@voxa/protocol';
import type {
  ConfigurationCapabilities,
  ConfigurationClient,
  CredentialClient,
  CredentialStatus,
  CustomOpenAIConfiguration,
  NotificationPreferences,
  RecordingPreferences,
  SummaryPreferences,
  TranscriptionPreferences,
  WorkspaceSettings,
  WorkspaceSettingsClient,
  WorkspaceSettingsPatch,
} from '../contracts';
import { UnsupportedOperationError } from '../errors';
import type { HttpTransport } from './transport';

const CONFIGURATION = '/api/v1/configuration';

function toSettings(response: ConfigurationResponse): WorkspaceSettings {
  return {
    transcription: { ...response.transcription },
    summary: { ...response.summary },
    retainMedia: response.recording.retainMedia,
    analyticsEnabled: response.privacy.analyticsEnabled,
    processingCompleteNotifications: response.notifications.processingComplete,
  };
}

function toRequest(patch: WorkspaceSettingsPatch): UpdateConfigurationRequest {
  const request: UpdateConfigurationRequest = {};
  if (patch.transcription) request.transcription = patch.transcription;
  if (patch.summary) request.summary = patch.summary;
  if (patch.retainMedia !== undefined) request.recording = { retainMedia: patch.retainMedia };
  if (patch.analyticsEnabled !== undefined) request.privacy = { analyticsEnabled: patch.analyticsEnabled };
  if (patch.processingCompleteNotifications !== undefined) {
    request.notifications = { processingComplete: patch.processingCompleteNotifications };
  }
  return request;
}

/** Workspace defaults stored by the server (`/api/v1/configuration`). */
export class HttpWorkspaceSettingsClient implements WorkspaceSettingsClient {
  constructor(private readonly transport: HttpTransport) {}

  async get(): Promise<WorkspaceSettings> {
    return toSettings(await this.transport.get<ConfigurationResponse>(CONFIGURATION));
  }

  async update(patch: WorkspaceSettingsPatch): Promise<WorkspaceSettings> {
    const response = await this.transport.patch<UpdateConfigurationResponse>(CONFIGURATION, toRequest(patch));
    return toSettings(response.configuration);
  }
}

function credentialPath(provider: string): string {
  return `/api/v1/credentials/${encodeURIComponent(provider)}`;
}

/**
 * Write-only provider credentials. The server never returns secret values,
 * and this client never stores them: a secret exists only for the duration
 * of the PUT request.
 */
export class HttpCredentialClient implements CredentialClient {
  constructor(private readonly transport: HttpTransport) {}

  async list(): Promise<CredentialStatus[]> {
    const response = await this.transport.get<CredentialListResponse>('/api/v1/credentials');
    return response.credentials.map(({ provider, configured }) => ({ provider, configured }));
  }

  async set(provider: string, secret: string): Promise<CredentialStatus> {
    const response = await this.transport.put<CredentialUpdateResponse>(credentialPath(provider), { value: secret });
    return { provider: response.credential.provider, configured: response.credential.configured };
  }

  async remove(provider: string): Promise<void> {
    await this.transport.delete(credentialPath(provider));
  }
}

/**
 * The shared ConfigurationClient contract over the server. Only settings the
 * protocol actually stores are implemented. Desktop-only preferences (native
 * folders, device defaults, OS notifications, custom local endpoints) and
 * secret read-back are unsupported, and the capabilities say so.
 */
export class HttpConfigurationClient implements ConfigurationClient {
  readonly capabilities: ConfigurationCapabilities = {
    remoteCredentialStorage: true,
    desktopStorageLocations: false,
    recordingDeviceDefaults: false,
    nativeNotificationPreferences: false,
  };

  private readonly settings: HttpWorkspaceSettingsClient;
  private readonly credentials: HttpCredentialClient;

  constructor(transport: HttpTransport) {
    this.settings = new HttpWorkspaceSettingsClient(transport);
    this.credentials = new HttpCredentialClient(transport);
  }

  async getTranscriptionPreferences(): Promise<TranscriptionPreferences> {
    const { transcription } = await this.settings.get();
    // Server-validated provider identifier; it may name a provider the desktop union does not list.
    return { provider: transcription.provider as TranscriptionPreferences['provider'], model: transcription.model, apiKey: null };
  }

  async saveTranscriptionPreferences(preferences: TranscriptionPreferences): Promise<void> {
    await this.settings.update({ transcription: { provider: preferences.provider, model: preferences.model } });
    if (preferences.apiKey) await this.credentials.set(preferences.provider, preferences.apiKey);
  }

  async getTranscriptionApiKey(_provider: string): Promise<string> {
    throw new UnsupportedOperationError('Reading a stored API key');
  }

  async getTranscriptionLanguage(): Promise<string> {
    return (await this.settings.get()).transcription.language;
  }

  async saveTranscriptionLanguage(language: string): Promise<void> {
    await this.settings.update({ transcription: { language } });
  }

  async getSummaryPreferences(): Promise<SummaryPreferences | null> {
    const { summary } = await this.settings.get();
    return { provider: summary.provider as SummaryPreferences['provider'], model: summary.model, whisperModel: '', apiKey: null };
  }

  async saveSummaryPreferences(preferences: SummaryPreferences): Promise<void> {
    await this.settings.update({ summary: { provider: preferences.provider, model: preferences.model } });
    if (preferences.apiKey) await this.credentials.set(preferences.provider, preferences.apiKey);
  }

  async getSummaryApiKey(_provider: string): Promise<string> {
    throw new UnsupportedOperationError('Reading a stored API key');
  }

  async getCustomOpenAIConfiguration(): Promise<CustomOpenAIConfiguration | null> {
    return null;
  }

  async saveCustomOpenAIConfiguration(_configuration: CustomOpenAIConfiguration): Promise<void> {
    throw new UnsupportedOperationError('Custom OpenAI-compatible endpoints');
  }

  async getAutoSummaryEnabled(): Promise<boolean> {
    return (await this.settings.get()).summary.autoGenerate;
  }

  async setAutoSummaryEnabled(enabled: boolean): Promise<void> {
    await this.settings.update({ summary: { autoGenerate: enabled } });
  }

  async getRecordingPreferences(): Promise<RecordingPreferences> {
    throw new UnsupportedOperationError('Native recording folders and devices');
  }

  async saveRecordingPreferences(_preferences: RecordingPreferences): Promise<void> {
    throw new UnsupportedOperationError('Native recording folders and devices');
  }

  async getDefaultRecordingsFolder(): Promise<string> {
    throw new UnsupportedOperationError('Native recording folders');
  }

  async getRecordingNotificationEnabled(): Promise<boolean> {
    throw new UnsupportedOperationError('Native recording notifications');
  }

  async setRecordingNotificationEnabled(_enabled: boolean): Promise<void> {
    throw new UnsupportedOperationError('Native recording notifications');
  }

  async getNotificationPreferences(): Promise<NotificationPreferences> {
    throw new UnsupportedOperationError('Native notification preferences');
  }

  async saveNotificationPreferences(_preferences: NotificationPreferences): Promise<void> {
    throw new UnsupportedOperationError('Native notification preferences');
  }
}
