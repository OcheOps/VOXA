import { afterAll, beforeEach, describe, expect, mock, test } from 'bun:test';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';

/*
 * Mounts the real web composition (WebApp + web views) with every Tauri
 * module replaced by a recorder. Any call into @tauri-apps/* fails the test:
 * web startup must never touch native APIs.
 */
const TAURI_MODULES = [
  '@tauri-apps/api/core', '@tauri-apps/api/event', '@tauri-apps/api/app', '@tauri-apps/api/path',
  '@tauri-apps/plugin-os', '@tauri-apps/plugin-process', '@tauri-apps/plugin-store', '@tauri-apps/plugin-updater',
];
const originals = new Map<string, unknown>();
for (const name of TAURI_MODULES) originals.set(name, { ...(await import(name)) });
const originalNavigation = { ...(await import('next/navigation')) };
afterAll(() => {
  for (const [name, module] of originals) mock.module(name, () => module as Record<string, unknown>);
  mock.module('next/navigation', () => originalNavigation);
});

const tauriCalls: string[] = [];
for (const name of TAURI_MODULES) {
  const original = originals.get(name) as Record<string, unknown>;
  const recorded: Record<string, unknown> = {};
  for (const key of Object.keys(original)) {
    recorded[key] = typeof original[key] === 'function'
      ? (...args: unknown[]) => { tauriCalls.push(`${name}.${key}(${String(args[0] ?? '')})`); throw new Error('Tauri is not available in the web runtime'); }
      : original[key];
  }
  mock.module(name, () => recorded);
}

let pathname = '/';
let search = new URLSearchParams();
const navigations: string[] = [];
// A fresh router object per render (unlike Next.js) guards against effects keyed on router identity.
mock.module('next/navigation', () => ({
  usePathname: () => pathname,
  useSearchParams: () => search,
  useRouter: () => ({ replace: (p: string) => navigations.push(`replace:${p}`), push: (p: string) => navigations.push(`push:${p}`) }),
}));

const sessionValues = new Map<string, string>();
const previousWindow = Object.getOwnPropertyDescriptor(globalThis, 'window');
afterAll(() => {
  if (previousWindow) Object.defineProperty(globalThis, 'window', previousWindow);
  else delete (globalThis as Record<string, unknown>).window;
});
const fakeWindow = {
  location: { origin: 'http://localhost:3118', get pathname() { return pathname; }, get search() { return search.toString() ? `?${search}` : ''; }, get href() { return `http://localhost:3118${pathname}`; } },
  sessionStorage: {
    getItem: (k: string) => sessionValues.get(k) ?? null,
    setItem: (k: string, v: string) => void sessionValues.set(k, v),
    removeItem: (k: string) => void sessionValues.delete(k),
  },
  addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => true,
  setTimeout, clearTimeout, setInterval, clearInterval,
};
Object.defineProperty(globalThis, 'window', { configurable: true, writable: true, value: fakeWindow });

const apiCalls: string[] = [];
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const previousFetch = globalThis.fetch;
afterAll(() => { globalThis.fetch = previousFetch; });
globalThis.fetch = (async (input: string | URL, init?: RequestInit) => {
  const url = new URL(String(input));
  apiCalls.push(`${init?.method ?? 'GET'} ${url.pathname}`);
  switch (url.pathname) {
    case '/api/v1/me': return json({ user: { id: 'usr_dev', email: null, displayName: 'Dev' }, workspace: { id: 'workspace-dev', name: 'Development workspace' }, role: 'owner' });
    case '/api/v1/capabilities': return json({ apiVersion: 'v1', mediaStorage: { available: false }, transcription: { available: false, providers: [] }, summary: { available: false, providers: [] }, configuration: { serverManaged: true, credentialManagement: true } });
    case '/api/v1/meetings': return json({ items: [{ id: 'm1', workspaceId: 'workspace-dev', title: 'Planning sync', status: 'created', createdAt: '2026-10-19T10:00:00Z', updatedAt: '2026-10-19T10:00:00Z' }], page: { nextCursor: null, hasMore: false } });
    case '/api/v1/meetings/m1': return json({ meeting: { id: 'm1', workspaceId: 'workspace-dev', title: 'Planning sync', status: 'created', createdAt: '2026-10-19T10:00:00Z', updatedAt: '2026-10-19T10:00:00Z' } });
    case '/api/v1/configuration': return json({ workspaceId: 'workspace-dev', transcription: { provider: 'none', model: '', language: 'auto', translateToEnglish: false }, summary: { provider: 'none', model: '', outputLanguage: 'auto', templateId: null, autoGenerate: false }, recording: { retainMedia: true }, privacy: { analyticsEnabled: false }, notifications: { processingComplete: false }, credentials: [] });
    case '/api/v1/credentials': return json({ credentials: [{ provider: 'openai', configured: true, maskedValue: '••••' }] });
  }
  return json({ error: { code: 'not_found', message: 'Resource not found' } }, 404);
}) as typeof fetch;

const { WebApp } = await import('../../src/web/WebApp');
const { WebHome } = await import('../../src/web/WebHome');
const { WebMeetingDetail } = await import('../../src/web/WebMeetingDetail');
const { WebSettings } = await import('../../src/web/WebSettings');

async function settle() {
  for (let i = 0; i < 12; i++) await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
}

function text(renderer: ReactTestRenderer): string {
  return JSON.stringify(renderer.toJSON());
}

beforeEach(() => {
  tauriCalls.length = 0;
  apiCalls.length = 0;
  sessionValues.clear();
  pathname = '/';
  search = new URLSearchParams();
  process.env.NEXT_PUBLIC_VOXA_API_URL = 'http://127.0.0.1:8080';
  process.env.NEXT_PUBLIC_VOXA_AUTH_MODE = 'development';
});

describe('web runtime startup', () => {
  test('development mode boots, loads /me and meetings, and never calls Tauri', async () => {
    let renderer!: ReactTestRenderer;
    await act(async () => { renderer = create(<WebApp><WebHome /></WebApp>); });
    await settle();
    const output = text(renderer);
    expect(output).toContain('Planning sync');
    expect(output).toContain('Development workspace');
    expect(output).toContain('Not available in Voxa Web yet');
    expect(apiCalls).toContain('GET /api/v1/me');
    expect(apiCalls).toContain('GET /api/v1/meetings');
    expect(tauriCalls).toEqual([]);
    renderer.unmount();
  });

  test('meeting detail and settings render from HTTP clients without Tauri', async () => {
    pathname = '/meeting-details';
    search = new URLSearchParams({ id: 'm1' });
    let detail!: ReactTestRenderer;
    await act(async () => { detail = create(<WebApp><WebMeetingDetail /></WebApp>); });
    await settle();
    expect(text(detail)).toContain('Transcripts are not available in Voxa Web yet');
    detail.unmount();

    pathname = '/settings';
    let settings!: ReactTestRenderer;
    await act(async () => { settings = create(<WebApp><WebSettings /></WebApp>); });
    await settle();
    const output = text(settings);
    expect(output).toContain('Provider API keys');
    expect(output).toContain('configured ••••');
    expect(output).toContain('Desktop-only settings');
    expect(apiCalls).toContain('GET /api/v1/configuration');
    expect(tauriCalls).toEqual([]);
    settings.unmount();
  });

  test('OIDC mode without a session shows sign-in and makes no API calls', async () => {
    process.env.NEXT_PUBLIC_VOXA_AUTH_MODE = 'oidc';
    process.env.NEXT_PUBLIC_VOXA_OIDC_ISSUER = 'https://id.example.test/realms/voxa';
    process.env.NEXT_PUBLIC_VOXA_OIDC_CLIENT_ID = 'voxa-web';
    let renderer!: ReactTestRenderer;
    await act(async () => { renderer = create(<WebApp><WebHome /></WebApp>); });
    await settle();
    expect(text(renderer)).toContain('Sign in to Voxa');
    expect(apiCalls).toEqual([]);
    expect(tauriCalls).toEqual([]);
    renderer.unmount();
  });

  test('missing configuration is reported instead of crashing', async () => {
    delete process.env.NEXT_PUBLIC_VOXA_API_URL;
    let renderer!: ReactTestRenderer;
    await act(async () => { renderer = create(<WebApp><WebHome /></WebApp>); });
    await settle();
    expect(text(renderer)).toContain('Voxa Web is not configured');
    renderer.unmount();
  });
});
