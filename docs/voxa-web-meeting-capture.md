# Voxa Web meeting capture (microphone + shared tab audio)

Voxa Web can record a browser meeting (Google Meet, Microsoft Teams web,
Zoom web client, Cisco Webex web, WhatsApp Web, or any other site) by
combining the user's microphone with the audio of a tab the user shares
through the browser's own share dialog. Nothing about a meeting provider is
special-cased: support follows from what the browser can capture. The
upload, recovery, storage, remux, transcription and summary pipeline is
unchanged, because the result is still one MediaRecorder stream.

Not implemented: browser extension, native Android capture, desktop
Zoom/Teams/Webex app capture, meeting bots or provider APIs. The desktop
(Tauri) recorder is unchanged.

## 1. Capture modes

| Mode | Records | How |
|---|---|---|
| Microphone only | the microphone | as before: getUserMedia → MediaRecorder, no Web Audio |
| Meeting tab + microphone | microphone + shared tab audio | getDisplayMedia + getUserMedia → Web Audio mix → MediaRecorder |
| Meeting tab only | shared tab audio | getDisplayMedia audio track → MediaRecorder (no Web Audio) |

Modes are offered only when feature detection says the browser can attempt
them (`BrowserRecordingClient.browserCapture`):

- `microphone`: getUserMedia + MediaRecorder with a supported format;
- `displayCapture`: `navigator.mediaDevices.getDisplayMedia` exists;
- `mixing`: `AudioContext` with `createMediaStreamDestination`;
- `sharedAudio`: `likely` when the browser advertises display-audio
  constraints (`suppressLocalAudioPlayback`/`restrictOwnAudio`, Chromium),
  `unknown` otherwise (the UI says “Your browser may not provide shared tab
  audio. Voxa will verify after you choose a source.”), `unavailable`
  without getDisplayMedia.

When the shared source has no audio, the message names what was shared (a
tab without “Share tab audio”, or a window/screen whose audio the browser
did not include).

## Capture provenance and diagnostics

Each recording session stores how it was captured when it started
(migration `0007_capture_provenance.sql`): `mode` (`microphone`,
`shared_audio`, `microphone_and_shared_audio`, `unknown`), `client` (`web`,
`desktop`, `extension`, `android`, `unknown`) and, for shared audio, the
`sharedSurface` kind (`browser`, `window`, `monitor`). No user agent, URL,
tab title or provider data is stored. Recordings made before this, or by
clients that do not report it, read as `unknown` and show no label. The
meeting page shows a subtle label per recording (“Microphone”, “Meeting
tab + microphone”, “Meeting tab”, or “Shared screen/window …”).

The browser keeps up to 200 local capture diagnostics (track started/ended,
AudioContext state, recorder started/stopped, fallback to the remaining
source, capture failures with a reason code). They contain no media,
transcript text, URLs or titles and are not sent anywhere; run
`voxaCaptureDiagnostics()` in the browser console to read them.

Compatibility status and the manual validation matrix:
`docs/browser-capture-compatibility.md`.

Whether a shared source really has audio is only known after the user
picks it; Voxa inspects the returned tracks and refuses to start with a
clear message if there is no audio track. `RecordingCapabilities` gained
`browserCapture`; `tabCapture` and `systemAudioCapture: 'display-media'`
are set only when a shared mode is available.

## 2. Microphone + shared audio mixing

```
microphone MediaStream ─ MediaStreamAudioSourceNode ─ Gain(1.0) ┐
                                                                ├─ WaveShaper soft limiter ─ MediaStreamDestination → MediaRecorder
shared tab audio ─────── MediaStreamAudioSourceNode ─ Gain(1.0) ┘
```

- Unity gains preserve each source's level; the soft limiter is linear
  below 0.65 and approaches 0.95, so normal speech is untouched and summed
  peaks do not clip (headroom also absorbs ~2% Opus overshoot). There is
  no compressor (Web Audio's compressor adds make-up gain).
- The graph is never connected to the speakers: no extra local playback,
  no feedback. Shared audio keeps playing normally in the meeting tab
  (`suppressLocalAudioPlayback: false`), so the user still hears the call.
- Measured in Chromium (synthetic sources through the production mixer,
  MediaRecorder, upload and decode): two 0.5-amplitude tones mixed keep
  0.49/0.48 each with a 0.90 peak and no clipped samples; 0.25/0.20 tones
  come out at 0.250/0.200.
- Output uses the existing MIME negotiation (WebM/Opus in Chromium) at
  96 kbps for mixed/shared (64 kbps microphone only), the same 15 s
  timeslice and the same chunk semantics.

**Separate sources (Task F).** Only the mix is stored. Recording three
streams would triple uploads and storage and needs server changes; the
mix is what transcription and summaries consume today. Source attribution
stays possible later without a rewrite: each source has its own node in
`MixGraph`, so a future per-source MediaRecorder (or per-source level
metadata) can tap the microphone and shared nodes separately. The capture
mode and shared surface are known to the client (`CaptureDescription`)
and could be sent as recording metadata when the server grows a field for
it.

## 3. Permission model

- Everything starts from the user's click on “Choose tab and start
  recording”. The share dialog is requested **first**, before anything
  else is awaited, because browsers allow getDisplayMedia (and starting
  audio) only during a user gesture (Firefox reports “requires transient
  activation” otherwise). Then the microphone permission, then the mix,
  then the server recording is created. If a step fails, everything
  acquired so far is released and no server recording exists.
- Voxa never selects a tab. The user chooses it in the browser's dialog
  and must enable “Share tab audio”. Voxa asks the browser to prefer tabs
  and to hide Voxa's own tab (`displaySurface: 'browser'`,
  `selfBrowserSurface: 'exclude'`); the browser may ignore these hints.
- A normal web page cannot capture another tab without that choice, and
  Voxa does not try to work around it. If Voxa and the meeting are in
  different tabs (the usual case), the user picks the meeting tab in the
  dialog.
- The browser shows its own “sharing” indicator and stop button for the
  whole capture.

## 4. Provider-specific user instructions

The recorder shows one tip per app (static guidance; Voxa cannot see which
site the shared tab shows, and nothing is sent to or read from the
provider). Tips describe only the verified mechanism: “Join the call in a
browser tab, then choose the <app> tab in the share dialog and turn on
‘Share tab audio’”, for Google Meet (meet.google.com), Teams
(teams.microsoft.com, teams.live.com), Zoom (app.zoom.us), Webex
(web.webex.com) and WhatsApp Web (web.whatsapp.com). A note says that only
audio playing inside the shared tab is recorded, that desktop or phone
apps cannot be captured here, and that live calls in each app have not
been validated yet.

## 5. Browser limitations (tested behaviour)

| Browser | Result |
|---|---|
| Chromium desktop (Chrome 155, Linux, headless) | **Tested.** Real tab capture of a second tab returns a “Tab audio” track (`displaySurface: browser`); the audio continues after Voxa stops the video track; all three modes record, upload, recover after reload, remux and transcribe (speech played in the meeting tab appears in the Whisper transcript). |
| Chromium desktop, window/screen sharing | Not tested here. Chrome documents audio only for tabs on Linux/macOS, and for the whole screen on Windows/ChromeOS (“system audio”); a window share has no audio. Voxa reports “no audio” when that happens. |
| Firefox desktop (158, Linux, headless) | Detection tested: getDisplayMedia, mixing and WebM/Opus are available; no display-audio constraints, so the UI says shared audio is uncertain. Firefox requires the user gesture. Actual shared audio not verified (Mozilla does not document audio for getDisplayMedia); if the share has no audio track, Voxa stops with the “no audio” message and microphone-only works. |
| Safari (macOS) | Not tested (not available here). Safari supports getDisplayMedia for video; audio capture is not documented. Detection reports `unknown`; Voxa refuses sources without audio. |
| Edge/Opera/Brave | Chromium-based; expected to match Chromium but not tested. |

Other notes:
- Echo: the microphone uses `echoCancellation`, `noiseSuppression` and
  `autoGainControl`. Browsers cancel their own playback (the meeting
  tab) from the microphone, which avoids recording the remote side twice
  when using speakers. Headphones still give the cleanest result.
- Shared audio is captured with all processing off; microphone-only
  processing is never applied to it.
- Noise suppression treats steady tones as noise (seen in tests); speech
  is unaffected.
- Headless Chrome renders a background tab's audio unevenly; automated
  level checks therefore use deterministic synthetic sources, and real
  tab captures are checked for presence.

## 6. Mobile limitations

Not tested on physical devices. Android Chrome and iOS Safari do not
document getDisplayMedia on phones; when it is missing (or mixing is), the
modes disappear and microphone-only recording remains. Voxa does not
capture the audio of native call apps (WhatsApp, Phone, Teams mobile) and
does not pretend to. A native Android companion is future work.

## 7. Audio quality considerations

- Levels are preserved (unity gain) and summed peaks are softly limited.
- Remote participants are recorded digitally from the tab, independent of
  speaker volume; the local speaker is recorded through the processed
  microphone.
- Bluetooth headsets may switch to a low-quality headset profile while the
  microphone is open; wired headsets avoid this.
- Mixed recordings are stored as one track, so speaker separation relies
  on future diarization.

## 8. Recovery and source loss

- Chunks, IndexedDB persistence, offline buffering, reload recovery,
  finalization, remux, transcription and summaries are unchanged
  (verified for mixed recordings: network loss + reload recovery
  reproduces the exact bytes).
- Source loss policy:
  - Microphone only: a lost microphone stops the recording (chunks kept).
  - Meeting tab + microphone: if sharing stops (browser “Stop sharing”,
    tab closed, meeting left) the shared source is removed from the mix and
    recording continues with the microphone, with a visible warning; if the
    microphone is lost, recording continues with the shared audio; when
    both are gone it stops. The MediaRecorder is never restarted, so the
    file stays one continuous, decodable stream.
  - Meeting tab only: when sharing stops, the recording stops.
- Stop releases everything: MediaRecorder, microphone and shared tracks,
  the destination track, and the AudioContext (closed).
- Performance: microphone-only uses no Web Audio. Mixed mode runs one
  AudioContext with five nodes. A 60 s mixed session at a 500 ms
  timeslice produced steady chunks with the page heap staying within a
  few MB (0.9 → 2.9 MB).

## 9. Privacy

- Only the tab (or window/screen) the user selects is captured, only
  after the browser's permission dialog, and only while the browser's
  sharing indicator is shown.
- Display video is stopped immediately after the audio track is
  confirmed; no video is recorded or uploaded. (In Chromium the tab audio
  track keeps running after the video track stops; no browser quirk
  required keeping video alive.)
- The recording indicator says exactly what is recorded (“Recording your
  microphone + meeting tab audio”) and that other participants are
  included. The existing recording notice stays; the user remains
  responsible for consent. Voxa sends no notifications to meeting
  providers.

## 10. Future: extension or native companion

- A browser extension (`chrome.tabCapture`) could capture the meeting tab
  without the picker on every start and keep per-source tracks.
- A native companion (desktop app or Android) could capture system or call
  audio that browsers cannot expose.
- Server-side, a recording-metadata field could carry the capture mode and
  per-source tracks for speaker attribution.

## Automated tests

- `tests/recording/browser-capture.test.ts`: detection, request order,
  constraints, mixing graph, source loss, cleanup, every failure path
  (cancelled picker, no audio, denied microphone, no getDisplayMedia,
  AudioContext that cannot start, encoder failure), limiter curve.
- `tests/web/recording-panel.test.tsx`: mode selector, instructions,
  provider tips, indicator text.
- `tests/browser/capture.browser.test.ts` (opt-in, real Chrome + real
  server): deterministic levels and clipping, real tab capture
  mixed/tab-only with tab closed mid-recording, network loss + reload
  recovery, extended session, and (with Whisper) speech from the meeting
  tab in the transcript.

## Manual validation checklist

Not yet performed with real meetings. For each of Google Meet, Teams web,
Zoom web client, Webex web and WhatsApp Web:

1. Voxa in one tab, the meeting in another; join the meeting.
2. In Voxa choose “Meeting tab + microphone”, click “Choose tab and start
   recording”, pick the meeting tab, enable “Share tab audio”.
3. Indicator shows “Recording your microphone + meeting tab audio”.
4. Speak locally; have a remote participant speak.
5. Stop; play back: both sides audible, no echo/doubling; transcript and
   summary include both sides.
6. Repeat with: participant silence and music; Bluetooth headset; wired
   headset; laptop speakers (check echo); network loss for 30 s during the
   call (recording continues, uploads catch up); stop tab sharing from the
   browser bar mid-meeting (warning, microphone continues, file plays to
   the end).
7. Try “Meeting tab only” while muted.
8. Try sharing a window instead of a tab: Voxa explains there is no audio.
9. In Firefox and Safari: confirm what happens (expected: no shared audio
   message; microphone only works).
10. On Android Chrome / iOS Safari: only microphone recording is offered.
