import { describe, expect, test } from 'bun:test';
import { HttpTranscriptionClient } from '../../src/clients/http/transcription-client';
import { HttpTransport } from '../../src/clients/http/transport';
import { UnsupportedOperationError } from '../../src/clients/errors';
import { API, fakeFetch, json } from './http-fake';

const transcriptResponse = {
  mediaAssetId: 'med_1', state: 'ready', error: null,
  transcript: {
    id: 'trn_1', workspaceId: 'wsp', meetingId: 'mtg_1', mediaAssetId: 'med_1', processingJobId: 'job_2', language: 'en',
    asrEngine: 'whisper.cpp', asrModel: 'ggml-base.en', createdAt: 't', updatedAt: 't',
    segments: [{ id: 'trn_1:0', text: 'Ask not.', startTimeSeconds: 1.5, endTimeSeconds: 3, durationSeconds: 1.5, sequence: 0, isFinal: true, confidence: null, source: null, timingProvenance: 'asr' }],
  },
};

function client(routes: Parameters<typeof fakeFetch>[0]) {
  const fake = fakeFetch(routes);
  return { client: new HttpTranscriptionClient(new HttpTransport({ baseUrl: API, getAccessToken: () => 't', fetch: fake.fetch })), requests: fake.requests };
}

describe('HttpTranscriptionClient', () => {
  test('reports remote-only capabilities', () => {
    expect(client({}).client.capabilities).toEqual({
      execution: 'remote', modelManagement: false, modelDownload: false, retranscription: true, importedAudio: false, translationToEnglish: true,
    });
  });

  test('maps transcript state and recording-relative segments', async () => {
    const { client: transcription } = client({ 'GET /api/v1/media/med_1/transcript': () => json(200, transcriptResponse) });
    const result = await transcription.getRecordingTranscript('med_1');
    expect(result.state).toBe('ready');
    expect(result.transcript?.asrModel).toBe('ggml-base.en');
    expect(result.transcript?.segments).toEqual([{ id: 'trn_1:0', text: 'Ask not.', startSeconds: 1.5, endSeconds: 3, sequence: 0 }]);
  });

  test('pending and failed states carry no fake transcript', async () => {
    const { client: transcription } = client({
      'GET /api/v1/media/med_1/transcript': () => json(200, { mediaAssetId: 'med_1', state: 'failed', error: { code: 'transcription_failed', message: 'Transcription failed.' }, transcript: null }),
    });
    const result = await transcription.getRecordingTranscript('med_1');
    expect(result).toEqual({ mediaAssetId: 'med_1', state: 'failed', error: { code: 'transcription_failed', message: 'Transcription failed.' }, transcript: null });
  });

  test('requests transcription and cancels only the recording\'s active jobs', async () => {
    const { client: transcription, requests } = client({
      'POST /api/v1/media/med_1/transcription': () => json(202, { job: {} }),
      'GET /api/v1/meetings/mtg_1/jobs': () => json(200, { items: [
        { id: 'job_done', mediaAssetId: 'med_1', status: 'completed' },
        { id: 'job_live', mediaAssetId: 'med_1', status: 'running' },
        { id: 'job_other', mediaAssetId: 'med_2', status: 'queued' },
      ] }),
      'POST /api/v1/jobs/job_live/cancel': () => json(200, { job: {} }),
    });
    await transcription.requestTranscription('med_1');
    expect(await transcription.cancelProcessing('mtg_1', 'med_1')).toBe(true);
    expect(requests.map((r) => `${r.method} ${r.url.pathname}`)).toEqual([
      'POST /api/v1/media/med_1/transcription', 'GET /api/v1/meetings/mtg_1/jobs', 'POST /api/v1/jobs/job_live/cancel',
    ]);
  });

  test('ensureReady reflects server capability; local engine operations are unsupported', async () => {
    const { client: transcription } = client({ 'GET /api/v1/capabilities': () => json(200, { transcription: { available: true, providers: ['whisper.cpp'] } }) });
    expect(await transcription.ensureReady()).toBe(true);
    expect(await transcription.listModels()).toEqual([]);
    await expect(transcription.initializeProvider('localWhisper')).rejects.toBeInstanceOf(UnsupportedOperationError);
    await expect(transcription.getSelectedProvider()).rejects.toBeInstanceOf(UnsupportedOperationError);
    await expect(transcription.startImport({} as never)).rejects.toBeInstanceOf(UnsupportedOperationError);
    await expect(transcription.startRetranscription({ source: {} as never, meetingId: 'm', language: null, model: null, provider: null })).rejects.toBeInstanceOf(UnsupportedOperationError);
  });
});
