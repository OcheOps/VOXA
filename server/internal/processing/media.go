package processing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Tools locates the external media programs. Binaries are resolved once at
// startup; only server-generated paths are ever passed to them.
type Tools struct {
	FFmpeg  string
	FFprobe string
}

// ProbeResult is what ffprobe reports about an audio file.
type ProbeResult struct {
	FormatName      string
	DurationSeconds *float64
	AudioCodec      string
	AudioStreams    int
}

// ErrInvalidMedia marks input the tools cannot read as audio: permanent.
var ErrInvalidMedia = errors.New("media is not readable audio")

// Probe inspects a file. Unreadable files and files without an audio stream
// return ErrInvalidMedia.
func (t Tools) Probe(ctx context.Context, path string, timeout time.Duration) (ProbeResult, error) {
	stdout, err := runTool(ctx, t.FFprobe, []string{"-v", "error", "-show_entries", "format=format_name,duration:stream=codec_type,codec_name", "-of", "json", "--", path}, runOptions{timeout: timeout})
	if err != nil {
		var toolErr *ToolError
		if errors.As(err, &toolErr) && !toolErr.TimedOut && !errors.Is(err, context.Canceled) {
			return ProbeResult{}, errors.Join(ErrInvalidMedia, err)
		}
		return ProbeResult{}, err
	}
	var parsed struct {
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		return ProbeResult{}, fmt.Errorf("parse ffprobe output: %w", err)
	}
	result := ProbeResult{FormatName: parsed.Format.FormatName}
	for _, stream := range parsed.Streams {
		if stream.CodecType == "audio" {
			result.AudioStreams++
			if result.AudioCodec == "" {
				result.AudioCodec = stream.CodecName
			}
		}
	}
	if seconds, err := strconv.ParseFloat(parsed.Format.Duration, 64); err == nil && seconds > 0 {
		result.DurationSeconds = &seconds
	}
	if result.AudioStreams == 0 {
		return result, ErrInvalidMedia
	}
	return result, nil
}

// container maps a stored MIME type to the muxer used for stream-copy remux.
type container struct {
	muxer string
	args  []string
}

func containerFor(mimeType string) (container, bool) {
	base := strings.TrimSpace(strings.Split(strings.ToLower(mimeType), ";")[0])
	switch base {
	case "audio/webm":
		// The WebM muxer writes Duration and Cues (a seek index) when the
		// output is a seekable file — exactly what MediaRecorder omits.
		return container{muxer: "webm"}, true
	case "audio/ogg":
		return container{muxer: "ogg"}, true
	case "audio/mp4":
		// Move the moov atom to the front so playback can start before the
		// whole file is fetched.
		return container{muxer: "mp4", args: []string{"-movflags", "+faststart"}}, true
	}
	return container{}, false
}

// Remux rewrites the container without re-encoding (stream copy): audio
// quality is untouched and CPU cost is minimal.
func (t Tools) Remux(ctx context.Context, input, output, mimeType string, timeout time.Duration) error {
	format, ok := containerFor(mimeType)
	if !ok {
		return fmt.Errorf("%w: unsupported container %q", ErrInvalidMedia, mimeType)
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", input, "-map", "0:a:0", "-c", "copy"}
	args = append(args, format.args...)
	args = append(args, "-f", format.muxer, output)
	_, err := runTool(ctx, t.FFmpeg, args, runOptions{timeout: timeout})
	return err
}

// DecodeForASR converts audio to 16 kHz mono 16-bit PCM WAV for Whisper.
func (t Tools) DecodeForASR(ctx context.Context, input, output string, timeout time.Duration) error {
	_, err := runTool(ctx, t.FFmpeg, []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", input,
		"-map", "0:a:0", "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-f", "wav", output}, runOptions{timeout: timeout})
	return err
}
