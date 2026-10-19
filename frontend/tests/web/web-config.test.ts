import { describe, expect, test } from 'bun:test';
import { DEFAULT_RECORDING_NOTICE, readWebConfig, WebConfigError } from '../../src/config/web';

const ORIGIN = 'https://voxa.example.com';
const oidc = {
  NEXT_PUBLIC_VOXA_API_URL: 'https://api.voxa.example.com/',
  NEXT_PUBLIC_VOXA_OIDC_ISSUER: 'https://id.example.com/realms/voxa',
  NEXT_PUBLIC_VOXA_OIDC_CLIENT_ID: 'voxa-web',
};

describe('readWebConfig', () => {
  test('defaults to OIDC with a derived callback and access-token bearer', () => {
    expect(readWebConfig(oidc, ORIGIN)).toEqual({
      apiBaseUrl: 'https://api.voxa.example.com',
      auth: {
        mode: 'oidc', issuer: 'https://id.example.com/realms/voxa', clientId: 'voxa-web',
        scope: 'openid profile email', redirectUri: `${ORIGIN}/auth/callback`, bearer: 'access_token',
      },
      recordingNotice: DEFAULT_RECORDING_NOTICE,
    });
  });

  test('keeps the issuer verbatim so it matches the server exactly', () => {
    const config = readWebConfig({ ...oidc, NEXT_PUBLIC_VOXA_OIDC_ISSUER: 'https://id.example.com' }, ORIGIN);
    expect(config.auth.mode === 'oidc' && config.auth.issuer).toBe('https://id.example.com');
  });

  test('development mode needs only the API URL', () => {
    expect(readWebConfig({ NEXT_PUBLIC_VOXA_API_URL: 'http://localhost:8080', NEXT_PUBLIC_VOXA_AUTH_MODE: 'development' }, ORIGIN))
      .toEqual({ apiBaseUrl: 'http://localhost:8080', auth: { mode: 'development' }, recordingNotice: DEFAULT_RECORDING_NOTICE });
    expect(readWebConfig({ NEXT_PUBLIC_VOXA_API_URL: 'http://localhost:8080', NEXT_PUBLIC_VOXA_AUTH_MODE: 'development', NEXT_PUBLIC_VOXA_RECORDING_NOTICE: ' Tell everyone. ' }, ORIGIN).recordingNotice)
      .toBe('Tell everyone.');
  });

  test('rejects unsafe or incomplete configuration', () => {
    const invalid = [
      {},
      { ...oidc, NEXT_PUBLIC_VOXA_API_URL: 'ftp://api' },
      { ...oidc, NEXT_PUBLIC_VOXA_API_URL: 'https://user:pw@api.example.com' },
      { ...oidc, NEXT_PUBLIC_VOXA_AUTH_MODE: 'none' },
      { ...oidc, NEXT_PUBLIC_VOXA_OIDC_ISSUER: 'http://id.example.com' },
      { ...oidc, NEXT_PUBLIC_VOXA_OIDC_CLIENT_ID: ' ' },
      { ...oidc, NEXT_PUBLIC_VOXA_OIDC_SCOPE: 'profile email' },
      { ...oidc, NEXT_PUBLIC_VOXA_OIDC_BEARER: 'refresh_token' },
      { ...oidc, NEXT_PUBLIC_VOXA_OIDC_REDIRECT_URI: 'http://evil.example.com/cb' },
    ];
    for (const env of invalid) {
      expect(() => readWebConfig(env, ORIGIN)).toThrow(WebConfigError);
    }
  });

  test('allows plain http only for loopback development', () => {
    const config = readWebConfig({ ...oidc, NEXT_PUBLIC_VOXA_OIDC_ISSUER: 'http://localhost:8081/realms/dev' }, 'http://localhost:3118');
    expect(config.auth.mode === 'oidc' && config.auth.redirectUri).toBe('http://localhost:3118/auth/callback');
  });
});
