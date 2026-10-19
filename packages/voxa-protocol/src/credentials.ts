export interface CredentialStatus { provider: string; configured: boolean; maskedValue?: '••••'; }
export interface CredentialWriteRequest { value: string; }

export function createCredentialStatus(provider: string, secret: string | null): CredentialStatus {
  return secret ? { provider, configured: true, maskedValue: '••••' } : { provider, configured: false };
}
