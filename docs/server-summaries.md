# Voxa server summaries

Status: server-side meeting summaries run as the `summary` processing job
type in the same in-process worker and PostgreSQL queue as remux and
transcription (see `docs/server-processing.md`). A completed transcript can
be summarized automatically or on request by an LLM provider the operator
enabled, and the result is shown in Voxa Web. The desktop (Tauri) summary
pipeline is unchanged.

Not implemented: diarization, speaker identification, semantic search,
chat with a meeting, editing server summaries, action-item automation
beyond what templates produce.

## 1. Architecture

```
transcription job completed ──(worker OnCompleted hook)──▶ SummaryService.AfterTranscription
                                                           (auto-summary on? config ready? transcript current?)
POST /meetings/{id}/summary ──▶ SummaryService.Request ──▶ processing_jobs: summary (queued, parameters snapshot)
worker claims summary ──▶ SummaryHandler
     transcript (snapshot ID) → "[MM:SS] text" lines
     summary.Registry.Open(workspace, provider)  ← workspace credential (decrypted in memory only)
     summary.Generate: chunk map → combine → template fill → translate/normalize
     SummaryRepository.Promote (same transaction re-checks job ownership)
GET /meetings/{id}/summary ◀── Voxa Web SummaryPanel (polls while queued/generating)
```

Packages: `internal/summary` (templates, prompts, chunking, output
cleanup, provider clients, registry, HTTP safety), `processing.SummaryHandler`,
`application.SummaryService` (state, request, cancel, auto-scheduling),
`api/summaries.go`, migration `0006_summaries.sql`.

## 2. Provider abstraction

```go
type Provider interface {
    Name() string
    Complete(ctx context.Context, model string, message Message) (Completion, error)
}
```

`Message` is a system + user prompt; `Completion` is the visible text,
whether reasoning was stripped, and token counts when reported. Failures
are `*summary.Error` with a `Kind` used for retry decisions. The pipeline
(`summary.Generate`) depends only on this interface, so tests use scripted
providers and fake HTTP servers.

Two clients implement it:

- `OpenAICompatible`: `POST {base}/chat/completions` (OpenAI schema). Used
  for `openai`, `groq`, `openrouter`, `custom-openai` and `ollama` (Ollama's
  `/v1` API). As on the desktop, Ollama requests send
  `reasoning_effort: "none"` and retry once without it if the server rejects
  the field.
- `Anthropic`: `POST {base}/messages`, `x-api-key`,
  `anthropic-version: 2023-06-01`, `max_tokens: 8192` (desktop value).

## 3. Credential access

- Provider keys are the existing encrypted workspace credentials
  (`PUT /api/v1/credentials/{provider}`), stored under the provider name
  (`openai`, `claude`, `groq`, `openrouter`, `custom-openai`, optional
  `ollama`).
- Only `summary.Registry.Open` reads them, through the internal
  `CredentialSecretReader`, for **the job's own workspace**. The decrypted
  value lives in the provider client for the duration of one job and the
  decrypted buffer is wiped afterwards (best effort, see
  `docs/server-credential-security.md`).
- Keys go only in the `Authorization` / `x-api-key` header of requests to
  the provider's configured endpoint. They are never logged, never stored in
  jobs or summaries, never part of API responses, and redacted from any
  provider error text before it is kept (`sanitize`: the key itself,
  `Bearer …`, and `sk-…`/`gsk_…` shapes).
- There is still no API to read a credential back.

## 4. Provider configuration

Operator (environment) configuration decides **which providers exist and
where requests go**:

| Variable | Default | Meaning |
|---|---|---|
| `VOXA_SUMMARY_PROVIDERS` | unset (none) | Comma-separated allow-list: `openai`, `claude`, `groq`, `openrouter`, `ollama`, `custom-openai`. Nothing is enabled by default, so transcripts never leave the server unless the operator opts in. |
| `VOXA_OLLAMA_URL` | — | Required when `ollama` is enabled, e.g. `http://ollama:11434`. |
| `VOXA_CUSTOM_OPENAI_URL` | — | Required when `custom-openai` is enabled: the API root that `/chat/completions` is appended to, e.g. `https://llm-gateway.internal/v1`. |

Cloud endpoints are fixed (`https://api.openai.com/v1`,
`https://api.anthropic.com/v1`, `https://api.groq.com/openai/v1`,
`https://openrouter.ai/api/v1`). Invalid values stop the server at startup.

Workspace configuration (`PATCH /api/v1/configuration`, Settings in Voxa
Web) chooses **provider, model, output language, template and
auto-generation** among the enabled providers. Workspace users never supply
URLs.

Summaries also require media processing to be running (media storage plus
ffmpeg/ffprobe), because they share its worker.

## 5. Transcript chunking

Behaviour is ported from `frontend/src-tauri/src/summary/processor.rs`:

- The transcript is formatted as on the desktop: one `[MM:SS] text` line
  per segment (recording-relative start time).
- Tokens are estimated as 0.35 × characters. When the estimate reaches the
  threshold, the text is split with the desktop `chunk_text` algorithm
  (chunk = threshold − 300 tokens, 100-token overlap, ending at the last
  sentence or word boundary that keeps more than the overlap). Each chunk is
  summarized, the chunk summaries are combined in one call, and the
  template pass runs on the combined text.
- Thresholds: Ollama uses the model's context length from `POST /api/show`
  minus 300 (fallback 4000, as on the desktop); hosted providers use the
  desktop's 100 000 tokens.
- **Divergence:** the desktop never chunks hosted providers; the server
  does above 100 000 tokens so no request is unbounded. A transcript that
  would need more than 200 chunk requests fails with
  `transcript_too_long`.
- Chunking is deterministic. After each chunk the job saves a checkpoint
  (`processing_jobs.checkpoint`: a fingerprint of transcript, model and chunk
  parameters, plus the finished chunk summaries) through a fenced write; a
  retried attempt resumes from it instead of re-sending finished chunks. A
  different transcript or model invalidates it.
- Context cancellation is checked before every provider call.

## 6. Template reuse

The six JSON templates in `frontend/src-tauri/templates` are copied into
`server/internal/summary/templates` and embedded; a test fails if a copy
drifts. Section instructions and the Markdown skeleton are rendered exactly
like `Template::to_section_instructions` / `to_markdown_structure`, and the
chunk, combine, final-report, translation and English-normalization
prompts are copied verbatim. The default template is `standard_meeting`.
`POST /meetings/{id}/summary` accepts an optional `templateId` and
`customPrompt` (≤ 4000 characters, added as the desktop's
`<user_context>` block).

Output language follows the desktop: a non-English target triggers a
translation pass; otherwise, unless the transcript language is known to be
English, an English-normalization pass runs and its failure falls back to
the first-pass report.

Result shape (`summaries.content`, JSONB):

```json
{ "format": "markdown", "markdown": "**Summary**\n\n…", "title": "Weekly sync",
  "englishMarkdown": "…only when translated…", "chunkCount": 1,
  "reasoningStripped": false, "normalizationFallback": false }
```

The desktop result is Markdown too (`{"markdown": …}`), so the server stores
Markdown inside a small structured object rather than inventing a JSON
summary schema. The title (`# …` first line) is lifted out as on the
desktop; the server does **not** rename the meeting.

Validation before anything is stored: provider envelope must be valid
JSON with content; `<think>` reasoning is removed and an unterminated
marker is rejected; the report must have visible content after the title;
at most 256 KiB of Markdown. Anything else fails the attempt
(`invalid_output`) and nothing partial is persisted.

## 7. Retries

| Code | Retry | Cause |
|---|---|---|
| `provider_rate_limited` | yes, at least `Retry-After` (capped at 1 h) | HTTP 429 |
| `provider_unavailable` | yes | 5xx, 408, connection failures (e.g. Ollama down) |
| `timeout` | yes | 300 s per request (desktop value) |
| `invalid_output` | yes | malformed or empty provider output |
| `provider_auth_failed` | no | 401/403 |
| `provider_model_unavailable` | no | 404 |
| `provider_rejected` | no | other 4xx |
| `provider_redirect` | no | 3xx (never followed) |
| `provider_response_too_large` | no | > 4 MiB response |
| `summary_not_configured` | no | missing credential/provider at run time |
| `transcript_changed`, `transcript_too_long`, `empty_transcript`, `invalid_template`, `superseded` | no | see below |

Summary jobs have 3 attempts with 1 min / 5 min / 15 min backoff (longer
than media jobs so providers are not hammered).

## 8. Cancellation

`POST /api/v1/meetings/{id}/summary/cancel` (or `/jobs/{id}/cancel`) marks
the job cancelled. The worker's next heartbeat (≤ ~10 s) cancels the job
context, which aborts the in-flight HTTP request and stops before the next
chunk. A cancelled or reclaimed attempt cannot store a result: `Promote`
locks the job row and requires `status = running` and the same attempt in
the transaction that writes the summary (`superseded` otherwise).

## 9. Provenance

Each summary stores `transcript_id`, `media_asset_id`, `processing_job_id`,
`provider`, `model`, `template_id`, `output_language`, timestamps, and
internal `usage` (`inputTokens`/`outputTokens` when the provider reports
them, request count, duration). Usage is not exposed by the API and is not
billing. The job's `parameters` snapshot (transcript, provider, model,
template, language, user context) is taken at enqueue; later configuration
changes do not affect a queued job.

There is one current summary per meeting. Regeneration never deletes it
first: the new result replaces it only when its job succeeds, so a failed or
cancelled regeneration leaves the previous summary visible (state `failed`
or `cancelled`, `summary` still returned), matching the desktop fallback.

## 10. Retranscription

A summary describes one transcript: the meeting's current transcript is the
transcript of its most recent ready recording (as `GET
/meetings/{id}/transcript`). Rules:

- Summaries start only from a final transcript (no transcription job
  active for that recording); otherwise `409 transcript_not_ready`.
- After retranscription the old summary is kept but reported as
  `stale: true` with `currentTranscriptId`; Voxa Web labels it.
- With auto-generation on, a completed transcription cancels any active
  summary job for the meeting and queues one for the new transcript.
- A queued job whose snapshotted transcript was replaced fails with
  `transcript_changed` instead of summarizing different text.

## 11. Web summary UI

`HttpSummaryClient` (`frontend/src/clients/http/summary-client.ts`)
implements the shared `SummaryClient` (remote execution, no local runtime
preflight; `text`/`provider`/`modelName` fields of the desktop request are
ignored) and `ServerSummaryClient` (`getMeetingSummary`, `generateSummary`,
`cancelSummary`, `listSummaryTemplates`). `SummaryPanel` on the meeting page:

| State | UI |
|---|---|
| `none` | “Generate summary” (or “once a transcript is ready”) |
| `queued` / `generating` | status (+ progress) and Cancel; previous summary still shown |
| `ready` | title, Markdown (react-markdown + GFM tables, raw HTML not rendered), provider/model/template, Regenerate; stale notice when applicable |
| `failed` / `cancelled` | message + Retry; previous summary kept |
| `unavailable` | reason: not enabled on this server, provider/model/API key missing |

Desktop summary components are BlockNote/Tauri-coupled, so the web uses
this thin read-only presentation. Editing server summaries is not supported.

## 12. Security considerations

- Workspace isolation: every query is scoped by workspace; summary jobs read
  the transcript only within the job's workspace and meeting; credentials
  are revealed only for the job's workspace. Cross-workspace requests get
  404.
- SSRF: only operator-configured endpoints are contacted. Loopback and
  private addresses are allowed **on purpose** for self-hosted Ollama or
  gateways; plaintext `http` to a non-local host is accepted with a startup
  warning. Workspace users cannot redirect a request (or another
  workspace's key) anywhere.
- Redirects are never followed (a redirect could forward the key).
- Bounded I/O: 300 s per request, 4 MiB responses, 4 KiB of error body
  read, 300 characters of sanitized detail kept in `internal_error`.
- API errors carry only a code and a fixed public message; provider bodies
  never reach clients.
- Logs contain job IDs, error codes and provider names only — no
  transcript text, summary text, prompts, keys or URLs with credentials.
- Provider output is untrusted: rendered as Markdown without HTML.

## 13. Supported providers

`openai`, `claude` (Anthropic), `groq`, `openrouter`, `ollama`,
`custom-openai`. Groq, OpenRouter, Ollama and custom endpoints share the
OpenAI-compatible client. The desktop-only `builtin-ai` (local sidecar) is
not available on the server.

## 14. Known limitations

- Only tested against fake providers in automated tests; no paid provider
  was called. Summary quality depends on the provider/model and has not been
  evaluated.
- A meeting with several recordings is summarized from its most recent
  recording's transcript only.
- Summaries share the processing worker (default concurrency 1): a long
  transcription delays summaries.
- The combine step is single-level, as on the desktop; very many chunk
  summaries are combined in one request.
- No per-workspace provider allow-list beyond the operator list; no token
  budgets or cost limits.
- Provider availability is not probed: capabilities show what the operator
  enabled, and a down provider fails jobs with retries.
- The server does not rename meetings from summary titles (the desktop
  does).

## Manual test checklist

1. Start without `VOXA_SUMMARY_PROVIDERS`: capabilities show
   `summary.available: false`; the meeting page says summaries are not
   available; transcription still works.
2. Start with `VOXA_SUMMARY_PROVIDERS=openai` (or `ollama` +
   `VOXA_OLLAMA_URL`): capabilities list it; log shows `summaries enabled`.
3. In Settings choose provider/model, add the API key; enable “Generate
   summaries automatically”.
4. Record ~1 min of speech: after the transcript appears, the summary goes
   queued → generating → ready with the standard template sections.
5. Regenerate: the old summary stays visible until the new one replaces it.
6. Set a wrong API key and regenerate: failed with “rejected the configured
   credentials”; the previous summary remains.
7. Cancel during generation: state cancelled within ~10 s.
8. Retranscribe the recording: the summary shows the stale notice; with
   auto-generation a new summary follows.
9. Another workspace cannot read or trigger the meeting's summary (404).
10. Server logs contain no transcript/summary text and no API key.
