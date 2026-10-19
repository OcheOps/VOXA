import type { CapabilitiesResponse, MeResponse } from '@voxa/protocol';
import type { HttpTransport } from './transport';

export type CurrentIdentity = MeResponse;
export type ServerCapabilities = CapabilitiesResponse;

/**
 * The authenticated caller and the server's advertised features. The `/me`
 * response is authoritative for user, workspace and role; token claims are
 * never used as application identity.
 */
export class HttpIdentityClient {
  constructor(private readonly transport: HttpTransport) {}

  me(signal?: AbortSignal): Promise<CurrentIdentity> {
    return this.transport.get<MeResponse>('/api/v1/me', { signal });
  }

  capabilities(signal?: AbortSignal): Promise<ServerCapabilities> {
    return this.transport.get<CapabilitiesResponse>('/api/v1/capabilities', { signal });
  }
}
