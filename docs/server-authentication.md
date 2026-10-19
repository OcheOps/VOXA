# Voxa server authentication and workspace membership

How the Go server (`server/`) authenticates requests and decides which
workspace a request may act in. Persistence is described in
`docs/server-persistence.md` and credential encryption in
`docs/server-credential-security.md`.

```text
Authorization: Bearer <JWT>
  -> Authenticator      (OIDC signature/iss/aud/exp/nbf)    -> Identity{issuer, subject, email?, name?}
  -> WorkspaceResolver  (users + memberships, bootstrap)     -> Principal{user, workspace, role}
  -> request context    -> existing services                 -> workspace-filtered SQL
```

Handlers and services see only the `Principal`. They never see tokens,
claims, or which auth mode is active.

## 1. Auth modes

`VOXA_AUTH_MODE` is **required**. There is no default, so a deployment can
never silently run without authentication.

| Mode | Use | Behavior |
| --- | --- | --- |
| `oidc` | Any deployment reachable by others | Validates bearer tokens from one OIDC provider and enforces workspace membership. Requires `VOXA_DATABASE_URL`. |
| `development` | Local development only | Every request is accepted, with no credentials, as the owner (`usr_development`) of `VOXA_DEV_WORKSPACE_ID` (default `workspace-dev`). Logs a loud warning at startup. |

An invalid configuration stops startup. `oidc` mode never falls back to
`development`.

## 2. OIDC setup

Voxa does not store passwords and has no provider-specific integrations. Any
standards-compliant OIDC provider works (Authentik, Keycloak, Zitadel, Auth0,
Okta, Microsoft Entra ID, Google and others) as long as it publishes
discovery metadata and JWKS.

| Variable | Required | Meaning |
| --- | --- | --- |
| `VOXA_OIDC_ISSUER` | yes | Issuer URL. It must match the provider's discovery `issuer` and every token's `iss` **exactly**, including any trailing slash. `https` is required (plain `http` is allowed only for `localhost`/loopback). |
| `VOXA_OIDC_AUDIENCE` | yes | Value that must appear in every token's `aud`: the Voxa client ID if clients send ID tokens, or the API audience if the provider issues JWT access tokens for Voxa. |
| `VOXA_BOOTSTRAP_OIDC_SUBJECT` | no | When set, only this `sub` can bootstrap the deployment (recommended). |
| `VOXA_BOOTSTRAP_WORKSPACE_ID` | no | Bootstrap adopts this workspace (creating it if missing) instead of generating one, for example `workspace-dev` to keep data created in development mode. |

Example (placeholders only):

```sh
export VOXA_AUTH_MODE=oidc
export VOXA_OIDC_ISSUER='https://id.example.com/realms/voxa'
export VOXA_OIDC_AUDIENCE='voxa'
export VOXA_BOOTSTRAP_OIDC_SUBJECT='<your-subject-id-from-the-provider>'
export VOXA_DATABASE_URL='postgres://voxa:<password>@db.internal:5432/voxa?sslmode=verify-full'
export VOXA_CREDENTIAL_KEY="$(cat /run/secrets/voxa_credential_key)"
export VOXA_CREDENTIAL_KEY_VERSION=2026-10
```

Configure the provider so the tokens your client sends carry the configured
audience. For example, in Keycloak add an audience mapper for access tokens,
or have clients send the ID token whose `aud` is the client ID. The server
needs no client secret: it only verifies tokens and never performs a login
flow itself.

### Validation

Library: `github.com/coreos/go-oidc/v3`, which uses `go-jose/v4` for JOSE.
For every protected request:

1. Extract `Authorization: Bearer <token>` (scheme is case-insensitive;
   tokens over 16 KiB are rejected).
2. Verify the JWS signature with a key from the provider's JWKS, restricted
   to the algorithms the provider advertises. Unsigned tokens and
   `alg: none` are never accepted.
3. Only after the signature verifies, check `iss` (exact match), that `aud`
   contains the configured audience, `exp`, and `nbf` when present.
4. Read `sub` (required), and `email` (ignored if `email_verified` is
   `false`), `name` or `preferred_username` as profile hints.

### Discovery and JWKS behavior

- Discovery runs once at startup (15s timeout). If it fails, startup fails.
- JWKS is fetched lazily and cached in memory. It is **not** fetched on
  every request.
- A token with an unknown `kid` triggers one refetch, with concurrent
  refetches collapsed. That is how provider key rotation is picked up.
- A failed refetch keeps the cached keys. During a provider outage, tokens
  signed with already-cached keys keep working. Tokens that need a key Voxa
  hasn't seen yet get `503 unavailable` (`signing_keys_unavailable`), not
  401.
- Limitation: refetches on unknown `kid` are not rate-limited. A client
  sending forged `kid`s can make Voxa query the provider's JWKS roughly
  once per request.

## 3. Identity semantics

The stable external identity is **issuer + subject**. A Voxa user (`users`
table) has an opaque ID (`usr_<128-bit hex>`) and a unique
`(issuer, subject)` pair. Email and display name are profile hints only: they
are refreshed when they change, and never used to look up, merge or
authorize users. The same `sub` from a different issuer is a different
user. Unusable hints (blank, oversized, control characters) are stored as
null.

## 4. First-user bootstrap

When the deployment is **uninitialized** (no `deployment_state` row), the
first identity that authenticates successfully, and matches
`VOXA_BOOTSTRAP_OIDC_SUBJECT` if that is set:

1. is checked again under a transaction-scoped advisory lock;
2. gets the workspace: `VOXA_BOOTSTRAP_WORKSPACE_ID` (adopted if it
   exists), or a new `wsp_<hex>` named "Default workspace";
3. becomes a Voxa user;
4. gets an `owner` membership in that workspace;
5. is recorded in the single-row `deployment_state` table.

All five steps commit in one transaction. Concurrent first logins serialize
on the lock, and every loser sees the deployment as initialized. If a loser
is the same identity, it resolves to the winner's user. Any other identity
is forbidden. Independently, the `deployment_state` primary key (a boolean
column constrained to `true`) makes a second bootstrap impossible at the
schema level. Startup logs whether the deployment is initialized, and warns
while it is not.

**Bootstrap is first come, first served.** If the provider lets people you
don't trust obtain tokens with your audience (for example a public Google
client), set `VOXA_BOOTSTRAP_OIDC_SUBJECT`, or bootstrap before exposing
the server.

## 5. Membership enforcement and roles

`workspace_memberships(workspace_id, user_id, role)` has primary key
`(workspace_id, user_id)` and role `owner` or `member`. Both roles currently
have full access to meetings, configuration and credentials. `owner` is
reserved for future administration. Resolution per request:

| Caller | Result |
| --- | --- |
| Invalid/missing token | `401 unauthorized` + `WWW-Authenticate: Bearer realm="voxa"` |
| Signing keys unavailable (provider outage, unseen key) | `503 unavailable` |
| Unknown identity after bootstrap | `403 forbidden` (no user row is created) |
| Known user, no membership | `403 forbidden` |
| Exactly one membership | that workspace and role |
| Several memberships | `409 conflict`, `details.reason = "workspace_selection_required"` |

Defense in depth: the membership check runs in the application layer
(`MembershipResolver`), and every repository query still filters by the
resolved workspace ID. A client-supplied workspace header is ignored.

Error bodies are the generic protocol errors. They never reveal whether a
user, email or workspace exists, why a token failed, or anything about
signing keys.

## 6. Development auth

`VOXA_AUTH_MODE=development` swaps in `DevelopmentAuthenticator` and
`DevelopmentResolver`. Every request (with or without a token) acts as
`usr_development`, owner of `VOXA_DEV_WORKSPACE_ID`. It works with memory
or PostgreSQL storage. In PostgreSQL mode the workspace row is created at
startup, and no user or membership rows are written. Never expose a
development-mode server.

## 7. Endpoints

| Route | Access |
| --- | --- |
| `GET /api/v1/health` | public (liveness) |
| `GET /readyz` | public (readiness, outside the protocol) |
| `GET /api/v1/capabilities` | public: reports only feature availability, nothing deployment-specific |
| `GET /api/v1/me` | authenticated: user `id`/`email`/`displayName`, workspace `id`/`name`, `role` |
| meetings, configuration, credentials | authenticated + membership |

There are no endpoints to create workspaces, invite users or change
memberships. `/me` never returns tokens, issuer, subject or claims.

## 8. Logging

Every request's access log includes `user_id`, `workspace_id` and `role` once
resolved. Authentication and authorization failures are logged as
`"request failed"` with `code`, `operation` (`auth.authenticate` /
`auth.resolve`) and a fixed `category`:

`missing_bearer`, `malformed_authorization`, `malformed_token`,
`signature_invalid`, `issuer_mismatch`, `audience_mismatch`, `expired`,
`not_yet_valid`, `claims_invalid`, `signing_keys_unavailable`,
`unknown_identity`, `not_a_member`, `bootstrap_not_permitted`,
`workspace_selection_required`, `identity_invalid`.

Library error text is discarded, because it can echo claim values. The
`Authorization` header, tokens, claims, emails and display names are never
logged. Tests send tokens and canary secrets through every path and assert
that they never appear in logs or response bodies.

## 9. HTTP hardening

All API responses carry `X-Content-Type-Options: nosniff`,
`Cache-Control: no-store` and `Referrer-Policy: no-referrer`. No CORS
headers are emitted, so cross-origin browser access is denied until an
explicit origin allow-list is added together with an HTTP client or PWA.

## 10. Managing members today (interim)

There is no membership API yet. An operator can grant access directly in
PostgreSQL. The user must have signed in once to learn their subject; that
attempt is denied and no row is created, so insert the user too:

```sql
INSERT INTO users (id, issuer, subject, created_at, updated_at)
VALUES ('usr_' || md5(random()::text), '<issuer>', '<subject>', now(), now());
INSERT INTO workspace_memberships (workspace_id, user_id, role)
SELECT '<workspace id>', id, 'member' FROM users WHERE issuer = '<issuer>' AND subject = '<subject>';
```

## 11. Known limitations

- Unknown identities are denied and not recorded. Access can only be
  granted through SQL.
- There is no multi-workspace selection: users with several memberships get
  `409` on every request.
- Roles are not differentiated yet.
- One OIDC issuer per deployment.
- No server-side session, logout or revocation: a valid token is accepted
  until it expires. Keep access-token lifetimes short at the provider.
- JWKS refetch on unknown `kid` is not rate-limited.
- If the provider is unreachable at startup, the server does not start.
- `meetings`, `workspace_configuration` and `workspace_credentials` keep their
  original `ON DELETE CASCADE` from `workspaces`, so deleting a workspace
  row deletes its data. There is no workspace deletion path in the API.

## 12. Future work

- **Invitations:** owners create single-use invitations bound to an email or
  subject. Accepting one while authenticated creates the user and a
  `member` row. This replaces the SQL procedure above.
- **Workspace selection:** clients choose the active workspace explicitly
  (for example an `X-Voxa-Workspace` header or a path segment). The server
  always validates it against memberships and never trusts it alone.
  `GET /api/v1/workspaces` would list the caller's memberships.
- **OIDC group mapping:** optionally map provider groups or roles to
  memberships, and make the role differences explicit. This is off by
  default, so token claims never silently change access.

## 13. Security assumptions

- TLS terminates in front of, or at, Voxa. Bearer tokens over plain HTTP are
  exposed.
- The OIDC provider is trusted to authenticate users and protect its signing
  keys. Anyone who can mint tokens with the configured issuer and audience
  can authenticate.
- The configured audience is specific to Voxa. Reusing an audience shared
  with other applications lets their tokens authenticate here.
- The database and the server environment are protected. Database write
  access can grant memberships.
