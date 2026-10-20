# Voxa Web recording and media

The first end-to-end media slice:

```text
browser microphone → MediaRecorder chunks → IndexedDB → chunked, resumable upload
  → server staging → assembled object + MediaAsset (PostgreSQL) → signed range playback in <audio>
```

Finalized recordings are then remuxed and transcribed on the server (see
`docs/server-processing.md`). There is no summarization yet. The desktop
(Tauri) recording pipeline is untouched.

## 1. Browser recording architecture

| Piece | Responsibility | File |
| --- | --- | --- |
| `BrowserRecordingClient` | Microphone and shared-tab capture (modes, mixing; see `docs/voxa-web-meeting-capture.md`), MediaRecorder lifecycle, chunk production, pause/resume, honest capabilities. Knows nothing about uploads. | `frontend/src/clients/browser/recording-client.ts`, `capture.ts` |
| `RecordingUploadCoordinator` | Local persistence first, ordered single-flight uploads, retry/backoff, `online` resume, finalize, recovery after reload. Framework-free. | `frontend/src/web/recording/upload-coordinator.ts` |
| `PendingChunkStore` | IndexedDB (`voxa-recordings`) storage for chunks not yet acknowledged by the server. | `frontend/src/web/recording/pending-store.ts` |
| `RecordingSessionController` | Joins one recording to one upload; also completes uploads when the recorder stops on its own (lost microphone, recorder error). | `frontend/src/web/recording/session.ts` |
| `HttpMediaClient` | Media protocol: create recording, chunks, status, finalize, list, playback URL, delete. Implements `MediaClient` and `ServerMediaClient`. | `frontend/src/clients/http/media-client.ts` |
| UI | `RecordingPanel` (controls, timer, consent notice, upload state, retry/discard) and `MeetingMedia` (list and player) on the meeting page. | `frontend/src/web/recording/*.tsx` |

A recording always starts from an existing meeting:
Meeting → RecordingSession → MediaAsset.

## 2. Recording and upload states

Recorder (session): `idle → starting → recording ⇄ paused → stopping → idle`.

Upload job, as shown in the UI:

| Phase | Meaning |
| --- | --- |
| `recording` | Recorder running; chunks upload as they arrive. With `waitingForNetwork`, the UI says *"Offline: recording continues, N parts saved on this device"*. |
| `waiting` | Recording stopped and parts are still on this device, waiting for the network or server. Keep the tab open. |
| `uploading` | Sending saved parts. |
| `finalizing` | All parts acknowledged; assembling on the server. |
| `ready` | Stored and playable. |
| `error` | Needs action: **Retry** or **Discard** (e.g. server-side conflict, upload expired). |

A `beforeunload` warning is shown while recording, or while any part exists
only on this device.

## 3. Upload protocol (`/api/v1`, bearer-authenticated, workspace-scoped)

| Step | Request | Notes |
| --- | --- | --- |
| Create | `POST /meetings/{meetingId}/recordings` `{ "mimeType": "audio/webm;codecs=opus" }` → `201 { recordingSession, mediaAsset, upload }` | Meeting must exist in the caller's workspace. The MIME type must be on the allowlist. |
| Chunk | `PUT /media/{id}/chunks/{index}`, body = raw bytes, `Content-Type: application/octet-stream` → `{ upload }` | `index` is a canonical integer 0–99999. Identical retry: `200`. Different bytes for a stored index: `409 chunk_mismatch`. Chunk 0 must start with the container's magic bytes. |
| Status | `GET /media/{id}/upload` → `{ upload: { status, receivedChunks, receivedBytes, maxChunkBytes, maxTotalBytes } }` | Used to resume. |
| Finalize | `POST /media/{id}/finalize` `{ "chunkCount": n, "durationSeconds": 123.4 }` → `{ mediaAsset }` (`ready`) | Requires exactly chunks `0..n-1` (`409 missing_chunks` with the missing indices, or `unexpected_chunks`). Idempotent for the same `n`. |
| List / get / delete | `GET /meetings/{id}/media`, `GET /media/{id}`, `DELETE /media/{id}` | |
| Playback | `GET /media/{id}/playback` → `{ url, expiresAt }` | Short-lived signed URL (see §8). |
| Content | `GET /media/{id}/content` (bearer) | Range-capable download for API clients. |

There are no client-supplied storage keys or filenames, and no server paths
appear in any response. `MediaAsset.storageKey` is internal only.

## 4. Local recovery (IndexedDB)

- **Durability:** every chunk is committed to IndexedDB *before* upload.
  `putChunk` resolves only after the transaction completes.
- **Ordering:** uploads go in index order, one at a time per recording. A
  chunk is deleted locally only after the server acknowledges it.
- **Retries:** failures are retried with backoff (1, 2, 5, 10, 30 s, then
  every 30 s) and immediately when the browser fires `online`. Retries run
  in the foreground: there is no reliance on Background Sync.
- **Reload or crash:** on the next sign-in, `recoverPending()` resumes
  leftover recordings:
  - it asks the server which chunks it already holds and drops those local
    copies;
  - it uploads the rest;
  - a recording interrupted mid-capture is finalized as the contiguous prefix
    the server holds. A truncated WebM prefix is playable (verified with
    ffmpeg). Its duration is unknown (`null`).
- **Multiple tabs:** a live recording holds a Web Lock
  (`voxa-recording-<id>`), so another tab never finalizes it. Without Web
  Locks, a recording counts as abandoned after 90 s with no heartbeat (one
  every 20 s while recording).
- **Background upload:** uploads continue only while a Voxa tab is open.
  Nothing uploads after the browser is closed. The next visit resumes.

## 5–6. Server media storage and filesystem layout

`mediastore.Store` (Go) is the storage seam. It supports staging chunks,
listing them, ordered atomic assembly, opening (seekable), deleting, and
listing uploads and objects. Keys are opaque, server-generated
(`obj_<128-bit hex>`, validated against `^[a-z]{2,8}_[0-9a-f]{32}$`) and
are never paths.

Filesystem implementation (`VOXA_MEDIA_DIR`):

```text
$VOXA_MEDIA_DIR/
  staging/obj_<hex>/000000.part   chunks of an upload in progress
  objects/obj_<hex>               assembled recording
  tmp/                            write-then-rename scratch space
```

- **Crash safety:** every chunk and object is written to `tmp/`, fsynced,
  then renamed into place, with a directory fsync. A crash never leaves a
  partial file under a final name.
- **Ordering:** finalize assembles first and then marks the asset `ready`
  in the database. If the server crashes in between, the finalize retry
  simply reassembles.
- **One server per directory:** locking is per upload within one server
  process.
- **Location:** keep the directory outside the source tree, e.g.
  `/var/lib/voxa/media`.

**Future S3/MinIO storage:** implement the same interface:
- chunks become staged objects (`staging/<key>/<index>`);
- assembly becomes a multipart upload that composes the parts server-side;
- `Open` becomes a ranged-GET reader.

Playback could then also use presigned GET URLs instead of the
server-signed stream route.

## 7. Authentication and workspace enforcement

- **Every route except the signed stream requires a bearer token.** This
  includes create, chunk, status, finalize, list, get, delete, playback and
  content.
- **Workspace scoping:** the service checks that the meeting exists in the
  caller's workspace. Every repository query filters by workspace.
- **Schema backstop:** `media_assets` and `recording_sessions` reference
  `meetings(workspace_id, id)`, so the database rejects an asset attached
  to another workspace's meeting.
- **Cross-workspace access** gets `404`.

## 8. Range playback

`<audio>` cannot send `Authorization` headers, so `GET /media/{id}/playback`
returns a URL of the form
`/api/v1/media/{id}/stream?workspace=…&expires=…&signature=…`:

- **Signature:** an HMAC-SHA256, keyed per server process, over
  `voxa.media.playback.v1 / workspace / asset / expiry`. It is valid for 2
  hours. A tampered, expired or relocated URL gets `403`. The query string is
  never logged (access logs record the path only).
- **Ranges:** `/stream` and `/content` use Go's `http.ServeContent`, which
  provides `Accept-Ranges`, `206 Partial Content`, `Content-Range`,
  `If-Range` and `416` for unsatisfiable ranges.
- **Response headers:** `Content-Type` is the stored, allowlisted MIME type.
  `nosniff`, `Cache-Control: private, no-store` and
  `Content-Disposition: inline` are also set.
- **Expiry:** the player fetches a fresh URL on a media error and resumes at
  the same position. Server restarts also invalidate URLs.
- **Not yet:** there is no permanent public link. Multi-replica deployments
  would need a shared signing key, which is not implemented.

## 9. MIME and format decision

Measured in headless Chrome, not assumed:

- **Supported format:** Chrome's `MediaRecorder` supports
  `audio/webm;codecs=opus` (not `audio/ogg` or `audio/mp4`).
- **Concatenation works:** a 400–1000 ms timeslice produced N chunks.
  Concatenating all of them in order gave a WebM file that decodes with zero
  errors (`ffmpeg -f null`). A *prefix* (a recording cut short) decodes all
  of its audio, ending with "File ended prematurely".
- **Chunks are not standalone:** a single non-first chunk is **not** a valid
  file (no EBML header), so chunks are only meaningful assembled from index 0.
- **No duration or seek index:** MediaRecorder WebM has neither. Chrome
  reports `duration = Infinity` until it has read the stream, yet seeking
  over range requests still works. An ffmpeg remux (`-c copy`) adds both;
  this is deferred to future server-side post-processing.

Choice: the client picks the first of `audio/webm;codecs=opus`,
`audio/ogg;codecs=opus`, `audio/webm` or `audio/mp4` that
`MediaRecorder.isTypeSupported()` accepts, at 64 kbps. The actual
`recorder.mimeType` is stored with the asset.

The server accepts only `audio/webm`, `audio/ogg` and `audio/mp4` (with the
codec parameters `opus` or `mp4a.40.2`). It checks chunk 0's magic bytes
(EBML, `OggS`, `ftyp`) and stores and serves everything as opaque bytes.
Only WebM/Opus in Chrome has been verified end-to-end. Firefox (Ogg/Opus)
and Safari (MP4) are expected to work but are untested.

Every recording is one encoder stream, and therefore one asset. Pausing
keeps the same stream. A reload ends the stream, and a new recording
becomes a new asset.

**Chunk cadence: 15 s.** At most about 15 s of audio is at risk in a crash.
Opus speech gives roughly 60–240 KB per chunk, and request overhead is
negligible. The whole meeting is never held in memory.

## 10. Limits

| Limit | Default | Configure |
| --- | --- | --- |
| Max chunk size | 8 MiB | fixed |
| Max recording size | 1 GiB (≈ 37 h of 64 kbps Opus) | `VOXA_MEDIA_MAX_RECORDING_MB` |
| Chunks per recording | 100 000 | fixed |
| Stale upload lifetime | 48 h without activity | fixed |
| Client-reported duration | 0 – 7 days | fixed |
| Chunk upload read deadline | 2 min per request | fixed |

Oversized chunks or recordings get `400 validation` (`field: chunk` or
`field: recording`).

## 11. Cleanup

The server runs cleanup at startup and then hourly. No scheduler framework is
involved.

1. **Stale uploads:** uploads idle longer than the lifetime become `failed`,
   their session becomes `failed`, and their staged chunks are deleted.
2. **Leftover staging:** staged chunks of assets that are no longer
   uploading, or that no longer exist, are deleted.
3. **Orphaned objects:** objects whose asset is gone (meeting deleted,
   delete interrupted) or `failed` are deleted. There is a 10-minute grace
   period for files written moments ago.

**Ready assets are never removed by cleanup.** Deleting a meeting cascades to
its media rows. The bytes are reclaimed by the next cleanup pass.

## 12. Known browser and mobile limitations

- **Tested so far:** Chrome desktop (headless, fake microphone) only.
  Firefox, Safari and every mobile browser are **untested**; do not claim
  mobile support until the checklist below has passed on them.
- **Mobile browsers** may suspend background tabs, stopping recording or
  uploads. iOS Safari's MediaRecorder support and formats differ.
- **Connectivity at start:** starting a recording needs the server, to create
  the session. After that, recording continues offline.
- **Expected duration display:** seeking and duration display in raw
  MediaRecorder WebM can be slow for long recordings until a server remux
  exists.
- **Recovery is per device:** only the device that recorded can resume an
  upload. Other devices see "Incomplete upload" until cleanup marks it
  failed.
- **Storage quota:** IndexedDB quota and eviction policies vary by browser.
  Long offline periods could hit the quota (a chunk save failure is shown as
  an error).

## 13. Tab and shared audio capture

Implemented: microphone + shared tab audio (Web Audio mix) and tab-only
modes, feature-detected and exposed as `browserCapture`, `tabCapture` and
`systemAudioCapture: 'display-media'`. Recordings still produce one
MediaRecorder stream, so everything in this document applies unchanged.
See `docs/voxa-web-meeting-capture.md`.

## 14. Future S3/MinIO storage

See §5–6: a second `mediastore.Store` implementation, selected by
configuration, with no protocol changes.

## 15. Transcription integration

Implemented: finalizing a recording enqueues a remux job (stream copy,
fixing WebM duration and cues, keeping the original upload) followed by a
whisper.cpp transcription job. The server advertises
`transcription.available` only when the engine is usable, and Voxa Web uses
`HttpTranscriptionClient`. See `docs/server-processing.md`.

## Manual validation checklist

Not yet performed on physical devices; the automated headless-Chrome test
covers the flow with a fake microphone.

- [ ] Chrome desktop: grant microphone permission; deny it once and check
      the error message.
- [ ] Record 2–5 minutes; pause and resume once.
- [ ] Mid-recording, disable the network (DevTools → Offline, or turn off
      Wi-Fi). Recording continues, and the status shows parts saved on this
      device.
- [ ] Restore the network: uploads resume without user action.
- [ ] Stop: the status goes through finalizing and reaches "Recording
      saved".
- [ ] Reload the page: the recording is listed and plays.
- [ ] Seek within the recording, including near the end.
- [ ] Reload during a recording (with the network off): after reloading,
      "Resuming upload…" appears and the recording becomes playable.
- [ ] Chrome on Android (if available): permission, a 2-minute recording,
      the app backgrounded briefly, stop, playback.
- [ ] Firefox and Safari desktop: format negotiation (Ogg/MP4), record,
      playback.
