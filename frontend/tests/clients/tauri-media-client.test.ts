import { beforeEach, describe, expect, mock, test } from 'bun:test';

type InvokeCall = { command: string; args?: Record<string, unknown> };
const calls: InvokeCall[] = [];
let failure: unknown;

const file = {
  path: '/tmp/call.wav', filename: 'call.wav', duration_seconds: 42,
  size_bytes: 2048, format: 'wav',
};

const invoke = mock(async (command: string, args?: Record<string, unknown>): Promise<unknown> => {
  calls.push({ command, args });
  if (failure) throw failure;
  if (command === 'api_get_meeting_metadata') return { folder_path: '/meetings/meeting-1' };
  if (command === 'read_audio_file') return [1, 2, 3];
  if (command === 'select_and_validate_audio_command') return file;
  if (command === 'validate_audio_file_command') return file;
  return undefined;
});

mock.module('@tauri-apps/api/core', () => ({ invoke }));
const { TauriMediaClient } = await import('../../src/clients/tauri/media-client');
const { createTauriMediaSource } = await import('../../src/clients/tauri/media-source');

beforeEach(() => {
  calls.length = 0;
  failure = undefined;
  invoke.mockClear();
});

describe('TauriMediaClient', () => {
  test('reports factual desktop media capabilities', () => {
    expect(new TauriMediaClient().capabilities).toEqual({
      nativeFilePicker: true, filesystemReveal: true, localByteReads: true,
      urlPlayback: false, rangedStreaming: false, import: true, exportDownload: false,
    });
  });

  test('resolves meeting media without exposing metadata payloads', async () => {
    const client = new TauriMediaClient();
    const source = await client.getMeetingSource('meeting-1');
    expect(source).not.toBeNull();
    expect(calls).toEqual([{
      command: 'api_get_meeting_metadata', args: { meetingId: 'meeting-1' },
    }]);
  });

  test('returns null when meeting media is missing', async () => {
    invoke.mockImplementationOnce(async () => ({ folder_path: null }));
    expect(await new TauriMediaClient().getMeetingSource('missing')).toBeNull();
  });

  test('translates local reads into a playable byte source', async () => {
    const source = createTauriMediaSource('/audio.wav');
    expect(await new TauriMediaClient().getPlaybackSource(source)).toEqual({
      kind: 'bytes', bytes: [1, 2, 3],
    });
    expect(calls).toEqual([{ command: 'read_audio_file', args: { filePath: '/audio.wav' } }]);
  });

  test('selects, validates, and normalizes import sources', async () => {
    const client = new TauriMediaClient();
    const expectedMetadata = {
      fileName: 'call.wav', durationSeconds: 42, sizeBytes: 2048, format: 'wav',
    };
    const selected = await client.selectImportSource();
    expect(selected).not.toBeNull();
    expect({ ...selected, source: undefined }).toEqual({ ...expectedMetadata, source: undefined });
    const validated = await client.validateDroppedImport('/tmp/call.wav');
    expect({ ...validated, source: undefined }).toEqual({ ...expectedMetadata, source: undefined });
    expect(calls).toEqual([
      { command: 'select_and_validate_audio_command', args: undefined },
      { command: 'validate_audio_file_command', args: { path: '/tmp/call.wav' } },
    ]);
  });

  test('preserves file-picker cancellation', async () => {
    invoke.mockImplementationOnce(async () => null);
    expect(await new TauriMediaClient().selectImportSource()).toBeNull();
  });

  test('keeps folder operations behind the adapter', async () => {
    const client = new TauriMediaClient();
    await client.openMeetingFolder('meeting-1');
    await client.openRecordingsFolder();
    expect(calls).toEqual([
      { command: 'open_meeting_folder', args: { meetingId: 'meeting-1' } },
      { command: 'open_recordings_folder', args: undefined },
    ]);
  });

  test('propagates native errors', async () => {
    failure = new Error('native read failed');
    await expect(new TauriMediaClient().getPlaybackSource(
      createTauriMediaSource('/broken.wav'),
    )).rejects.toThrow('native read failed');
  });
});
