import { beforeEach, describe, expect, mock, test } from 'bun:test';

type Listener = (event: { payload: unknown }) => void;
type InvokeCall = { command: string; args?: Record<string, unknown> };

const calls: InvokeCall[] = [];
const listeners = new Map<string, Listener>();

const invoke = mock(async (command: string, args?: Record<string, unknown>): Promise<unknown> => {
  calls.push({ command, args });
  if (command === 'api_get_transcript_config') return { provider: 'localWhisper', model: 'small' };
  if (command === 'whisper_has_available_models') return true;
  if (command === 'whisper_get_available_models') {
    return [{ name: 'small', size_mb: 244, status: 'Available' }];
  }
  if (command === 'get_transcription_status') {
    return { chunks_in_queue: 2, is_processing: true, last_activity_ms: 10 };
  }
  if (command === 'get_transcript_history') return [];
  return undefined;
});

const listen = mock(async (eventName: string, listener: Listener) => {
  listeners.set(eventName, listener);
  return () => { listeners.delete(eventName); };
});

mock.module('@tauri-apps/api/core', () => ({ invoke }));
mock.module('@tauri-apps/api/event', () => ({ listen }));

const { TauriTranscriptionClient } = await import('../../src/clients/tauri/transcription-client');
const { createTauriMediaSource } = await import('../../src/clients/tauri/media-source');

beforeEach(() => {
  calls.length = 0;
  listeners.clear();
  invoke.mockClear();
  listen.mockClear();
});

describe('TauriTranscriptionClient commands', () => {
  test('ensures the selected provider is initialized and ready', async () => {
    const client = new TauriTranscriptionClient();

    expect(await client.getSelectedProvider()).toBe('localWhisper');
    expect(await client.ensureReady()).toBe(true);

    expect(calls).toEqual([
      { command: 'api_get_transcript_config', args: undefined },
      { command: 'api_get_transcript_config', args: undefined },
      { command: 'whisper_init', args: undefined },
      { command: 'whisper_has_available_models', args: undefined },
    ]);
  });

  test('normalizes model inventory and preserves status calls', async () => {
    const client = new TauriTranscriptionClient();

    expect(await client.listModels('localWhisper')).toEqual([
      { name: 'small', sizeMb: 244, status: 'available' },
    ]);
    expect(await client.getStatus()).toEqual({
      chunksInQueue: 2,
      isProcessing: true,
      lastActivityMs: 10,
    });

    expect(calls).toEqual([
      { command: 'whisper_get_available_models', args: undefined },
      { command: 'get_transcription_status', args: undefined },
    ]);
  });

  test('preserves existing-audio transcription command payloads', async () => {
    const client = new TauriTranscriptionClient();

    await client.startRetranscription({
      meetingId: 'meeting-1',
      source: createTauriMediaSource('/meetings/1'),
      language: 'fr',
      model: 'small',
      provider: 'whisper',
    });
    await client.cancelRetranscription();
    await client.startImport({
      source: createTauriMediaSource('/tmp/call.wav'),
      title: 'Call',
      language: null,
      model: 'small',
      provider: 'whisper',
    });
    await client.cancelImport();

    expect(calls).toEqual([
      {
        command: 'start_retranscription_command',
        args: {
          meetingId: 'meeting-1',
          meetingFolderPath: '/meetings/1',
          language: 'fr',
          model: 'small',
          provider: 'whisper',
        },
      },
      { command: 'cancel_retranscription_command', args: undefined },
      {
        command: 'start_import_audio_command',
        args: {
          sourcePath: '/tmp/call.wav',
          title: 'Call',
          language: null,
          model: 'small',
          provider: 'whisper',
        },
      },
      { command: 'cancel_import_command', args: undefined },
    ]);
  });
});

describe('TauriTranscriptionClient events', () => {
  test('translates transcript, completion, error, and model progress events', async () => {
    const client = new TauriTranscriptionClient();
    const transcriptUpdates: unknown[] = [];
    const completions: string[] = [];
    const errors: unknown[] = [];
    const progress: unknown[] = [];

    const cleanups = [
      client.onUpdate(update => transcriptUpdates.push(update)),
      client.onComplete(() => completions.push('complete')),
      client.onError(error => errors.push(error)),
      client.onModelDownloadProgress(event => progress.push(event)),
    ];
    await Promise.resolve();

    const update = {
      text: 'hello', timestamp: '12:00:00', source: 'mic', sequence_id: 7,
      chunk_start_time: 1, is_partial: false, confidence: 0.9,
      audio_start_time: 1, audio_end_time: 2, duration: 1,
    };
    listeners.get('transcript-update')?.({ payload: update });
    listeners.get('transcription-complete')?.({ payload: undefined });
    listeners.get('transcription-error')?.({
      payload: { error: 'failed', userMessage: 'Try again', actionable: true, phase: 'active' },
    });
    listeners.get('model-download-progress')?.({
      payload: { modelName: 'small', progress: 25 },
    });

    expect(transcriptUpdates).toEqual([{
      text: 'hello', timestamp: '12:00:00', source: 'mic', sequenceId: 7,
      chunkStartTime: 1, isPartial: false, confidence: 0.9,
      audioStartTime: 1, audioEndTime: 2, duration: 1,
    }]);
    expect(completions).toEqual(['complete']);
    expect(errors).toEqual([{ error: 'failed', userMessage: 'Try again', actionable: true, phase: 'active' }]);
    expect(progress).toEqual([{ provider: 'localWhisper', modelName: 'small', progress: 25 }]);

    cleanups.forEach(cleanup => cleanup());
  });

  test('suppresses callbacks and unlistens when cancelled before registration resolves', async () => {
    let pendingListener!: Listener;
    let resolveRegistration!: (cleanup: () => void) => void;
    const nativeCleanup = mock(() => {});
    listen.mockImplementationOnce((_name, listener) => {
      pendingListener = listener;
      return new Promise(resolve => { resolveRegistration = resolve; });
    });
    const callback = mock(() => {});
    const client = new TauriTranscriptionClient();

    const unsubscribe = client.onComplete(callback);
    unsubscribe();
    pendingListener({ payload: undefined });
    resolveRegistration(nativeCleanup);
    await Promise.resolve();
    await Promise.resolve();
    unsubscribe();

    expect(callback).not.toHaveBeenCalled();
    expect(nativeCleanup).toHaveBeenCalledTimes(1);
  });

  test('reports asynchronous listener registration failures to the consumer', async () => {
    const registrationError = new Error('event bridge unavailable');
    listen.mockImplementationOnce(() => Promise.reject(registrationError));
    const onRegistrationError = mock(() => {});
    const client = new TauriTranscriptionClient();

    const unsubscribe = client.onUpdate(() => {}, onRegistrationError);
    await Promise.resolve();
    await Promise.resolve();

    expect(onRegistrationError).toHaveBeenCalledWith(registrationError);
    unsubscribe();
  });
});
