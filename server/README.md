# Voxa server

This directory contains Voxa's first server skeleton: a single Go process that
implements the existing `voxa-protocol` v1 HTTP contract with in-memory
repositories. It proves the HTTP, application, domain, repository, and adapter
boundaries; it is not production storage or a processing service.

## Architecture

```text
net/http middleware and handlers
  -> application services
  -> repository interfaces
  -> concurrency-safe in-memory adapters
```

- `cmd/voxa` manually wires and runs the process.
- `internal/api` owns protocol-v1 JSON DTOs, routing, strict decoding, API
  errors, request IDs, and access logging.
- `internal/application` owns meeting/configuration/credential use cases,
  workspace scope, validation, and opaque pagination cursors.
- `internal/domain` contains storage-neutral server models.
- `internal/repository` declares persistence ports without SQL concepts.
- `internal/adapters/memory` provides ephemeral, mutex-protected adapters.

The TypeScript package `packages/voxa-protocol` and
`docs/server-protocol-v1.md` remain the protocol source of truth. Go DTOs use
explicit JSON names to match its camel-case wire format. No filesystem path is
part of a meeting response.

## Development workspace

Authentication and membership are deferred. Middleware supplies one
server-controlled workspace ID to every request. It defaults to
`workspace-dev` and may be changed with `VOXA_DEV_WORKSPACE_ID`. The API never
accepts a workspace override from a client.

`VOXA_LISTEN_ADDR` controls the listen address and defaults to `:8080`.

## Run and test

```sh
cd server
go test ./...
go vet ./...
go run ./cmd/voxa
```

## Example workflow

```sh
curl -s http://localhost:8080/api/v1/health

curl -s -X POST http://localhost:8080/api/v1/meetings \
  -H 'Content-Type: application/json' \
  -d '{"title":"Weekly planning"}'

curl -s http://localhost:8080/api/v1/meetings

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

## Current limitations

- All data and credential values disappear when the process exits.
- The credential adapter keeps plaintext values in memory. It is explicitly
  non-production and must be replaced by encrypted storage or a secret manager.
- There is no authentication or workspace membership resolution.
- Capabilities report media storage, transcription, and summaries as
  unavailable. No upload, object storage, worker, WebSocket, or job execution
  code exists here.
- Desktop folders, audio devices, tray behavior, local model paths, and other
  native settings are rejected as unknown configuration fields.

The repository interfaces are ready for PostgreSQL adapters, but production
persistence should add transactions, migrations, encrypted credentials, and
integration tests after authentication/workspace decisions are settled.
