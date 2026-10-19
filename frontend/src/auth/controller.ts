import { VoxaApiError } from '@/clients/http/transport';
import type { CurrentIdentity } from '@/clients/http/identity-client';
import type { InMemoryTokenStore } from './token-store';
import type { OidcSession } from './oidc';

export type AuthState =
  | { kind: 'initializing' }
  | { kind: 'signed-out'; reason: 'none' | 'expired' | 'signed-out' | 'rejected' }
  | { kind: 'redirecting' }
  | { kind: 'completing' }
  | { kind: 'loading-identity' }
  | { kind: 'ready'; identity: CurrentIdentity }
  | { kind: 'forbidden' }
  | { kind: 'workspace-selection-required' }
  | { kind: 'error'; message: string; retry: 'identity' | 'sign-in' };

/** Non-secret marker meaning "this tab was signed in", used to re-authenticate after a reload. */
export const SIGNED_IN_MARKER = 'voxa.web.signedIn';

export interface AuthControllerOptions {
  mode: 'oidc' | 'development';
  oidc?: OidcSession;
  tokens: InMemoryTokenStore;
  identity: { me(signal?: AbortSignal): Promise<CurrentIdentity> };
  session: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;
  /** Current path + query, used as the post-login destination. */
  currentPath: () => string;
  /** Replace the current URL (removes the authorization code from history). */
  replacePath: (path: string) => void;
}

/**
 * Web authentication state machine, independent of React:
 *
 *   signed-out --signIn--> redirecting --(provider)--> completing
 *     --> loading-identity --GET /me--> ready | forbidden | workspace-selection-required | error
 *
 * Any 401 from the API (expired or revoked token) returns to signed-out.
 */
export class AuthController {
  private state: AuthState = { kind: 'initializing' };
  private readonly listeners = new Set<() => void>();

  constructor(private readonly options: AuthControllerOptions) {}

  getState = (): AuthState => this.state;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  private set(state: AuthState): void {
    this.state = state;
    this.listeners.forEach((listener) => listener());
  }

  /** Called once on page load (not on the callback route). */
  async start(): Promise<void> {
    if (this.options.mode === 'development') {
      await this.loadIdentity();
      return;
    }
    if (this.options.session.getItem(SIGNED_IN_MARKER) === '1') {
      // The tab was signed in before a reload; tokens lived only in memory,
      // so start a new authorization (usually instant with an IdP session).
      await this.signIn();
      return;
    }
    this.set({ kind: 'signed-out', reason: 'none' });
  }

  async signIn(): Promise<void> {
    if (!this.options.oidc) return;
    this.set({ kind: 'redirecting' });
    try {
      await this.options.oidc.signIn(this.options.currentPath());
    } catch {
      this.options.session.removeItem(SIGNED_IN_MARKER);
      this.set({ kind: 'error', message: 'Could not reach the identity provider.', retry: 'sign-in' });
    }
  }

  /** Called on the redirect callback route. */
  async completeSignIn(url?: string): Promise<void> {
    if (!this.options.oidc) return;
    this.set({ kind: 'completing' });
    let returnTo = '/';
    try {
      const result = await this.options.oidc.completeSignIn(url);
      this.options.tokens.set(result.token, result.expiresAt);
      this.options.session.setItem(SIGNED_IN_MARKER, '1');
      returnTo = result.returnTo;
    } catch {
      this.options.tokens.clear();
      this.options.session.removeItem(SIGNED_IN_MARKER);
      this.options.replacePath('/');
      this.set({ kind: 'error', message: 'Sign-in could not be completed.', retry: 'sign-in' });
      return;
    }
    this.options.replacePath(returnTo);
    await this.loadIdentity();
  }

  async loadIdentity(): Promise<void> {
    this.set({ kind: 'loading-identity' });
    try {
      this.set({ kind: 'ready', identity: await this.options.identity.me() });
    } catch (error) {
      if (error instanceof VoxaApiError && error.status === 401) {
        // A freshly issued token was rejected: do not auto-retry on reload.
        this.options.tokens.clear();
        this.options.session.removeItem(SIGNED_IN_MARKER);
        this.set({ kind: 'signed-out', reason: 'rejected' });
      } else if (error instanceof VoxaApiError && error.status === 403) {
        this.set({ kind: 'forbidden' });
      } else if (error instanceof VoxaApiError && error.status === 409 && error.details?.reason === 'workspace_selection_required') {
        this.set({ kind: 'workspace-selection-required' });
      } else {
        this.set({ kind: 'error', message: 'The Voxa server could not be reached.', retry: 'identity' });
      }
    }
  }

  /** Transport hook for 401 responses after sign-in (expired/revoked token). */
  handleUnauthorized = (): void => {
    if (this.options.mode === 'development') return;
    this.options.tokens.clear();
    if (this.state.kind === 'ready') this.set({ kind: 'signed-out', reason: 'expired' });
  };

  async signOut(): Promise<void> {
    this.options.tokens.clear();
    this.options.session.removeItem(SIGNED_IN_MARKER);
    this.set({ kind: 'signed-out', reason: 'signed-out' });
    try {
      await this.options.oidc?.signOut();
    } catch {
      // Providers without end-session support: the local sign-out stands.
    }
  }
}
