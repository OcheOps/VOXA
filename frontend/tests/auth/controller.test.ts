import { describe, expect, test } from 'bun:test';
import { AuthController, SIGNED_IN_MARKER, type AuthControllerOptions } from '../../src/auth/controller';
import { InMemoryTokenStore } from '../../src/auth/token-store';
import { VoxaApiError, VoxaTransportError } from '../../src/clients/http/transport';
import type { OidcSession } from '../../src/auth/oidc';

const identity = {
  user: { id: 'usr_1', email: null, displayName: 'Ada' },
  workspace: { id: 'wsp_1', name: 'Default workspace' },
  role: 'owner',
} as const;

function memoryStorage() {
  const values = new Map<string, string>();
  return {
    values,
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => void values.set(key, value),
    removeItem: (key: string) => void values.delete(key),
  };
}

function setup(overrides: Partial<AuthControllerOptions> & { me?: () => Promise<unknown>; oidcResult?: () => Promise<{ token: string; expiresAt?: number; returnTo: string }> } = {}) {
  const calls: string[] = [];
  const replaced: string[] = [];
  const session = memoryStorage();
  const tokens = new InMemoryTokenStore();
  const oidc: OidcSession = {
    signIn: async (returnTo) => void calls.push(`signIn:${returnTo}`),
    completeSignIn: overrides.oidcResult ?? (async () => ({ token: 'tok', expiresAt: Math.floor(Date.now() / 1000) + 3600, returnTo: '/settings' })),
    signOut: async () => void calls.push('signOut'),
  };
  const controller = new AuthController({
    mode: 'oidc',
    oidc,
    tokens,
    identity: { me: (overrides.me ?? (async () => identity)) as never },
    session,
    currentPath: () => '/meeting-details?id=m1',
    replacePath: (path) => void replaced.push(path),
    ...overrides,
  });
  return { controller, calls, replaced, session, tokens };
}

const apiError = (status: number, code: string, details: Record<string, unknown> | null = null) =>
  new VoxaApiError(status, code as never, 'msg', 'req', details);

describe('AuthController', () => {
  test('starts signed out without a prior tab session', async () => {
    const { controller, calls } = setup();
    await controller.start();
    expect(controller.getState()).toEqual({ kind: 'signed-out', reason: 'none' });
    expect(calls).toEqual([]);
  });

  test('re-authenticates after a reload when the tab was signed in', async () => {
    const { controller, calls, session } = setup();
    session.setItem(SIGNED_IN_MARKER, '1');
    await controller.start();
    expect(controller.getState().kind).toBe('redirecting');
    expect(calls).toEqual(['signIn:/meeting-details?id=m1']);
  });

  test('callback stores the token in memory, cleans the URL and loads /me', async () => {
    const { controller, replaced, session, tokens } = setup();
    await controller.completeSignIn('https://app/auth/callback?code=c&state=s');
    expect(tokens.get()).toBe('tok');
    expect(session.getItem(SIGNED_IN_MARKER)).toBe('1');
    expect(replaced).toEqual(['/settings']);
    expect(controller.getState()).toEqual({ kind: 'ready', identity } as never);
    // Only the non-secret marker is in session storage.
    expect([...session.values.values()].join()).not.toContain('tok');
  });

  test('failed callback clears state and does not loop', async () => {
    const { controller, replaced, session, tokens } = setup({ oidcResult: async () => { throw new Error('state mismatch'); } });
    session.setItem(SIGNED_IN_MARKER, '1');
    await controller.completeSignIn();
    expect(controller.getState()).toMatchObject({ kind: 'error', retry: 'sign-in' });
    expect(session.getItem(SIGNED_IN_MARKER)).toBeNull();
    expect(tokens.get()).toBeNull();
    expect(replaced).toEqual(['/']);
  });

  test('maps /me outcomes', async () => {
    const cases: Array<[unknown, string]> = [
      [apiError(403, 'forbidden'), 'forbidden'],
      [apiError(409, 'conflict', { reason: 'workspace_selection_required' }), 'workspace-selection-required'],
      [apiError(409, 'conflict'), 'error'],
      [apiError(503, 'unavailable'), 'error'],
      [new VoxaTransportError('network', 'offline'), 'error'],
    ];
    for (const [error, kind] of cases) {
      const { controller } = setup({ me: async () => { throw error; } });
      await controller.completeSignIn();
      expect(controller.getState().kind).toBe(kind as never);
    }
  });

  test('a token rejected by the API signs out without auto-retry', async () => {
    const { controller, session, tokens } = setup({ me: async () => { throw apiError(401, 'unauthorized'); } });
    await controller.completeSignIn();
    expect(controller.getState()).toEqual({ kind: 'signed-out', reason: 'rejected' });
    expect(session.getItem(SIGNED_IN_MARKER)).toBeNull();
    expect(tokens.get()).toBeNull();
  });

  test('401 after sign-in expires the session', async () => {
    const { controller, tokens } = setup();
    await controller.completeSignIn();
    controller.handleUnauthorized();
    expect(controller.getState()).toEqual({ kind: 'signed-out', reason: 'expired' });
    expect(tokens.get()).toBeNull();
  });

  test('sign-out clears everything and asks the provider', async () => {
    const { controller, calls, session, tokens } = setup();
    await controller.completeSignIn();
    await controller.signOut();
    expect(tokens.get()).toBeNull();
    expect(session.getItem(SIGNED_IN_MARKER)).toBeNull();
    expect(calls).toContain('signOut');
    expect(controller.getState()).toEqual({ kind: 'signed-out', reason: 'signed-out' });
  });

  test('development mode needs no provider', async () => {
    const { controller } = setup({ mode: 'development', oidc: undefined });
    await controller.start();
    expect(controller.getState().kind).toBe('ready');
    controller.handleUnauthorized();
    expect(controller.getState().kind).toBe('ready');
  });
});

describe('InMemoryTokenStore', () => {
  test('never returns an expired token', () => {
    let now = 1_000_000;
    const store = new InMemoryTokenStore(() => now);
    store.set('t', 1_000 + 60);
    expect(store.get()).toBe('t');
    now = (1_000 + 60) * 1000;
    expect(store.get()).toBeNull();
  });
});
