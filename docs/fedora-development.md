# Fedora Workstation development

This documents the current CPAL/Pulse-compatible Linux backend. It does not
switch Voxa to native PipeWire capture.

## Host packages

On a current Fedora Workstation install:

```bash
sudo dnf group install "Development Tools"
sudo dnf install clang cmake curl dbus-devel file gcc-c++ git openssl-devel pkgconf-pkg-config \
  alsa-lib-devel libappindicator-gtk3-devel librsvg2-devel \
  webkit2gtk4.1-devel pipewire-utils pulseaudio-utils
```

Install a current Rust toolchain with rustup and Node.js 20+ with the preferred
Node version manager. Enable pnpm through Corepack:

```bash
rustup toolchain install stable
rustup default stable
corepack enable
corepack prepare pnpm@latest --activate
```

If AppImage packaging fails, also install `fuse-libs`. GPU builds need their
vendor SDK; validate CPU mode first.

## Install, run and build

From `frontend/`:

```bash
pnpm install --frozen-lockfile
pnpm tauri:dev:cpu
NO_STRIP=true pnpm tauri:build:cpu
```

The first Rust build may download/build Whisper, ONNX and FFmpeg-related
dependencies and can take substantially longer than later builds.

## Verify microphone capture

Confirm that PipeWire and its Pulse compatibility server are active and list
input nodes:

```bash
systemctl --user status pipewire pipewire-pulse wireplumber
wpctl status
pactl list short sources
```

Record five seconds from the default source outside Voxa:

```bash
timeout 5 pw-record /tmp/voxa-microphone.wav
pw-play /tmp/voxa-microphone.wav
```

Then start Voxa, select the same microphone, record while speaking, stop, and
play the saved meeting. Watch the terminal for device-open, stream and sample
format errors.

## Find and verify monitor sources

PipeWire-Pulse normally exposes an output monitor source. Find it with:

```bash
pactl list short sinks
pactl list short sources
wpctl status
```

Look for a source whose name ends in `.monitor`. While audio is playing, test it
independently (replace the example device name):

```bash
timeout 10 parec --device=alsa_output.pci-0000_00_1f.3.analog-stereo.monitor \
  --file-format=wav /tmp/voxa-system.wav
paplay /tmp/voxa-system.wav
```

In Voxa, choose the corresponding monitor/system device, play continuous audio,
record ten seconds, and confirm both playback and live transcription contain
that audio. Repeat once with mic plus system audio to exercise the current mix.

## Likely GNOME Wayland/PipeWire failures

- No `.monitor` source is exposed when `pipewire-pulse` is absent, stopped, or
  the active sink has not produced a monitor node.
- The current CPAL backend relies on ALSA/Pulse compatibility and device names;
  it does not negotiate capture through the Wayland ScreenCast portal.
- A default sink change, USB/Bluetooth disconnect, or Bluetooth profile switch
  can invalidate the selected monitor/device while recording.
- Monitor matching can fail when PipeWire/Pulse presents a description that
  differs from the device name expected by the application.
- GNOME microphone privacy settings or per-application PipeWire permissions can
  yield an enumerated device that produces silence or cannot be opened.
- Sample-rate/channel-layout differences between mic and monitor can expose
  resampling, startup-alignment, or underrun issues in the current mixer.
- Running a packaged build under an additional sandbox changes device and
  filesystem visibility; validate the unpackaged Tauri dev build first.

Useful live diagnostics are `wpctl status`, `pw-top`, `pactl subscribe`, and
`journalctl --user -f -u pipewire -u pipewire-pulse -u wireplumber`.

