import { describe, expect, test } from 'bun:test';
import { HttpSummaryClient } from '../../src/clients/http/summary-client';
import { HttpTransport } from '../../src/clients/http/transport';
import { UnsupportedOperationError } from '../../src/clients/errors';
import { API, fakeFetch, json } from './http-fake';

const summary = {
  id: 'sum_1', workspaceId: 'wsp', meetingId: 'mtg_1', processingJobId: 'job_1', transcriptId: 'trn_1', mediaAssetId: 'med_1',
  provider: 'openai', model: 'gpt-4o-mini', templateId: 'standard_meeting', outputLanguage: null,
  content: { format: 'markdown', markdown: '**Summary**\n\nShip Friday.', title: 'Launch', chunkCount: 1, reasoningStripped: false, normalizationFallback: false },
  createdAt: '2026-10-20T10:00:00Z', updatedAt: '2026-10-20T10:00:00Z',
};
const job = (status: string) => ({ id: 'job_2', workspaceId: 'wsp', meetingId: 'mtg_1', mediaAssetId: 'med_1', kind: 'summary', status, progress: 0.4, errorCode: null, errorMessage: null, createdAt: 't', startedAt: null, completedAt: null });

function client(routes: Parameters<typeof fakeFetch>[0]) {
  const fake = fakeFetch(routes);
  return { client: new HttpSummaryClient(new HttpTransport({ baseUrl: API, getAccessToken: () => 'token', fetch: fake.fetch })), requests: fake.requests };
}

describe('HttpSummaryClient', () => {
  test('remote capabilities with no local runtime', async () => {
    const { client: summaries } = client({});
    expect(summaries.capabilities.execution).toBe('remote');
    expect(summaries.capabilities.localRuntimePreflight).toBe(false);
    expect(summaries.capabilities.cancellation).toBe(true);
    expect(await summaries.checkRuntime()).toEqual({ ready: true });
    await expect(summaries.save()).rejects.toBeInstanceOf(UnsupportedOperationError);
  });

  test('maps a regenerating summary: state, progress, retained result and staleness', async () => {
    const { client: summaries } = client({
      'GET /api/v1/meetings/mtg_1/summary': () => json(200, { meetingId: 'mtg_1', state: 'generating', reason: null, stale: true, currentTranscriptId: 'trn_2', error: null, job: job('running'), summary }),
    });
    expect(await summaries.getMeetingSummary('mtg_1')).toEqual({
      meetingId: 'mtg_1', state: 'generating', reason: null, stale: true, error: null, jobId: 'job_2', progress: 0.4,
      summary: { id: 'sum_1', markdown: '**Summary**\n\nShip Friday.', title: 'Launch', provider: 'openai', model: 'gpt-4o-mini', templateId: 'standard_meeting', outputLanguage: null, transcriptId: 'trn_1', updatedAt: '2026-10-20T10:00:00Z' },
    });
    const state = await summaries.getState('mtg_1');
    expect(state.status).toBe('processing');
    expect(state.meetingName).toBe('Launch');
    expect(state.processId).toBe('job_2');
  });

  test('generate sends only template and context; provider fields from the shared request are ignored', async () => {
    const { client: summaries, requests } = client({
      'POST /api/v1/meetings/mtg_1/summary': () => json(202, { job: job('queued') }),
    });
    const result = await summaries.generate({ text: 'whole transcript', provider: 'openai', modelName: 'gpt', meetingId: 'mtg_1', chunkSize: 1, overlap: 1, customPrompt: 'Team sync', templateId: 'daily_standup', summaryLanguage: null });
    expect(result.processId).toBe('job_2');
    await summaries.generateSummary('mtg_1');
    expect(requests.map((r) => r.body)).toEqual([{ templateId: 'daily_standup', customPrompt: 'Team sync' }, {}]);
    expect(JSON.stringify(requests.map((r) => r.body))).not.toContain('whole transcript');
  });

  test('cancel reports whether a job was running', async () => {
    let running = true;
    const { client: summaries } = client({
      'POST /api/v1/meetings/mtg_1/summary/cancel': () => {
        if (!running) return json(409, { error: { code: 'conflict', message: 'Conflict', details: { reason: 'not_running' } } });
        running = false;
        return json(200, { job: job('cancelled') });
      },
    });
    expect(await summaries.cancel('mtg_1')).toEqual({ cancelled: true, message: 'Summary generation cancelled', meetingId: 'mtg_1' });
    expect(await summaries.cancelSummary('mtg_1')).toBe(false);
  });

  test('unavailable and failed states; templates', async () => {
    const { client: summaries } = client({
      'GET /api/v1/meetings/mtg_1/summary': () => json(200, { meetingId: 'mtg_1', state: 'unavailable', reason: 'credential_missing', stale: false, currentTranscriptId: null, error: null, job: null, summary: null }),
      'GET /api/v1/meetings/mtg_2/summary': () => json(200, { meetingId: 'mtg_2', state: 'failed', reason: null, stale: false, currentTranscriptId: 'trn_1', error: { code: 'provider_auth_failed', message: 'The summary provider rejected the configured credentials.' }, job: job('failed'), summary }),
      'GET /api/v1/summary-templates': () => json(200, { items: [{ id: 'standard_meeting', name: 'Standard Meeting Notes', description: 'd' }] }),
    });
    const unavailable = await summaries.getMeetingSummary('mtg_1');
    expect(unavailable.state).toBe('unavailable');
    expect(unavailable.reason).toBe('credential_missing');
    const failed = await summaries.getState('mtg_2');
    expect(failed.status).toBe('failed');
    expect(failed.error).toBe('The summary provider rejected the configured credentials.');
    expect(failed.data).toEqual(summary.content);
    expect(await summaries.listTemplates()).toEqual([{ id: 'standard_meeting', name: 'Standard Meeting Notes', description: 'd' }]);
  });
});
