import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import type { Subprocess } from 'bun';
import { createWebRuntime, type WebRuntime } from '../../src/web/runtime';

/*
 * Full-stack OIDC: the real Voxa Go server (VOXA_AUTH_MODE=oidc) trusts a
 * local issuer served by this test; the web runtime signs in with
 * Authorization Code + PKCE, calls /me (first-user bootstrap) and uses the
 * bearer token for meeting CRUD. No browser and no external provider.
 *
 *   VOXA_E2E_SERVER_BIN=/path/to/voxa \
 *   VOXA_E2E_DATABASE_URL=postgres://... (empty database or fresh schema) \
 *   bun test tests/integration/oidc-e2e.integration.test.ts
 */
const SERVER_BIN = process.env.VOXA_E2E_SERVER_BIN;
const DATABASE_URL = process.env.VOXA_E2E_DATABASE_URL;
const ORIGIN = 'http://localhost:3118';
const AUDIENCE = 'voxa-web';

let issuerServer: ReturnType<typeof Bun.serve>;
let issuer = '';
let apiUrl = '';
let voxa: Subprocess | undefined;
let keyPair: CryptoKeyPair;
let publicJwk: JsonWebKey;
const nextSubject = 'alice';

const b64 = (data: ArrayBuffer | string) => Buffer.from(typeof data === 'string' ? data : new Uint8Array(data)).toString('base64url');

async function sign(claims: Record<string, unknown>): Promise<string> {
  const input = `${b64(JSON.stringify({ alg: 'RS256', typ: 'JWT', kid: 'e2e-key' }))}.${b64(JSON.stringify(claims))}`;
  const signature = await crypto.subtle.sign('RSASSA-PKCS1-v1_5', keyPair.privateKey, new TextEncoder().encode(input));
  return `${input}.${b64(signature)}`;
}

async function waitFor(url: string, timeoutMs = 20_000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      if ((await fetch(url)).ok) return;
    } catch { /* not up yet */ }
    await Bun.sleep(200);
  }
  throw new Error(`timed out waiting for ${url}`);
}

const sessionValues = new Map<string, string>();
const sessionStorage = {
  get length() { return sessionValues.size; },
  key: (i: number) => [...sessionValues.keys()][i] ?? null,
  getItem: (k: string) => sessionValues.get(k) ?? null,
  setItem: (k: string, v: string) => void sessionValues.set(k, v),
  removeItem: (k: string) => void sessionValues.delete(k),
  clear: () => sessionValues.clear(),
} as Storage;
let redirectedTo = '';
const previousWindow = Object.getOwnPropertyDescriptor(globalThis, 'window');

describe.skipIf(!SERVER_BIN || !DATABASE_URL)('Voxa Web OIDC against the real server', () => {
  beforeAll(async () => {
    keyPair = await crypto.subtle.generateKey(
      { name: 'RSASSA-PKCS1-v1_5', modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: 'SHA-256' },
      true, ['sign', 'verify'],
    ) as CryptoKeyPair;
    publicJwk = await crypto.subtle.exportKey('jwk', keyPair.publicKey);

    issuerServer = Bun.serve({
      port: 0,
      async fetch(request) {
        const url = new URL(request.url);
        if (url.pathname === '/.well-known/openid-configuration') {
          return Response.json({
            issuer, authorization_endpoint: `${issuer}/authorize`, token_endpoint: `${issuer}/token`, jwks_uri: `${issuer}/jwks`,
            response_types_supported: ['code'], subject_types_supported: ['public'], id_token_signing_alg_values_supported: ['RS256'],
            code_challenge_methods_supported: ['S256'],
          });
        }
        if (url.pathname === '/jwks') return Response.json({ keys: [{ ...publicJwk, kid: 'e2e-key', alg: 'RS256', use: 'sig' }] });
        if (url.pathname === '/token') {
          const form = new URLSearchParams(await request.text());
          if (!form.get('code_verifier')) return Response.json({ error: 'invalid_request' }, { status: 400 });
          // The test's "authorize" step encodes subject and nonce into the code.
          const { subject, nonce } = JSON.parse(Buffer.from(form.get('code')!, 'base64url').toString());
          const now = Math.floor(Date.now() / 1000);
          const base = { iss: issuer, sub: subject, aud: AUDIENCE, iat: now, exp: now + 600 };
          return Response.json({
            token_type: 'Bearer', expires_in: 600,
            access_token: await sign({ ...base, email: `${subject}@example.test` }),
            id_token: await sign({ ...base, nonce }),
          });
        }
        return new Response('not found', { status: 404 });
      },
    });
    issuer = `http://127.0.0.1:${issuerServer.port}`;

    const port = 20000 + Math.floor(Math.random() * 20000);
    apiUrl = `http://127.0.0.1:${port}`;
    const key = Buffer.from(crypto.getRandomValues(new Uint8Array(32))).toString('base64');
    voxa = Bun.spawn([SERVER_BIN!], {
      env: {
        PATH: process.env.PATH ?? '', VOXA_AUTH_MODE: 'oidc', VOXA_OIDC_ISSUER: issuer, VOXA_OIDC_AUDIENCE: AUDIENCE,
        VOXA_DATABASE_URL: DATABASE_URL!, VOXA_CREDENTIAL_KEY: key, VOXA_CREDENTIAL_KEY_VERSION: 'e2e',
        VOXA_LISTEN_ADDR: `127.0.0.1:${port}`, VOXA_ALLOWED_ORIGINS: ORIGIN,
      },
      stdout: 'ignore', stderr: 'ignore',
    });
    await waitFor(`${apiUrl}/readyz`);

    // Minimal browser window: oidc-client-ts navigates via window.self.location.
    const fakeWindow: Record<string, unknown> = {
      location: { assign: (url: string) => { redirectedTo = url; }, replace: (url: string) => { redirectedTo = url; } },
      sessionStorage,
      addEventListener: () => {},
      removeEventListener: () => {},
    };
    fakeWindow.self = fakeWindow;
    fakeWindow.top = fakeWindow;
    Object.defineProperty(globalThis, 'window', { configurable: true, writable: true, value: fakeWindow });
  }, 30_000);

  afterAll(() => {
    voxa?.kill();
    issuerServer?.stop(true);
    if (previousWindow) Object.defineProperty(globalThis, 'window', previousWindow);
    else delete (globalThis as Record<string, unknown>).window;
  });

  function runtime(replaced: string[]): WebRuntime {
    return createWebRuntime({
      config: { apiBaseUrl: apiUrl, auth: { mode: 'oidc', issuer, clientId: AUDIENCE, scope: 'openid profile email', redirectUri: `${ORIGIN}/auth/callback`, bearer: 'access_token' } },
      origin: ORIGIN,
      sessionStorage,
      currentPath: () => '/meeting-details?id=abc',
      replacePath: (path) => void replaced.push(path),
    });
  }

  /**
   * In a browser the redirect promise never settles (the page unloads), so
   * the test starts the redirect without awaiting it and waits for navigation.
   */
  async function redirected(start: () => Promise<void>): Promise<void> {
    redirectedTo = '';
    void start();
    for (let i = 0; i < 100 && !redirectedTo; i++) await Bun.sleep(20);
    expect(redirectedTo.startsWith(`${issuer}/authorize?`)).toBe(true);
  }

  /** Plays the identity provider: approves the pending authorization request. */
  function approve(subject: string): string {
    const authorize = new URL(redirectedTo);
    expect(authorize.searchParams.get('code_challenge_method')).toBe('S256');
    const code = Buffer.from(JSON.stringify({ subject, nonce: authorize.searchParams.get('nonce') })).toString('base64url');
    return `${ORIGIN}/auth/callback?code=${code}&state=${authorize.searchParams.get('state')}`;
  }

  test('first sign-in bootstraps the owner and the bearer authorizes meeting CRUD', async () => {
    const replaced: string[] = [];
    const web = runtime(replaced);
    await web.auth.start();
    expect(web.auth.getState().kind).toBe('signed-out');

    await redirected(() => web.auth.signIn());
    expect(web.auth.getState().kind).toBe('redirecting');
    await web.auth.completeSignIn(approve(nextSubject));

    const state = web.auth.getState();
    expect(state.kind).toBe('ready');
    if (state.kind !== 'ready') return;
    expect(state.identity.role).toBe('owner');
    expect(state.identity.user.email).toBe('alice@example.test');
    expect(replaced).toEqual(['/meeting-details?id=abc']);

    const { meeting_id } = await web.client.meetings.create({ title: 'E2E planning', transcripts: [], folderPath: null });
    await web.client.meetings.rename(meeting_id, 'E2E renamed');
    expect((await web.client.meetings.list()).find((m) => m.id === meeting_id)?.title).toBe('E2E renamed');

    // A "page reload": new runtime, tokens gone; the tab marker re-authenticates.
    const reloaded = runtime([]);
    await redirected(() => reloaded.auth.start());
    expect(reloaded.auth.getState().kind).toBe('redirecting');
    await reloaded.auth.completeSignIn(approve('alice'));
    expect((await reloaded.client.meetings.list()).some((m) => m.id === meeting_id)).toBe(true);
    await reloaded.client.meetings.delete(meeting_id);
    expect((await reloaded.client.meetings.list()).some((m) => m.id === meeting_id)).toBe(false);
  }, 30_000);

  test('a second identity is authenticated but forbidden', async () => {
    sessionValues.clear();
    const web = runtime([]);
    await redirected(() => web.auth.signIn());
    await web.auth.completeSignIn(approve('mallory'));
    expect(web.auth.getState().kind).toBe('forbidden');
  }, 30_000);

  test('requests without a valid token are rejected by the server', async () => {
    const response = await fetch(`${apiUrl}/api/v1/meetings`, { headers: { Authorization: 'Bearer not-a-token', Origin: ORIGIN } });
    expect(response.status).toBe(401);
    expect(response.headers.get('access-control-allow-origin')).toBe(ORIGIN);
  });
});
