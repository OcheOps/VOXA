# Voxa server media processing and transcription

Status: first asynchronous pipeline. A finalized browser recording is
remuxed (container repair, no re-encoding) and then transcribed with
self-hosted whisper.cpp. Transcripts are stored in PostgreSQL and shown in
Voxa Web with click-to-seek. Summaries run as a third job type in the same
queue and worker (see `docs/server-summaries.md`). Diarization and speaker
identification are **not** implemented.

The desktop (Tauri) app is unchanged: it still transcribes locally with
whisper-rs/Parakeet and never talks to these workers.

## 1. Architecture

```
browser ──chunks──▶ API ──finalize──▶ media_assets (ready)
                                   └─▶ processing_jobs: remux (queued)
worker loop (in the voxa process)
  claim remux ─▶ ffprobe + ffmpeg -c copy ─▶ new object, asset.storage_key switched
             └─▶ enqueue transcription
  claim transcription ─▶ ffmpeg → 16 kHz mono PCM WAV ─▶ whisper-cli -oj
             └─▶ transcripts + transcript_segments (replace)
Voxa Web ◀── GET /media/{id}/transcript (polls while preparing/transcribing)
```

- Workers run inside the existing server process (`server/internal/processing`),
  started by `cmd/voxa` when media storage is enabled. There is no separate
  worker binary, broker or distributed scheduler; PostgreSQL is the queue.
- Packages: `domain` (job/transcript types), `repository` (ports),
  `adapters/postgres|memory` (queue + transcript storage),
  `processing` (exec helpers, ffmpeg/ffprobe, whisper engine, handlers,
  worker), `application.ProcessingService` (state for the API),
  `api/processing.go` (HTTP).

## 2. PostgreSQL queue (`0005_processing.sql`)

`processing_jobs` columns: `id`, `workspace_id`, `meeting_id` (FK),
`media_asset_id` (FK, `ON DELETE CASCADE`), `type` (`remux` |
`transcription`), `status` (`queued` | `running` | `completed` | `failed` |
`cancelled`), `attempts`, `max_attempts`, `run_after`, `lease_expires_at`,
`progress`, `error_code`, `error_message` (public), `internal_error`
(server-side only, never returned by the API), timestamps.

- **One active job per recording and type:** partial unique index
  `processing_jobs_one_active_idx (media_asset_id, type) WHERE status IN
  ('queued','running')`. A duplicate enqueue is a conflict (API: `409
  already_processing`).
- **Claim** is a single statement: a CTE selects runnable rows (`queued`
  with `run_after <= now`, or `running` with an expired lease, and
  `attempts < max_attempts`) `ORDER BY run_after, created_at ... FOR UPDATE
  SKIP LOCKED`, then sets `running`, increments `attempts` and sets the
  lease. Concurrent workers never claim the same row and never block on
  each other.
- **Fencing:** `attempts` after the claim is the worker's token. Heartbeat,
  complete, fail and release all match `id AND attempts AND status='running'`,
  so a worker whose lease expired and whose job was re-claimed cannot
  overwrite the new attempt's result.
- **Exhaustion sweep:** `FailExhausted` marks `running` jobs whose lease
  expired with no attempts left as `failed` (`abandoned`).
- `transcripts` (one per media asset, `UNIQUE (media_asset_id)`) and
  `transcript_segments` (`PRIMARY KEY (transcript_id, sequence)`,
  `0 <= start <= end`) hold results. `media_assets` gains
  `original_storage_key` and `processed_at`.

## 3. Worker lifecycle

`WorkerConfig` defaults: concurrency 1 (`VOXA_PROCESSING_WORKERS`, 1–16),
lease 60 s, heartbeat 10 s, idle poll 2 s.

1. Each slot claims one job, runs its handler and heartbeats every 10 s
   (extending the lease and saving progress).
2. A heartbeat that no longer owns the job (cancelled or re-claimed)
   cancels the handler's context, which terminates the child process group.
3. Outcome: success → `completed`; retryable failure with attempts left →
   back to `queued` with `run_after = now + backoff`; otherwise `failed`.
4. Shutdown (SIGTERM): contexts are cancelled, child processes are killed,
   and running jobs are **released** to `queued` without consuming an
   attempt. `main` waits for workers before closing the database pool.
5. Crash: the lease expires and another claim recovers the job (that
   attempt counts).
6. Maintenance every minute: reconcile (enqueue remux for `ready` assets
   with `processed_at IS NULL` and no job, e.g. recordings finalized
   while workers were disabled) and `FailExhausted`. Stale work
   directories (> 24 h) are removed by the existing media cleanup loop.

Logs contain `job_id`, `job_type`, `workspace_id`, `meeting_id`,
`media_asset_id`, `attempt`, `duration_ms` and `error_code` only — never
transcript text, file paths, internal error text, credentials or tokens.

## 4. Remux

Browser `MediaRecorder` output is a live stream: WebM without duration or
cues (seeking is slow or broken), sometimes MP4 fragments. Remux rewrites
the container without touching audio samples:

```
ffmpeg -nostdin -hide_banner -loglevel error -y -i <in> -map 0:a:0 -c copy \
       [-movflags +faststart for mp4] -f <webm|ogg|mp4> <tmp out>
```

**Derivation model:** the same MediaAsset points at the processed object.
The original upload key is kept in `original_storage_key` (the file is not
deleted), `storage_key` switches to the new `obj_…` file, and
`processed_at`, `duration_seconds`, `size_bytes` are updated in one row
update. IDs, signed playback URLs and the client API do not change, and a
second MediaAsset type was not needed.

Outcomes: valid → processed object; ffprobe says not audio / unreadable →
`invalid_media` (permanent, no transcription); ffmpeg fails on every
attempt → the asset is marked processed **with the original** and
transcription still runs on it (`remux_failed` is recorded on the remux job).

Output is written to `<media>/tmp/work-*`, fsynced and atomically renamed
into place; work directories are removed afterwards and stale ones after
24 h by media cleanup. Deleting the recording deletes both the processed
object and the original.

## 5. ffmpeg / ffprobe dependency

Both must be on `PATH`. If either is missing the server logs a warning and
runs with **processing disabled** (uploads and playback still work;
transcription reports unavailable). All tool invocations use argument
arrays (no shell), `-nostdin`, server-generated paths inside the media
root, a timeout (probe 2 min, remux 20 min, decode 60 min), their own
process group (SIGTERM, then SIGKILL after a grace period), and stderr
capture bounded to 16 KiB which is kept only in `internal_error`.

## 6. Whisper engine

Chosen: **whisper.cpp `whisper-cli`** as a child process.

- Same engine and GGML model files as the desktop app's whisper-rs, so
  quality and model knowledge carry over; MIT licence; CPU by default with
  optional CUDA/Vulkan/Metal builds; no Python runtime.
- Invoked as `whisper-cli -m <model> -f <audio.wav> -l <lang|auto> -oj -of
  <work>/result -np -pp [-t N] [-tr]`. Results are read from the JSON file
  (segment `offsets` in milliseconds); `-pp` progress lines on stderr drive
  job progress.
- Rejected: faster-whisper / WhisperX (Python + CTranslate2 stack, separate
  model format); a long-running whisper.cpp HTTP server (another service to
  run and secure); linking whisper.cpp into Go via cgo (build complexity,
  a crash takes down the API); paid cloud APIs (privacy, and excluded).
- Engine is behind the `processing.Engine` interface so another engine can
  be added without touching the queue or API.

## 7. Model configuration

| Variable | Default | Meaning |
|---|---|---|
| `VOXA_WHISPER_MODEL` | unset | Path to a GGML model file (e.g. `/var/lib/voxa/models/ggml-base.en.bin`). Unset → transcription unavailable. |
| `VOXA_WHISPER_BIN` | `whisper-cli` | whisper.cpp CLI binary (name on `PATH` or absolute path). |
| `VOXA_PROCESSING_WORKERS` | `1` | Concurrent jobs in this process (1–16). |

At startup the server checks the model file (exists, ≥ 1 MiB) and that the
binary runs (`--help`). Failure is logged and transcription is reported
unavailable; the server still starts. Models are never downloaded by the
server and are not in Git: operators download them, e.g.
`https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.en.bin`.
Language and translate-to-English come from the workspace transcription
configuration (`auto` when unset). `asr_model` records the model file name
(without `.bin`) on each transcript. Changing the model takes effect on
restart; existing transcripts keep their provenance and can be re-run.

## 8. Transcript persistence

- One transcript per recording; re-transcription **replaces** it (delete
  segments + insert in one transaction) so the API never shows a mix.
- Segment fields: `sequence` (0-based, ordered), `start_seconds`,
  `end_seconds`, `text` (trimmed; empty segments dropped), `confidence`
  (always `NULL`: whisper.cpp JSON gives no calibrated segment confidence,
  and Voxa does not fabricate one).
- Transcript fields: `language` (detected/configured), `asr_engine`
  (`whisper.cpp`), `asr_model`, `processing_job_id`.
- All segments are final (no partial/streaming results server side).

## 9. Timestamp provenance

Times are **recording-relative seconds** from the start of the stored
recording, produced by the ASR engine (`timingProvenance: "asr"`). They are
clamped to the probed duration. They are not wall-clock times, not aligned
to other recordings of the meeting, and not word-level. Segment IDs are
`<transcriptId>:<sequence>` and change if the recording is re-transcribed.

## 10. Retries and cancellation

| Code | Retry | Meaning |
|---|---|---|
| `timeout`, `storage_unavailable`, `processing_failed`, `transcription_failed`, `remux_failed` | yes, up to 3 attempts, backoff 30 s / 2 min / 10 min | transient tool or storage failure |
| `invalid_media`, `media_missing`, `invalid_job`, `engine_unavailable` | no | permanent |
| `abandoned` | — | lease expired with no attempts left |

`POST /api/v1/jobs/{id}/cancel` sets `cancelled` immediately; a running
worker notices at its next heartbeat (≤ ~10 s) and kills the child process
group. Cancelling a remux marks the asset processed with the original file
so reconciliation does not re-queue it. `POST
/api/v1/media/{id}/transcription` re-runs transcription (only one active
job per recording).

## 11. Capabilities

`GET /api/v1/capabilities` reports `transcription: {available: true,
providers: ["whisper.cpp"]}` only when media storage is enabled, ffmpeg and
ffprobe were found, and the whisper check passed. `summary` stays
unavailable. With transcription unavailable, transcript reads return
`state: "unavailable"` and transcription requests return `503`.

## 12. Web transcript flow

`HttpTranscriptionClient` (`frontend/src/clients/http/transcription-client.ts`)
is the web `TranscriptionClient` (`execution: 'remote'`, no model
management/download, no imports) and `ServerTranscriptClient`
(`getRecordingTranscript`, `getMeetingTranscript`, `requestTranscription`,
`cancelProcessing`). Each stored recording in a meeting shows its player and
a `TranscriptPanel`:

| State | UI |
|---|---|
| `preparing` | “Preparing recording…” + Cancel |
| `transcribing` | “Transcribing…” + Cancel (job progress is stored and listed under `/meetings/{id}/jobs`, not shown yet) |
| `ready` | segments (`m:ss` + text); clicking seeks the player to the segment start; “Transcribe again” |
| `failed` / `cancelled` | public error message + Retry |
| `unavailable` | “Transcription is not available on this server.” |
| `none` | “Transcribe” button |

The panel polls every 3 s only while preparing/transcribing and reloads the
recording list when processing finishes (duration may change after remux).

## 13. Deployment

Install `ffmpeg` (includes `ffprobe`) and a whisper.cpp build providing
`whisper-cli` (distribution package, source build, or the official image
`ghcr.io/ggml-org/whisper.cpp`). Put models outside the repository, readable
by the server user. The worker writes only inside `VOXA_MEDIA_DIR`.
Running several server processes against one database is safe (SKIP LOCKED
claims and fenced updates), but each needs the same media directory.

## 14. CPU and GPU

CPU works everywhere; on CPU, `tiny`/`base` run faster than real time on
modern machines, `small`/`medium` are slower and `large-v3` is impractical
without a GPU. GPU acceleration is a property of the whisper.cpp build
(CUDA, Vulkan, Metal) and needs no Voxa change. Keep
`VOXA_PROCESSING_WORKERS` low (1–2) on CPU: whisper.cpp already uses
several threads per job. There is no GPU scheduling or autoscaling.

## 15. Future: diarization and speakers

Not implemented. A later job type would read the same 16 kHz WAV and write
speaker labels onto segments (`source` is reserved in the protocol) without
changing the queue. Speaker identification needs consent and enrolment
design first.

## 16. Summaries

Implemented as the `summary` job type: queued after a completed
transcription (worker `OnCompleted` hook, when auto-generation is enabled)
or on request, with an immutable parameter snapshot, chunk checkpoints,
`Retry-After`-aware retries and fenced result promotion. One active summary
job per meeting (`processing_jobs_one_active_summary_idx`). See
`docs/server-summaries.md`.

## Quality and limitations

Transcription quality has been checked only on a short clean English sample
(JFK clip) with `ggml-tiny.en`; it has **not** been evaluated on real
meetings, long recordings, noisy audio, accents or non-English speech.
Known limitations: no diarization; segment-level timing only; whisper may
hallucinate text on silence/music; one transcript per recording (a meeting
with several recordings shows each separately); progress depends on
whisper's coarse progress output.

## Manual test checklist

1. Start PostgreSQL, set `VOXA_MEDIA_DIR`, `VOXA_WHISPER_MODEL` (and
   `VOXA_WHISPER_BIN` if needed), start the server; startup log shows
   processing enabled and transcription available; `/api/v1/capabilities`
   lists `whisper.cpp`.
2. Start without `VOXA_WHISPER_MODEL`: server starts, capability says
   unavailable, the meeting view says transcription is not available,
   playback still works.
3. Rename `ffmpeg` off `PATH`: warning logged, processing disabled.
4. In Voxa Web record ~30 s of speech, stop: “Preparing recording…” then
   “Transcribing…”, then segments appear.
5. Duration is shown correctly and the player can seek anywhere (remux).
6. Click a later segment: playback jumps to that time and plays.
7. “Transcribe again”: state goes back to transcribing; the result replaces
   the old one.
8. Cancel during transcribing: state becomes cancelled within ~10 s; the
   whisper process is gone (`pgrep whisper-cli`).
9. Stop the server during transcription: the job returns to `queued`
   (attempt not consumed) and completes after restart.
10. `kill -9` the server during transcription: after the 60 s lease the job
    is re-claimed and completes.
11. Upload a non-audio file as a recording (curl): the remux job fails with
    `invalid_media`, the UI shows the error, no transcription runs.
12. Second workspace user cannot read the transcript, jobs or cancel
    (404).
13. Server logs contain no transcript text, media paths or tokens.
14. The desktop app still records and transcribes locally.
