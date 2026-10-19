import { describe, expect, test } from 'bun:test';
import { HttpMeetingClient } from '../../src/clients/http/meeting-client';
import { HttpTransport } from '../../src/clients/http/transport';
import { UnsupportedOperationError } from '../../src/clients/errors';
import { API, fakeFetch, json } from './http-fake';

const meeting = (id: string, title = `Meeting ${id}`) => ({
  id, workspaceId: 'wsp_hidden', title, status: 'created',
  createdAt: '2026-10-19T10:00:00.000001Z', updatedAt: '2026-10-19T11:00:00Z',
});

function client(routes: Parameters<typeof fakeFetch>[0]) {
  const fake = fakeFetch(routes);
  return { client: new HttpMeetingClient(new HttpTransport({ baseUrl: API, getAccessToken: () => 't', fetch: fake.fetch })), requests: fake.requests };
}

describe('HttpMeetingClient', () => {
  test('follows cursor pagination and hides workspace ids', async () => {
    const { client: meetings, requests } = client({
      'GET /api/v1/meetings': ({ url }) => url.searchParams.get('cursor') === 'page-2'
        ? json(200, { items: [meeting('m3')], page: { nextCursor: null, hasMore: false } })
        : json(200, { items: [meeting('m1'), meeting('m2')], page: { nextCursor: 'page-2', hasMore: true } }),
    });
    expect(await meetings.list()).toEqual([
      { id: 'm1', title: 'Meeting m1' }, { id: 'm2', title: 'Meeting m2' }, { id: 'm3', title: 'Meeting m3' },
    ]);
    expect(requests.map((r) => r.url.searchParams.get('cursor'))).toEqual([null, 'page-2']);
    expect(requests.every((r) => r.url.searchParams.get('limit') === '100')).toBe(true);
    expect(JSON.stringify(await meetings.list())).not.toContain('wsp_');
  });

  test('maps get/metadata to the shared shape without transcripts', async () => {
    const { client: meetings } = client({ 'GET /api/v1/meetings/m%201': () => json(200, { meeting: meeting('m 1', 'Spaced') }) });
    expect(await meetings.getMetadata('m 1')).toEqual({
      id: 'm 1', title: 'Spaced', created_at: '2026-10-19T10:00:00.000001Z', updated_at: '2026-10-19T11:00:00Z',
    });
    const details = await meetings.get('m 1');
    expect(details.transcripts).toEqual([]);
    expect(await meetings.getTranscripts()).toEqual({ transcripts: [], total_count: 0, has_more: false });
    expect(await meetings.searchTranscripts()).toEqual([]);
    expect(meetings.capabilities).toEqual({ transcripts: false, transcriptSearch: false, createWithTranscripts: false });
  });

  test('create, rename and delete use protocol requests', async () => {
    const { client: meetings, requests } = client({
      'POST /api/v1/meetings': () => json(201, { meeting: meeting('m9', 'New') }),
      'PATCH /api/v1/meetings/m9': () => json(200, { meeting: meeting('m9', 'Renamed') }),
      'DELETE /api/v1/meetings/m9': () => new Response(null, { status: 204 }),
    });
    expect(await meetings.create({ title: 'New', transcripts: [], folderPath: null })).toEqual({ meeting_id: 'm9' });
    await meetings.rename('m9', 'Renamed');
    await meetings.delete('m9');
    expect(requests.map((r) => [r.method, r.url.pathname, r.body])).toEqual([
      ['POST', '/api/v1/meetings', { title: 'New' }],
      ['PATCH', '/api/v1/meetings/m9', { title: 'Renamed' }],
      ['DELETE', '/api/v1/meetings/m9', undefined],
    ]);
  });

  test('refuses desktop-only creation inputs instead of dropping them', async () => {
    const { client: meetings, requests } = client({});
    await expect(meetings.create({ title: 'x', transcripts: [{} as never], folderPath: null })).rejects.toBeInstanceOf(UnsupportedOperationError);
    await expect(meetings.create({ title: 'x', transcripts: [], folderPath: '/tmp/x' })).rejects.toBeInstanceOf(UnsupportedOperationError);
    expect(requests).toHaveLength(0);
  });
});
