import type { ApiErrorCode } from '@voxa/protocol';

/** A protocol error returned by the Voxa API (`{ error: { code, message } }`). */
export class VoxaApiError extends Error {
  readonly status: number;
  readonly code: ApiErrorCode;
  readonly requestId: string | null;
  readonly details: Record<string, unknown> | null;

  constructor(status: number, code: ApiErrorCode, message: string, requestId: string | null, details: Record<string, unknown> | null) {
    super(message);
    this.name = 'VoxaApiError';
    this.status = status;
    this.code = code;
    this.requestId = requestId;
    this.details = details;
  }
}

/** The API could not be reached, or answered with something that is not the protocol. */
export class VoxaTransportError extends Error {
  readonly kind: 'network' | 'invalid_response';
  readonly status: number | null;
  readonly requestId: string | null;

  constructor(kind: 'network' | 'invalid_response', message: string, status: number | null = null, requestId: string | null = null) {
    super(message);
    this.name = 'VoxaTransportError';
    this.kind = kind;
    this.status = status;
    this.requestId = requestId;
  }
}

const ERROR_CODES: readonly ApiErrorCode[] = ['validation', 'not_found', 'unauthorized', 'forbidden', 'conflict', 'unavailable', 'internal'];
const STATUS_CODES: Record<number, ApiErrorCode> = {
  400: 'validation', 401: 'unauthorized', 403: 'forbidden', 404: 'not_found', 409: 'conflict', 503: 'unavailable',
};

export type FetchLike = (input: string, init: RequestInit) => Promise<Response>;

export interface HttpTransportOptions {
  /** Absolute API origin (and optional path prefix), without a trailing slash. */
  baseUrl: string;
  /** Current bearer token, or null when unauthenticated. Called per request. */
  getAccessToken: () => string | null;
  /** Invoked on any 401 so the auth layer can recover (re-authenticate). */
  onUnauthorized?: (error: VoxaApiError) => void;
  fetch?: FetchLike;
}

export interface RequestOptions {
  body?: unknown;
  query?: Record<string, string | number | undefined>;
  signal?: AbortSignal;
}

/**
 * Minimal fetch-based client for the Voxa protocol. It never logs and never
 * places request bodies or tokens into errors; error messages come only from
 * the server's fixed, safe protocol messages.
 */
export class HttpTransport {
  private readonly options: HttpTransportOptions;
  private readonly fetchImpl: FetchLike;

  constructor(options: HttpTransportOptions) {
    this.options = options;
    this.fetchImpl = options.fetch ?? ((input, init) => globalThis.fetch(input, init));
  }

  get<T>(path: string, options: RequestOptions = {}): Promise<T> {
    return this.request<T>('GET', path, options);
  }
  post<T>(path: string, body: unknown, options: RequestOptions = {}): Promise<T> {
    return this.request<T>('POST', path, { ...options, body });
  }
  patch<T>(path: string, body: unknown, options: RequestOptions = {}): Promise<T> {
    return this.request<T>('PATCH', path, { ...options, body });
  }
  put<T>(path: string, body: unknown, options: RequestOptions = {}): Promise<T> {
    return this.request<T>('PUT', path, { ...options, body });
  }
  delete(path: string, options: RequestOptions = {}): Promise<void> {
    return this.request<void>('DELETE', path, options);
  }

  async request<T>(method: string, path: string, options: RequestOptions = {}): Promise<T> {
    const url = new URL(this.options.baseUrl + path);
    for (const [key, value] of Object.entries(options.query ?? {})) {
      if (value !== undefined) url.searchParams.set(key, String(value));
    }
    const headers: Record<string, string> = { Accept: 'application/json' };
    const token = this.options.getAccessToken();
    if (token) headers.Authorization = `Bearer ${token}`;
    if (options.body !== undefined) headers['Content-Type'] = 'application/json';

    let response: Response;
    try {
      response = await this.fetchImpl(url.toString(), {
        method,
        headers,
        body: options.body === undefined ? undefined : JSON.stringify(options.body),
        signal: options.signal,
        // Bearer tokens only: no cookies, no cached responses, and no
        // following redirects to another origin with the Authorization header.
        credentials: 'omit',
        cache: 'no-store',
        redirect: 'error',
        mode: 'cors',
      });
    } catch (error) {
      if (isAbort(error, options.signal)) throw error;
      throw new VoxaTransportError('network', 'The Voxa server could not be reached');
    }

    const requestId = response.headers.get('X-Request-ID');
    if (!response.ok) {
      const apiError = await decodeError(response, requestId);
      if (apiError.status === 401) this.options.onUnauthorized?.(apiError);
      throw apiError;
    }
    if (response.status === 204) return undefined as T;
    try {
      return (await response.json()) as T;
    } catch (error) {
      if (isAbort(error, options.signal)) throw error;
      throw new VoxaTransportError('invalid_response', 'The Voxa server returned an unreadable response', response.status, requestId);
    }
  }
}

function isAbort(error: unknown, signal?: AbortSignal): boolean {
  return Boolean(signal?.aborted) || (error instanceof DOMException && error.name === 'AbortError');
}

async function decodeError(response: Response, requestId: string | null): Promise<VoxaApiError> {
  let payload: unknown = null;
  try {
    payload = await response.json();
  } catch {
    // Non-JSON error bodies (proxies, gateways) fall back to the status mapping.
  }
  const body = (payload as { error?: Record<string, unknown> } | null)?.error;
  const code = typeof body?.code === 'string' && (ERROR_CODES as readonly string[]).includes(body.code)
    ? (body.code as ApiErrorCode)
    : STATUS_CODES[response.status] ?? 'internal';
  const message = typeof body?.message === 'string' ? body.message : `Request failed with status ${response.status}`;
  const details = body?.details && typeof body.details === 'object' ? (body.details as Record<string, unknown>) : null;
  const id = typeof body?.requestId === 'string' ? body.requestId : requestId;
  return new VoxaApiError(response.status, code, message, id, details);
}
