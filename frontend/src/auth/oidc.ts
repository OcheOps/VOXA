import { InMemoryWebStorage, UserManager, WebStorageStateStore, type User } from 'oidc-client-ts';
import type { BearerTokenSource, WebAuthConfig } from '@/config/web';

type OidcConfig = Extract<WebAuthConfig, { mode: 'oidc' }>;

export interface SignInResult {
  token: string;
  expiresAt?: number;
  returnTo: string;
}

/** The subset of oidc-client-ts used by Voxa (replaced by doubles in tests). */
export interface UserManagerLike {
  signinRedirect(args: { state?: unknown; nonce?: string }): Promise<void>;
  signinRedirectCallback(url?: string): Promise<User>;
  signoutRedirect(args?: { id_token_hint?: string }): Promise<void>;
  removeUser(): Promise<void>;
}

export interface OidcSession {
  signIn(returnTo: string): Promise<void>;
  completeSignIn(url?: string): Promise<SignInResult>;
  signOut(): Promise<void>;
}

/**
 * Authorization Code + PKCE (S256) with state and nonce, against generic
 * OIDC discovery. Storage:
 *  - user/tokens: in memory only (InMemoryWebStorage), lost on reload;
 *  - transaction state (state, nonce, PKCE verifier, return path):
 *    sessionStorage under "voxa.oidc.", needed only to survive the
 *    redirect, consumed by the callback and expired after 10 minutes.
 * No client secret (public client), no refresh tokens, no silent renew.
 */
export function createUserManager(config: OidcConfig, origin: string, sessionStore: Storage): UserManager {
  return new UserManager({
    authority: config.issuer,
    client_id: config.clientId,
    redirect_uri: config.redirectUri,
    post_logout_redirect_uri: `${origin}/`,
    response_type: 'code',
    response_mode: 'query',
    scope: config.scope,
    userStore: new WebStorageStateStore({ store: new InMemoryWebStorage() }),
    stateStore: new WebStorageStateStore({ prefix: 'voxa.oidc.', store: sessionStore }),
    staleStateAgeInSeconds: 600,
    automaticSilentRenew: false,
    monitorSession: false,
    loadUserInfo: false,
    filterProtocolClaims: true,
  });
}

/** Only same-origin relative paths are accepted as post-login destinations. */
export function safeReturnPath(value: unknown): string {
  if (typeof value !== 'string' || !value.startsWith('/') || value.startsWith('//') || value.includes('\\') || value.startsWith('/auth/callback')) {
    return '/';
  }
  return value;
}

function randomNonce(): string {
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

function bearerFrom(user: User, source: BearerTokenSource): { token: string; expiresAt?: number } {
  if (source === 'id_token') {
    const exp = typeof user.profile?.exp === 'number' ? user.profile.exp : undefined;
    if (!user.id_token) throw new Error('The identity provider returned no ID token');
    return { token: user.id_token, expiresAt: exp };
  }
  if (!user.access_token) throw new Error('The identity provider returned no access token');
  return { token: user.access_token, expiresAt: user.expires_at };
}

export function createOidcSession(manager: UserManagerLike, bearer: BearerTokenSource): OidcSession {
  let idTokenHint: string | undefined;
  return {
    async signIn(returnTo) {
      await manager.signinRedirect({ state: { returnTo: safeReturnPath(returnTo) }, nonce: randomNonce() });
    },
    async completeSignIn(url) {
      const user = await manager.signinRedirectCallback(url);
      // The library keeps the user only in memory; Voxa holds just the
      // bearer and an id_token hint for logout.
      idTokenHint = user.id_token;
      await manager.removeUser();
      const { token, expiresAt } = bearerFrom(user, bearer);
      return { token, expiresAt, returnTo: safeReturnPath((user.state as { returnTo?: unknown } | undefined)?.returnTo) };
    },
    async signOut() {
      const hint = idTokenHint;
      idTokenHint = undefined;
      await manager.signoutRedirect(hint ? { id_token_hint: hint } : undefined);
    },
  };
}
