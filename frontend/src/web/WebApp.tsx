'use client';

import { useEffect, useRef, useState } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { Toaster } from 'sonner';
import { VoxaClientProvider } from '@/clients';
import { buildEnvironment, readWebConfig, WebConfigError } from '@/config/web';
import { SidebarProvider } from '@/components/Sidebar/SidebarProvider';
import { RecordingStateProvider } from '@/contexts/RecordingStateContext';
import { TooltipProvider } from '@/components/ui/tooltip';
import { Button } from '@/components/ui/button';
import { createWebRuntime, type WebRuntime } from './runtime';
import { ServerCapabilitiesProvider, useAuth, WebRuntimeProvider } from './context';
import { WebShell } from './WebShell';

export const CALLBACK_PATH = '/auth/callback';

function Centered({ title, children }: { title: string; children?: React.ReactNode }) {
  return (
    <main className="flex min-h-screen items-center justify-center bg-gray-50 p-6">
      <div className="w-full max-w-md rounded-lg border border-gray-200 bg-white p-8 text-center shadow-sm">
        <h1 className="mb-3 text-xl font-semibold text-gray-900">{title}</h1>
        <div className="space-y-4 text-sm text-gray-600">{children}</div>
      </div>
    </main>
  );
}

/** Shows the right screen for every authentication state; renders the app only when ready. */
function AuthGate({ children }: { children: React.ReactNode }) {
  const { state, controller } = useAuth();
  switch (state.kind) {
    case 'ready':
      return <>{children}</>;
    case 'signed-out':
      return (
        <Centered title="Sign in to Voxa">
          {state.reason === 'expired' && <p>Your session expired. Sign in again to continue.</p>}
          {state.reason === 'rejected' && <p>The Voxa server did not accept your sign-in. Check that the web client and server use the same OIDC issuer and audience.</p>}
          {state.reason === 'signed-out' && <p>You have signed out.</p>}
          <Button onClick={() => controller.signIn()}>Sign in</Button>
        </Centered>
      );
    case 'forbidden':
      return (
        <Centered title="No access to this Voxa server">
          <p>You are signed in, but your account is not a member of a workspace here. Ask the server owner to grant you access.</p>
          <Button variant="outline" onClick={() => controller.signOut()}>Sign out</Button>
        </Centered>
      );
    case 'workspace-selection-required':
      return (
        <Centered title="Multiple workspaces">
          <p>Your account belongs to more than one workspace. Choosing a workspace is not supported in Voxa Web yet.</p>
          <Button variant="outline" onClick={() => controller.signOut()}>Sign out</Button>
        </Centered>
      );
    case 'error':
      return (
        <Centered title="Something went wrong">
          <p>{state.message}</p>
          <Button onClick={() => (state.retry === 'identity' ? controller.loadIdentity() : controller.signIn())}>Try again</Button>
        </Centered>
      );
    case 'redirecting':
      return <Centered title="Redirecting to sign in…" />;
    case 'completing':
      return <Centered title="Completing sign in…" />;
    default:
      return <Centered title="Loading Voxa…" />;
  }
}

/** Starts the session once: completes a redirect on the callback route, otherwise boots normally. */
function AuthBootstrap({ callback }: { callback: boolean }) {
  const { controller } = useAuth();
  useEffect(() => {
    if (callback) void controller.completeSignIn(window.location.href);
    else void controller.start();
    // Run exactly once per page load.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  return null;
}

/**
 * Web composition root. Nothing here (or below it) touches Tauri: clients
 * come from the HTTP composition and desktop-only providers are not mounted.
 */
export function WebApp({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const [runtime, setRuntime] = useState<WebRuntime | null>(null);
  const [configError, setConfigError] = useState<string | null>(null);
  const routerRef = useRef(router);
  routerRef.current = router;

  // Built once, after mount: static export prerenders this component without
  // a window, and the runtime (token store, auth state) must never be rebuilt.
  useEffect(() => {
    try {
      const config = readWebConfig(buildEnvironment(), window.location.origin);
      setRuntime(createWebRuntime({
        config,
        origin: window.location.origin,
        sessionStorage: window.sessionStorage,
        currentPath: () => window.location.pathname + window.location.search,
        replacePath: (path) => routerRef.current.replace(path),
      }));
    } catch (error) {
      setConfigError(error instanceof WebConfigError ? error.message : 'Voxa Web could not start.');
    }
  }, []);

  if (configError) {
    return (
      <Centered title="Voxa Web is not configured">
        <p>{configError}</p>
      </Centered>
    );
  }
  if (!runtime) return <Centered title="Loading Voxa…" />;

  return (
    <WebRuntimeProvider value={runtime}>
      <VoxaClientProvider value={runtime.client}>
        <AuthBootstrap callback={pathname === CALLBACK_PATH} />
        <AuthGate>
          <ServerCapabilitiesProvider>
            <RecordingStateProvider>
              <SidebarProvider>
                <TooltipProvider>
                  <WebShell>{children}</WebShell>
                </TooltipProvider>
              </SidebarProvider>
            </RecordingStateProvider>
          </ServerCapabilitiesProvider>
        </AuthGate>
      </VoxaClientProvider>
    </WebRuntimeProvider>
  );
}

export function WebRootLayout({ children, fontClassName }: { children: React.ReactNode; fontClassName: string }) {
  return (
    <html lang="en">
      <body className={`${fontClassName} font-sans antialiased`}>
        <WebApp>{children}</WebApp>
        <Toaster position="bottom-center" richColors closeButton />
      </body>
    </html>
  );
}
