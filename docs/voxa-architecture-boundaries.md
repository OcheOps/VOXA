# Proposed Voxa Rust boundaries

This is a migration map, not a mandate to split the current Tauri crate at
once. Each boundary should be introduced only when a real second caller (for
example, a server process) needs it. Tauri command registration, window/event
emission, tray, dialogs, notifications and app-path resolution stay in the
desktop shell.

## `voxa-domain`

- Move later: shared meeting/transcript types from `src/api/api.rs`,
  `src/database/models.rs`, and the non-I/O parts of
  `src/audio/recording_state.rs`.
- Own: identifiers, timestamps, meeting/transcript/summary value objects and
  validation. Prefer `serde`, `chrono`, and `uuid` only.
- Must not own: SQLx, Tauri, CPAL, Whisper, HTTP clients, filesystem paths, or
  UI event payload mechanics.
- Interfaces: stable domain inputs/results consumed by storage, transcription,
  summary and protocol crates.

## `voxa-audio-core`

- Move later: platform-neutral DSP from `src/audio/audio_processing.rs`,
  `pipeline.rs`, `vad.rs`, `buffer_pool.rs`, `batch_processor.rs`,
  `audio_v2/{limiter,mixer,normalizer,resampler,sync}.rs`, and portable
  decoding/encoding where practical.
- Own: sample buffers, resampling, normalization, mixing, VAD and track routing;
  dependencies such as `rubato`, `realfft`, `ebur128`, `nnnoiseless`, and
  `silero`.
- Must not own: Tauri state/events, device permission UI, OS device discovery,
  app directories, or database writes.
- Interfaces: `AudioSource`, `AudioFrame`, `TrackSink`, `SpeechSegmentSink`, and
  a pipeline configuration/result API.

## `voxa-capture-desktop`

- Move later: `src/audio/capture/`, `devices/`, `stream.rs`,
  `system_audio_stream.rs`, `device_monitor.rs`, `permissions.rs`,
  `playback_monitor.rs`, and the OS-specific portions of `recording_manager.rs`.
- Own: CPAL and native Core Audio/Windows/Linux device APIs, device lifecycle,
  permissions, and conversion from devices into `voxa-audio-core` frames.
- Must not own: meetings, SQLite, summary providers, or Tauri commands/events.
- Keep outside: `recording_commands.rs`, Tauri managed state and frontend event
  emission.
- Interfaces: enumerate/open/watch capture devices and emit typed mic/system
  frames plus device events.

## `voxa-transcription`

- Move later: `src/audio/transcription/`, `src/whisper_engine/`,
  `src/parakeet_engine/`, `src/audio/stt.rs`, and transcription-specific batch
  orchestration in `import.rs`/`retranscription.rs`.
- Own: engine selection, model lifecycle, Whisper/Parakeet inference, language
  configuration, work queues and timestamped transcript output.
- Must not own: Tauri, meeting persistence, window events, capture devices or
  summary generation.
- Keep outside: command modules and progress-event adaptation.
- Interfaces: `Transcriber`, `TranscriptionJob`, `TranscriptSegment`, model
  inventory/download ports, progress and cancellation callbacks.

## `voxa-summary`

- Move later: platform-neutral code from `src/summary/`, plus provider clients
  in `src/{anthropic,groq,ollama,openai,openrouter}/`.
- Own: templates, prompt construction, provider-neutral LLM requests,
  cancellation, language detection and optional provider adapters.
- Must not own: Tauri commands, meeting SQL, capture or playback.
- Interfaces: `SummaryGenerator`, `SummaryRepository`, `TemplateRepository`,
  and provider/configuration ports. Provider credentials remain supplied by the
  host process.

## `voxa-storage`

- Move later: `src/database/`, persistence portions of `src/api/api.rs`, and
  meeting/audio metadata persistence from `recording_saver.rs` and
  `incremental_saver.rs`.
- Own: repository traits plus the current SQLite implementation and migrations;
  SQLx belongs only in its adapter layer.
- Must not own: Tauri paths/state, UI events, capture, transcription engines or
  LLM clients. A host supplies the storage root and database connection.
- Interfaces: `MeetingRepository`, `TranscriptRepository`, `SummaryRepository`,
  `MediaRepository`, and transactional unit-of-work operations.

## `voxa-protocol`

- Move later: transport DTOs currently spread across `src/api/api.rs`, Tauri
  command argument/result structs and frontend `src/types/`.
- Own: versioned, serializable request/result/event shapes and compatibility
  rules. It may depend on `voxa-domain` only where that does not expose internal
  persistence details.
- Must not own: Tauri, HTTP frameworks, SQLx, CPAL, inference engines, or
  business workflow implementations.
- Interfaces: DTO conversion at both Tauri and future HTTP/WebSocket edges.

## Migration order

Start with protocol/domain types that already cross the Tauri boundary, then
extract storage repository traits, transcription, audio core, desktop capture,
and summary. Keep one deployable desktop process throughout; crate boundaries
are for ownership and reuse, not microservices.

## Current frontend boundary

`frontend/src/clients/contracts.ts` defines transport-neutral capability
interfaces. `frontend/src/clients/tauri/` adapts existing commands and events,
and `VoxaClientProvider` supplies the aggregate client. Meeting CRUD/retrieval
and the active recording workflow now use this boundary. Other direct Tauri
calls intentionally remain and are tracked in `frontend-tauri-coupling.md`.

## Recording client boundary

The current desktop flow is:

```text
React UI
  -> RecordingClient
  -> TauriRecordingClient
  -> existing Tauri commands/events
  -> existing Rust AudioPipeline
```

`TauriRecordingClient` owns native command/event names, native payload casing,
the asynchronous Tauri listener lifecycle, and desktop save-path construction.
React sees normalized devices, state, events and intent-level operations such
as `start()`, `pause()`, `resume()` and `stop()`.

The future browser flow can use the same consumer boundary without sharing the
desktop implementation:

```text
React UI
  -> RecordingClient
  -> BrowserRecordingClient
  -> getUserMedia / getDisplayMedia / MediaRecorder / AudioContext
```

`RecordingCapabilities` makes transport differences explicit:

- `systemAudioCapture` distinguishes native capture, display-media capture and
  no system-audio support.
- `deviceEnumeration` distinguishes full input/output enumeration, input-only
  enumeration and unavailable enumeration.
- `microphoneHotSwap` distinguishes automatic recovery, manual switching and
  no switching.
- `audioLevelMonitoring`, `pauseResume`, and `tabCapture` are independent
  features rather than assumed parity.

A browser implementation can therefore report its real support and reject or
hide unsupported UI. It need not accept a native save path: `stop()` expresses
finalization intent, and each adapter owns how its transport materializes the
recording.

Inherently platform-specific behavior remains outside or behind the adapter:

- native microphone/system device enumeration and naming;
- desktop automatic microphone fallback events;
- operating-system permissions and settings links;
- tray/global shortcut recording requests;
- native recordings-folder preferences and folder opening;
- selection of CPAL/Core Audio/WASAPI/Pulse-compatible capture backends.

Transcription failures and completion remain owned by `TranscriptionClient`.
Only speech activity, chunk-drop warnings and session/lifecycle events live on
`RecordingClient` because their primary owner is the active capture pipeline.

## Transcription client boundary

The current desktop path is:

```text
React UI
  -> TranscriptionClient
  -> TauriTranscriptionClient
  -> existing Tauri IPC
  -> existing Whisper / Parakeet runtime
```

The client owns selected-provider readiness, provider initialization, model
inventory, transcription queue status, timestamped live transcript events,
transcription errors/completion, model download events, retranscription jobs,
and the transcription phase of imported-audio jobs. It normalizes native
snake_case payloads while preserving recording-relative transcript timestamps,
duration, sequence, partial/final state and confidence.

Persistent provider/model/language preferences remain configuration concerns.
Native file selection is owned by `MediaClient`. Provider-specific
model-manager commands temporarily remain in `lib/whisper.ts` and
`lib/parakeet.ts`; their event streams now cross `TranscriptionClient`.

The future server path can preserve the React contract:

```text
React UI
  -> TranscriptionClient
  -> HttpTranscriptionClient
  -> Voxa API / job system
  -> transcription worker
```

`TranscriptionCapabilities` states that the desktop implementation executes
locally and supports model management/download, retranscription, imported
audio, and Whisper translation-to-English. A server adapter can report remote
execution and model download as unavailable/not applicable without pretending
that a browser installs local Whisper models. Provider-specific configuration
can remain an optional UI surface selected according to capabilities.

## Media client boundary

The current desktop path is:

```text
React UI
  -> MediaClient
  -> TauriMediaClient
  -> Tauri IPC / native filesystem
```

React works with opaque `MediaSource` values, normalized import metadata and
`MediaPlaybackSource`. It does not parse recording paths or know native command
names. The Tauri adapter resolves meeting folders, selects and validates import
files, reads local bytes and opens native recording folders. Audio decoding,
play/pause/seek and duration remain browser UI behavior in `useAudioPlayer`.

The future web path can preserve the same React contract:

```text
React UI
  -> MediaClient
  -> HttpMediaClient
  -> Voxa API
  -> object storage / range-capable playback
```

`MediaPlaybackSource` can carry in-memory bytes (used by desktop today) or a
browser-consumable URL. An HTTP adapter can therefore return an authenticated
or short-lived range-capable URL without changing playback controls.
`MediaCapabilities` independently reports native picking, filesystem reveal,
local byte reads, URL playback, ranged streaming, import and export/download;
browser/server transports do not pretend to have desktop filesystem access.

Ownership remains narrow:

- `MediaClient` selects, validates, locates, reads and reveals media.
- `TranscriptionClient` runs ASR over an opaque media source.
- `RecordingClient` captures and finalizes active recording sessions.
- `SummaryClient` generates and persists meeting summaries.
- configuration owns persistent folder, provider and language preferences.

Desktop drag/drop event receipt remains at the application-shell boundary.
Native default-folder preferences and checkpoint recovery remain in their
configuration/persistence boundaries rather than being absorbed into media.

## Summary client boundary

The current desktop path is:

```text
React UI
  -> SummaryClient
  -> TauriSummaryClient
  -> Tauri IPC
  -> existing summary processor / LLM provider
```

`SummaryClient` owns asynchronous job creation, state/result reads,
cancellation, edited-summary persistence, template inventory and the Ollama or
built-in-model readiness checks required immediately before execution. Native
snake_case job identifiers are normalized at the adapter. Generation and
regeneration intentionally share the same request because the existing native
pipeline distinguishes them through UI state rather than a different command.

The desktop processor is polling-based; `SidebarProvider` keeps the established
five-second interval, timeout, request ownership and navigation restoration,
but polling reads now cross `SummaryClient` instead of invoking Tauri directly.
There are no active native summary lifecycle events to subscribe to.

The future server path can preserve the shared React workflow:

```text
React UI
  -> SummaryClient
  -> HttpSummaryClient
  -> Voxa API / job system
  -> summary worker
```

`SummaryCapabilities` represents local, remote or mixed execution, asynchronous
jobs, polling, cancellation, templates, runtime preflight and the providers
available through a transport. An HTTP adapter can therefore expose only
server-approved providers, return remote job identifiers, poll server state and
cancel jobs without browser access to llama.cpp or local model files.

Ownership remains separated:

- `SummaryClient` owns runtime execution and job/result lifecycle.
- `TranscriptionClient` produces transcript content; it does not summarize it.
- `MeetingClient` supplies persisted transcript pages consumed by summaries.
- `MediaClient` owns recording/import media and playback, not summary content.
- configuration owns saved provider, API key, endpoint, model, output language,
  template preference and auto-summary defaults.
- model management owns built-in model download, installation, deletion and
  folder operations; only execution-time readiness crosses `SummaryClient`.

## Configuration boundary

The desktop configuration path is now:

```text
React UI
  -> ConfigurationClient
  -> TauriConfigurationClient
  -> existing native database / preferences store
```

`ConfigurationClient` owns persisted transcription and summary defaults,
provider credentials/endpoints, recording/storage defaults, and notification
preferences. Its capability description states that the desktop adapter has
desktop storage locations, device defaults and native notification preferences,
but does not claim remote credential storage. Existing API keys remain in the
same native persistence unchanged; this task does not encrypt, mask, rotate or
migrate them.

The future server path is:

```text
React UI
  -> ConfigurationClient
  -> HttpConfigurationClient
  -> Voxa API
  -> user/workspace settings + encrypted credential storage
```

An HTTP adapter can retain the intent-level methods while resolving effective
values from per-user preferences, workspace defaults and server-controlled
capabilities. Server Voxa must use encrypted credential storage or an external
secret manager; React must not learn its storage mechanics. Desktop-only
settings can be disabled through capabilities instead of being fabricated.

Final frontend ownership is:

- `MeetingClient`: meeting lifecycle and data.
- `RecordingClient`: capture lifecycle and devices.
- `TranscriptionClient`: ASR readiness and runtime execution.
- `MediaClient`: playback, import and media access.
- `SummaryClient`: summary execution and job lifecycle.
- `ConfigurationClient`: persisted defaults, preferences and credentials.
- desktop shell: updater, drag/drop, windows/tray, notifications and OS events.
- model management: installation, download, cancellation, deletion and folders.

`TranscriptionClient.getSelectedProvider()` still reads the native transcript
configuration inside its adapter because runtime readiness needs the effective
provider. This is intentional adapter-to-native overlap; React preference
screens use `ConfigurationClient` and no longer know that command name.

