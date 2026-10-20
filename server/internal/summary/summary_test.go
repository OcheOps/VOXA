package summary

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

const testKey = "sk-test-SECRETSECRETSECRET1234"

func TestTemplatesMatchDesktopCopies(t *testing.T) {
	desktop := filepath.Join("..", "..", "..", "frontend", "src-tauri", "templates")
	entries, err := os.ReadDir(desktop)
	if err != nil {
		t.Skipf("desktop templates not available: %v", err)
	}
	count := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		count++
		want, _ := os.ReadFile(filepath.Join(desktop, entry.Name()))
		got, err := templateFiles.ReadFile("templates/" + entry.Name())
		if err != nil || string(got) != string(want) {
			t.Fatalf("server template %s drifted from the desktop copy", entry.Name())
		}
	}
	if count != len(Templates()) {
		t.Fatalf("server has %d templates, desktop %d", len(Templates()), count)
	}
	standard, ok := GetTemplate(DefaultTemplateID)
	if !ok || !strings.Contains(standard.SectionInstructions(), "For the 'Action Items' section:") ||
		!strings.Contains(standard.SectionInstructions(), "Items in this section should follow the format: `| **Owner** |") ||
		standard.MarkdownStructure() != "# <Add Title here>\n\n**Summary**\n\n**Key Decisions**\n\n**Action Items**\n\n**Discussion Highlights**\n\n" {
		t.Fatalf("standard template rendering:\n%s\n%s", standard.SectionInstructions(), standard.MarkdownStructure())
	}
}

func TestChunkTextMatchesDesktopSemantics(t *testing.T) {
	if chunks := ChunkText("short", 100, 10); len(chunks) != 1 || chunks[0] != "short" {
		t.Fatalf("short = %q", chunks)
	}
	// Desktop test: content after an early sentence boundary is not lost.
	text := "Intro. LOST_MARKER trailing content ensures chunking"
	found := false
	for _, chunk := range ChunkText(text, 10, 1) {
		found = found || strings.Contains(chunk, "LOST_MARKER")
	}
	if !found {
		t.Fatal("marker dropped")
	}
	long := strings.Repeat("Alpha beta gamma delta. ", 4000)
	first, second := ChunkText(long, 3000, 100), ChunkText(long, 3000, 100)
	if len(first) < 3 || strings.Join(first, "|") != strings.Join(second, "|") {
		t.Fatalf("chunking not deterministic or not split: %d", len(first))
	}
	for i, chunk := range first {
		if len([]rune(chunk)) > 8572 {
			t.Fatalf("chunk %d has %d chars", i, len([]rune(chunk)))
		}
		if i > 0 && !strings.HasSuffix(chunk, ". ") && i < len(first)-1 {
			t.Fatalf("chunk %d does not end on a sentence boundary", i)
		}
	}
	// Overlap: each chunk starts inside the previous one.
	if !strings.Contains(first[0], first[1][:50]) {
		t.Fatal("consecutive chunks do not overlap")
	}
	multibyte := strings.Repeat("é ", 5000)
	for _, chunk := range ChunkText(multibyte, 1000, 50) {
		if !strings.HasPrefix(chunk, "é") && !strings.HasPrefix(chunk, " ") {
			t.Fatalf("chunk split inside a character: %q", chunk[:4])
		}
	}
}

func TestCleanupAndTitle(t *testing.T) {
	text, stripped := cleanMarkdown("<think>private plan</think>\n```markdown\n# Weekly sync\n\n**Summary**\n\nShipped.\n```")
	if !stripped || text != "# Weekly sync\n\n**Summary**\n\nShipped." {
		t.Fatalf("clean = %q %v", text, stripped)
	}
	if err := requireVisible("Final summary", "<think>unterminated"); err == nil {
		t.Fatal("unterminated reasoning accepted")
	}
	title, body := splitTitle(text)
	if title != "Weekly sync" || body != "**Summary**\n\nShipped." {
		t.Fatalf("title=%q body=%q", title, body)
	}
	if _, body := splitTitle("## Not a title\nBody"); body != "## Not a title\nBody" {
		t.Fatalf("subheading stripped: %q", body)
	}
}

// fakeOpenAI is an OpenAI-compatible endpoint that records requests.
type fakeOpenAI struct {
	requests atomic.Int32
	headers  []http.Header
	bodies   []map[string]any
	handle   func(w http.ResponseWriter, body map[string]any, n int)
}

func (f *fakeOpenAI) serve(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		n := int(f.requests.Add(1))
		f.headers = append(f.headers, r.Header.Clone())
		f.bodies = append(f.bodies, body)
		f.handle(w, body, n)
	}))
	t.Cleanup(server.Close)
	return server
}

func chat(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 20},
	})
}

func openAI(url string) *OpenAICompatible {
	return &OpenAICompatible{ProviderName: ProviderOpenAI, BaseURL: url, APIKey: testKey, Client: NewHTTPClient()}
}

func TestOpenAICompatibleSuccessAndHeaders(t *testing.T) {
	fake := &fakeOpenAI{handle: func(w http.ResponseWriter, _ map[string]any, _ int) { chat(w, "  # Title\n\nBody  ") }}
	server := fake.serve(t)
	completion, err := openAI(server.URL+"/v1").Complete(context.Background(), "gpt-test", Message{System: "sys", User: "user"})
	if err != nil || completion.Text != "# Title\n\nBody" || *completion.InputTokens != 100 || *completion.OutputTokens != 20 {
		t.Fatalf("completion=%#v err=%v", completion, err)
	}
	if got := fake.headers[0].Get("Authorization"); got != "Bearer "+testKey {
		t.Fatalf("authorization = %q", got)
	}
	messages := fake.bodies[0]["messages"].([]any)
	if fake.bodies[0]["model"] != "gpt-test" || len(messages) != 2 || fake.bodies[0]["reasoning_effort"] != nil {
		t.Fatalf("body = %v", fake.bodies[0])
	}
}

func TestOllamaDisablesReasoningAndFallsBack(t *testing.T) {
	fake := &fakeOpenAI{handle: func(w http.ResponseWriter, body map[string]any, n int) {
		if body["reasoning_effort"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unknown field reasoning_effort","param":"reasoning_effort"}}`))
			return
		}
		chat(w, "ok")
	}}
	server := fake.serve(t)
	ollama := &OpenAICompatible{ProviderName: ProviderOllama, BaseURL: server.URL + "/v1", DisableReasoning: true, Client: NewHTTPClient()}
	completion, err := ollama.Complete(context.Background(), "llama3", Message{System: "s", User: "u"})
	if err != nil || completion.Text != "ok" || fake.requests.Load() != 2 {
		t.Fatalf("completion=%#v err=%v requests=%d", completion, err, fake.requests.Load())
	}
	if fake.headers[0].Get("Authorization") != "" {
		t.Fatal("Ollama without a credential sent an Authorization header")
	}
}

func TestAnthropicSuccess(t *testing.T) {
	var headers http.Header
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"thinking","thinking":"x"},{"type":"text","text":" Report "}],"usage":{"input_tokens":7,"output_tokens":3}}`))
	}))
	defer server.Close()
	client := &Anthropic{BaseURL: server.URL + "/v1", APIKey: testKey, Client: NewHTTPClient()}
	completion, err := client.Complete(context.Background(), "claude-test", Message{System: "sys", User: "user"})
	if err != nil || completion.Text != "Report" || !completion.ReasoningStripped || *completion.InputTokens != 7 {
		t.Fatalf("completion=%#v err=%v", completion, err)
	}
	if headers.Get("x-api-key") != testKey || headers.Get("anthropic-version") != "2023-06-01" || headers.Get("Authorization") != "" {
		t.Fatalf("headers = %v", headers)
	}
	if body["system"] != "sys" || body["max_tokens"].(float64) != 8192 {
		t.Fatalf("body = %v", body)
	}
}

func classify(t *testing.T, handler http.HandlerFunc) *Error {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	_, err := openAI(server.URL).Complete(context.Background(), "m", Message{})
	var providerErr *Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(providerErr.Error(), testKey) {
		t.Fatalf("credential leaked into error: %s", providerErr.Error())
	}
	return providerErr
}

func TestErrorClassificationAndSanitization(t *testing.T) {
	cases := []struct {
		name      string
		handler   http.HandlerFunc
		kind      ErrorKind
		retryable bool
	}{
		{"invalid credentials", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
			// Providers sometimes echo the key back; it must be redacted.
			_, _ = w.Write([]byte(`{"error":"Incorrect API key provided: ` + r.Header.Get("Authorization") + `"}`))
		}, KindAuth, false},
		{"rate limited", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "42")
			w.WriteHeader(429)
		}, KindRateLimited, true},
		{"server error", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }, KindUnavailable, true},
		{"unknown model", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }, KindModel, false},
		{"bad request", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(400) }, KindRejected, false},
		{"redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://attacker.invalid/collect", http.StatusTemporaryRedirect)
		}, KindRedirect, false},
		{"malformed", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>not json")) }, KindInvalidOutput, true},
		{"no choices", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"choices":[]}`)) }, KindInvalidOutput, true},
		{"oversized", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"`))
			_, _ = w.Write([]byte(strings.Repeat("a", MaxResponseBytes)))
			_, _ = w.Write([]byte(`"}}]}`))
		}, KindTooLarge, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := classify(t, c.handler)
			if err.Kind != c.kind || err.Retryable() != c.retryable {
				t.Fatalf("kind=%s retryable=%v err=%v", err.Kind, err.Retryable(), err)
			}
			if c.kind == KindRateLimited && err.RetryAfter != 42*time.Second {
				t.Fatalf("retry after = %v", err.RetryAfter)
			}
			if c.kind == KindAuth && (!strings.Contains(err.Detail, "[redacted]") || strings.Contains(err.Detail, "SECRET")) {
				t.Fatalf("detail not redacted: %q", err.Detail)
			}
		})
	}
}

func TestRedirectIsNeverFollowed(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
	defer target.Close()
	err := classify(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusPermanentRedirect)
	})
	if err.Kind != KindRedirect || followed.Load() {
		t.Fatalf("redirect followed=%v err=%v", followed.Load(), err)
	}
}

func TestCancellationAndTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	started := time.Now()
	_, err := openAI(server.URL).Complete(ctx, "m", Message{})
	if !errors.Is(err, context.Canceled) || time.Since(started) > 5*time.Second {
		t.Fatalf("cancel err=%v after %v", err, time.Since(started))
	}
	deadline, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	_, err = openAI(server.URL).Complete(deadline, "m", Message{})
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.Kind != KindTimeout {
		t.Fatalf("timeout err = %v", err)
	}
}

func TestEndpointValidation(t *testing.T) {
	for _, bad := range []string{"", "ftp://host", "http://", "https://user:pass@host", "https://host/v1?x=1", "https://host/#frag", "not a url"} {
		if _, _, err := ValidateEndpoint(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	for raw, wantWarning := range map[string]bool{
		"http://localhost:11434": false, "http://10.0.0.5:11434/": false, "http://127.0.0.1:8080/v1": false,
		"https://llm.example.com/v1": false, "http://llm.example.com/v1": true,
	} {
		base, warning, err := ValidateEndpoint(raw)
		if err != nil || strings.HasSuffix(base, "/") || (warning != "") != wantWarning {
			t.Fatalf("%s: base=%q warning=%q err=%v", raw, base, warning, err)
		}
	}
	if _, _, err := Specs(Settings{Enabled: []string{"ollama"}}); err == nil {
		t.Fatal("ollama enabled without URL")
	}
	if _, _, err := Specs(Settings{Enabled: []string{"builtin-ai"}}); err == nil {
		t.Fatal("unknown provider accepted")
	}
	specs, _, err := Specs(Settings{Enabled: []string{"openai", " claude ", "openai", "custom-openai"}, CustomOpenAIURL: "https://gw.example.com/v1/"})
	if err != nil || len(specs) != 3 || specs[2].BaseURL != "https://gw.example.com/v1" || !specs[0].CredentialRequired || specs[2].CredentialRequired {
		t.Fatalf("specs=%#v err=%v", specs, err)
	}
}

type secrets map[string]string

func (s secrets) RevealCredential(_ context.Context, workspaceID domain.WorkspaceID, provider string) (domain.CredentialSecret, error) {
	value, ok := s[string(workspaceID)+"/"+provider]
	if !ok {
		return domain.CredentialSecret{}, repository.ErrNotFound
	}
	return domain.NewCredentialSecret([]byte(value)), nil
}

func TestRegistryUsesOnlyTheJobWorkspaceCredential(t *testing.T) {
	fake := &fakeOpenAI{handle: func(w http.ResponseWriter, _ map[string]any, _ int) { chat(w, "ok") }}
	server := fake.serve(t)
	registry := NewRegistry([]Spec{{Name: ProviderCustomOpenAI, BaseURL: server.URL}, {Name: ProviderOpenAI, BaseURL: server.URL, CredentialRequired: true}},
		NewHTTPClient(), secrets{"ws-a/custom-openai": "key-of-a", "ws-b/openai": "key-of-b"})
	provider, release, err := registry.Open(context.Background(), "ws-a", ProviderCustomOpenAI)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = provider.Complete(context.Background(), "m", Message{})
	release()
	if got := fake.headers[0].Get("Authorization"); got != "Bearer key-of-a" {
		t.Fatalf("authorization = %q", got)
	}
	// Workspace A has no OpenAI key: B's key is never used.
	if _, _, err := registry.Open(context.Background(), "ws-a", ProviderOpenAI); err == nil {
		t.Fatal("opened OpenAI without the workspace's own credential")
	}
	if _, _, err := registry.Open(context.Background(), "ws-a", ProviderGroq); err == nil {
		t.Fatal("opened a provider the operator did not enable")
	}
	reason, _ := registry.Readiness(domain.SummaryDefaults{Provider: "openai", Model: "gpt"}, func(string) (bool, error) { return false, nil })
	if reason != ReasonCredentialMissing {
		t.Fatalf("readiness = %q", reason)
	}
	for config, want := range map[domain.SummaryDefaults]string{
		{Provider: "none"}:                         ReasonNotConfigured,
		{Provider: "groq", Model: "m"}:             ReasonProviderNotEnabled,
		{Provider: "custom-openai"}:                ReasonModelMissing,
		{Provider: "custom-openai", Model: "m"}:    "",
		{Provider: "openai", Model: "gpt-4o-mini"}: "",
	} {
		if got, _ := registry.Readiness(config, func(string) (bool, error) { return true, nil }); got != want {
			t.Fatalf("%v: readiness %q want %q", config, got, want)
		}
	}
}

// scripted is a Provider returning canned completions and recording prompts.
type scripted struct {
	calls   []Message
	respond func(message Message, n int) (Completion, error)
}

func (s *scripted) Name() string { return "scripted" }
func (s *scripted) Complete(ctx context.Context, _ string, message Message) (Completion, error) {
	if err := ctx.Err(); err != nil {
		return Completion{}, err
	}
	s.calls = append(s.calls, message)
	return s.respond(message, len(s.calls))
}

func tokens(n int) *int { return &n }

func TestGenerateSinglePassEnglish(t *testing.T) {
	provider := &scripted{respond: func(Message, int) (Completion, error) {
		return Completion{Text: "# Launch review\n\n**Summary**\n\nWe ship Friday.", InputTokens: tokens(50), OutputTokens: tokens(10)}, nil
	}}
	template, _ := GetTemplate(DefaultTemplateID)
	var progress []float64
	result, err := Generate(context.Background(), provider, Request{Transcript: "[00:01] We ship Friday.", TranscriptLanguage: "en", Template: template,
		CustomPrompt: "Product team", Model: "m", ChunkTokens: 100000, Progress: func(p float64) { progress = append(progress, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.calls) != 1 || !strings.Contains(provider.calls[0].User, "<transcript_chunks>\n[00:01] We ship Friday.") ||
		!strings.Contains(provider.calls[0].User, "<user_context>\nProduct team") || !strings.Contains(provider.calls[0].System, "**Key Decisions**") {
		t.Fatalf("calls = %#v", provider.calls)
	}
	if result.Title != "Launch review" || result.Markdown != "**Summary**\n\nWe ship Friday." || result.ChunkCount != 1 ||
		*result.Usage.InputTokens != 50 || result.Usage.Requests != 1 || len(progress) != 1 || progress[0] != 1 {
		t.Fatalf("result = %#v progress=%v", result, progress)
	}
	if content := result.Content(); content.Format != "markdown" || content.EnglishMarkdown != "" {
		t.Fatalf("content = %#v", content)
	}
}

func TestGenerateChunksTranslatesAndResumesFromCheckpoint(t *testing.T) {
	transcript := strings.Repeat("[00:01] Budget discussion continues. ", 400) // ~14k chars
	template, _ := GetTemplate(DefaultTemplateID)
	failAt := 3
	respond := func(message Message, n int) (Completion, error) {
		switch {
		case strings.Contains(message.User, "<transcript_chunk>"):
			if n == failAt {
				return Completion{}, &Error{Kind: KindUnavailable, Status: 503}
			}
			return Completion{Text: "chunk summary"}, nil
		case strings.Contains(message.User, "<summaries>"):
			return Completion{Text: "combined"}, nil
		case strings.Contains(message.System, "precise translator"):
			return Completion{Text: "# Réunion\n\n**Résumé**\n\nBudget."}, nil
		default:
			return Completion{Text: "# Meeting\n\n**Summary**\n\nBudget."}, nil
		}
	}
	var saved []byte
	request := Request{Transcript: transcript, TranscriptLanguage: "en", Template: template, OutputLanguage: "fr", Model: "m", ChunkTokens: 1500,
		SaveCheckpoint: func(data []byte) error { saved = append([]byte(nil), data...); return nil }}
	first := &scripted{respond: respond}
	if _, err := Generate(context.Background(), first, request); err == nil {
		t.Fatal("expected the injected chunk failure")
	}
	chunks := ChunkText(transcript, 1200, 100)
	if len(chunks) < 3 || len(saved) == 0 {
		t.Fatalf("chunks=%d saved=%d", len(chunks), len(saved))
	}
	// Retry: completed chunks are not sent again.
	failAt = -1
	request.Checkpoint = saved
	second := &scripted{respond: respond}
	result, err := Generate(context.Background(), second, request)
	if err != nil {
		t.Fatal(err)
	}
	chunkCalls := 0
	for _, call := range second.calls {
		if strings.Contains(call.User, "<transcript_chunk>") {
			chunkCalls++
		}
	}
	if chunkCalls != len(chunks)-2 {
		t.Fatalf("retry re-sent %d of %d chunks (2 were checkpointed)", chunkCalls, len(chunks))
	}
	if result.Title != "Réunion" || result.Markdown != "**Résumé**\n\nBudget." || result.ChunkCount != len(chunks) ||
		result.Content().EnglishMarkdown != "# Meeting\n\n**Summary**\n\nBudget." {
		t.Fatalf("result = %#v", result)
	}
	// A different transcript invalidates the checkpoint.
	request.Transcript += " More."
	third := &scripted{respond: respond}
	if _, err := Generate(context.Background(), third, request); err != nil {
		t.Fatal(err)
	}
	if calls := len(third.calls); calls < len(chunks)+2 {
		t.Fatalf("stale checkpoint reused (%d calls)", calls)
	}
}

func TestGenerateRejectsUnusableOutputAndNormalizationFallsBack(t *testing.T) {
	template, _ := GetTemplate(DefaultTemplateID)
	for _, text := range []string{"", "<think>never closed", "# Only a title"} {
		provider := &scripted{respond: func(Message, int) (Completion, error) { return Completion{Text: text}, nil }}
		_, err := Generate(context.Background(), provider, Request{Transcript: "[00:00] hi", TranscriptLanguage: "en", Template: template, Model: "m", ChunkTokens: 100000})
		var providerErr *Error
		if !errors.As(err, &providerErr) || providerErr.Kind != KindInvalidOutput {
			t.Fatalf("%q accepted: %v", text, err)
		}
	}
	// Unknown transcript language: normalization runs; if it fails the pass-1 report is kept.
	provider := &scripted{respond: func(message Message, n int) (Completion, error) {
		if n == 2 {
			return Completion{}, &Error{Kind: KindUnavailable}
		}
		return Completion{Text: "# T\n\nBody"}, nil
	}}
	result, err := Generate(context.Background(), provider, Request{Transcript: "[00:00] hola", Template: template, Model: "m", ChunkTokens: 100000})
	if err != nil || !result.NormalizationFallback || result.Markdown != "Body" || len(provider.calls) != 2 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if _, err := Generate(context.Background(), provider, Request{Transcript: "  ", Template: template}); !errors.Is(err, ErrEmptyTranscript) {
		t.Fatalf("empty transcript err = %v", err)
	}
}

func TestGenerateStopsBetweenChunksWhenCancelled(t *testing.T) {
	template, _ := GetTemplate(DefaultTemplateID)
	ctx, cancel := context.WithCancel(context.Background())
	provider := &scripted{respond: func(Message, int) (Completion, error) { cancel(); return Completion{Text: "chunk"}, nil }}
	_, err := Generate(ctx, provider, Request{Transcript: strings.Repeat("word ", 5000), Template: template, Model: "m", ChunkTokens: 1000})
	if !errors.Is(err, context.Canceled) || len(provider.calls) != 1 {
		t.Fatalf("err=%v calls=%d", err, len(provider.calls))
	}
}
