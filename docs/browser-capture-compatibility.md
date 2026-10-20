# Browser capture compatibility

Status of Voxa Web meeting capture (microphone, meeting tab + microphone,
meeting tab only; see `docs/voxa-web-meeting-capture.md`) by platform,
browser and meeting app.

Status labels:

- **verified**: exercised end to end, with the evidence named here;
- **partially verified**: some parts exercised, the rest named as untested;
- **unsupported**: known not to work, with the reason;
- **untested**: not exercised. Nothing here is “expected to work”.

Automated and manual results are kept separate. Last updated 2026-10-20.

## 1. Automated results

Environment: Fedora Linux, Google Chrome 155.0.8059.79 headless
(`--headless=new`), Firefox 158.0 headless, Voxa server built from this
branch, ffmpeg, whisper.cpp (`ggml-tiny.en`). Meeting apps are stood in for
by a local page that plays audio; no real meeting service is contacted.

| Capability | Chromium (Fedora, headless) | Firefox 158 (Fedora, headless) | Evidence |
|---|---|---|---|
| Feature detection | verified: all three modes, `sharedAudio: likely` | verified: all three modes offered, `sharedAudio: unknown` (UI warns) | capture suite; Firefox probe |
| Microphone-only recording | verified | verified: production recorder, 7 × 500 ms chunks concatenate to a valid WebM/Opus file (48 kHz mono) that ffmpeg decodes | recording + capture suites; Firefox probe |
| Real tab capture (`getDisplayMedia`, tab picked by title) | verified: “Tab audio” track, `displaySurface: browser` | untested: Firefox rejects getDisplayMedia without a real user gesture (`InvalidStateError`, “requires transient activation”) and that gesture cannot be produced headless | capture suite; Firefox probe |
| Tab audio continues after the video track is stopped | verified | untested | probe + capture suite |
| Microphone + tab mix: both sources in one file | verified (presence per 0.5 s window) | untested | `microphone + meeting tab` test |
| Mix levels and clipping | verified with deterministic sources: 0.5 + 0.5 tones keep 0.49/0.48 each, peak ≈ 0.90, 0 clipped samples; 0.25/0.20 keep 0.250/0.200 | untested | `deterministic …` tests |
| Tab ends → continue microphone + warning | verified (real tab closed; synthetic end) | untested | capture suite |
| Microphone ends → continue tab + warning | verified (synthetic end in real Chrome) | untested | capture suite |
| Both end → stop | verified (unit) | n/a | `browser-capture.test.ts` |
| Tab-only, sharing ends → stop | verified | untested | capture suite |
| Network loss + reload recovery of a mixed recording (exact bytes) | verified | untested | capture suite |
| Full chain: mixed capture → offline chunks → network restored → reload → recovery → finalize → remux → transcript → summary | verified: tab speech transcribed (“…ask not what your country can do for you…”), summary generated from it, microphone tone present, provenance intact | untested | `mixed capture → offline → …` test |
| Capture provenance stored and returned after recovery | verified | n/a (server side) | capture suite, API and repository tests |
| Endurance (mixed, 15 s timeslice) | see §4 | untested | opt-in endurance test |

Not producible headless (covered by unit tests with fakes only): a
cancelled share picker, a shared source without audio, window or screen
sharing.

Measurement notes:

- Headless Chrome renders a background tab's audio unevenly (window levels
  0.005–0.4 for a constant tone, also in tab-only mode without Voxa's
  mixer), so real-tab tests check presence; levels are checked with
  deterministic sources through the production mixer.
- Chrome's microphone noise suppression removes steady tones (a constant
  440 Hz test tone faded to near zero within seconds), so the fake
  microphone is a pulsed tone. Speech was not affected in the full-chain
  test.

## 2. Manual results

**No manual validation has been performed yet.** Every combination below is
**untested**. Voxa cannot credibly claim support for any specific meeting
platform until the matrix in §5 is filled in.

| | Fedora / Chromium | Windows / Chrome | macOS / Chrome |
|---|---|---|---|
| Google Meet | untested | untested | untested |
| Microsoft Teams Web | untested | untested | untested |
| Zoom Web | untested | untested | untested |
| Cisco Webex Web | untested | untested | untested |
| WhatsApp Web | untested | untested | untested |

What the automated results do and do not imply: Chromium tab capture of a
page playing audio is verified on Fedora. Whether a given meeting app plays
its call audio inside the tab in a way that Chrome captures (for example
when the app routes audio through special output devices or a separate
window), and how it behaves on Windows and macOS, is untested.

## 3. Platform and browser status

| Platform / browser | Status | Notes |
|---|---|---|
| Fedora / Chrome (desktop) | partially verified | Automated headless only (§1). No headed run with real devices, speakers or headsets. |
| Fedora / Chromium (distribution package) | untested | Not installed here; Chrome results are expected to transfer but are not claimed. |
| Windows / Chrome | untested | Chrome documents screen (system) audio on Windows in addition to tab audio; Voxa would record it as `sharedSurface: monitor`. Not verified. |
| macOS / Chrome | untested | |
| Firefox (desktop) | partially verified | Microphone-only verified (Fedora, headless, production recorder: 7 chunks → valid WebM/Opus 48 kHz mono, decodes). Firefox enforces the user-gesture rule for getDisplayMedia (`InvalidStateError`, also with `privacy.userActivation.enabled=false`), so shared audio is untested; detection advertises no display-audio support and the UI warns. If a share returns no audio track, Voxa stops with the precise “no audio” message and microphone-only still works. |
| Safari (macOS/iOS) | untested | Not available in this environment. |
| Edge / Brave / Opera | untested | Chromium-based; not claimed. |
| Android Chrome | untested | No device available; see §6. |
| iOS Safari | untested | No device available. |

## 4. Endurance results

The opt-in test (`VOXA_E2E_ENDURANCE_MINUTES`, production 15 s timeslice,
real tab capture + fake microphone, chunk copies disabled) records, every
minute, JS heap, storage usage, pending (unuploaded) chunks and
AudioContext state, plus chunk cadence, renderer CPU (CDP `TaskDuration`)
and final integrity.

Run 1 (30 min, before the fix): **passed** all checks except storage,
which exposed a leak.

| Metric | Result |
|---|---|
| Chunks | 120 (production 15 s timeslice; gaps 10.5–15.1 s) |
| JS heap | 0.7–1.8 MB, sawtooth with GC, no upward trend |
| AudioContext | `running` throughout |
| Upload backlog | at most 1 pending chunk |
| Renderer CPU | 0.15% (CDP `TaskDuration` / wall time) |
| Final media | 1800.2 s decoded; microphone present in 1800/1800 s, tab audio in 3600/3600 half-second windows |
| Origin storage | **grew linearly to 21.4 MB (≈0.7 MB per recorded minute) and stayed after upload, with 0 chunks pending, also after reload** |

Cause (isolated with a probe): in Chromium, an IndexedDB value stored as a
`Blob` that has been read back keeps its backing file after the record is
deleted (40 × 180 KB blobs: read back → 6.96 MB retained; never read →
reclaimed; stored as `ArrayBuffer` and read back → reclaimed to 0.25 MB).
Voxa reads every chunk back to upload it. Fix: the pending store now saves
chunk bytes as an `ArrayBuffer` (with the MIME type) and returns an
in-memory Blob; rows written by older builds (Blob values) are still read
(regression test in the capture suite). The endurance test now asserts
that storage after a 10+ minute run stays below 6 MB.

Run 2 (with the fix): **stopped early on request after about 5 minutes**,
so it is not a completed 30-minute verification. Samples taken until then:

| Elapsed | Origin storage | JS heap | Pending chunks | AudioContext |
|---|---|---|---|---|
| 0 s | 0.00 MB | 1.3 MB | 0 | running |
| 60 s | 0.02 MB | 1.8 MB | 0 | running |
| 121 s | 0.04 MB | 3.9 MB | 0 | running |
| 181 s | 0.06 MB | 6.1 MB | 0 | running |
| 241 s | 0.08 MB | 2.5 MB | 0 | running |

Storage grew about 19 KB per minute instead of about 0.7 MB per minute
(0.08 MB at 4 minutes versus 2.99 MB in run 1). Heap ranged 2.6–6.4 MB in
these few samples (run 1: 0.7–1.8 MB); the trend over a full run was not
observed. Status: storage fix **partially verified**; a full 30-minute run
with the fix is still to do (`VOXA_E2E_ENDURANCE_MINUTES=30`).

## 5. Manual validation matrix (to be filled in)

Platforms: Fedora / Chromium, Windows / Chrome, macOS / Chrome.
Apps: Google Meet, Microsoft Teams Web, Zoom Web (browser client), Cisco
Webex Web, WhatsApp Web (only audio playing in the tab).
Audio routes: laptop speakers, wired headset, Bluetooth headset, USB headset
(if available).

### Setup for each run

1. Voxa Web in one tab, the meeting app in another tab of the same window;
   a second person (or a second device) joins as the remote participant.
2. Voxa: “Meeting tab + microphone” → “Choose tab and start recording” →
   pick the meeting tab → turn on “Share tab audio”.
3. After the run: play back, wait for transcript and summary, and note the
   source label on the recording.

### Scenarios

| # | Scenario | What to do |
|---|---|---|
| S1 | Local user speaks | Speak a known sentence. |
| S2 | Remote participant speaks | Remote reads a known sentence. |
| S3 | Both speak | Overlap for ~10 s. |
| S4 | Silence | 60 s with nobody speaking. |
| S5 | Music / shared media | Remote shares a video with sound, or plays music. |
| S6 | Mute / unmute microphone | Mute in the meeting app for 30 s, then unmute. |
| S7 | Change microphone before recording | Pick a non-default microphone in Voxa. |
| S8 | Remote loud / quiet | Remote moves close to / away from their mic. |
| S9 | Tab share stopped manually | Click the browser's “Stop sharing” mid-call. |
| S10 | Microphone disconnected | Unplug the USB/wired headset mid-call. |
| S11 | Network outage | Disable network for 60 s (note: this also drops the call). |
| S12 | Reload after outage | Reload the Voxa tab while offline, then go online. |
| S13 | Long recording | 30+ minutes. |

### Record for each combination

| Field | Values |
|---|---|
| Capture starts | yes / no (error text) |
| Microphone audible in playback | yes / no / degraded |
| Remote audio audible in playback | yes / no / degraded |
| Clipping | none / occasional / frequent |
| Echo (remote heard twice or delayed) | none / audible |
| Remote / local balance | balanced / remote louder / local louder |
| Noise-suppression artifacts | none / noticeable |
| Bluetooth degradation (headset profile) | n/a / none / noticeable |
| Transcript quality | both sides / local only / remote only / poor |
| Upload recovery | n/a / recovered / lost data |
| Playback | full length / gaps |
| Summary generated | yes / no |
| Source label | as expected / wrong |
| Diagnostics (`voxaCaptureDiagnostics()` in the console) | paste unusual events |
| Notable browser UX | free text (picker wording, sharing bar, prompts) |

Results table template (one row per platform × app × route × scenario):

| Platform | Browser version | App | Route | Scenario | Starts | Mic | Remote | Clip | Echo | Balance | NS | BT | Transcript | Recovery | Playback | Summary | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|

Rules: a combination becomes **verified** only when every field was
observed; leave the rest **untested**. Do not change limiter or gain
settings based on one setup; record the evidence and repeat on a second
setup first.

## 6. Mobile findings

No Android or iOS device was available, so nothing on mobile has been
observed. Untested items: Chrome Android microphone-only recording; whether
getDisplayMedia exists; whether shared-audio modes appear; behaviour when
the screen locks, when switching apps, and in background tabs. Voxa
relies only on feature detection: when getDisplayMedia or Web Audio mixing
is missing, the shared modes are not offered and microphone-only recording
remains. Voxa does not and cannot capture native WhatsApp, Teams or phone
call audio.

## 7. Known compatibility gaps

- No live meeting app has been validated (§2).
- Windows and macOS are untested; Windows screen (“system”) audio is
  undocumented in Voxa beyond being labelled `monitor`.
- Firefox and Safari shared audio are untested. Their feature detection
  does not advertise display-audio support, so the UI warns before
  sharing, and if a share returns no audio track Voxa stops with the
  precise “no audio” message (microphone-only remains available).
- Mobile is untested.
- The user must pick the tab and enable tab audio on every recording.
- Mixed recordings are one track; source attribution is not available.
- Capture provenance records how a recording **started**; later source
  loss appears only in local diagnostics and the on-screen warning.
