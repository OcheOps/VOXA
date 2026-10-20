import { AuthController } from '@/auth/controller';
import { createOidcSession, createUserManager } from '@/auth/oidc';
import { InMemoryTokenStore } from '@/auth/token-store';
import type { VoxaClient } from '@/clients/contracts';
import { HttpIdentityClient } from '@/clients/http/identity-client';
import { HttpTransport, type FetchLike } from '@/clients/http/transport';
import { createWebVoxaClient } from '@/clients/web';
import type { WebConfig } from '@/config/web';
import { createPendingStore } from './recording/pending-store';
import { browserRecordingLocks, RecordingUploadCoordinator } from './recording/upload-coordinator';
import { RecordingSessionController } from './recording/session';
import { BrowserRecordingClient } from '@/clients/browser/recording-client';

export interface WebRuntime {
  config: WebConfig;
  client: VoxaClient;
  identity: HttpIdentityClient;
  auth: AuthController;
  uploads: RecordingUploadCoordinator;
  /** Present when this browser can record (microphone + MediaRecorder + supported format). */
  recordingSession: RecordingSessionController | null;
}

export interface WebRuntimeDependencies {
  config: WebConfig;
  origin: string;
  sessionStorage: Storage;
  currentPath: () => string;
  replacePath: (path: string) => void;
  fetch?: FetchLike;
  pendingStore?: import('./recording/pending-store').PendingChunkStore;
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
  const client = createWebVoxaClient(transport, config.apiBaseUrl);
  const scope = globalThis as { navigator?: { onLine?: boolean }; window?: Pick<Window, 'addEventListener' | 'removeEventListener'> };
  const uploads = new RecordingUploadCoordinator({
    media: client.serverMedia!,
    store: dependencies.pendingStore ?? createPendingStore(),
    locks: browserRecordingLocks(),
    isOnline: () => scope.navigator?.onLine !== false,
    onOnline: (callback) => {
      scope.window?.addEventListener('online', callback);
      return () => scope.window?.removeEventListener('online', callback);
    },
  });
  const recordingSession = client.recording instanceof BrowserRecordingClient && client.recording.capabilities.microphoneCapture
    ? new RecordingSessionController(client.recording, uploads)
    : null;
  if (client.recording instanceof BrowserRecordingClient && typeof window !== 'undefined') {
    // Local debugging only: `voxaCaptureDiagnostics()` in the console lists
    // recent capture events (no media, transcript text, URLs or titles).
    const recorder = client.recording;
    (window as unknown as { voxaCaptureDiagnostics?: () => unknown }).voxaCaptureDiagnostics = () => recorder.getDiagnostics();
  }
  return { config, client, identity, auth, uploads, recordingSession };
}
