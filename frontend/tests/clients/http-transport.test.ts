import { describe, expect, test } from 'bun:test';
import { HttpTransport, VoxaApiError, VoxaTransportError } from '../../src/clients/http/transport';
import { API, fakeFetch, json } from './http-fake';

const SECRET = 'sk-WEB-CANARY-1234';
const TOKEN = 'eyJhbGciOi.canary-token.sig';

describe('HttpTransport', () => {
  test('sends bearer token, JSON body and safe fetch options', async () => {
    const { fetch, requests } = fakeFetch({ 'POST /api/v1/meetings': () => json(201, { ok: true }) });
    const transport = new HttpTransport({ baseUrl: API, getAccessToken: () => TOKEN, fetch });
    await transport.post('/api/v1/meetings', { title: 'Planning' });
    const [request] = requests;
    expect(request.headers.Authorization).toBe(`Bearer ${TOKEN}`);
    expect(request.headers['Content-Type']).toBe('application/json');
    expect(request.body).toEqual({ title: 'Planning' });
    expect(request.init.credentials).toBe('omit');
    expect(request.init.redirect).toBe('error');
    expect(request.init.cache).toBe('no-store');
    expect(request.url.search).toBe('');
  });

  test('omits Authorization when there is no session and encodes query parameters', async () => {
    const { fetch, requests } = fakeFetch({ 'GET /api/v1/meetings': () => json(200, {}) });
    const transport = new HttpTransport({ baseUrl: API, getAccessToken: () => null, fetch });
    await transport.get('/api/v1/meetings', { query: { limit: 10, cursor: 'a b&c', skip: undefined } });
    expect(requests[0].headers.Authorization).toBeUndefined();
    expect(requests[0].url.searchParams.get('cursor')).toBe('a b&c');
    expect(requests[0].url.searchParams.has('skip')).toBe(false);
  });

  test('translates protocol errors with request id and details', async () => {
    const { fetch } = fakeFetch({
      'GET /api/v1/me': () => json(409, { error: { code: 'conflict', message: 'Resource conflict', details: { reason: 'workspace_selection_required' }, requestId: 'req-9' } }),
    });
    const transport = new HttpTransport({ baseUrl: API, getAccessToken: () => TOKEN, fetch });
    const error = await transport.get('/api/v1/me').then((): any => undefined, (e: any) => e);
    expect(error).toBeInstanceOf(VoxaApiError);
    expect(error.status).toBe(409);
    expect(error.code).toBe('conflict');
    expect(error.requestId).toBe('req-9');
    expect(error.details).toEqual({ reason: 'workspace_selection_required' });
  });

  test('401 notifies the auth layer and still throws', async () => {
    const { fetch } = fakeFetch({ 'GET /api/v1/me': () => json(401, { error: { code: 'unauthorized', message: 'Authentication required' } }, { 'X-Request-ID': 'req-1' }) });
    const seen: VoxaApiError[] = [];
    const transport = new HttpTransport({ baseUrl: API, getAccessToken: () => TOKEN, fetch, onUnauthorized: (e) => seen.push(e) });
    await expect(transport.get('/api/v1/me')).rejects.toBeInstanceOf(VoxaApiError);
    expect(seen).toHaveLength(1);
    expect(seen[0].requestId).toBe('req-1');
  });

  test('non-protocol error bodies fall back to status mapping', async () => {
    const { fetch } = fakeFetch({ 'GET /api/v1/me': () => new Response('<html>Bad gateway</html>', { status: 503 }) });
    const error = await new HttpTransport({ baseUrl: API, getAccessToken: () => null, fetch }).get('/api/v1/me').then((): any => undefined, (e: any) => e);
    expect(error).toBeInstanceOf(VoxaApiError);
    expect(error.code).toBe('unavailable');
  });

  test('malformed success bodies and network failures are transport errors', async () => {
    const { fetch } = fakeFetch({ 'GET /api/v1/me': () => new Response('not json', { status: 200 }) });
    const malformed = await new HttpTransport({ baseUrl: API, getAccessToken: () => null, fetch }).get('/api/v1/me').then((): any => undefined, (e: any) => e);
    expect(malformed).toBeInstanceOf(VoxaTransportError);
    expect(malformed.kind).toBe('invalid_response');

    const offline = new HttpTransport({ baseUrl: API, getAccessToken: () => null, fetch: async () => { throw new TypeError('Failed to fetch'); } });
    const network = await offline.get('/api/v1/me').then((): any => undefined, (e: any) => e);
    expect(network).toBeInstanceOf(VoxaTransportError);
    expect(network.kind).toBe('network');
  });

  test('propagates cancellation as an AbortError', async () => {
    const transport = new HttpTransport({
      baseUrl: API,
      getAccessToken: () => null,
      fetch: (_input, init) => new Promise((_resolve, reject) => {
        init.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')));
      }),
    });
    const controller = new AbortController();
    const pending = transport.get('/api/v1/meetings', { signal: controller.signal });
    controller.abort();
    const error = await pending.then((): any => undefined, (e: any) => e);
    expect(error).toBeInstanceOf(DOMException);
    expect(error.name).toBe('AbortError');
  });

  test('never places tokens or request bodies in errors', async () => {
    const { fetch } = fakeFetch({ 'PUT /api/v1/credentials/openai': () => json(500, { error: { code: 'internal', message: 'Internal server error' } }) });
    const error = await new HttpTransport({ baseUrl: API, getAccessToken: () => TOKEN, fetch })
      .put('/api/v1/credentials/openai', { value: SECRET })
      .then((): any => undefined, (e: any) => e);
    const rendered = `${error} ${error.message} ${JSON.stringify(error)} ${error.stack ?? ''}`;
    expect(rendered).not.toContain(SECRET);
    expect(rendered).not.toContain(TOKEN);
  });
});
