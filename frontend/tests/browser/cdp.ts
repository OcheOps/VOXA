/** Minimal Chrome DevTools Protocol driver for browser tests (no dependencies). */
export interface ChromePage {
  send(method: string, params?: Record<string, unknown>): Promise<any>;
  evaluate<T = unknown>(expression: string): Promise<T>;
  close(): void;
}

export function chromeBinary(): string | null {
  for (const candidate of ['google-chrome', 'google-chrome-stable', 'chromium', 'chromium-browser']) {
    if (Bun.which(candidate)) return candidate;
  }
  return null;
}

export async function launchChrome(userDataDir: string): Promise<ChromePage> {
  const port = 9300 + Math.floor(Math.random() * 600);
  const proc = Bun.spawn([chromeBinary()!, '--headless=new', `--remote-debugging-port=${port}`, '--no-first-run', '--no-default-browser-check',
    '--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream', '--autoplay-policy=no-user-gesture-required',
    `--user-data-dir=${userDataDir}`, 'about:blank'], { stdout: 'ignore', stderr: 'ignore' });
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
      const response = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
      if (response.result?.exceptionDetails) {
        throw new Error(`page error: ${JSON.stringify(response.result.exceptionDetails.exception?.description ?? response.result.exceptionDetails).slice(0, 800)}`);
      }
      return response.result?.result?.value as T;
    },
    close() {
      ws.close();
      proc.kill();
    },
  };
}
