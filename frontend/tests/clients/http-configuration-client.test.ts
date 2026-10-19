import { describe, expect, test } from 'bun:test';
import { HttpConfigurationClient, HttpCredentialClient, HttpWorkspaceSettingsClient } from '../../src/clients/http/configuration-client';
import { HttpTransport } from '../../src/clients/http/transport';
import { UnsupportedOperationError } from '../../src/clients/errors';
import { API, fakeFetch, json, type RecordedRequest } from './http-fake';

const SECRET = 'sk-CONFIG-CANARY-5678';

function serverConfiguration(overrides: Record<string, unknown> = {}) {
  return {
    workspaceId: 'wsp_1',
    transcription: { provider: 'openai', model: 'whisper-1', language: 'en', translateToEnglish: false },
    summary: { provider: 'claude', model: 'claude-x', outputLanguage: 'auto', templateId: null, autoGenerate: true },
    recording: { retainMedia: true },
    privacy: { analyticsEnabled: false },
    notifications: { processingComplete: true },
    credentials: [{ provider: 'openai', configured: true, maskedValue: '••••' }],
    ...overrides,
  };
}

function setup() {
  let stored = serverConfiguration();
  const credentials = new Map<string, boolean>([['openai', true]]);
  const fake = fakeFetch({
    'GET /api/v1/configuration': () => json(200, stored),
    'PATCH /api/v1/configuration': ({ body }: RecordedRequest) => {
      const patch = body as Record<string, Record<string, unknown>>;
      stored = {
        ...stored,
        transcription: { ...stored.transcription, ...(patch.transcription ?? {}) },
        summary: { ...stored.summary, ...(patch.summary ?? {}) },
        recording: { ...stored.recording, ...(patch.recording ?? {}) },
        privacy: { ...stored.privacy, ...(patch.privacy ?? {}) },
        notifications: { ...stored.notifications, ...(patch.notifications ?? {}) },
      } as typeof stored;
      return json(200, { configuration: stored });
    },
    'GET /api/v1/credentials': () => json(200, { credentials: [...credentials].map(([provider]) => ({ provider, configured: true, maskedValue: '••••' })) }),
    'PUT /api/v1/credentials/anthropic': () => { credentials.set('anthropic', true); return json(200, { credential: { provider: 'anthropic', configured: true, maskedValue: '••••' } }); },
    'PUT /api/v1/credentials/openai': () => json(200, { credential: { provider: 'openai', configured: true, maskedValue: '••••' } }),
    'DELETE /api/v1/credentials/openai': () => { credentials.delete('openai'); return new Response(null, { status: 204 }); },
  });
  const transport = new HttpTransport({ baseUrl: API, getAccessToken: () => 't', fetch: fake.fetch });
  return { fake, transport };
}

describe('HttpWorkspaceSettingsClient', () => {
  test('maps server configuration and sends partial patches', async () => {
    const { fake, transport } = setup();
    const settings = new HttpWorkspaceSettingsClient(transport);
    expect(await settings.get()).toEqual({
      transcription: { provider: 'openai', model: 'whisper-1', language: 'en', translateToEnglish: false },
      summary: { provider: 'claude', model: 'claude-x', outputLanguage: 'auto', templateId: null, autoGenerate: true },
      retainMedia: true, analyticsEnabled: false, processingCompleteNotifications: true,
    });
    const updated = await settings.update({ summary: { outputLanguage: 'fr' }, analyticsEnabled: true, retainMedia: false });
    expect(updated.summary.outputLanguage).toBe('fr');
    expect(updated.analyticsEnabled).toBe(true);
    expect(fake.requests.at(-1)?.body).toEqual({ summary: { outputLanguage: 'fr' }, privacy: { analyticsEnabled: true }, recording: { retainMedia: false } });
  });
});

describe('HttpCredentialClient', () => {
  test('lists metadata, writes and deletes without ever reading secrets', async () => {
    const { fake, transport } = setup();
    const credentials = new HttpCredentialClient(transport);
    expect(await credentials.list()).toEqual([{ provider: 'openai', configured: true }]);
    expect(await credentials.set('anthropic', SECRET)).toEqual({ provider: 'anthropic', configured: true });
    await credentials.remove('openai');
    expect(await credentials.list()).toEqual([{ provider: 'anthropic', configured: true }]);
    const put = fake.requests.find((r) => r.method === 'PUT');
    expect(put?.body).toEqual({ value: SECRET });
    // Only the PUT body ever carries the secret; listings never contain it.
    expect(fake.requests.filter((r) => JSON.stringify(r.body ?? '').includes(SECRET))).toHaveLength(1);
    expect(JSON.stringify(await credentials.list())).not.toContain('maskedValue');
  });
});

describe('HttpConfigurationClient', () => {
  test('reports web capabilities truthfully', () => {
    const { transport } = setup();
    expect(new HttpConfigurationClient(transport).capabilities).toEqual({
      remoteCredentialStorage: true, desktopStorageLocations: false, recordingDeviceDefaults: false, nativeNotificationPreferences: false,
    });
  });

  test('implements server-backed preferences', async () => {
    const { transport, fake } = setup();
    const configuration = new HttpConfigurationClient(transport);
    expect(await configuration.getTranscriptionPreferences()).toEqual({ provider: 'openai', model: 'whisper-1', apiKey: null } as never);
    expect(await configuration.getTranscriptionLanguage()).toBe('en');
    await configuration.saveTranscriptionLanguage('de');
    expect(await configuration.getTranscriptionLanguage()).toBe('de');
    expect(await configuration.getAutoSummaryEnabled()).toBe(true);
    await configuration.setAutoSummaryEnabled(false);
    expect(await configuration.getAutoSummaryEnabled()).toBe(false);
    expect((await configuration.getSummaryPreferences())?.apiKey).toBeNull();
    await configuration.saveSummaryPreferences({ provider: 'openai', model: 'gpt', whisperModel: '', apiKey: SECRET });
    expect(fake.requests.some((r) => r.method === 'PUT' && r.url.pathname === '/api/v1/credentials/openai')).toBe(true);
    expect(await configuration.getCustomOpenAIConfiguration()).toBeNull();
  });

  test('desktop-only operations and secret read-back are unsupported', async () => {
    const { transport, fake } = setup();
    const configuration = new HttpConfigurationClient(transport);
    for (const call of [
      () => configuration.getTranscriptionApiKey('openai'),
      () => configuration.getSummaryApiKey('openai'),
      () => configuration.getRecordingPreferences(),
      () => configuration.saveRecordingPreferences({} as never),
      () => configuration.getDefaultRecordingsFolder(),
      () => configuration.getRecordingNotificationEnabled(),
      () => configuration.setRecordingNotificationEnabled(true),
      () => configuration.getNotificationPreferences(),
      () => configuration.saveNotificationPreferences({} as never),
      () => configuration.saveCustomOpenAIConfiguration({} as never),
    ]) {
      await expect(call()).rejects.toBeInstanceOf(UnsupportedOperationError);
    }
    expect(fake.requests).toHaveLength(0);
  });
});
