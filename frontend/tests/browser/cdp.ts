/** Minimal Chrome DevTools Protocol driver for browser tests (no dependencies). */
export interface ChromePage {
  send(method: string, params?: Record<string, unknown>): Promise<any>;
  /** Evaluates with a simulated user gesture (needed by getDisplayMedia and audio). */
  evaluate<T = unknown>(expression: string): Promise<T>;
  /** Opens another tab (e.g. a meeting page to share); returns its target id. */
  openTab(url: string): Promise<string>;
  closeTab(id: string): Promise<void>;
  close(): void;
}

export function chromeBinary(): string | null {
  for (const candidate of ['google-chrome', 'google-chrome-stable', 'chromium', 'chromium-browser']) {
    if (Bun.which(candidate)) return candidate;
  }
  return null;
}

/**
 * `fakeUi: false` is needed for real tab capture: the fake media UI turns
 * getDisplayMedia into a fake screen. Microphone permission is then granted
 * through CDP and `--auto-select-tab-capture-source-by-title=…` picks the tab.
 */
export async function launchChrome(userDataDir: string, options: { args?: string[]; fakeUi?: boolean } = {}): Promise<ChromePage> {
  const port = 9300 + Math.floor(Math.random() * 600);
  const proc = Bun.spawn([chromeBinary()!, '--headless=new', `--remote-debugging-port=${port}`, '--no-first-run', '--no-default-browser-check',
    '--use-fake-device-for-media-stream', ...(options.fakeUi === false ? [] : ['--use-fake-ui-for-media-stream']), '--autoplay-policy=no-user-gesture-required',
    ...(options.args ?? []), `--user-data-dir=${userDataDir}`, 'about:blank'], { stdout: 'ignore', stderr: 'ignore' });
  let page: { webSocketDebuggerUrl: string } | undefined;
  for (let i = 0; i < 150 && !page; i++) {
    try {
      const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json() as Array<{ type: string; webSocketDebuggerUrl: string }>;
      page = targets.find((t) => t.type === 'page');
    } catch { /* starting */ }
    if (!page) await Bun.sleep(100);
  }
  if (!page) throw new Error('Chrome did not start');
  const ws = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => { ws.onopen = resolve; ws.onerror = reject; });
  let id = 0;
  const pending = new Map<number, (message: any) => void>();
  ws.onmessage = (event) => {
    const message = JSON.parse(String(event.data));
    if (message.id && pending.has(message.id)) {
      pending.get(message.id)!(message);
      pending.delete(message.id);
    }
  };
  const send = (method: string, params: Record<string, unknown> = {}) => new Promise<any>((resolve) => {
    const messageId = ++id;
    pending.set(messageId, resolve);
    ws.send(JSON.stringify({ id: messageId, method, params }));
  });
  return {
    send,
    async evaluate<T>(expression: string) {
      const response = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true, userGesture: true });
      if (response.result?.exceptionDetails) {
        throw new Error(`page error: ${JSON.stringify(response.result.exceptionDetails.exception?.description ?? response.result.exceptionDetails).slice(0, 800)}`);
      }
      return response.result?.result?.value as T;
    },
    async openTab(url: string) {
      const target = await (await fetch(`http://127.0.0.1:${port}/json/new?${url}`, { method: 'PUT' })).json() as { id: string };
      return target.id;
    },
    async closeTab(id: string) {
      await fetch(`http://127.0.0.1:${port}/json/close/${id}`);
    },
    close() {
      ws.close();
      proc.kill();
    },
  };
}
