# Voxa server

A single Go process that implements the `voxa-protocol` v1 HTTP contract.
Meetings and workspace configuration persist in PostgreSQL when
`VOXA_DATABASE_URL` is set. Otherwise everything runs on ephemeral in-memory
adapters. Provider credentials are **always** in-memory for now (see
[Limitations](#limitations)). This is not yet a media or processing service.

Design rationale (schema, migrations, pagination, atomicity):
[`docs/server-persistence.md`](../docs/server-persistence.md).

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
- `internal/adapters/postgres` provides pgx/v5 adapters, the connection pool,
  workspace bootstrap, and the migration runner.
- `migrations/` holds versioned `NNNN_description.sql` files, embedded into
  the binary.

The TypeScript package `packages/voxa-protocol` and
`docs/server-protocol-v1.md` remain the protocol source of truth. No
filesystem path is part of a meeting response.

### Dependencies

| Module | Why |
| --- | --- |
| `github.com/jackc/pgx/v5` | PostgreSQL driver and `pgxpool` connection pool. No ORM or query generator. |
| `github.com/jackc/puddle/v2`, `jackc/pgpassfile`, `jackc/pgservicefile`, `golang.org/x/sync`, `golang.org/x/text` | Indirect dependencies of pgx. |

The module requires Go 1.25 or newer, because pgx v5.10 requires it.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `VOXA_LISTEN_ADDR` | `:8080` | HTTP listen address. |
| `VOXA_DEV_WORKSPACE_ID` | `workspace-dev` | The single server-controlled workspace every request is scoped to. |
| `VOXA_DATABASE_URL` | unset | PostgreSQL connection string (URL or key/value). Set: PostgreSQL adapters. Unset: memory adapters. |

The database URL is never logged. Startup logs only the host and database
name. libpq-style parameters such as `sslmode`, `connect_timeout` and
`pool_max_conns` are honored. Without `connect_timeout`, each dial times out
after 5s.

## Startup behavior

Without `VOXA_DATABASE_URL`, the server logs a warning that all data is
ephemeral and starts on memory adapters.

With `VOXA_DATABASE_URL`, these steps run within a 30s startup budget, and
any failure exits non-zero before the listener opens:

1. Parse the URL and connect. Verify connectivity with a ping.
2. Apply pending migrations under an advisory lock, logging each one.
3. Ensure the `VOXA_DEV_WORKSPACE_ID` row exists in `workspaces`, and log
   whether it was created.
4. Log a **warning that credentials are not persisted**.
5. Serve HTTP.

On SIGINT/SIGTERM the server stops accepting connections, drains in-flight
requests for up to 10s, and then closes the pool.

### Health and readiness

- `GET /api/v1/health` (protocol) reports process liveness only. It is
  unchanged.
- `GET /readyz` (operational, not part of the protocol) returns `200
  {"status":"ready","storage":"postgres"}` when the pool answers a ping
  within 2s, and `503` otherwise. In memory mode it always returns ready.

### Development workspace (temporary)

Authentication and membership are deferred. Middleware supplies one
server-controlled workspace ID to every request. With PostgreSQL, that ID is
bootstrapped as a real `workspaces` row at startup. The API never accepts a
workspace override, and clients cannot create workspaces. Auth and
membership work will replace this.

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

Docker is optional and only provides the database:

```sh
cd server
docker compose up -d postgres        # postgres:17, 127.0.0.1:55432, named volume
export VOXA_DATABASE_URL='postgres://voxa:voxa-dev@127.0.0.1:55432/voxa?sslmode=disable'
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

PostgreSQL integration tests (adapter contract, migrations, bootstrap) run
only when `VOXA_TEST_DATABASE_URL` is set, and skip cleanly otherwise. Each
test creates and drops its own schema, so the dev database can be used:

```sh
VOXA_TEST_DATABASE_URL='postgres://voxa:voxa-dev@127.0.0.1:55432/voxa?sslmode=disable' \
  go test -count=1 ./internal/adapters/postgres/
```

## Example workflow

```sh
curl -s http://localhost:8080/api/v1/health
curl -s http://localhost:8080/readyz

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
- Database errors never reach HTTP responses. They surface as `internal`
  (`not_found` and `conflict` where applicable).

## Limitations

- **Credentials are not persisted.** With PostgreSQL enabled, provider
  secrets are still plaintext in process memory and are lost on every
  restart. This is non-production behavior, and it is logged as a warning at
  startup. Encrypted storage or a secret manager is still required.
- There is no authentication or workspace membership. One configured
  development workspace serves all requests.
- There are no down migrations. Migrations that cannot run inside a
  transaction (such as `CREATE INDEX CONCURRENTLY`) are not supported yet.
- Database outages surface as `internal` (500), not `unavailable` (503).
  Use `/readyz` for load-balancer health.
- Unexpected internal errors are not logged with detail yet.
- Capabilities report media storage, transcription and summaries as
  unavailable. No upload, object storage, worker, WebSocket or job
  execution code exists.
- Desktop folders, audio devices, tray behavior, local model paths and other
  native settings are rejected as unknown configuration fields.
