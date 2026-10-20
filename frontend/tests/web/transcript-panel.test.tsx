import { afterAll, describe, expect, mock, test } from 'bun:test';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { RecordingTranscript, ServerTranscriptClient, VoxaClient } from '../../src/clients/contracts';

const originalToast = { ...(await import('sonner')) };
afterAll(() => mock.module('sonner', () => originalToast));
mock.module('sonner', () => ({ toast: { error: () => {}, info: () => {}, success: () => {} } }));

const { VoxaClientProvider } = await import('../../src/clients');
const { TranscriptPanel } = await import('../../src/web/recording/TranscriptPanel');

const ready: RecordingTranscript = {
  mediaAssetId: 'med_1', state: 'ready', error: null,
  transcript: { id: 'trn_1', language: 'en', asrEngine: 'whisper.cpp', asrModel: 'ggml-tiny.en', segments: [
    { id: 's0', text: 'And so my fellow Americans', startSeconds: 0, endSeconds: 7.9, sequence: 0 },
    { id: 's1', text: 'ask what you can do for your country.', startSeconds: 7.94, endSeconds: 10.76, sequence: 1 },
  ] },
};

function render(responses: RecordingTranscript[], calls: string[] = []) {
  const transcripts: ServerTranscriptClient = {
    getRecordingTranscript: async () => responses.length > 1 ? responses.shift()! : responses[0],
    getMeetingTranscript: async () => responses[0],
    requestTranscription: async () => void calls.push('request'),
    cancelProcessing: async () => { calls.push('cancel'); return true; },
  };
  const seeks: number[] = [];
  const player = { seek: (seconds: number) => void seeks.push(seconds) };
  let renderer!: ReactTestRenderer;
  act(() => {
    renderer = create(
      <VoxaClientProvider value={{ serverTranscripts: transcripts } as unknown as VoxaClient}>
        <TranscriptPanel meetingId="mtg_1" mediaAssetId="med_1" player={player} />
      </VoxaClientProvider>,
    );
  });
  return { renderer: () => renderer, seeks, calls };
}

async function settle() {
  for (let i = 0; i < 5; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
}

describe('TranscriptPanel', () => {
  test('clicking a segment seeks playback to its recording-relative start', async () => {
    const { renderer, seeks } = render([ready]);
    await settle();
    const buttons = renderer().root.findAll((node) => node.type === 'button' && typeof node.props['aria-label'] === 'string' && node.props['aria-label'].startsWith('Play from'));
    expect(buttons).toHaveLength(2);
    act(() => buttons[1].props.onClick());
    expect(seeks).toEqual([7.94]);
    expect(JSON.stringify(renderer().toJSON())).toContain('ask what you can do for your country.');
  });

  test('shows simple processing states with retry and cancel', async () => {
    for (const [state, text] of [['preparing', 'Preparing recording'], ['transcribing', 'Transcribing'], ['unavailable', 'not available on this server']] as const) {
      const { renderer } = render([{ mediaAssetId: 'med_1', state, error: null, transcript: null }]);
      await settle();
      expect(JSON.stringify(renderer().toJSON())).toContain(text);
      renderer().unmount();
    }
    const failed = render([{ mediaAssetId: 'med_1', state: 'failed', error: { code: 'transcription_failed', message: 'Transcription failed.' }, transcript: null }, ready]);
    await settle();
    expect(JSON.stringify(failed.renderer().toJSON())).toContain('Transcription failed.');
    const retry = failed.renderer().root.findAll((node) => node.type === 'button').find((node) => JSON.stringify(node.props.children).includes('Retry'))!;
    await act(async () => { retry.props.onClick(); });
    await settle();
    expect(failed.calls).toEqual(['request']);
    expect(JSON.stringify(failed.renderer().toJSON())).toContain('And so my fellow Americans');
  });

  test('an empty ready transcript says no speech was detected', async () => {
    const { renderer } = render([{ ...ready, transcript: { ...ready.transcript!, segments: [] } }]);
    await settle();
    expect(JSON.stringify(renderer().toJSON())).toContain('No speech was detected');
  });
});
