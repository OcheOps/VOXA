/**
 * Holds the current bearer token in memory only. It is never written to
 * localStorage/sessionStorage, so it disappears with the page and is not
 * readable by other tabs. An expired token is never returned.
 */
export class InMemoryTokenStore {
  private token: string | null = null;
  private expiresAtMs: number | null = null;

  constructor(private readonly now: () => number = Date.now) {}

  set(token: string, expiresAtSeconds?: number): void {
    this.token = token;
    this.expiresAtMs = expiresAtSeconds ? expiresAtSeconds * 1000 : null;
  }

  clear(): void {
    this.token = null;
    this.expiresAtMs = null;
  }

  get(): string | null {
    if (this.token && this.expiresAtMs !== null && this.now() >= this.expiresAtMs) {
      this.clear();
    }
    return this.token;
  }
}
