# Voxa server protocol v1

This document defines the first server-facing domain and HTTP contract. The
source of truth is `packages/voxa-protocol`; all paths below are rooted at
`/api/v1`. A Go reference implementation lives in `server/`, with in-memory
adapters and PostgreSQL persistence for meetings and configuration (see
`docs/server-persistence.md`); it does not change or replace the TypeScript
contract.

## Package structure

```text
packages/voxa-protocol/
  src/
    ids.ts          branded, JSON-safe identifiers
    domain.ts       transport-independent entities
    credentials.ts  safe credential status/write shapes
    api-v1.ts       versioned HTTP DTOs and route contract
    index.ts        public exports
  tests/
    protocol.test.ts
```

One package is deliberate. Splitting `voxa-domain` and `voxa-protocol` before a
second implementation needs separate dependency lifecycles would add packaging
work without creating a useful boundary. The modules can become packages later
without changing their public concepts.

The package has no React, Tauri, SQL, filesystem, object-storage, HTTP-framework,
or inference dependency.

## Existing contract assessment

The current frontend contracts duplicate raw `string` meeting/job identifiers,
provider names, status values, progress, errors, and capability descriptions.
The following concepts are suitable for eventual mapping to the shared package:

- meeting metadata and CRUD intent;
- recording-relative transcript timing, sequence, final/partial state,
  confidence and ASR provenance;
- summary job state and generic structured summary data;
- media metadata and URL playback capability;
- provider/model defaults and server-relevant preferences.

These remain frontend-only or desktop-only:

- `MediaSource`, native references, local byte playback, file pickers, dropped
  paths and folder reveal;
- `RecordingClient` device enumeration, audio levels, hot-swap and active
  capture state;
- recording folders, selected native input/output devices, native audio
  backend and model installation paths;
- Tauri event subscriptions, model download UI state, runtime preflight and
  desktop notifications/tray behavior.

Current `MeetingClient` creates a record using `folderPath` and returns
`meeting_id`; those are compatibility details of the desktop adapter. The HTTP
contract accepts only a title and returns a `Meeting`. Current snake_case
timestamps and transcript fields will be normalized by future HTTP adapters;
their semantics are preserved.

## Domain entities

- **User**: `id`, `email`, `createdAt`. Authentication and profiles are deferred.
- **Workspace**: `id`, `name`, `createdAt`, `updatedAt`. It is the server
  ownership/security boundary.
- **Meeting**: workspace-owned logical record with title, lifecycle status and
  timestamps. It never contains a path.
- **RecordingSession**: one capture session for a Meeting, independently
  identified and timed. It is not the Meeting itself.
- **MediaAsset**: opaque asset identity plus MIME type, duration and size. Its
  storage locator is private infrastructure metadata.
- **Transcript**: a meeting transcription result associated optionally with a
  ProcessingJob and containing ordered segments.
- **TranscriptSegment**: text plus recording-relative seconds, duration,
  sequence, finality, confidence and source. Optional `speakerId` and
  `timingProvenance` allow diarization/alignment later without replacing the
  shape. Wall-clock display timestamps are not substituted for recording time.
- **Summary**: recursive `JsonValue` content preserves existing structured JSON
  while excluding functions, class instances, symbols, cycles, and other
  non-wire values.
- **ProcessingJob**: generic lifecycle record for transcription, summary,
  import, transcode and future diarization; states are `queued`, `running`,
  `completed`, `failed`, and `cancelled`. It defines no queue implementation.

## Identifiers

`UserId`, `WorkspaceId`, `MeetingId`, `RecordingSessionId`, `MediaAssetId`,
`TranscriptId`, `SummaryId`, and `ProcessingJobId` are branded TypeScript
strings. Constructors reject blank values. The brand disappears at runtime, so
wire values remain ordinary JSON strings and require no UUID dependency.

## Workspace scoping

Meetings, sessions, media, transcripts, summaries, jobs and server configuration
carry `workspaceId` in the domain. Top-level API routes stay concise rather than
repeating `/workspaces/:workspaceId` everywhere. The future authenticated server
will resolve one active Workspace into request context and authorize every
repository operation against it. How clients select among multiple memberships
(session claim, header, or explicit workspace-switch endpoint) is deferred with
authentication design.

Response resources include `workspaceId` where ownership is useful to clients;
nested resources do not require a second tenant identifier in URLs. A
single-user self-host can create one default Workspace and use exactly the same
contract.

## HTTP endpoints

| Method | Path | Request | Success response |
| --- | --- | --- | --- |
| GET | `/api/v1/health` | none | `HealthResponse` |
| GET | `/api/v1/capabilities` | none | `CapabilitiesResponse` |
| GET | `/api/v1/meetings` | `cursor?`, `limit?` | `MeetingListResponse` |
| POST | `/api/v1/meetings` | `CreateMeetingRequest` | `CreateMeetingResponse` |
| GET | `/api/v1/meetings/:meetingId` | path ID | `GetMeetingResponse` |
| PATCH | `/api/v1/meetings/:meetingId` | `UpdateMeetingRequest` | `UpdateMeetingResponse` |
| DELETE | `/api/v1/meetings/:meetingId` | path ID | no body |
| GET | `/api/v1/configuration` | none | `ConfigurationResponse` |
| PATCH | `/api/v1/configuration` | `UpdateConfigurationRequest` | `UpdateConfigurationResponse` |
| GET | `/api/v1/credentials` | none | `CredentialListResponse` |
| PUT | `/api/v1/credentials/:provider` | `CredentialWriteRequest` | `CredentialUpdateResponse` |
| DELETE | `/api/v1/credentials/:provider` | path provider | no body |

HTTP handlers, status-code bindings and runtime request validation are deferred
to the API service. The `ApiV1Contract` interface binds methods, paths and DTOs
at compile time without selecting a web framework.

### Examples

Create a Meeting:

```json
{ "title": "Weekly planning" }
```

```json
{
  "meeting": {
    "id": "meeting_01",
    "workspaceId": "workspace_01",
    "title": "Weekly planning",
    "status": "created",
    "createdAt": "2026-10-19T10:00:00.000Z",
    "updatedAt": "2026-10-19T10:00:00.000Z"
  }
}
```

List Meetings:

```json
{
  "items": [],
  "page": { "nextCursor": null, "hasMore": false }
}
```

Capabilities:

```json
{
  "apiVersion": "v1",
  "mediaStorage": { "available": true },
  "transcription": { "available": true, "providers": ["whisper"] },
  "summary": { "available": true, "providers": ["ollama", "openai"] },
  "configuration": { "serverManaged": true, "credentialManagement": true }
}
```

## Configuration and credential rules

`ConfigurationResponse` contains effective server-relevant defaults:
transcription provider/model/language/translation, summary provider/model/output
language/template/auto-generation, server media retention, analytics consent,
and processing-complete notification preference. PATCH accepts partial typed
subresources.

Configuration GET responses never return API keys. Credential reads expose only
`provider`, `configured`, and optional fixed masked display text. Raw values are
accepted only by the write request and must not be echoed. Future storage must
encrypt credentials or delegate to an external secret manager. Provider errors,
logs and API error details must never contain credentials.

Scope rules:

- **User-level**: personal UI preferences, personal language choices and
  notification delivery preferences where they are not workspace policy.
- **Workspace-level**: server provider/model defaults, provider availability,
  workspace credentials, media retention and future shared policies.
- **Desktop-local-only**: recordings folder, tray behavior, selected audio
  devices, audio backend, local model installation path, local filesystem
  reveal and native notification execution.

The v1 response currently represents the effective Workspace configuration.
Per-user override endpoints are deferred until identity/membership semantics are
implemented.

## Errors

All failures use:

```json
{
  "error": {
    "code": "not_found",
    "message": "Meeting not found",
    "details": { "resource": "meeting" },
    "requestId": "request_01"
  }
}
```

Stable codes are `validation`, `not_found`, `unauthorized`, `forbidden`,
`conflict`, `unavailable`, and `internal`. `details` and `requestId` are
optional. Native exceptions, SQL messages, local paths, provider response bodies
and secrets must be converted to safe public errors. The shared `apiError`
factory emits fixed public messages and accepts only a correlation ID; handlers
must construct any optional JSON details deliberately rather than forwarding
native errors.

## Pagination

Only the Meeting list is paginated in v1. `cursor` is opaque and `limit` is a
requested page size; responses return `nextCursor` and `hasMore`. The storage
implementation chooses cursor encoding. Offset pagination used by the current
desktop transcript view is not promoted into this server contract.

## Current client to future HTTP mapping

| Current boundary | Future implementation | Protocol direction |
| --- | --- | --- |
| `MeetingClient` | `HttpMeetingClient` | `/api/v1/meetings`; normalize DTOs, never send `folderPath` |
| `ConfigurationClient` | `HttpConfigurationClient` | configuration plus masked/write-only credentials |
| `MediaClient` | `HttpMediaClient` | later asset metadata, upload and range-playback endpoints |
| `TranscriptionClient` | `HttpTranscriptionClient` | later ProcessingJob creation/status/cancellation endpoints |
| `SummaryClient` | `HttpSummaryClient` | later summary ProcessingJob endpoints |
| `RecordingClient` | browser or desktop capture adapter | captures locally; it is not a remote microphone controller |

The current frontend models stay unchanged until an HTTP adapter exists. That
adapter will translate raw current IDs, snake_case desktop data and frontend
capabilities to/from the shared protocol.

## Deferred work

- authentication, membership and active-workspace selection;
- production API handlers and runtime validation;
- KMS/Vault-backed credential keys and bulk credential re-encryption (server
  credentials are already stored AES-256-GCM encrypted in PostgreSQL; see
  `docs/server-credential-security.md`);
- any replacement/migration of desktop SQLite;
- object storage and resumable/chunked media upload;
- media download, signed URLs and HTTP range playback;
- transcription, summary, import/transcode and diarization job execution;
- queue/workers or any microservice split;
- browser/PWA recording and capture APIs;
- Android capture;
- speaker diarization implementation;
- migration of existing local Meetings.
