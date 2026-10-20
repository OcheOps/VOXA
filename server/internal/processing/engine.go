package processing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// EngineSegment is one recognized segment with recording-relative times.
type EngineSegment struct {
	StartSeconds float64
	EndSeconds   float64
	Text         string
}

type EngineResult struct {
	Language *string
	Segments []EngineSegment
}

type TranscribeOptions struct {
	// Language is an ISO code or "auto".
	Language  string
	Translate bool
	Timeout   time.Duration
	// OnProgress receives values in [0,1] when the engine reports progress.
	OnProgress func(float64)
}

// Engine is a speech-recognition backend. Implementations must honour ctx
// cancellation by terminating any child process.
type Engine interface {
	Name() string
	Model() string
	Transcribe(ctx context.Context, wavPath, workDir string, options TranscribeOptions) (EngineResult, error)
}

// WhisperCLI runs whisper.cpp's whisper-cli executable with a ggml model —
// the same engine and model format the desktop app embeds via whisper-rs.
type WhisperCLI struct {
	Binary    string
	ModelPath string
	Threads   int
}

func (w WhisperCLI) Name() string  { return "whisper.cpp" }
func (w WhisperCLI) Model() string { return strings.TrimSuffix(filepath.Base(w.ModelPath), ".bin") }

var languagePattern = regexp.MustCompile(`^(auto|[a-z]{2,3})$`)
var progressPattern = regexp.MustCompile(`progress\s*=\s*(\d+)%`)

// CheckWhisper verifies that the binary runs and the model file is present,
// so the server never advertises transcription it cannot perform.
func CheckWhisper(ctx context.Context, w WhisperCLI) error {
	info, err := os.Stat(w.ModelPath)
	if err != nil {
		return fmt.Errorf("whisper model not readable: %w", err)
	}
	if info.IsDir() || info.Size() < 1<<20 {
		return errors.New("whisper model file looks invalid (expected a ggml .bin model)")
	}
	if _, err := runTool(ctx, w.Binary, []string{"--help"}, runOptions{timeout: 30 * time.Second}); err != nil {
		return fmt.Errorf("whisper binary not runnable: %w", err)
	}
	return nil
}

func (w WhisperCLI) Transcribe(ctx context.Context, wavPath, workDir string, options TranscribeOptions) (EngineResult, error) {
	language := strings.ToLower(strings.TrimSpace(options.Language))
	if !languagePattern.MatchString(language) {
		language = "auto"
	}
	outputBase := filepath.Join(workDir, "transcript")
	args := []string{"-m", w.ModelPath, "-f", wavPath, "-l", language, "-oj", "-of", outputBase, "-np", "-pp"}
	if w.Threads > 0 {
		args = append(args, "-t", strconv.Itoa(w.Threads))
	}
	if options.Translate {
		args = append(args, "-tr")
	}
	_, err := runTool(ctx, w.Binary, args, runOptions{timeout: options.Timeout, onStderrLine: func(line string) {
		if match := progressPattern.FindStringSubmatch(line); match != nil && options.OnProgress != nil {
			if percent, err := strconv.Atoi(match[1]); err == nil {
				options.OnProgress(float64(percent) / 100)
			}
		}
	}})
	if err != nil {
		return EngineResult{}, err
	}
	raw, err := os.ReadFile(outputBase + ".json")
	if err != nil {
		return EngineResult{}, fmt.Errorf("whisper produced no output: %w", err)
	}
	return parseWhisperJSON(raw)
}

// parseWhisperJSON maps whisper-cli -oj output. Offsets are milliseconds
// from the start of the input, i.e. recording-relative.
func parseWhisperJSON(raw []byte) (EngineResult, error) {
	var output struct {
		Result struct {
			Language string `json:"language"`
		} `json:"result"`
		Transcription []struct {
			Offsets struct {
				From int64 `json:"from"`
				To   int64 `json:"to"`
			} `json:"offsets"`
			Text string `json:"text"`
		} `json:"transcription"`
	}
	if err := json.Unmarshal(raw, &output); err != nil {
		return EngineResult{}, fmt.Errorf("parse whisper output: %w", err)
	}
	result := EngineResult{Segments: []EngineSegment{}}
	if language := strings.TrimSpace(output.Result.Language); language != "" {
		result.Language = &language
	}
	for _, item := range output.Transcription {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		start, end := float64(item.Offsets.From)/1000, float64(item.Offsets.To)/1000
		if start < 0 {
			start = 0
		}
		if end < start {
			end = start
		}
		result.Segments = append(result.Segments, EngineSegment{StartSeconds: start, EndSeconds: end, Text: text})
	}
	return result, nil
}
