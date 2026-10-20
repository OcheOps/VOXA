package summary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
)

const (
	// chunkOverlapTokens matches the desktop (100 tokens ≈ 286 characters).
	chunkOverlapTokens = 100
	// MaxChunks bounds the provider requests one summary may make.
	MaxChunks = 200
	// MaxSummaryBytes bounds the stored Markdown.
	MaxSummaryBytes = 256 << 10
)

// ErrTranscriptTooLong reports a transcript that would need more than
// MaxChunks provider requests.
var ErrTranscriptTooLong = errors.New("transcript too long to summarize")

// ErrEmptyTranscript reports a transcript with no text.
var ErrEmptyTranscript = errors.New("transcript has no text")

// Request is everything one summary needs. It carries no credentials.
type Request struct {
	Transcript         string
	TranscriptLanguage string
	Template           Template
	CustomPrompt       string
	OutputLanguage     string
	Model              string
	// ChunkTokens is the threshold above which the transcript is chunked.
	ChunkTokens int
	// Checkpoint is the previous attempt's saved state (may be nil).
	Checkpoint []byte
	// SaveCheckpoint persists progress after each chunk; an error stops the run.
	SaveCheckpoint func([]byte) error
	Progress       func(float64)
}

// Result is a validated summary.
type Result struct {
	Markdown              string
	EnglishMarkdown       string
	Title                 string
	ReasoningStripped     bool
	NormalizationFallback bool
	ChunkCount            int
	Usage                 domain.SummaryUsage
}

// Content is the stored JSON shape (summaries.content), compatible with the
// desktop result's "markdown" field.
type Content struct {
	Format                string `json:"format"`
	Markdown              string `json:"markdown"`
	Title                 string `json:"title,omitempty"`
	EnglishMarkdown       string `json:"englishMarkdown,omitempty"`
	ChunkCount            int    `json:"chunkCount"`
	ReasoningStripped     bool   `json:"reasoningStripped"`
	NormalizationFallback bool   `json:"normalizationFallback"`
}

func (r Result) Content() Content {
	content := Content{Format: "markdown", Markdown: r.Markdown, Title: r.Title, ChunkCount: r.ChunkCount,
		ReasoningStripped: r.ReasoningStripped, NormalizationFallback: r.NormalizationFallback}
	if r.EnglishMarkdown != r.Markdown {
		content.EnglishMarkdown = r.EnglishMarkdown
	}
	return content
}

type checkpoint struct {
	Fingerprint string   `json:"fingerprint"`
	Chunks      []string `json:"chunks"`
}

func fingerprint(request Request, chunks []string) string {
	hash := sha256.New()
	for _, part := range []string{request.Transcript, request.Model, strconv.Itoa(request.ChunkTokens), strconv.Itoa(len(chunks))} {
		hash.Write([]byte(part))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type run struct {
	ctx                 context.Context
	provider            Provider
	request             Request
	result              *Result
	inTokens, outTokens int
	reported            bool
	done, total         int
}

func (r *run) step() {
	r.done++
	if r.request.Progress != nil && r.total > 0 {
		r.request.Progress(float64(r.done) / float64(r.total))
	}
}

// call runs one completion, cleans it and requires visible content.
func (r *run) call(stage string, message Message) (string, error) {
	if err := r.ctx.Err(); err != nil {
		return "", err
	}
	completion, err := r.provider.Complete(r.ctx, r.request.Model, message)
	r.result.Usage.Requests++
	if err != nil {
		return "", err
	}
	if completion.InputTokens != nil {
		r.inTokens += *completion.InputTokens
		r.reported = true
	}
	if completion.OutputTokens != nil {
		r.outTokens += *completion.OutputTokens
		r.reported = true
	}
	text, stripped := cleanMarkdown(completion.Text)
	r.result.ReasoningStripped = r.result.ReasoningStripped || stripped || completion.ReasoningStripped
	if err := requireVisible(stage, text); err != nil {
		return "", err
	}
	return text, nil
}

// Generate produces a summary the way the desktop does: chunk summaries and
// a combine pass for long transcripts, a template-filling final pass, then
// translation (non-English target) or English normalization (transcript not
// known to be English). Completed chunk summaries are checkpointed so a
// retry resumes instead of repeating them.
func Generate(ctx context.Context, provider Provider, request Request) (result Result, err error) {
	started := time.Now()
	if strings.TrimSpace(request.Transcript) == "" {
		return result, ErrEmptyTranscript
	}
	r := &run{ctx: ctx, provider: provider, request: request, result: &result}
	defer func() {
		result.Usage.DurationMs = time.Since(started).Milliseconds()
		if r.reported {
			in, out := r.inTokens, r.outTokens
			result.Usage.InputTokens, result.Usage.OutputTokens = &in, &out
		}
	}()

	content := request.Transcript
	languageStep := 0
	action, targetLanguage := finalLanguageAction(request.OutputLanguage, request.TranscriptLanguage)
	if action != returnEnglish {
		languageStep = 1
	}
	result.ChunkCount = 1
	if RoughTokenCount(request.Transcript) >= request.ChunkTokens {
		chunks := ChunkText(request.Transcript, request.ChunkTokens-promptReserveTokens, chunkOverlapTokens)
		if len(chunks) > MaxChunks {
			return result, ErrTranscriptTooLong
		}
		r.total = len(chunks) + 1 + languageStep
		if len(chunks) > 1 {
			r.total++
		}
		state := checkpoint{Fingerprint: fingerprint(request, chunks), Chunks: make([]string, len(chunks))}
		var previous checkpoint
		if len(request.Checkpoint) > 0 && json.Unmarshal(request.Checkpoint, &previous) == nil &&
			previous.Fingerprint == state.Fingerprint && len(previous.Chunks) == len(chunks) {
			state.Chunks = previous.Chunks
		}
		for i, chunk := range chunks {
			if state.Chunks[i] != "" {
				r.step() // resumed from the previous attempt
				continue
			}
			summary, err := r.call(fmt.Sprintf("Summary chunk %d of %d", i+1, len(chunks)), Message{System: chunkSystemPrompt, User: chunkUserPrompt(chunk)})
			if err != nil {
				return result, err
			}
			state.Chunks[i] = summary
			if request.SaveCheckpoint != nil {
				encoded, _ := json.Marshal(state)
				if err := request.SaveCheckpoint(encoded); err != nil {
					return result, err
				}
			}
			r.step()
		}
		result.ChunkCount = len(chunks)
		if len(chunks) == 1 {
			content = state.Chunks[0]
		} else {
			combined, err := r.call("Combined summary", Message{System: combineSystemPrompt, User: combineUserPrompt(strings.Join(state.Chunks, "\n---\n"))})
			if err != nil {
				return result, err
			}
			content = combined
			r.step()
		}
	} else {
		r.total = 1 + languageStep
	}

	english, err := r.call("Final summary", Message{
		System: finalSystemPrompt(request.Template.SectionInstructions(), request.Template.MarkdownStructure()),
		User:   finalUserPrompt(content, request.CustomPrompt),
	})
	if err != nil {
		return result, err
	}
	r.step()

	final := english
	switch action {
	case translate:
		translated, err := r.call("Translation", Message{System: translationSystemPrompt(targetLanguage), User: translationUserPrompt(targetLanguage, english)})
		if err != nil {
			return result, err
		}
		final = translated
	case normalizeEnglish:
		// As on the desktop, a failed normalization keeps the pass-1 report.
		normalized, err := r.call("English normalization", Message{System: englishNormalizationSystemPrompt, User: normalizationUserPrompt(english)})
		switch {
		case err == nil:
			english, final = normalized, normalized
		case ctx.Err() != nil:
			return result, ctx.Err()
		default:
			result.NormalizationFallback = true
		}
	}
	if languageStep == 1 {
		r.step()
	}

	title, body := splitTitle(final)
	if strings.TrimSpace(body) == "" {
		return result, invalidOutput("final summary has no content after the title")
	}
	if len(body) > MaxSummaryBytes || len(english) > MaxSummaryBytes {
		return result, invalidOutput(fmt.Sprintf("summary exceeds %d bytes", MaxSummaryBytes))
	}
	result.Title, result.Markdown, result.EnglishMarkdown = title, body, english
	if action == normalizeEnglish || action == returnEnglish {
		result.EnglishMarkdown = body
	}
	return result, nil
}
