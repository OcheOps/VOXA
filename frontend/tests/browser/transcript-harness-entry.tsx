/*
 * Browser test harness: the production Voxa Web meeting media view
 * (player + TranscriptPanel) wired to a real Voxa server through the real
 * HTTP clients. Not shipped.
 */
import { createRoot } from 'react-dom/client';
import { VoxaClientProvider } from '../../src/clients';
import { HttpTransport } from '../../src/clients/http/transport';
import { createWebVoxaClient } from '../../src/clients/web';
import { WebRuntimeProvider } from '../../src/web/context';
import type { WebRuntime } from '../../src/web/runtime';
import { MeetingMedia } from '../../src/web/recording/MeetingMedia';

(window as any).harness = {
  render(api: string, meetingId: string) {
    const client = createWebVoxaClient(new HttpTransport({ baseUrl: api, getAccessToken: () => null }), api);
    const runtime = { client, uploads: { getJobs: () => [], subscribe: () => () => {} } } as unknown as WebRuntime;
    const root = document.createElement('div');
    document.body.appendChild(root);
    createRoot(root).render(
      <WebRuntimeProvider value={runtime}>
        <VoxaClientProvider value={client}>
          <MeetingMedia meetingId={meetingId} />
        </VoxaClientProvider>
      </WebRuntimeProvider>,
    );
    (window as any).voxaClient = client;
  },
  text() { return document.body.innerText; },
  segments() {
    return [...document.querySelectorAll<HTMLButtonElement>('button[aria-label^="Play from"]')].map((button) => button.innerText);
  },
  async clickSegment(index: number) {
    const audio = document.querySelector('audio')!;
    if (audio.readyState < 1) await new Promise((resolve) => { audio.onloadedmetadata = resolve; });
    document.querySelectorAll<HTMLButtonElement>('button[aria-label^="Play from"]')[index].click();
    await new Promise((resolve) => setTimeout(resolve, 300));
    return { currentTime: audio.currentTime, duration: audio.duration, error: audio.error?.code ?? null };
  },
};
