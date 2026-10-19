export interface RecordedRequest {
  url: URL;
  method: string;
  headers: Record<string, string>;
  body: unknown;
  init: RequestInit;
}

type Responder = (request: RecordedRequest) => Response | Promise<Response>;

/** A scripted fetch that records requests and answers from a route table. */
export function fakeFetch(routes: Record<string, Responder>) {
  const requests: RecordedRequest[] = [];
  const fetch = async (input: string, init: RequestInit): Promise<Response> => {
    const url = new URL(input);
    const request: RecordedRequest = {
      url,
      method: init.method ?? 'GET',
      headers: { ...(init.headers as Record<string, string>) },
      body: typeof init.body === 'string' ? JSON.parse(init.body) : undefined,
      init,
    };
    requests.push(request);
    const responder = routes[`${request.method} ${url.pathname}`];
    if (!responder) return json(404, { error: { code: 'not_found', message: 'Resource not found' } });
    return responder(request);
  };
  return { fetch, requests };
}

export function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });
}

export const API = 'https://api.voxa.test';
