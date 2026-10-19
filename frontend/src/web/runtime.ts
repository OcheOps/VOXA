import { AuthController } from '@/auth/controller';
import { createOidcSession, createUserManager } from '@/auth/oidc';
import { InMemoryTokenStore } from '@/auth/token-store';
import type { VoxaClient } from '@/clients/contracts';
import { HttpIdentityClient } from '@/clients/http/identity-client';
import { HttpTransport, type FetchLike } from '@/clients/http/transport';
import { createWebVoxaClient } from '@/clients/web';
import type { WebConfig } from '@/config/web';

export interface WebRuntime {
  config: WebConfig;
  client: VoxaClient;
  identity: HttpIdentityClient;
  auth: AuthController;
}

export interface WebRuntimeDependencies {
  config: WebConfig;
  origin: string;
  sessionStorage: Storage;
  currentPath: () => string;
  replacePath: (path: string) => void;
  fetch?: FetchLike;
}

/**
 * Wires the browser runtime: one in-memory token store feeds the HTTP
 * transport; any 401 is routed back to the auth controller.
 */
export function createWebRuntime(dependencies: WebRuntimeDependencies): WebRuntime {
  const { config } = dependencies;
  const tokens = new InMemoryTokenStore();
  let auth: AuthController | null = null;
  const transport = new HttpTransport({
    baseUrl: config.apiBaseUrl,
    getAccessToken: () => tokens.get(),
    onUnauthorized: () => auth?.handleUnauthorized(),
    fetch: dependencies.fetch,
  });
  const identity = new HttpIdentityClient(transport);
  const oidc = config.auth.mode === 'oidc'
    ? createOidcSession(createUserManager(config.auth, dependencies.origin, dependencies.sessionStorage), config.auth.bearer)
    : undefined;
  auth = new AuthController({
    mode: config.auth.mode,
    oidc,
    tokens,
    identity,
    session: dependencies.sessionStorage,
    currentPath: dependencies.currentPath,
    replacePath: dependencies.replacePath,
  });
  return { config, client: createWebVoxaClient(transport), identity, auth };
}
