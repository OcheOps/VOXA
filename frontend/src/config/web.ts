/**
 * Browser-runtime configuration from public build-time variables. Only
 * non-secret values belong here: never database URLs, encryption keys or
 * OIDC client secrets (public browser clients have none).
 */
export type BearerTokenSource = 'access_token' | 'id_token';

export type WebAuthConfig =
  | { mode: 'development' }
  | {
      mode: 'oidc';
      issuer: string;
      clientId: string;
      scope: string;
      /** Absolute callback URL; defaults to `<origin>/auth/callback`. */
      redirectUri: string;
      /** Which token is sent to the Voxa API as the bearer credential. */
      bearer: BearerTokenSource;
    };

export interface WebConfig {
  apiBaseUrl: string;
  auth: WebAuthConfig;
}

export class WebConfigError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'WebConfigError';
  }
}

export interface WebEnvironment {
  NEXT_PUBLIC_VOXA_API_URL?: string;
  NEXT_PUBLIC_VOXA_AUTH_MODE?: string;
  NEXT_PUBLIC_VOXA_OIDC_ISSUER?: string;
  NEXT_PUBLIC_VOXA_OIDC_CLIENT_ID?: string;
  NEXT_PUBLIC_VOXA_OIDC_SCOPE?: string;
  NEXT_PUBLIC_VOXA_OIDC_REDIRECT_URI?: string;
  NEXT_PUBLIC_VOXA_OIDC_BEARER?: string;
}

// Each variable is written out literally so Next.js inlines it at build time.
export function buildEnvironment(): WebEnvironment {
  return {
    NEXT_PUBLIC_VOXA_API_URL: process.env.NEXT_PUBLIC_VOXA_API_URL,
    NEXT_PUBLIC_VOXA_AUTH_MODE: process.env.NEXT_PUBLIC_VOXA_AUTH_MODE,
    NEXT_PUBLIC_VOXA_OIDC_ISSUER: process.env.NEXT_PUBLIC_VOXA_OIDC_ISSUER,
    NEXT_PUBLIC_VOXA_OIDC_CLIENT_ID: process.env.NEXT_PUBLIC_VOXA_OIDC_CLIENT_ID,
    NEXT_PUBLIC_VOXA_OIDC_SCOPE: process.env.NEXT_PUBLIC_VOXA_OIDC_SCOPE,
    NEXT_PUBLIC_VOXA_OIDC_REDIRECT_URI: process.env.NEXT_PUBLIC_VOXA_OIDC_REDIRECT_URI,
    NEXT_PUBLIC_VOXA_OIDC_BEARER: process.env.NEXT_PUBLIC_VOXA_OIDC_BEARER,
  };
}

function isLoopback(hostname: string): boolean {
  return hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '[::1]';
}

function absoluteURL(name: string, value: string | undefined, requireHttpsOutsideLoopback: boolean): URL {
  let url: URL;
  try {
    url = new URL((value ?? '').trim());
  } catch {
    throw new WebConfigError(`${name} must be an absolute URL`);
  }
  if (url.protocol !== 'https:' && url.protocol !== 'http:') {
    throw new WebConfigError(`${name} must use http or https`);
  }
  if (url.username || url.password || url.search || url.hash) {
    throw new WebConfigError(`${name} must not contain credentials, a query or a fragment`);
  }
  if (requireHttpsOutsideLoopback && url.protocol === 'http:' && !isLoopback(url.hostname)) {
    throw new WebConfigError(`${name} must use https (http is allowed only for localhost)`);
  }
  return url;
}

export function readWebConfig(env: WebEnvironment, origin: string): WebConfig {
  const api = absoluteURL('NEXT_PUBLIC_VOXA_API_URL', env.NEXT_PUBLIC_VOXA_API_URL, false);
  const apiBaseUrl = api.toString().replace(/\/+$/, '');
  const mode = (env.NEXT_PUBLIC_VOXA_AUTH_MODE ?? 'oidc').trim();

  if (mode === 'development') {
    return { apiBaseUrl, auth: { mode: 'development' } };
  }
  if (mode !== 'oidc') {
    throw new WebConfigError('NEXT_PUBLIC_VOXA_AUTH_MODE must be "oidc" or "development"');
  }
  // Validated as a URL, but passed through verbatim: it must match the
  // server's VOXA_OIDC_ISSUER and the token "iss" exactly (URL parsing may
  // add a trailing slash).
  absoluteURL('NEXT_PUBLIC_VOXA_OIDC_ISSUER', env.NEXT_PUBLIC_VOXA_OIDC_ISSUER, true);
  const issuer = env.NEXT_PUBLIC_VOXA_OIDC_ISSUER!.trim();
  const clientId = (env.NEXT_PUBLIC_VOXA_OIDC_CLIENT_ID ?? '').trim();
  if (!clientId) {
    throw new WebConfigError('NEXT_PUBLIC_VOXA_OIDC_CLIENT_ID is required');
  }
  const scope = (env.NEXT_PUBLIC_VOXA_OIDC_SCOPE ?? 'openid profile email').trim();
  if (!scope.split(/\s+/).includes('openid')) {
    throw new WebConfigError('NEXT_PUBLIC_VOXA_OIDC_SCOPE must include "openid"');
  }
  const redirectUri = env.NEXT_PUBLIC_VOXA_OIDC_REDIRECT_URI
    ? absoluteURL('NEXT_PUBLIC_VOXA_OIDC_REDIRECT_URI', env.NEXT_PUBLIC_VOXA_OIDC_REDIRECT_URI, true).toString()
    : `${origin}/auth/callback`;
  const bearer = (env.NEXT_PUBLIC_VOXA_OIDC_BEARER ?? 'access_token').trim();
  if (bearer !== 'access_token' && bearer !== 'id_token') {
    throw new WebConfigError('NEXT_PUBLIC_VOXA_OIDC_BEARER must be "access_token" or "id_token"');
  }
  return { apiBaseUrl, auth: { mode: 'oidc', issuer, clientId, scope, redirectUri, bearer } };
}
