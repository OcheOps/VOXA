/**
 * Raised when a runtime does not implement an operation (for example a
 * desktop-only filesystem action in the web runtime). UI should consult
 * client capabilities first; this error is the safety net, never a crash.
 */
export class UnsupportedOperationError extends Error {
  readonly operation: string;

  constructor(operation: string) {
    super(`${operation} is not available in this runtime`);
    this.name = 'UnsupportedOperationError';
    this.operation = operation;
  }
}

export function isUnsupportedOperation(error: unknown): error is UnsupportedOperationError {
  return error instanceof UnsupportedOperationError;
}
