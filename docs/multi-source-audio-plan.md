# Multi-source audio preservation plan

## Current mix point

Microphone and system chunks enter `AudioPipeline::run` in
`frontend/src-tauri/src/audio/pipeline.rs`. The pipeline adds each chunk to
`AudioMixerRingBuffer`, extracts aligned `mic_window` and `sys_window` buffers,
and calls `ProfessionalAudioMixer::mix_window`. Only that mixed buffer is then:

1. passed through VAD and sent to the transcription engine; and
2. sent through `recording_sender_for_mixed` to `RecordingSaver`.

The source distinction is therefore lost immediately after `mix_window`.
Current transcription and saved playback both intentionally consume the mixed
signal.

## Smallest future change

Keep the existing mixer, VAD, transcription sender and mixed recording sender
unchanged. Add two optional track sinks at the aligned-window boundary, just
after `extract_window` and before `mix_window`. Send the exact aligned mic and
system windows to independent savers, while continuing to send the mix through
the current path. Make separate-track persistence opt-in so a partial track
write cannot stop the current recording.

Conceptual types:

```rust
enum AudioTrackKind { Microphone, System, Mixed }

struct TrackAudioChunk {
    track: AudioTrackKind,
    samples: Vec<f32>,
    sample_rate: u32,
    timestamp_seconds: f64,
    sequence: u64,
}

struct RecordingTrackSinks {
    microphone: Option<TrackSink>,
    system: Option<TrackSink>,
    mixed: TrackSink,
}
```

Using aligned windows preserves a common clock and makes later re-mixing
deterministic. Track metadata should record codec/container, sample rate,
channels, start offset, duration, checksum and whether the track completed.

Proposed layout (under the existing recording folder):

```text
meeting-id/
  audio/
    microphone.wav
    system.wav
    mixed.wav
  metadata.json
```

The existing mixed filename should remain readable during migration (or be
represented as an alias in metadata), and old meetings without a track manifest
must continue to resolve as mixed-only. Do not put the raw audio blobs in
SQLite; store relative media references and track metadata there when the
schema is extended.

