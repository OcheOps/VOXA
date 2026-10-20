import { afterAll, describe, expect, mock, test } from 'bun:test';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { RecordingCapabilities, VoxaClient } from '../../src/clients/contracts';
import type { SessionState } from '../../src/web/recording/session';

// The unload guard registers a beforeunload listener while recording.
// Other test files may leave a partial window behind, so stub only what is missing.
type Listeners = { addEventListener?: unknown; removeEventListener?: unknown };
const scope = globalThis as unknown as { window?: Listeners };
const hadWindow = 'window' in scope;
// Never reassign an existing window: other test files may install a read-only one.
if (!hadWindow) scope.window = {};
const win = scope.window as Listeners;
const missing = typeof win.addEventListener !== 'function';
if (missing) Object.assign(win, { addEventListener() {}, removeEventListener() {} });
afterAll(() => {
  if (!hadWindow) delete scope.window;
  else if (missing) { delete win.addEventListener; delete win.removeEventListener; }
});

const originalToast = { ...(await import('sonner')) };
afterAll(() => mock.module('sonner', () => originalToast));
mock.module('sonner', () => ({ toast: { error: () => {}, info: () => {}, success: () => {} } }));

const { VoxaClientProvider } = await import('../../src/clients');
const { WebRuntimeProvider } = await import('../../src/web/context');
const { RecordingPanel } = await import('../../src/web/recording/RecordingPanel');
const { captureSummary } = await import('../../src/web/recording/capture-ui');

const base: RecordingCapabilities = {
  microphoneCapture: true, systemAudioCapture: 'display-media', deviceEnumeration: 'unavailable', microphoneHotSwap: 'unavailable',
  audioLevelMonitoring: false, pauseResume: true, tabCapture: true,
  browserCapture: { microphone: true, displayCapture: true, sharedAudio: 'likely', mixing: true, modes: ['microphone', 'microphone+shared', 'shared'] },
};

function render(capabilities: RecordingCapabilities, state: Partial<SessionState> = {}) {
  const starts: unknown[][] = [];
  const session: SessionState = { phase: 'idle', meetingId: null, mediaAssetId: null, error: null, capture: null, warning: null, ...state };
  const recordingSession = {
    getState: () => session, subscribe: () => () => {},
    start: async (...args: unknown[]) => void starts.push(args), stop: async () => {}, pause: async () => {}, resume: async () => {},
  };
  const client = { recording: { capabilities, listDevices: async () => [], getState: async () => ({ activeDuration: 0 }) } } as unknown as VoxaClient;
  const runtime = {
    client, recordingSession, config: { recordingNotice: 'Tell participants they are being recorded.' },
    uploads: { getJobs: () => [], subscribe: () => () => {}, retry: () => {}, discard: async () => {} },
  };
  let renderer!: ReactTestRenderer;
  act(() => {
    renderer = create(
      <WebRuntimeProvider value={runtime as never}>
        <VoxaClientProvider value={client}>
          <RecordingPanel meetingId="mtg_1" />
        </VoxaClientProvider>
      </WebRuntimeProvider>,
    );
  });
  return { renderer, starts };
}

const text = (renderer: ReactTestRenderer) => JSON.stringify(renderer.toJSON());

describe('RecordingPanel capture modes', () => {
  test('offers only the modes the browser supports', () => {
    const microphoneOnly = render({ ...base, tabCapture: false, browserCapture: { ...base.browserCapture!, displayCapture: false, sharedAudio: 'unavailable', modes: ['microphone'] } });
    expect(microphoneOnly.renderer.root.findAll((n) => n.type === 'input' && n.props.type === 'radio')).toHaveLength(0);
    expect(text(microphoneOnly.renderer)).not.toContain('Meeting tab');

    const { renderer } = render(base);
    const radios = renderer.root.findAll((n) => n.type === 'input' && n.props.type === 'radio');
    expect(radios.map((r) => r.props.value)).toEqual(['microphone', 'microphone+shared', 'shared']);
  });

  test('a meeting-tab mode explains the share picker, shows provider tips, and starts with that mode', async () => {
    const { renderer, starts } = render(base);
    const radio = renderer.root.findAll((n) => n.type === 'input' && n.props.value === 'microphone+shared')[0];
    act(() => radio.props.onChange());
    const output = text(renderer);
    expect(output).toContain('Choose the tab where your meeting is running and turn on “Share tab audio”');
    expect(output).toContain('Voxa cannot choose the tab for you');
    for (const site of ['Google Meet', 'Microsoft Teams', 'Zoom', 'Cisco Webex', 'WhatsApp Web']) expect(output).toContain(site);
    const start = renderer.root.findAll((n) => n.type === 'button' && ([] as unknown[]).concat(n.props.children).includes('Choose tab and start recording'))[0];
    await act(async () => { start.props.onClick(); });
    expect(starts).toEqual([['mtg_1', null, 'microphone+shared']]);
  });

  test('the recording indicator states exactly what is captured and shows warnings', () => {
    const { renderer } = render(base, {
      phase: 'recording', meetingId: 'mtg_1', mediaAssetId: 'med_1',
      capture: { mode: 'microphone+shared', microphone: true, shared: { surface: 'browser' } },
    });
    expect(text(renderer)).toContain('Recording your microphone + meeting tab audio');
    expect(text(renderer)).toContain('including other participants');
    const stopped = render(base, {
      phase: 'recording', meetingId: 'mtg_1', mediaAssetId: 'med_1', warning: 'Tab audio sharing stopped. Recording continues with your microphone only.',
      capture: { mode: 'microphone+shared', microphone: true, shared: null },
    });
    expect(text(stopped.renderer)).toContain('Recording your microphone');
    expect(text(stopped.renderer)).toContain('Tab audio sharing stopped');
    expect(text(stopped.renderer)).not.toContain('including other participants');
  });

  test('summaries of capture state', () => {
    expect(captureSummary({ mode: 'shared', microphone: false, shared: { surface: 'monitor' } })).toBe('Recording shared screen audio');
    expect(captureSummary({ mode: 'microphone', microphone: true, shared: null })).toBe('Recording your microphone');
    expect(captureSummary(null)).toBe('Recording');
  });
});

describe('recording source labels', () => {
  test('label stored provenance subtly, and hide unknown', async () => {
    const { provenanceLabel, sharedAudioNote } = await import('../../src/web/recording/capture-ui');
    expect(provenanceLabel({ mode: 'microphone', client: 'web', sharedSurface: null })).toBe('Microphone');
    expect(provenanceLabel({ mode: 'microphone_and_shared_audio', client: 'web', sharedSurface: 'browser' })).toBe('Meeting tab + microphone');
    expect(provenanceLabel({ mode: 'shared_audio', client: 'web', sharedSurface: 'browser' })).toBe('Meeting tab');
    expect(provenanceLabel({ mode: 'microphone_and_shared_audio', client: 'web', sharedSurface: 'monitor' })).toBe('Shared screen + microphone');
    expect(provenanceLabel({ mode: 'unknown', client: 'unknown', sharedSurface: null })).toBeNull();
    expect(provenanceLabel(null)).toBeNull();
    expect(sharedAudioNote({ microphone: true, displayCapture: true, sharedAudio: 'unknown', mixing: true, modes: ['microphone', 'shared'] }))
      .toBe('Your browser may not provide shared tab audio. Voxa will verify after you choose a source.');
    expect(sharedAudioNote({ microphone: true, displayCapture: true, sharedAudio: 'likely', mixing: true, modes: ['microphone', 'shared'] })).toBeNull();
  });
});
