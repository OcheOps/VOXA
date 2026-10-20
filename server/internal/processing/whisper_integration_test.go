package processing

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
)

// TestRealWhisperPipeline runs remux + transcription with a real whisper.cpp
// engine. Opt-in (model files are not in the repository):
//
//	VOXA_TEST_WHISPER_BIN=/path/to/whisper-cli
//	VOXA_TEST_WHISPER_MODEL=/path/to/ggml-tiny.en.bin
//	VOXA_TEST_WHISPER_SAMPLE=/path/to/jfk.wav   (whisper.cpp samples/jfk.wav)
//	VOXA_TEST_MEDIA_DIR=/dir/the/engine/can/read   (optional; default a temp dir)
func TestRealWhisperPipeline(t *testing.T) {
	binary, model, sample := os.Getenv("VOXA_TEST_WHISPER_BIN"), os.Getenv("VOXA_TEST_WHISPER_MODEL"), os.Getenv("VOXA_TEST_WHISPER_SAMPLE")
	if binary == "" || model == "" || sample == "" {
		t.Skip("set VOXA_TEST_WHISPER_BIN, VOXA_TEST_WHISPER_MODEL and VOXA_TEST_WHISPER_SAMPLE to run")
	}
	tools := requireTools(t)
	if dir := os.Getenv("VOXA_TEST_MEDIA_DIR"); dir != "" {
		work, err := os.MkdirTemp(dir, "media-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(work) })
		t.Setenv("TMPDIR", work)
	}
	whisper := WhisperCLI{Binary: binary, ModelPath: model, Threads: 4}
	if err := CheckWhisper(context.Background(), whisper); err != nil {
		t.Fatalf("whisper check: %v", err)
	}
	// Browser-like upload: streamed WebM/Opus without duration metadata.
	webm, err := exec.Command(tools.FFmpeg, "-v", "error", "-i", sample, "-c:a", "libopus", "-b:a", "48k", "-f", "webm", "pipe:1").Output()
	if err != nil {
		t.Fatal(err)
	}
	p := newPipeline(t, tools, webm)
	p.transcribe.Engine = whisper
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := p.remux.Handle(ctx, p.job(t, domain.JobRemux), func(float64) {}); err != nil {
		t.Fatal(err)
	}
	var lastProgress float64
	if err := p.transcribe.Handle(ctx, p.job(t, domain.JobTranscription), func(v float64) { lastProgress = v }); err != nil {
		t.Fatal(err)
	}
	transcript, err := p.transcripts.GetByAsset(ctx, p.asset.WorkspaceID, p.asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for i, segment := range transcript.Segments {
		if segment.Sequence != i || segment.EndSeconds < segment.StartSeconds || segment.EndSeconds > 11.1 || segment.Confidence != nil {
			t.Fatalf("segment %d = %#v", i, segment)
		}
		text += " " + segment.Text
	}
	if !strings.Contains(strings.ToLower(text), "ask not what your country can do for you") || transcript.ASREngine != "whisper.cpp" || lastProgress < 0.9 {
		t.Fatalf("transcript = %q engine=%s progress=%v", text, transcript.ASREngine, lastProgress)
	}
}
