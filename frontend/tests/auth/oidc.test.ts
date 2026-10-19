import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { OidcClient } from 'oidc-client-ts';
import { createOidcSession, createUserManager, safeReturnPath } from '../../src/auth/oidc';

// A local OIDC provider: discovery + token endpoint. No external network.
let server: ReturnType<typeof Bun.serve>;
let issuer = '';
const tokenRequests: URLSearchParams[] = [];

function unsignedJwt(claims: Record<string, unknown>): string {
  const encode = (value: unknown) => Buffer.from(JSON.stringify(value)).toString('base64url');
  return `${encode({ alg: 'RS256', typ: 'JWT' })}.${encode(claims)}.signature`;
}

beforeAll(() => {
  server = Bun.serve({
    port: 0,
    async fetch(request) {
      const url = new URL(request.url);
      if (url.pathname === '/.well-known/openid-configuration') {
        return Response.json({
          issuer, authorization_endpoint: `${issuer}/authorize`, token_endpoint: `${issuer}/token`,
          jwks_uri: `${issuer}/jwks`, end_session_endpoint: `${issuer}/logout`,
          response_types_supported: ['code'], code_challenge_methods_supported: ['S256'],
        });
      }
      if (url.pathname === '/token') {
        const form = new URLSearchParams(await request.text());
        tokenRequests.push(form);
        const nonce = form.get('test_nonce');
        const now = Math.floor(Date.now() / 1000);
        return Response.json({
          access_token: 'access-token-value', token_type: 'Bearer', expires_in: 300,
          id_token: unsignedJwt({ iss: issuer, sub: 'subject-1', aud: 'voxa-web', iat: now, exp: now + 300, nonce: pendingNonce ?? nonce }),
        });
      }
      return new Response('not found', { status: 404 });
    },
  });
  issuer = `http://127.0.0.1:${server.port}`;
});
afterAll(() => server.stop(true));

let pendingNonce: string | undefined;

function storage() {
  const values = new Map<string, string>();
  return {
    values,
    get length() { return values.size; },
    key: (i: number) => [...values.keys()][i] ?? null,
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => void values.set(key, value),
    removeItem: (key: string) => void values.delete(key),
    clear: () => values.clear(),
  } as Storage & { values: Map<string, string> };
}

const config = (bearer: 'access_token' | 'id_token' = 'access_token') => ({
  mode: 'oidc' as const, issuer, clientId: 'voxa-web', scope: 'openid profile email',
  redirectUri: 'http://localhost:3118/auth/callback', bearer,
});

describe('OIDC authorization code + PKCE', () => {
  test('signs in with S256 PKCE, state and nonce, keeping only transaction state in sessionStorage', async () => {
    const session = storage();
    const manager = createUserManager(config(), 'http://localhost:3118', session);
    expect(manager.settings.client_secret).toBeUndefined();
    expect(manager.settings.disablePKCE).toBe(false);

    // Build the authorization request exactly as signinRedirect would (without navigating).
    const client = new OidcClient(manager.settings);
    const request = await client.createSigninRequest({ state: { returnTo: '/settings' }, nonce: 'n-123', request_type: 'si:r' });
    const authorize = new URL(request.url);
    expect(authorize.origin + authorize.pathname).toBe(`${issuer}/authorize`);
    expect(authorize.searchParams.get('response_type')).toBe('code');
    expect(authorize.searchParams.get('code_challenge_method')).toBe('S256');
    expect(authorize.searchParams.get('code_challenge')?.length).toBeGreaterThan(40);
    expect(authorize.searchParams.get('nonce')).toBe('n-123');
    expect(authorize.searchParams.get('client_secret')).toBeNull();
    const state = authorize.searchParams.get('state')!;
    expect(state.length).toBeGreaterThan(10);
    const stored = [...session.values.entries()];
    expect(stored).toHaveLength(1);
    expect(stored[0][0].startsWith('voxa.oidc.')).toBe(true);
    expect(stored[0][1]).toContain('code_verifier');

    // Callback: the code is exchanged with the stored verifier and nonce checked.
    pendingNonce = 'n-123';
    const voxa = createOidcSession(manager, 'access_token');
    const result = await voxa.completeSignIn(`http://localhost:3118/auth/callback?code=auth-code&state=${state}`);
    expect(result.token).toBe('access-token-value');
    expect(result.returnTo).toBe('/settings');
    expect(result.expiresAt).toBeGreaterThan(Date.now() / 1000);
    const exchange = tokenRequests.at(-1)!;
    expect(exchange.get('grant_type')).toBe('authorization_code');
    expect(exchange.get('code_verifier')?.length).toBeGreaterThan(40);
    expect(exchange.get('client_secret')).toBeNull();
    // The transaction state was consumed; no token was persisted anywhere.
    expect(session.values.size).toBe(0);
  });

  test('rejects a callback whose state was never issued', async () => {
    const manager = createUserManager(config(), 'http://localhost:3118', storage());
    await expect(createOidcSession(manager, 'access_token').completeSignIn('http://localhost:3118/auth/callback?code=x&state=forged')).rejects.toThrow();
  });

  test('rejects an ID token whose nonce does not match', async () => {
    const session = storage();
    const manager = createUserManager(config(), 'http://localhost:3118', session);
    const request = await new OidcClient(manager.settings).createSigninRequest({ state: {}, nonce: 'expected', request_type: 'si:r' });
    const state = new URL(request.url).searchParams.get('state');
    pendingNonce = 'attacker-nonce';
    await expect(createOidcSession(manager, 'access_token').completeSignIn(`http://localhost:3118/auth/callback?code=c&state=${state}`)).rejects.toThrow();
  });

  test('uses the ID token as bearer when configured', async () => {
    const session = storage();
    const manager = createUserManager(config('id_token'), 'http://localhost:3118', session);
    const request = await new OidcClient(manager.settings).createSigninRequest({ state: {}, nonce: 'n-2', request_type: 'si:r' });
    pendingNonce = 'n-2';
    const result = await createOidcSession(manager, 'id_token').completeSignIn(`http://localhost:3118/auth/callback?code=c&state=${new URL(request.url).searchParams.get('state')}`);
    expect(result.token.split('.')).toHaveLength(3);
    expect(result.returnTo).toBe('/');
  });

  test('signIn sends a sanitized return path and a fresh nonce each time', async () => {
    const seen: Array<{ state?: unknown; nonce?: string }> = [];
    const session = createOidcSession({
      signinRedirect: async (args) => void seen.push(args),
      signinRedirectCallback: async () => { throw new Error('unused'); },
      signoutRedirect: async () => {},
      removeUser: async () => {},
    }, 'access_token');
    await session.signIn('//evil.example/steal');
    await session.signIn('/settings');
    expect(seen[0].state).toEqual({ returnTo: '/' });
    expect(seen[1].state).toEqual({ returnTo: '/settings' });
    expect(seen[0].nonce).toMatch(/^[0-9a-f]{64}$/);
    expect(seen[0].nonce).not.toBe(seen[1].nonce);
  });

  test('return paths must be same-origin', () => {
    for (const bad of ['https://evil.example', '//evil.example', '/\\evil', 'settings', '/auth/callback?code=x', 42, null]) {
      expect(safeReturnPath(bad)).toBe('/');
    }
    expect(safeReturnPath('/meeting-details?id=m1')).toBe('/meeting-details?id=m1');
  });
});
