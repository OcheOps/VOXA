package processing

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunToolTimeoutAndCancelKillTheProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	// The shell starts a grandchild and waits; both must die.
	script := "sleep 30 & echo $! > " + pidFile + "; wait"
	_, err := runTool(context.Background(), "sh", []string{"-c", script}, runOptions{timeout: 300 * time.Millisecond})
	var toolErr *ToolError
	if !errors.As(err, &toolErr) || !toolErr.TimedOut {
		t.Fatalf("err = %v, want timeout", err)
	}
	assertDead(t, pidFile)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	_, err = runTool(ctx, "sh", []string{"-c", script}, runOptions{timeout: time.Minute})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want cancellation", err)
	}
	assertDead(t, pidFile)
}

func assertDead(t *testing.T, pidFile string) {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	for i := 0; i < 50; i++ {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild %d survived", pid)
}

func TestRunToolBoundsCapturedOutput(t *testing.T) {
	_, err := runTool(context.Background(), "sh", []string{"-c", "head -c 100000 /dev/zero | tr '\\0' 'x' >&2; exit 3"}, runOptions{timeout: 10 * time.Second})
	var toolErr *ToolError
	if !errors.As(err, &toolErr) || toolErr.ExitCode != 3 || len(toolErr.Stderr) > maxCapture {
		t.Fatalf("err = %v (stderr %d bytes)", err, len(toolErr.Stderr))
	}
}

func TestParseWhisperJSON(t *testing.T) {
	raw, err := os.ReadFile("testdata/whisper-jfk.json")
	if err != nil {
		t.Fatal(err)
	}
	result, err := parseWhisperJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Language == nil || *result.Language != "en" || len(result.Segments) != 2 {
		t.Fatalf("result = %#v", result)
	}
	first, second := result.Segments[0], result.Segments[1]
	if first.StartSeconds != 0 || first.EndSeconds != 7.94 || second.StartSeconds != 7.94 || second.EndSeconds != 10.76 ||
		!strings.HasPrefix(first.Text, "And so my fellow Americans") || strings.HasPrefix(first.Text, " ") {
		t.Fatalf("segments = %#v", result.Segments)
	}
	if _, err := parseWhisperJSON([]byte("not json")); err == nil {
		t.Fatal("garbage accepted")
	}
	empty, _ := parseWhisperJSON([]byte(`{"transcription":[{"offsets":{"from":5,"to":1},"text":"  "}]}`))
	if len(empty.Segments) != 0 || empty.Language != nil {
		t.Fatalf("blank segments kept: %#v", empty)
	}
}

func TestToSegmentsClampsToRecording(t *testing.T) {
	duration := 9.0
	segments := toSegments([]EngineSegment{{0, 4, "a"}, {4, 10.5, "b"}, {11, 12, "c"}}, &duration)
	if len(segments) != 3 || segments[1].EndSeconds != 9 || segments[2].StartSeconds != 9 || segments[2].EndSeconds != 9 || segments[2].Sequence != 2 || segments[0].Confidence != nil {
		t.Fatalf("segments = %#v", segments)
	}
}
