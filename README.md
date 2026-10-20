# Voxa

**A self-hosted meeting recorder and AI meeting assistant, without a bot
joining your call.** Voxa records a meeting straight from your browser or
desktop (your microphone plus the meeting tab's audio), keeps recording
through network drops, stores the audio on your own server, transcribes it
with Whisper and summarizes it with the LLM provider *you* choose. Think
Fireflies or Read.ai, but you own the data and there is no monthly SaaS fee.

Voxa is built on [Meetily](https://github.com/Zackriya-Solutions/meeting-minutes)
(MIT). It keeps Meetily's local desktop app and adds a self-hosted server
and a browser app, so a team can record meetings from a web browser and
keep everything on infrastructure they control.

> Status: active development on `feat/voxa-foundation`. Not production
> ready. See [What works today](#what-works-today) and the honest
> [compatibility status](docs/browser-capture-compatibility.md).

## What we are building

```
Browser (Voxa Web)                        Voxa server (Go, self-hosted)
  microphone ─┐                            ├─ meetings, workspaces, OIDC sign-in
  meeting tab ┴─ mix → chunks ──upload──▶  ├─ media storage (resumable uploads)
  (Meet, Teams, Zoom web, Webex, …)        ├─ processing queue (PostgreSQL)
                                           │   ├─ remux (ffmpeg)
  transcript ◀──── click-to-seek ───────── │   ├─ transcription (whisper.cpp)
  summary    ◀──────────────────────────── │   └─ summaries (your LLM provider)
                                           └─ encrypted provider credentials
Desktop app (Tauri, from Meetily): local recording, local Whisper, local summaries
```

Goals:

- **Record any browser meeting** without bots joining the call: your
  microphone plus the meeting tab's audio, chosen by you in the browser's
  share dialog.
- **Own your data**: self-hosted server, PostgreSQL, filesystem media,
  self-hosted Whisper. Nothing leaves your server unless you enable a
  cloud summary provider.
- **Reliable capture**: chunks are saved on the device first, so a network
  drop or a reload does not lose the recording.
- **Honest compatibility**: features are enabled by browser capability
  detection, and support for each meeting app is only claimed after it has
  been tested.

## What works today

Last updated 2026-10-20.

| Area | Status |
|---|---|
| Go server: PostgreSQL, encrypted provider credentials, OIDC sign-in, workspace isolation | working |
| Voxa Web: meetings, settings, API keys; responsive layout (desktop sidebar, phone menu) | working; first UI pass done |
| Browser recording: microphone only, meeting tab + microphone, meeting tab only | working in Chromium desktop (automated tests) |
| Real meetings | **Google Meet** (Fedora/Chrome): both sides captured and transcribed. **Teams web**: tester-reported working. Zoom, Webex, WhatsApp Web: not yet tried |
| Offline/reload recovery of recordings | working (automated tests, incl. a 30-minute run) |
| Remux, transcription (whisper.cpp), transcript with click-to-seek | working |
| Summaries (OpenAI, Anthropic, Groq, OpenRouter, Ollama, OpenAI-compatible) | working with a fake provider in tests; real providers not yet exercised |
| Recording source labels (capture provenance) | working |
| Desktop app (Meetily) | unchanged |
| Deployment | **planned**, not built: [docs/deployment-plan.md](docs/deployment-plan.md) |
| Firefox / Safari shared tab audio, mobile, Windows, macOS | untested; see [compatibility](docs/browser-capture-compatibility.md) |
| Cloud transcription, “export to your LLM”, browser extension, Android app, diarization | not started |

Known limitations from real use:

- The meeting must be open in the **same Chrome profile** as Voxa: the share
  dialog only lists tabs of the current profile.
- On Linux, sharing a whole screen or window gives **no audio**; share the
  meeting tab instead.

## Roadmap

Next, roughly in order:

1. **Deploy** on a VPS: Docker Compose with Caddy (HTTPS), PostgreSQL and
   Zitadel sign-in ([plan](docs/deployment-plan.md)). Needed before teammates
   can use it (browsers only allow capture on HTTPS).
2. **Validate real meetings**: Zoom web, Webex web, WhatsApp Web, Windows
   and macOS Chrome, headsets and speakers; record results in the
   [validation matrix](docs/browser-capture-compatibility.md).
3. **Better transcription**: switch to the free `large-v3-turbo` Whisper
   model (one setting), and optionally add cloud transcription (Groq,
   OpenAI; Deepgram/AssemblyAI for speaker labels) as an opt-in, with local
   Whisper staying the default.
4. **Export to your LLM**: copy the transcript with Voxa's summary prompt
   into ChatGPT/Claude/Gemini and paste the answer back as the summary (uses
   a subscription you already have instead of an API key).
5. **UI polish**: transcript view, recording-in-progress state, meeting
   status in the list.

Later: diarization, separate microphone/remote tracks, browser extension,
mobile/native capture, search, summary editing, team/admin features.

## Models and API keys

- **Transcription** runs locally with whisper.cpp; the model is the
  server's `VOXA_WHISPER_MODEL` (a GGML file from
  [whisper.cpp's models](https://huggingface.co/ggerganov/whisper.cpp)).
  `base.en` is fast but rough; `small.en` is better; `large-v3-turbo`
  (quantized) is the recommended free upgrade.
- **Summaries** use a provider the server operator enables
  (`VOXA_SUMMARY_PROVIDERS`), chosen in Settings, with an API key you add
  under API keys (stored encrypted, never shown again). Free tiers exist at
  Groq, OpenRouter and Google Gemini (via the OpenAI-compatible option);
  OpenAI and Anthropic are paid per use. API keys are billed separately
  from ChatGPT/Claude subscriptions.

## Try it on a meeting (local)

You need Go 1.25+, Node 20+ with pnpm, ffmpeg, and Google Chrome or
Chromium. Transcription additionally needs whisper.cpp (`whisper-cli`) and a
GGML model; summaries need an LLM provider (Ollama works locally).

```sh
# 1. Server (development auth: no sign-in, data in memory)
cd server
VOXA_AUTH_MODE=development \
VOXA_ALLOWED_ORIGINS=http://localhost:3118 \
VOXA_MEDIA_DIR=$HOME/.local/share/voxa/media \
VOXA_WHISPER_MODEL=/path/to/ggml-base.en.bin \
VOXA_SUMMARY_PROVIDERS=ollama VOXA_OLLAMA_URL=http://localhost:11434 \
go run ./cmd/voxa

# 2. Web app
cd frontend
pnpm install
cp .env.web.example .env.web.local      # development auth, API at http://localhost:8080
pnpm run dev:web                        # open http://localhost:3118 in Chrome
```

Then:

1. Join your meeting (Google Meet, Teams web, Zoom web client, Webex web…)
   in a Chrome tab, **in the same Chrome profile** you open Voxa in (the
   share dialog only lists tabs of the current profile).
2. In Voxa (another tab), create a meeting and choose **Meeting tab +
   microphone**.
3. Click **Choose tab and start recording**, pick the meeting tab in
   Chrome's share dialog and turn on **Share tab audio**.
4. Talk, let others talk, then **Stop**. The recording uploads, then is
   transcribed and (with a summary provider configured in Settings)
   summarized.
5. Please record what happened in the
   [manual validation matrix](docs/browser-capture-compatibility.md#5-manual-validation-matrix-to-be-filled-in).
   Tell the other participants they are being recorded.

Without `VOXA_WHISPER_MODEL` recording and playback still work, and
transcription reports itself unavailable. Without `VOXA_SUMMARY_PROVIDERS`
summaries are unavailable.

## Documentation

| Topic | Document |
|---|---|
| Server (configuration, storage, migrations) | [server/README.md](server/README.md) |
| Voxa Web runtime | [docs/voxa-web.md](docs/voxa-web.md) |
| Browser recording and uploads | [docs/voxa-web-recording.md](docs/voxa-web-recording.md) |
| Meeting tab capture | [docs/voxa-web-meeting-capture.md](docs/voxa-web-meeting-capture.md) |
| Compatibility status and validation matrix | [docs/browser-capture-compatibility.md](docs/browser-capture-compatibility.md) |
| Deployment plan | [docs/deployment-plan.md](docs/deployment-plan.md) |
| Processing and transcription | [docs/server-processing.md](docs/server-processing.md) |
| Summaries | [docs/server-summaries.md](docs/server-summaries.md) |
| Authentication | [docs/server-authentication.md](docs/server-authentication.md) |
| Credential security | [docs/server-credential-security.md](docs/server-credential-security.md) |
| Protocol | [docs/server-protocol-v1.md](docs/server-protocol-v1.md) |
| Desktop app build (Meetily) | [docs/BUILDING.md](docs/BUILDING.md) |

## Repository layout

```
server/                   Voxa server (Go): API, auth, media, processing, summaries
frontend/                 Next.js app: desktop (Tauri) and Voxa Web runtimes
frontend/src-tauri/       Desktop app (Rust/Tauri, from Meetily)
packages/voxa-protocol/   Shared TypeScript protocol types
backend/                  Archived Meetily Python backend (unsupported)
docs/                     Design and status documents
```

## License

MIT, see [LICENSE.md](LICENSE.md). Voxa is a fork of Meetily by Zackriya
Solutions.

## Acknowledgments

- [Meetily](https://github.com/Zackriya-Solutions/meeting-minutes), the desktop app Voxa builds on.
- [whisper.cpp](https://github.com/ggerganov/whisper.cpp) for transcription.
- Code borrowed by Meetily from [Screenpipe](https://github.com/mediar-ai/screenpipe) and [transcribe-rs](https://crates.io/crates/transcribe-rs).
- NVIDIA for the Parakeet model and [istupakov](https://huggingface.co/istupakov/parakeet-tdt-0.6b-v3-onnx) for its ONNX conversion.
