/**
 * Frontend runtime, fixed at build time. `desktop` (the default) is the
 * Tauri application; `web` is the browser application talking to the Voxa
 * HTTP API. The value is inlined by Next.js, so it never depends on
 * user-agent sniffing or on probing for Tauri globals.
 */
export type VoxaRuntime = 'desktop' | 'web';

export const VOXA_RUNTIME: VoxaRuntime = process.env.NEXT_PUBLIC_VOXA_RUNTIME === 'web' ? 'web' : 'desktop';

export const isWebRuntime = VOXA_RUNTIME === 'web';
