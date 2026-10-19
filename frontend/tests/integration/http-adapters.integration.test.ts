import { describe, expect, test } from 'bun:test';
import { HttpConfigurationClient, HttpCredentialClient, HttpWorkspaceSettingsClient } from '../../src/clients/http/configuration-client';
import { HttpIdentityClient } from '../../src/clients/http/identity-client';
import { HttpMeetingClient } from '../../src/clients/http/meeting-client';
import { HttpTransport, VoxaApiError } from '../../src/clients/http/transport';
import { UnsupportedOperationError } from '../../src/clients/errors';

/*
 * Runs the web HTTP adapters against a real Voxa Go server. Start one in
 * development auth mode and point VOXA_INTEGRATION_API_URL at it:
 *
 *   VOXA_AUTH_MODE=development VOXA_LISTEN_ADDR=127.0.0.1:18080 \
 *   VOXA_ALLOWED_ORIGINS=http://localhost:3118 go run ./cmd/voxa
 *   VOXA_INTEGRATION_API_URL=http://127.0.0.1:18080 bun test tests/integration
 */
const API = process.env.VOXA_INTEGRATION_API_URL;
const ORIGIN = process.env.VOXA_INTEGRATION_ORIGIN ?? 'http://localhost:3118';
const SECRET = `sk-INTEGRATION-${crypto.randomUUID()}`;

describe.skipIf(!API)('HTTP adapters against the Voxa server', () => {
  const transport = new HttpTransport({ baseUrl: API ?? '', getAccessToken: () => null });
  const meetings = new HttpMeetingClient(transport);
  const settings = new HttpWorkspaceSettingsClient(transport);
  const credentials = new HttpCredentialClient(transport);
  const identity = new HttpIdentityClient(transport);

  test('identity and capabilities', async () => {
    const me = await identity.me();
    expect(me.role).toBe('owner');
    expect(me.workspace.id.length).toBeGreaterThan(0);
    const capabilities = await identity.capabilities();
    // Media storage depends on VOXA_MEDIA_DIR; processing jobs do not exist yet.
    expect(typeof capabilities.mediaStorage.available).toBe('boolean');
    expect(capabilities.transcription.available).toBe(false);
    expect(capabilities.summary.available).toBe(false);
  });

  test('meeting CRUD and cursor pagination', async () => {
    const prefix = `itest-${Date.now()}`;
    const created: string[] = [];
    for (let i = 0; i < 103; i++) {
      created.push((await meetings.create({ title: `${prefix}-${i}`, transcripts: [], folderPath: null })).meeting_id);
    }
    const listed = await meetings.list();
    const ours = listed.filter((m) => m.title.startsWith(prefix));
    expect(ours.map((m) => m.id)).toEqual(created);
    expect(new Set(listed.map((m) => m.id)).size).toBe(listed.length);

    await meetings.rename(created[0], `${prefix}-renamed`);
    const metadata = await meetings.getMetadata(created[0]);
    expect(metadata.title).toBe(`${prefix}-renamed`);
    expect(new Date(metadata.updated_at).getTime()).toBeGreaterThanOrEqual(new Date(metadata.created_at).getTime());

    for (const id of created) await meetings.delete(id);
    const missing = await meetings.getMetadata(created[0]).then(() => null, (e: unknown) => e);
    expect(missing).toBeInstanceOf(VoxaApiError);
    expect((missing as VoxaApiError).status).toBe(404);
  });

  test('server validation errors are decoded', async () => {
    const error = await meetings.create({ title: '   ', transcripts: [], folderPath: null }).then(() => null, (e: unknown) => e);
    expect(error).toBeInstanceOf(VoxaApiError);
    expect((error as VoxaApiError).code).toBe('validation');
    expect((error as VoxaApiError).requestId).toBeTruthy();
  });

  test('workspace settings round trip with partial patches', async () => {
    const before = await settings.get();
    const updated = await settings.update({ summary: { outputLanguage: 'fr' }, analyticsEnabled: !before.analyticsEnabled });
    expect(updated.summary.outputLanguage).toBe('fr');
    expect(updated.summary.provider).toBe(before.summary.provider);
    expect(updated.analyticsEnabled).toBe(!before.analyticsEnabled);
    await settings.update({ summary: { outputLanguage: before.summary.outputLanguage }, analyticsEnabled: before.analyticsEnabled });
    const configuration = new HttpConfigurationClient(transport);
    await expect(configuration.getRecordingPreferences()).rejects.toBeInstanceOf(UnsupportedOperationError);
  });

  test('credentials are write-only', async () => {
    await credentials.set('integration-provider', SECRET);
    const listed = await credentials.list();
    expect(listed).toContainEqual({ provider: 'integration-provider', configured: true });
    const raw = await fetch(`${API}/api/v1/credentials`).then((r) => r.text());
    expect(raw).not.toContain(SECRET);
    await credentials.remove('integration-provider');
    expect((await credentials.list()).some((c) => c.provider === 'integration-provider')).toBe(false);
  });

  test('CORS allows the configured web origin and no other', async () => {
    const preflight = (origin: string) => fetch(`${API}/api/v1/meetings`, {
      method: 'OPTIONS',
      headers: { Origin: origin, 'Access-Control-Request-Method': 'POST', 'Access-Control-Request-Headers': 'authorization,content-type' },
    });
    const allowed = await preflight(ORIGIN);
    expect(allowed.status).toBe(204);
    expect(allowed.headers.get('access-control-allow-origin')).toBe(ORIGIN);
    const denied = await preflight('https://evil.example');
    expect(denied.status).toBe(403);
    expect(denied.headers.get('access-control-allow-origin')).toBeNull();
  });
});
