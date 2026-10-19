# Voxa server persistence

This document records the design of the Go server's PostgreSQL persistence
(`server/`). Operating instructions live in `server/README.md`; the HTTP
contract is unchanged and remains defined by `docs/server-protocol-v1.md`.

## Scope

| Data | `VOXA_DATABASE_URL` unset | `VOXA_DATABASE_URL` set |
| --- | --- | --- |
| Meetings | memory | PostgreSQL |
| Workspace configuration | memory | PostgreSQL |
| Development workspace | implicit | `workspaces` row, bootstrapped at startup |
| Provider credentials | memory | **memory (not persisted)** |

Credentials are deliberately not stored in PostgreSQL. They need encrypted
storage or an external secret manager, which is a separate task. With
PostgreSQL enabled the server logs a warning at every startup that
credentials are ephemeral.

## Layering

```text
cmd/voxa (startup wiring, backend selection, migrations, bootstrap, /readyz)
  -> internal/api              protocol-v1 HTTP
  -> internal/application      validation, PATCH semantics, cursors
  -> internal/repository       storage-neutral ports
       <- internal/adapters/memory    concurrency-safe maps
       <- internal/adapters/postgres  pgx/v5, explicit SQL
server/migrations              embedded, versioned .sql files
```

The repository interfaces did not change. The backend is chosen once, in
`cmd/voxa/storage.go`; handlers and services never check the environment.
`internal/repository/repositorytest` holds behavioral contract tests that
both adapters must pass identically.

## Driver

`github.com/jackc/pgx/v5` (with `pgxpool`) is the only new direct
dependency. There is no ORM and no query generator: queries are short,
explicit, parameterized SQL with typed scanning. Pool sizing uses pgx
defaults. The only override is a 5s per-connection dial timeout, applied
when the URL does not set `connect_timeout`.

## Schema (`0001_initial.sql`)

- `workspaces(id, created_at)`: a minimal real entity, so meetings and
  configuration have referential integrity.
- `meetings(workspace_id, id, title, status, created_at, updated_at)`, with
  primary key `(workspace_id, id)` and index `(workspace_id, created_at, id)`.
  The composite key matches the in-memory adapter's per-workspace identity.
  Every query filters by workspace. There are no filesystem paths.
- `workspace_configuration`: one row per workspace, with **typed columns**
  for every field of the current configuration contract (transcription,
  summary, recording, privacy, notifications). The contract is small and
  stable, so typed columns give real constraints and readable SQL, and they
  prevent arbitrary client JSON from being stored. There is no JSONB.
  Desktop-only preferences are rejected at the API and have no columns.

Identifier columns use `COLLATE "C"`, so `(created_at, id)` ordering is
byte-wise and identical to the memory adapter regardless of database locale.
Meeting IDs (`mtg_<hex>`) are still generated in application code, and no
database sequence or integer ID is exposed. CHECK constraints mirror domain
invariants (title length, known statuses) as a backstop. Validation itself
stays in the application layer.

Configuration defaults are owned by `application.DefaultConfiguration()`, not
by SQL defaults. A missing row reads as those defaults.

## Migrations

A small built-in runner (`internal/adapters/postgres/migrate.go`), with no
framework:

- Files are named `NNNN_description.sql`, embedded into the binary, and
  applied in version order.
- A `pg_advisory_lock` on a dedicated, non-pooled session serializes
  concurrent runners. Closing that session always releases the lock.
- Each migration runs in one transaction together with its
  `schema_migrations(version, name, checksum, applied_at)` row. If one fails,
  that migration rolls back, earlier ones stay applied, and the run stops
  with an error, which stops server startup.
- Startup refuses to continue if an applied migration's SHA-256 checksum
  changed, or if the database contains a version the binary does not know
  (a newer schema).
- Rollback/down migrations are **intentionally deferred**. Fix forward with
  a new migration. Restoring from a backup is the recovery path for a bad
  migration.

Because each file runs inside a transaction, statements that cannot run in
one (for example `CREATE INDEX CONCURRENTLY`) are not supported yet.

## Pagination

The cursor stays opaque on the wire: base64url JSON of `{createdAt, id}`,
encoded and decoded by the application layer and shared by both adapters.
PostgreSQL uses keyset pagination with no OFFSET:

```sql
WHERE workspace_id = $1 AND (created_at, id) > ($2, $3)
ORDER BY created_at, id
LIMIT $limit + 1   -- the extra row determines hasMore
```

A malformed cursor is a `validation` error before any query runs. Rows
inserted ahead of a cursor are not shown on later pages; rows inserted after
it are. Neither causes duplicates or skips among existing rows.

`MeetingService` cuts timestamps to microseconds, the precision of
`timestamptz`, so a `POST` response matches every later `GET` and cursor
positions compare exactly.

## Atomicity

- **Configuration PATCH**: one transaction does
  `INSERT … ON CONFLICT DO NOTHING` (materialize defaults), then
  `SELECT … FOR UPDATE`, then the application patch closure, then `UPDATE`,
  then commit. Concurrent patches serialize on the row lock, so none is
  lost. If the closure returns an error (validation), the transaction rolls
  back and nothing persists. A contract test runs 20 concurrent
  read-modify-write patches; it reliably loses updates if `FOR UPDATE` is
  removed.
- **Meetings**: each operation is a single statement, so no explicit
  transaction is needed. `UPDATE`/`DELETE` affecting zero rows becomes
  `ErrNotFound`.
- **Workspace bootstrap**: a single `INSERT … ON CONFLICT DO NOTHING`.

## Error translation and safety

The adapter maps `pgx.ErrNoRows` to `ErrNotFound` and unique violations to
`ErrConflict`. Other PostgreSQL errors are reduced to the operation name and
SQLSTATE. Messages and details are dropped because they can echo row
values. Services map anything else to `internal`. Every repository call runs
under a 5s query timeout derived from the request context. All user values
are bind parameters. A malformed `VOXA_DATABASE_URL` is reported without
echoing the string, and logs carry only host and database name.

## Health and readiness

`GET /api/v1/health` is unchanged and remains process liveness. Startup
already verifies connectivity, migrations, and bootstrap before the listener
opens. An operational `GET /readyz` (outside `/api/v1`, not part of the
protocol) pings the pool and returns `503` when PostgreSQL is unreachable.

## Development workspace bootstrap (temporary)

The server ensures that the configured `VOXA_DEV_WORKSPACE_ID` exists in
`workspaces` at startup, and it scopes every request to that ID. Clients
cannot create workspaces or choose a scope. This is a placeholder until
authentication and membership exist. Changing the variable switches to a
different, isolated workspace and leaves the old rows in place.
