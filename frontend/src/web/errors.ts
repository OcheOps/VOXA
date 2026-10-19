import { isUnsupportedOperation } from '@/clients/errors';
import { VoxaApiError, VoxaTransportError } from '@/clients/http/transport';

/**
 * User-facing description of a failure. Uses only the server's fixed, safe
 * protocol messages and request IDs, never raw error text that could carry
 * request data.
 */
export function describeError(error: unknown): string {
  if (error instanceof VoxaApiError) {
    if (error.code === 'validation') {
      const field = typeof error.details?.field === 'string' ? error.details.field : null;
      const reason = typeof error.details?.reason === 'string' ? error.details.reason : null;
      if (field && reason) return `${field} ${reason}`;
    }
    return error.requestId ? `${error.message} (request ${error.requestId})` : error.message;
  }
  if (error instanceof VoxaTransportError) return error.message;
  if (isUnsupportedOperation(error)) return error.message;
  return 'Unexpected error';
}
