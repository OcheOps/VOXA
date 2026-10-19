# Voxa server

A single Go process that implements the `voxa-protocol` v1 HTTP contract.
When `VOXA_DATABASE_URL` is set, meetings, workspace configuration and
provider credentials persist in PostgreSQL. Credentials are stored
**AES-256-GCM encrypted** with a key that never enters the database.
Without a database URL, everything runs on ephemeral in-memory adapters
(development only). Requests are authenticated with OIDC bearer tokens and
scoped to the caller's workspace membership, or, in explicit development
mode, to one fixed workspace. This is not yet a media or processing service.

Design rationale:
[`docs/server-authentication.md`](../docs/server-authentication.md) (OIDC,
bootstrap, membership, roles, endpoints),
[`docs/server-persistence.md`](../docs/server-persistence.md) (schema,
migrations, pagination, atomicity, outage classification) and
[`docs/server-credential-security.md`](../docs/server-credential-security.md)
(encryption, AAD, keys, rotation, threat model).

## Architecture

```text
net/http middleware and handlers
  -> application services
  -> repository interfaces
  -> memory adapters | PostgreSQL adapters (chosen at startup)
```

- `cmd/voxa` wires the process. `storage.go` selects the backend, runs
  migrations, and bootstraps the workspace. `migrate.go` provides the
  `voxa migrate` command.
- `internal/api` owns protocol-v1 JSON DTOs, routing, strict decoding, API
  errors, request IDs, and access logging.
- `internal/application` owns meeting/configuration/credential use cases,
  workspace scope, validation, PATCH semantics, and opaque cursors.
- `internal/domain` contains storage-neutral server models.
- `internal/repository` declares persistence ports without SQL concepts.
  `repository/repositorytest` holds the contract tests every adapter must
  pass.
- `internal/adapters/memory` provides ephemeral, mutex-protected adapters.
- `internal/adapters/postgres` provides pgx/v5 adapters (including the
  encrypted credential repository), the connection pool, workspace bootstrap,
  outage classification, and the migration runner.
- `internal/auth` provides the `Authenticator` implementations
  (generic OIDC bearer validation, development identity) and `authtest`, a
  local OIDC issuer for tests. `internal/application/access.go` holds the
  workspace resolvers (membership and bootstrap, development).
- `internal/credentialcrypto` provides AES-256-GCM sealing, AAD
  construction, and the `KeyRing` abstraction.
- `migrations/` holds versioned `NNNN_description.sql` files, embedded into
  the binary.

The TypeScript package `packages/voxa-protocol` and
`docs/server-protocol-v1.md` remain the protocol source of truth. No
filesystem path is part of a meeting response.

### Dependencies

| Module | Why |
| --- | --- |
| `github.com/jackc/pgx/v5` | PostgreSQL driver and `pgxpool` connection pool. No ORM or query generator. |
| `github.com/jackc/puddle/v2` | pgx's pool implementation; imported directly only to recognize a closed pool as an outage. |
| `github.com/coreos/go-oidc/v3` | OIDC discovery, JWKS caching and rotation, JWT signature/`iss`/`aud`/`exp`/`nbf` validation. Actively maintained (v3.21.0, 2026-09). |
| `github.com/go-jose/go-jose/v4` | JOSE implementation used by go-oidc. Imported directly only by the `authtest` test issuer. |
| `golang.org/x/oauth2` | Indirect dependency of go-oidc. |
| `jackc/pgpassfile`, `jackc/pgservicefile`, `golang.org/x/sync`, `golang.org/x/text` | Indirect dependencies of pgx. |

Encryption uses only the Go standard library (`crypto/aes`,
`crypto/cipher`, `crypto/rand`).

The module requires Go 1.25 or newer, because pgx v5.10 requires it.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `VOXA_LISTEN_ADDR` | `:8080` | HTTP listen address. |
| `VOXA_AUTH_MODE` | **required** | `oidc` (anything others can reach) or `development` (local only: every request acts as the dev workspace owner). No default. |
| `VOXA_OIDC_ISSUER` | — | `oidc` mode: issuer URL, exact match with discovery and `iss`. https (http only for loopback). |
| `VOXA_OIDC_AUDIENCE` | — | `oidc` mode: required `aud` value (client ID or API audience). |
| `VOXA_BOOTSTRAP_OIDC_SUBJECT` | unset | `oidc` mode: only this `sub` may bootstrap the deployment (recommended). |
| `VOXA_BOOTSTRAP_WORKSPACE_ID` | unset | `oidc` mode: bootstrap adopts this workspace (e.g. `workspace-dev`, to keep development data). |
| `VOXA_DEV_WORKSPACE_ID` | `workspace-dev` | `development` mode only: the fixed workspace. Ignored in `oidc` mode. |
| `VOXA_ALLOWED_ORIGINS` | unset | Comma-separated exact browser origins allowed to call the API cross-origin (Voxa Web), e.g. `https://voxa.example.com,http://localhost:3118`. Unset means no CORS headers at all. `*` is rejected. See `docs/voxa-web.md`. |
| `VOXA_DATABASE_URL` | unset | PostgreSQL connection string (URL or key/value). Set: PostgreSQL adapters. Unset: memory adapters. |
| `VOXA_CREDENTIAL_KEY` | unset | **Required with PostgreSQL.** Base64 (standard, padded) of exactly 32 random bytes. Never logged. |
| `VOXA_CREDENTIAL_KEY_VERSION` | unset | **Required with PostgreSQL.** Non-secret identifier stored with each credential, e.g. `2026-10`. Pattern `[A-Za-z0-9][A-Za-z0-9._-]{0,31}`. |
| `VOXA_CREDENTIAL_PREVIOUS_KEYS` | unset | Optional decrypt-only keys for rotation: `version:base64,version:base64`. |

The database URL is never logged. Startup logs only the host and database
name. libpq-style parameters such as `sslmode`, `connect_timeout` and
`pool_max_conns` are honored. Without `connect_timeout`, each dial times out
after 5s.

## Startup behavior

`VOXA_AUTH_MODE` is validated first. A missing or invalid mode, missing
OIDC settings, or `oidc` without `VOXA_DATABASE_URL` exits with status 2.

Without `VOXA_DATABASE_URL` (development only), the server logs a warning
that all data is ephemeral and starts on memory adapters.

With `VOXA_DATABASE_URL`, these steps run in order, and any failure exits
non-zero before the listener opens (steps 2–5 share a 30s budget):

1. Validate and load the credential key ring. A missing or invalid key
   stops startup here, before any database connection. The server never
   falls back to plaintext in-memory credentials.
2. Parse the URL and connect. Verify connectivity with a ping.
3. Apply pending migrations under an advisory lock, logging each one.
4. In `development` mode, ensure the `VOXA_DEV_WORKSPACE_ID` row exists in
   `workspaces`, and log whether it was created.
5. Check the key versions referenced by stored credentials. Warn about any
   version that isn't loaded, because those rows can't be decrypted until
   the key is supplied.
6. Log `credential_backend=postgres-encrypted`, `active_key_version` and
   `loaded_key_versions`. Key versions are non-secret metadata. Keys,
   ciphertexts, nonces and the database URL are never logged.
7. Set up auth. `development` logs a loud warning. `oidc` runs provider
   discovery (15s timeout; failure stops startup) and logs the issuer,
   audience and whether the deployment is initialized. While it isn't, a
   warning says the first authenticated identity will become owner.
8. Serve HTTP.

On SIGINT/SIGTERM the server stops accepting connections, drains in-flight
requests for up to 10s, and then closes the pool.

### Health and readiness

- `GET /api/v1/health` (protocol) reports process liveness only. It is
  unchanged.
- `GET /readyz` (operational, not part of the protocol) returns `200
  {"status":"ready","storage":"postgres"}` when the pool answers a ping
  within 2s, and `503 {"status":"unavailable",...}` otherwise, with no
  connection details. In memory mode it always returns ready. A process
  with invalid key configuration never starts, so it never reports ready.

### Errors and logging

Database outages (connect failures, timeouts, a closed pool, server
shutdown and connection-class SQLSTATEs) return the protocol's `unavailable`
error with HTTP 503. Validation, not-found and conflict errors are never
reclassified, and unclassifiable failures stay `internal` (500). Every 500
and 503 is logged as `msg: "request failed"` with request ID, method, route
template, operation, public code, category, workspace ID, provider (when
relevant) and a sanitized error chain. Request bodies, headers, query
strings, secrets, keys and SQL parameters are never logged.

### Authentication and workspaces

Public: `GET /api/v1/health`, `GET /api/v1/capabilities`, `GET /readyz`.
Everything else (`/api/v1/me`, meetings, configuration, credentials) needs
`Authorization: Bearer <token>`, and resolves to exactly one workspace
membership:

- invalid or missing token: `401`;
- unknown identity or no membership: `403`;
- several memberships: `409 workspace_selection_required`;
- identity provider keys unreachable: `503`.

The first identity to authenticate against an uninitialized deployment
becomes owner of the default workspace. Later identities get no access
until an operator grants it (see the SQL in
`docs/server-authentication.md`). Clients cannot choose or create
workspaces. Every response sends `nosniff`, `no-store` and
`Referrer-Policy: no-referrer`. CORS headers are sent only for origins
listed in `VOXA_ALLOWED_ORIGINS`: exact origins, no credentials mode,
preflight answered before authentication.

## Migrations

Migrations run automatically at server startup. They can also be run
explicitly, for example as a deploy step before the server starts:

```sh
VOXA_DATABASE_URL=... go run ./cmd/voxa migrate          # apply pending (same as `migrate up`)
VOXA_DATABASE_URL=... go run ./cmd/voxa migrate status   # list applied/pending, read-only
```

Applied state is tracked in `schema_migrations`:

```sql
SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version;
```

Rules:

- Add a new file `NNNN_description.sql` with the next version number.
  **Never edit an applied migration.** Startup rejects checksum mismatches.
- Each file runs in its own transaction with its tracking row. If one fails,
  only that migration rolls back and the run stops.
- A binary that sees an applied version it does not know refuses to start.
- **Rollback is intentionally deferred.** There are no down migrations. Fix
  forward with a new migration, or restore from backup.

## Local PostgreSQL

Docker is optional and only provides the database. Generate your own
credential key; the repository deliberately contains none:

```sh
cd server
docker compose up -d postgres        # postgres:17, 127.0.0.1:55432, named volume
export VOXA_DATABASE_URL='postgres://voxa:voxa-dev@127.0.0.1:55432/voxa?sslmode=disable'
export VOXA_CREDENTIAL_KEY="$(openssl rand -base64 32)"   # keep it: losing it loses stored credentials
export VOXA_CREDENTIAL_KEY_VERSION=dev-1
export VOXA_AUTH_MODE=development   # or oidc + VOXA_OIDC_ISSUER/VOXA_OIDC_AUDIENCE
go run ./cmd/voxa
docker compose down                  # keep data;  `down -v` deletes the volume
```

With an existing local PostgreSQL install:

```sh
createuser --pwprompt voxa
createdb --owner voxa voxa
export VOXA_DATABASE_URL='postgres://voxa:PASSWORD@localhost:5432/voxa'
```

## Tests

```sh
cd server
gofmt -l .
go vet ./...
go test ./...          # unit + memory contract tests; no database needed
go test -race ./...
```

Unit tests include OIDC validation against a local in-process issuer
(discovery, JWKS, key rotation, provider outage; no external provider),
HTTP auth/membership/isolation tests, bootstrap concurrency tests,
AES-GCM/AAD/key-validation tests, credential contract
tests against the memory adapter, outage classification against a pool with
no reachable server, and HTTP leak tests. None of them need a database.

PostgreSQL integration tests (adapter contracts, encrypted credential rows,
rotation, tamper/relocation, identity/bootstrap concurrency, migrations
and upgrades, OIDC HTTP flow across restart) run only when `VOXA_TEST_DATABASE_URL` is set, and skip cleanly
otherwise. Each
test creates and drops its own schema, so the dev database can be used:

```sh
VOXA_TEST_DATABASE_URL='postgres://voxa:voxa-dev@127.0.0.1:55432/voxa?sslmode=disable' \
  go test -count=1 ./internal/adapters/postgres/
```

## Example workflow

In `development` mode no token is needed. In `oidc` mode, add
`-H "Authorization: Bearer $TOKEN"` (a token from your provider whose `aud`
matches `VOXA_OIDC_AUDIENCE`) to every request except health and readiness.

```sh
curl -s http://localhost:8080/api/v1/health
curl -s http://localhost:8080/readyz
curl -s http://localhost:8080/api/v1/me

curl -s -X POST http://localhost:8080/api/v1/meetings \
  -H 'Content-Type: application/json' \
  -d '{"title":"Weekly planning"}'

curl -s 'http://localhost:8080/api/v1/meetings?limit=20'

curl -s -X PATCH http://localhost:8080/api/v1/meetings/MEETING_ID \
  -H 'Content-Type: application/json' \
  -d '{"title":"Weekly retrospective"}'

curl -s -X PATCH http://localhost:8080/api/v1/configuration \
  -H 'Content-Type: application/json' \
  -d '{"transcription":{"provider":"whisper","language":"en"}}'

curl -s -X PUT http://localhost:8080/api/v1/credentials/openai \
  -H 'Content-Type: application/json' \
  -d '{"value":"example-development-secret"}'

# Returns configured state and a fixed mask, never the submitted value.
curl -s http://localhost:8080/api/v1/credentials

curl -i -X DELETE http://localhost:8080/api/v1/meetings/MEETING_ID
```

## Persistence guarantees

With PostgreSQL:

- Meetings and configuration survive restarts. Responses after a restart
  match the originals exactly, because timestamps are stored at microsecond
  precision.
- Every query is scoped by workspace. Cross-workspace get, update and delete
  return `not_found`.
- The meeting list is keyset-paginated by `(created_at, id)`, with no OFFSET.
  Cursors are opaque, and malformed cursors return `validation`.
- Configuration PATCH is an atomic read-modify-write under a row lock. No
  concurrent patch is lost, and a rejected patch changes nothing.
- Credentials survive restarts as AES-256-GCM ciphertext, each with a fresh
  nonce, its key version, and AAD binding it to workspace and provider. No
  HTTP endpoint returns secret material. GET returns only provider,
  configured state and a fixed mask.
- Database errors never reach HTTP responses. They surface as `unavailable`
  (outages), `internal`, `not_found` or `conflict`.

## Limitations

- **Memory mode stores credentials in plaintext process memory.** It is for
  development and tests only.
- A compromised running server (or anyone who can read its environment)
  can decrypt every credential. Encryption protects the database and its
  backups, not the live process. TLS is still required in production,
  both to clients and to PostgreSQL. See the threat model in
  `docs/server-credential-security.md`.
- Keys come from environment variables. KMS/Vault-backed key rings and
  bulk re-encryption after rotation are not implemented yet.
- Membership administration (invitations, granting access, workspace
  selection, OIDC group mapping) is not implemented yet. Only the bootstrap
  owner gets access automatically. Others are added through SQL, and users
  with several memberships get `409`.
- Both roles have the same access. There is no server-side logout or token
  revocation: tokens are valid until they expire.
- A development-mode server lets anyone who can reach it act as owner.
- There are no down migrations. Migrations that cannot run inside a
  transaction (such as `CREATE INDEX CONCURRENTLY`) are not supported yet.
- Outage classification is conservative. Unrecognized driver failures
  surface as `internal` (500) rather than `unavailable`.
- Capabilities report media storage, transcription and summaries as
  unavailable. No upload, object storage, worker, WebSocket or job
  execution code exists.
- Desktop folders, audio devices, tray behavior, local model paths and other
  native settings are rejected as unknown configuration fields.
