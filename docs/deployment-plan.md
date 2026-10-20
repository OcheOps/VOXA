# Voxa deployment plan (single VPS)

Goal: Voxa on a real HTTPS URL, with sign-in, persistent storage,
transcription and summaries, so real meetings can be recorded day to day.
Status: **plan**, not implemented yet.

Decisions (2026-10-20): one cloud VPS, Docker Compose, Caddy for automatic
HTTPS, bundled Zitadel for sign-in, PostgreSQL shared by Voxa and Zitadel.
Summaries: keep the API-key providers and add “export to your LLM”
(§8).

## 1. Shape

```
                    Internet (443)
                         │
                      Caddy  (TLS via Let's Encrypt, security headers)
           ┌─────────────┴──────────────┐
  voxa.example.com                 auth.example.com
   ├─ /          static Voxa Web     └─ Zitadel (OIDC)
   ├─ /api/*  ─┐                          │
   └─ /readyz ─┤                          │
               ▼                          ▼
        voxa-server (Go) ─────────── PostgreSQL 17
         ├─ ffmpeg / ffprobe          ├─ db: voxa
         ├─ whisper-cli + model       └─ db: zitadel
         └─ media volume
        (optional) ollama  — local summaries, compose profile
```

- **Same origin for web and API** (`https://voxa.example.com` serves the web
  app and proxies `/api` to the server): no CORS needed, and Chrome's
  microphone/tab capture works because the page is HTTPS.
- Only ports 80/443 are public; PostgreSQL, the Voxa server and Zitadel
  listen on the internal Docker network.

## 2. Sizing

| Load | VPS | Notes |
|---|---|---|
| 1–5 users, a few meetings/day | 4 vCPU, 8 GB RAM, 80 GB disk | Whisper `base`/`small` on CPU is faster than real time for `base` |
| Heavier, or `medium`/`large-v3` models | 8+ vCPU, 16 GB, or a GPU host | whisper.cpp CUDA build |

Storage: mixed recordings are ~0.7 MB per minute (96 kbps Opus); 1 hour
≈ 45 MB. 50 GB holds ~1000 hours plus databases.

## 3. Artifacts to build (repo)

| File | Purpose |
|---|---|
| `deploy/Dockerfile.server` | Multi-stage: build `voxa` (Go), build `whisper-cli` from whisper.cpp; runtime image with ffmpeg, non-root user, `/data/media`, `/models`. |
| `deploy/Dockerfile.web` | Builds the static Voxa Web export (`pnpm build:web`) with production `NEXT_PUBLIC_VOXA_*` values; output copied into the Caddy image or a volume. |
| `deploy/compose.yaml` | caddy, voxa-server, postgres, zitadel; `ollama` under a profile. Health checks, restart policies, named volumes. |
| `deploy/Caddyfile` | Two sites; `/api/*` and `/readyz` → voxa-server; static files with SPA fallback; HSTS, CSP, `Permissions-Policy` allowing microphone/display-capture for self only. |
| `deploy/.env.example` | Domains, Postgres password, `VOXA_CREDENTIAL_KEY` (+ version), Zitadel master key, OIDC issuer/audience/client ID, Whisper model, summary providers. No defaults for secrets. |
| `deploy/setup.sh` | Generates secrets into `.env` (mode 600), downloads the Whisper model, creates the `zitadel` database, prints next steps. |
| `docs/deployment.md` | Operator guide (replaces this plan once implemented). |

Server configuration in production:
`VOXA_AUTH_MODE=oidc`, `VOXA_DATABASE_URL` (internal), `VOXA_CREDENTIAL_KEY`,
`VOXA_MEDIA_DIR=/data/media`, `VOXA_WHISPER_MODEL=/models/ggml-<model>.bin`,
`VOXA_SUMMARY_PROVIDERS` (operator choice), `VOXA_BOOTSTRAP_OIDC_SUBJECT`
(your Zitadel user ID, so you become workspace owner on first sign-in).
No `VOXA_ALLOWED_ORIGINS` needed (same origin). Development auth is never
used.

## 4. Sign-in (Zitadel)

1. First start creates the Zitadel admin (credentials from `.env`); sign in
   at `https://auth.example.com`.
2. Create a project **Voxa** and an application **Voxa Web**: type *User
   Agent* (public client), *PKCE*, redirect URI
   `https://voxa.example.com/auth/callback`, post-logout
   `https://voxa.example.com/`, **access token type JWT** (Voxa validates
   JWTs), and add the project ID to the audience.
3. Put the issuer, client ID and audience into `.env`; rebuild the web
   image (public values are compiled in) and restart the server.
4. Create your user, copy its ID into `VOXA_BOOTSTRAP_OIDC_SUBJECT`.
5. Optional: SMTP in Zitadel for password resets and invitations.

## 5. Provisioning steps

1. VPS (Ubuntu 24.04 or Debian 12), SSH keys only, unattended upgrades.
2. DNS: `A`/`AAAA` records for `voxa.` and `auth.` subdomains.
3. Firewall: allow 22 (your IP if possible), 80, 443.
4. Install Docker Engine + Compose plugin.
5. Clone the repo (or pull published images), `cd deploy`, run
   `./setup.sh`, edit `.env`, `docker compose up -d`.
6. Configure Zitadel (§4), restart.

## 6. Verification checklist (go-live)

1. `https://voxa.example.com/readyz` is 200; capabilities show media,
   transcription and summaries as expected.
2. Sign in with Zitadel; you are workspace owner.
3. Record a Google Meet (meeting tab + microphone): upload, remux,
   transcript, summary (API provider and export, §8).
4. Reload mid-recording and drop the network briefly: recording recovers.
5. `docker compose restart`: meetings, recordings and transcripts persist.
6. Backup and **restore test** on a scratch machine (§7).
7. Security: no dev auth (`/api/v1/me` without a token is 401), TLS A
   grade, PostgreSQL not reachable from outside.

## 7. Operations

- **Backups:** nightly `pg_dump` of both databases plus the media volume
  (restic to object storage, e.g. Backblaze B2). The credential encryption
  key must be backed up **separately** (without it, stored provider keys
  cannot be decrypted).
- **Updates:** tagged images; `docker compose pull && docker compose up -d`.
  Database migrations run automatically at server start (advisory-locked).
- **Monitoring:** container health checks on `/readyz`; disk-usage alert
  (media grows); optional uptime check on the public URL.
- **Logs:** JSON logs via `docker compose logs`; they contain no
  transcript text, tokens or keys.
- **Key rotation:** `VOXA_CREDENTIAL_PREVIOUS_KEYS` supports rotating the
  credential key without downtime.

## 8. Summaries: API providers + “export to your LLM”

Keep the existing server-side providers (OpenAI, Anthropic, Groq,
OpenRouter, Ollama, OpenAI-compatible) for automatic summaries. Add an
export path for people who use a ChatGPT/Claude/Gemini subscription instead
of API keys:

1. **Export for your LLM** on the meeting page: one click copies (or
   downloads as `.md`) the transcript with timestamps, wrapped in the same
   template prompt Voxa uses for its own summaries (template and output
   language selectable).
2. The user pastes it into their LLM chat.
3. **Paste summary back**: a text box stores the LLM's answer as the
   meeting summary, with provenance `provider: manual`, the transcript it
   was generated from and the template used. It appears like any other
   summary (stale detection on retranscription included).

Needs: one server endpoint to save a manual summary (validated Markdown,
size-limited, workspace-scoped), a prompt-builder shared with the server
templates, and the UI. No API keys are involved, and nothing is sent
anywhere by Voxa.

## 9. Order of work

1. Deployment artifacts (§3) and a local dry run of the whole stack with
   real HTTPS (Caddy internal CA) on this machine.
2. Zitadel wiring and the go-live checklist on the VPS.
3. Backups + restore test.
4. In parallel / next: UI redesign, then “export to your LLM” (§8).

## 10. Open questions for the operator

- Domain names for the app and sign-in.
- VPS provider and size; whether a GPU is worth it.
- Whisper model: `base.en` (fast, English), `small` (multilingual), or
  larger.
- Which API summary providers to enable on the server (none are enabled by
  default).
- Whether Zitadel should allow self-registration or invite-only.
