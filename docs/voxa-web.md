# Voxa Web

Voxa Web is the existing Next.js frontend built in a second mode that talks to
the Voxa Go API over HTTP instead of the Tauri core. There is one codebase and
one component library. The desktop app is unchanged.

## 1. Runtime architecture

```text
                    ┌── desktop (default) ── Tauri clients ── native Voxa (Rust)
shared React UI ────┤
 (client contracts) └── web ── HTTP clients ── fetch + Bearer ── Voxa Go API
```

- **Runtime selection** is a build-time constant: `NEXT_PUBLIC_VOXA_RUNTIME=web`
  (`src/config/runtime.ts`). There is no user-agent sniffing and no probing
  for Tauri globals. Anything other than `web` means desktop, so existing
  builds are untouched.
- **Composition roots.** `app/layout.tsx` renders `DesktopRootLayout` (the
  previous layout, verbatim) or `WebRootLayout` (`src/web/WebApp.tsx`). Each
  page (`/`, `/meeting-details`, `/settings`) picks its desktop or web view
  with the same constant. `/auth/callback` is the OIDC redirect target.
- **Web composition.** `createWebVoxaClient` (`src/clients/web.ts`) fills the
  same `VoxaClient` contract. The web provider stack mounts no
  Tauri-dependent provider. It reuses `SidebarProvider` (meeting state) and
  `RecordingStateProvider` (driven by the client contract), plus the shared
  UI primitives.
- **Why a thin web shell rather than the desktop Sidebar and meeting page?**
  Those components (and the desktop providers such as Config, Onboarding,
  Ollama, Updater and drag-drop) call Tauri directly at about 40 sites and
  are built around recording, local transcripts and local models. Reusing
  them would mean auditing all of that first. The web runtime instead reuses
  the shared data and state layers (client contracts, `SidebarProvider`,
  `RecordingStateProvider`) and UI primitives, and renders small web views
  (`src/web/*`) on top. Moving desktop views onto capabilities is
  incremental future work.
- **Guarantee.** `tests/web/web-startup.test.tsx` mounts the web root and
  views with every `@tauri-apps/*` module replaced by a recorder, and fails
  if web startup makes any native call.

## 2. HTTP transport

`src/clients/http/transport.ts` is a minimal `fetch` client (no Axios):

- Absolute base URL from `NEXT_PUBLIC_VOXA_API_URL`.
- `Authorization: Bearer <token>` from the in-memory token store, when a
  session exists.
- JSON bodies, `credentials: 'omit'` (no cookies), `cache: 'no-store'`, and
  `redirect: 'error'` (no forwarding of the Authorization header).
- Protocol errors are decoded into `VoxaApiError {status, code, message,
  requestId, details}`. Non-JSON error bodies fall back to a status mapping.
- Network failures and unreadable bodies become `VoxaTransportError`.
- `AbortSignal` is honored, and aborts reject with `AbortError`.
- Any 401 calls `onUnauthorized`, which signs the session out as expired.
- The transport never logs, and error messages never contain tokens or
  request bodies.

Clients:

| Client | Implementation |
| --- | --- |
| Meetings | `HttpMeetingClient`: list follows `nextCursor` (100 per page), plus get, create (title only), rename and delete. Workspace IDs are never exposed. Transcripts and search report unavailable. |
| Configuration | `HttpConfigurationClient`: the shared contract over `/api/v1/configuration`. Desktop-only operations and API-key read-back throw `UnsupportedOperationError`. |
| Workspace settings | `HttpWorkspaceSettingsClient`: typed get and partial `PATCH` of server defaults. |
| Credentials | `HttpCredentialClient`: list metadata, `PUT` a secret, `DELETE`. Secrets are never read or stored. |
| Identity | `HttpIdentityClient`: `GET /api/v1/me` and `GET /api/v1/capabilities`. |
| Recording | `BrowserRecordingClient`: microphone via `getUserMedia` + `MediaRecorder` (see `docs/voxa-web-recording.md`). |
| Media | `HttpMediaClient`: chunked uploads, meeting media, signed range playback. |
| Transcription | `HttpTranscriptionClient`: remote execution (no model management or download, no imports); per-recording transcript state and segments, request (re)transcription, cancel processing. See `docs/server-processing.md`. |
| Summaries | `HttpSummaryClient`: remote execution; meeting summary state and result, generate/regenerate, cancel, templates. Provider, model and keys are server configuration and never pass through the browser. See `docs/server-summaries.md`. |

DTOs are typed against `@voxa/protocol` through a type-only `tsconfig` path,
so there is no runtime dependency and no build step.

## 3. Web OIDC flow

Library: **`oidc-client-ts` 3.5.0** (authts, Apache-2.0, maintained, released
2026-03, one dependency: `jwt-decode`). It provides discovery, Authorization
Code + PKCE (S256), state, nonce and token exchange, so Voxa implements none
of the protocol itself.

```text
signed-out ─Sign in─> redirecting ──provider login──> /auth/callback
   ─code + PKCE exchange─> loading-identity ─GET /api/v1/me─> ready
                                                ├─ 401 → signed-out (rejected)
                                                ├─ 403 → "No access to this Voxa server"
                                                ├─ 409 workspace_selection_required → "Multiple workspaces" (unsupported)
                                                └─ network/5xx → error + retry
ready ─any API 401─> signed-out (expired)
```

- Public client: no client secret exists in the browser.
- A fresh 256-bit nonce for every sign-in, validated against the ID token.
- `state` carries the return path. Only same-origin relative paths are
  accepted (open-redirect protection).
- After the callback the URL is replaced, so the authorization code leaves
  the address bar and history.
- The `/me` response is the application identity (user, workspace, role).
  Token claims are never used for that.

## 4. Token storage

| Data | Where | Lifetime |
| --- | --- | --- |
| Bearer token (access token, or ID token if configured) | JavaScript memory (`InMemoryTokenStore`), never Web Storage | Until reload, expiry or sign-out |
| OIDC transaction state (state, nonce, PKCE verifier, return path) | `sessionStorage`, prefix `voxa.oidc.` | Until the callback consumes it (stale after 10 min) |
| `voxa.web.signedIn = "1"` (contains no token) | `sessionStorage` | Until sign-out, rejection or a failed callback |
| Provider API keys | Never stored client-side; exist only for the duration of the `PUT` | — |

Tradeoff: keeping the token in memory means it is not exposed to script
reading storage, and it disappears with the tab. The cost is that a reload
loses it. The "signed in" marker then starts a new authorization
automatically, which is instant when the provider session is still valid.
There are no refresh tokens and no silent iframe renewal: when the token
expires, the next API call returns 401 and the user is asked to sign in
again. Keep provider access-token lifetimes reasonable (for example 15–60
minutes).

## 5. Server capabilities

`GET /api/v1/capabilities` (public) is loaded once by
`ServerCapabilitiesProvider`. Nothing about backend features is hardcoded in
React.

## 6. Client capabilities

Each client reports what *its runtime* can do (`recording.capabilities`,
`media.capabilities`, `transcription.capabilities.execution`,
`meetings.capabilities`, `configuration.capabilities`). A feature is offered
only when **both** the client and the server support it. For example,
recording would need a browser capture client *and* server media storage.
`CapabilityNotice` lists what is unavailable instead of failing.

## 7. CORS (server)

The Go server allows cross-origin browser access only for exact origins in
`VOXA_ALLOWED_ORIGINS`:

- **Unset means no CORS headers at all.**
- Origins are exact `scheme://host[:port]` values. `*`, `null`, wildcards,
  paths and credentials are rejected at startup.
- Preflight (`OPTIONS`) is answered before authentication. It allows only
  `GET, POST, PATCH, PUT, DELETE` and the headers `Authorization,
  Content-Type, X-Request-ID`, with `Max-Age: 600`. Anything else gets 403.
- Allowed origins get `Access-Control-Allow-Origin: <origin>` on every
  response, including 401/403, so the app can react to them. `X-Request-ID`
  and `WWW-Authenticate` are exposed. `Vary: Origin` is always sent.
- `Access-Control-Allow-Credentials` is never sent: bearer tokens only, no
  cookies.

## 8. Local development

Development auth (no identity provider):

```sh
# terminal 1: API (memory storage, development identity, web origin allowed)
cd server
VOXA_AUTH_MODE=development VOXA_ALLOWED_ORIGINS=http://localhost:3118 go run ./cmd/voxa

# terminal 2: web UI
cd frontend
cp .env.web.example .env.web.local        # NEXT_PUBLIC_VOXA_AUTH_MODE=development
pnpm run dev:web                          # http://localhost:3118
```

With OIDC (needs PostgreSQL; see `server/README.md`):

```sh
cd server
export VOXA_AUTH_MODE=oidc VOXA_OIDC_ISSUER=https://id.example.com/realms/voxa VOXA_OIDC_AUDIENCE=voxa-web
export VOXA_DATABASE_URL=... VOXA_CREDENTIAL_KEY="$(openssl rand -base64 32)" VOXA_CREDENTIAL_KEY_VERSION=dev-1
export VOXA_ALLOWED_ORIGINS=http://localhost:3118
go run ./cmd/voxa

cd frontend   # .env.web.local: AUTH_MODE=oidc, ISSUER, CLIENT_ID (same issuer; aud must match)
pnpm run dev:web
```

At the provider, register a **public** client with redirect URI
`http://localhost:3118/auth/callback`, PKCE S256, and post-logout redirect
`http://localhost:3118/`. Make sure the bearer token's `aud` equals
`VOXA_OIDC_AUDIENCE`.

Production build: `pnpm run build:web` produces a static site in
`frontend/out/`. Serve it over HTTPS from any static host. Desktop builds
(`pnpm build`, `pnpm tauri:build`) are unchanged.

Tests:

```sh
cd frontend
bun test                                   # unit + web startup (integration suites skip)
# HTTP adapters against a real server in development mode:
VOXA_INTEGRATION_API_URL=http://127.0.0.1:8080 bun test tests/integration/http-adapters.integration.test.ts
# Full OIDC stack (local issuer + real server + PostgreSQL):
VOXA_E2E_SERVER_BIN=/path/to/voxa VOXA_E2E_DATABASE_URL=postgres://... bun test tests/integration/oidc-e2e.integration.test.ts
```

## 9. Environment variables

Frontend (public, compiled into the bundle; `.env.web.local` is read by
`scripts/next-web.js`, which accepts only `NEXT_PUBLIC_VOXA_*` keys):

| Variable | Default | Meaning |
| --- | --- | --- |
| `NEXT_PUBLIC_VOXA_RUNTIME` | `desktop` | Set to `web` by `dev:web` and `build:web`. |
| `NEXT_PUBLIC_VOXA_API_URL` | required | Voxa API origin, e.g. `https://api.voxa.example.com`. |
| `NEXT_PUBLIC_VOXA_AUTH_MODE` | `oidc` | `oidc` or `development`. |
| `NEXT_PUBLIC_VOXA_OIDC_ISSUER` | required (oidc) | Must equal the server's `VOXA_OIDC_ISSUER` exactly. https, or http on localhost. |
| `NEXT_PUBLIC_VOXA_OIDC_CLIENT_ID` | required (oidc) | Public client ID. |
| `NEXT_PUBLIC_VOXA_OIDC_SCOPE` | `openid profile email` | Must include `openid`. |
| `NEXT_PUBLIC_VOXA_OIDC_REDIRECT_URI` | `<origin>/auth/callback` | Override only if the app is served under a different path. |
| `NEXT_PUBLIC_VOXA_OIDC_BEARER` | `access_token` | `id_token` for providers whose access tokens are opaque or lack the Voxa audience. |

Never put `VOXA_DATABASE_URL`, `VOXA_CREDENTIAL_KEY` or any client secret in
frontend variables. The launcher refuses non-`NEXT_PUBLIC_VOXA_*` keys.

Server: `VOXA_ALLOWED_ORIGINS` (above), plus the existing auth and storage
variables. **TLS is required in OIDC mode**: serve both the web app and the
API over HTTPS, because bearer tokens over plain HTTP are exposed.

## 10. Working in Voxa Web today

- Sign-in (OIDC), sign-out, session expiry and re-authentication on reload.
- The current user, workspace and role (from `/me`).
- Clear screens for no-access (403) and multiple workspaces (409).
- Meetings: list (paginated), create, open, rename and delete. Everything is
  server-backed and survives reloads.
- Settings: transcription and summary defaults, media retention, analytics
  and processing notifications.
- Provider API keys: configured/masked state, set or replace, remove. Keys
  are never shown again.
- Recording on the meeting page:
  - microphone only, meeting tab + microphone, or meeting tab only, where
    the browser supports sharing tab audio (see
    `docs/voxa-web-meeting-capture.md`);
  - start, pause/resume and stop, with a consent reminder;
  - parts are saved on the device while offline, uploads resume
    automatically, and recordings are recovered after a reload;
  - range-streamed playback (see `docs/voxa-web-recording.md`);
  - per-recording processing status (preparing, transcribing, ready,
    failed) and the server transcript; clicking a segment seeks playback
    (see `docs/server-processing.md`).
- Meeting summaries generated by the server: automatic after transcription
  (when enabled) or on demand, regenerate and cancel, stale-transcript
  notice, previous summary kept on failure (see `docs/server-summaries.md`).

## 11. Unsupported in Voxa Web (by design, for now)

Capturing audio the browser cannot share (desktop meeting apps, native
call apps, tabs the user did not select), file import, summary and transcript editing,
transcript search, diarization, chat with a meeting,
local models, the recordings folder, file reveal, the tray, the updater,
OS notifications, workspace selection, and invitations. Capabilities report
these truthfully and the UI hides them or explains why they are missing.

## 12. Future work

- **Meeting capture beyond the browser:** tab audio capture exists (see
  `docs/voxa-web-meeting-capture.md`). A browser extension or native
  companion could capture without the share picker or from desktop/mobile
  meeting apps.
- **Desktop views on capabilities:** gradually move the desktop Sidebar and
  meeting page off direct `invoke` calls, so both runtimes share more
  presentation.
- **Bundle size:** each runtime's bundle still contains the other's code (it
  is never executed). Lazy-loading the composition roots would trim this.
