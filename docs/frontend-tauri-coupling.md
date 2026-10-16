# Remaining frontend Tauri coupling

After the meeting, recording, active transcription and media slices, 54 frontend
source files import a Tauri API directly. Six are intentional adapter-layer
files under `src/clients/tauri/` (including the shared subscription helper);
the other 48 are future migration inventory.

## Successfully migrated recording files

These files no longer import Tauri directly for recording behavior:

- `src/components/DeviceSelection.tsx`
- `src/components/RecordingControls.tsx`
- `src/components/TranscriptView.tsx` (speech activity only)
- `src/contexts/RecordingStateContext.tsx`
- `src/contexts/RecordingPostProcessingProvider.tsx`
- `src/contexts/TranscriptContext.tsx` (recording session lifecycle only)
- `src/hooks/usePermissionCheck.ts`
- `src/hooks/useRecordingStateSync.ts`

`src/hooks/useRecordingStart.ts` and `src/hooks/useRecordingStop.ts` use
`RecordingClient` for lifecycle operations. They still import Tauri for
separate concerns described below. The old Tauri-backed
`src/services/recordingService.ts` has been removed.

## Successfully migrated transcription files

These files now use `TranscriptionClient` for transcription-owned behavior:

- `src/hooks/useRecordingStart.ts` (selected-provider readiness/initialization)
- `src/hooks/useRecordingStop.ts` (queue status and completion)
- `src/contexts/TranscriptContext.tsx` (history and live transcript updates)
- `src/hooks/useModalState.ts` (errors and model completion)
- `src/hooks/useTranscriptionModels.ts` (available local models)
- `src/components/WhisperModelManager.tsx` and
  `src/components/ParakeetModelManager.tsx` (download events)
- `src/components/MeetingDetails/RetranscribeDialog.tsx`
- `src/hooks/useImportAudio.ts` (transcription job only)
- `src/components/shared/DownloadProgressToast.tsx`
- `src/components/onboarding/steps/DownloadProgressStep.tsx`
- `src/contexts/OnboardingContext.tsx`

The obsolete `src/services/transcriptService.ts` was removed. Native
transcription command names, event names and payload casing now live in
`TauriTranscriptionClient`.

## Successfully migrated media files

These files now use `MediaClient` for media/file behavior:

- `src/hooks/useAudioPlayer.ts` requests a transport-neutral playback source.
- `src/hooks/useImportAudio.ts` delegates native selection and validation and
  retains only an opaque `MediaSource` for the transcription job.
- `src/components/ImportAudio/ImportAudioDialog.tsx` renders normalized media
  metadata and no longer reads or forwards a file path.
- `src/components/MeetingDetails/TranscriptButtonGroup.tsx` resolves meeting
  media through the client before offering retranscription.
- `src/components/MeetingDetails/RetranscribeDialog.tsx` forwards an opaque
  source rather than a meeting folder path.
- `src/hooks/meeting-details/useMeetingOperations.ts` opens a meeting folder
  through `MediaClient`.
- `src/components/RecordingSettings.tsx` and
  `src/components/PreferenceSettings.tsx` open the recordings folder through
  `MediaClient`; their remaining Tauri imports belong to configuration.

Native local reads, file-picker/validation, media metadata lookup and folder
command names now live in `TauriMediaClient`.

## Intentional adapter imports (6)

- `src/clients/tauri/meeting-client.ts`
- `src/clients/tauri/recording-client.ts`
- `src/clients/tauri/media-client.ts`
- `src/clients/tauri/summary-client.ts`
- `src/clients/tauri/transcription-client.ts`
- `src/clients/tauri/subscribe.ts`

`TauriRecordingClient` is now the only module that knows recording command
names, recording event names, native payload casing, or how to construct the
desktop recording save path.

## Remaining recording-adjacent coupling

- `src/app/layout.tsx`: `request-recording-toggle` is a desktop tray/shell
  event and intentionally remains at the application-shell boundary.
- `src/hooks/useRecordingStop.ts`: uses the Tauri store plugin for an analytics
  preference; stop and transcription-complete events are client-backed.
- `src/components/RecordingSettings.tsx` and `src/contexts/ConfigContext.tsx`:
  recording preferences, default-folder lookup and configuration persistence were
  deliberately excluded from the active recording transport slice.
- `src/components/AudioBackendSelector.tsx`, `BluetoothPlaybackWarning.tsx`,
  `PermissionWarning.tsx`, and onboarding `PermissionsStep.tsx`: native backend,
  playback-device and OS permission UI remain platform configuration concerns.
- `src/components/PreferenceSettings.tsx`: retains database/models folder
  actions and notification preferences; recordings-folder opening is client-backed.
- `src/lib/recordingNotification.tsx`: desktop notification integration.
- `src/lib/analytics.ts`: native analytics command forwarding.

None of the active start/stop/pause/resume, device enumeration, audio-level,
recording-state, duration, session-folder, recording-stop, speech activity, or
microphone hot-swap UI paths invoke or listen to Tauri directly.

## Remaining transcription-specific coupling

- `src/lib/whisper.ts` and `src/lib/parakeet.ts` remain provider-specific model
  management adapters used by the two native model-manager components. Their
  model load/download/cancel/delete and models-folder operations are not yet
  generalized because their UI metadata and cancellation behavior differ.
- `WhisperModelManager.tsx` and `ParakeetModelManager.tsx` still invoke
  `api_save_transcript_config`; this is persistent preference ownership, not
  runtime transcription. Their model progress events are client-backed.
- `OnboardingContext.tsx` and `DownloadProgressStep.tsx` retain Parakeet
  download/retry commands and unrelated database/summary-model commands.
  Provider initialization, readiness, and model events are client-backed.
- `ConfigContext.tsx`, settings and sidebar retain transcript configuration,
  language preference and native models-folder persistence/UI operations.
- `useImportAudio.ts` uses `MediaClient` for file selection/validation and
  `TranscriptionClient` for starting, cancelling and observing the ASR job.
- `useTranscriptRecovery.ts` retains checkpoint inspection/recovery commands,
  which straddle persistence and recovery orchestration rather than live ASR.

No React consumer knows the native names for live transcript, transcription
completion/error, model download, retranscription, or import-job events.

## Remaining media-specific coupling

- `src/app/layout.tsx` receives native desktop drag/drop events and paths. This
  intentionally remains an application-shell boundary; it asks `MediaClient`
  to validate the path before the import dialog receives an opaque selection.
- `src/contexts/ConfigContext.tsx` and `src/components/RecordingSettings.tsx`
  read the default recordings location as a persistent desktop preference.
- `src/hooks/useTranscriptRecovery.ts` reads the active recording session
  folder while orchestrating checkpoint recovery. That belongs to the future
  persistence/recovery boundary rather than playback/import media.
- Database and models-folder actions remain configuration/model-manager or
  desktop-shell concerns, not `MediaClient` responsibilities.

No React playback/import/retranscription component invokes media commands,
constructs meeting paths, or receives a selected native file path. Only the
Tauri adapter can unwrap a `MediaSource` to its native reference.

## Remaining files by area

The 54 direct-import files comprise:

- 6 transport-adapter files
- 4 app/page files
- 25 components
- 3 contexts
- 8 hooks
- 6 libraries
- 2 services

The largest remaining areas are model management/transcription, summary
generation, onboarding/permissions, configuration, analytics, updates and
desktop-shell integration. Future work should migrate one
capability slice at a time rather than mechanically remove imports.

