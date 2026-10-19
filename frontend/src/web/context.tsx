'use client';

import { createContext, useContext, useEffect, useState, useSyncExternalStore } from 'react';
import type { AuthController, AuthState } from '@/auth/controller';
import type { ServerCapabilities } from '@/clients/http/identity-client';
import type { WebRuntime } from './runtime';

const WebRuntimeContext = createContext<WebRuntime | null>(null);

export const WebRuntimeProvider = WebRuntimeContext.Provider;

export function useWebRuntime(): WebRuntime {
  const runtime = useContext(WebRuntimeContext);
  if (!runtime) throw new Error('useWebRuntime must be used inside the web runtime');
  return runtime;
}

export function useAuth(): { state: AuthState; controller: AuthController } {
  const { auth } = useWebRuntime();
  const state = useSyncExternalStore(auth.subscribe, auth.getState, auth.getState);
  return { state, controller: auth };
}

/** The authenticated identity; only valid below the auth gate. */
export function useCurrentIdentity() {
  const { state } = useAuth();
  if (state.kind !== 'ready') throw new Error('useCurrentIdentity used before authentication completed');
  return state.identity;
}

const ServerCapabilitiesContext = createContext<ServerCapabilities | null>(null);

/**
 * Loads server-advertised features (`GET /api/v1/capabilities`) once. Kept
 * separate from client capabilities: a feature works only when both the
 * browser client and the server support it.
 */
export function ServerCapabilitiesProvider({ children }: { children: React.ReactNode }) {
  const { identity } = useWebRuntime();
  const [capabilities, setCapabilities] = useState<ServerCapabilities | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    identity.capabilities(controller.signal).then(setCapabilities).catch(() => setCapabilities(null));
    return () => controller.abort();
  }, [identity]);
  return <ServerCapabilitiesContext.Provider value={capabilities}>{children}</ServerCapabilitiesContext.Provider>;
}

/** Null while loading or when the server could not be asked. */
export function useServerCapabilities(): ServerCapabilities | null {
  return useContext(ServerCapabilitiesContext);
}
