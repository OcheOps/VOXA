package main

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestLoadSummaryProviders(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	if specs, err := loadSummaryProviders(quiet, env(nil)); err != nil || len(specs) != 0 {
		t.Fatalf("default must enable nothing: %v %v", specs, err)
	}
	for _, values := range []map[string]string{
		{summaryProvidersEnv: "openai,builtin-ai"},
		{summaryProvidersEnv: "ollama"},
		{summaryProvidersEnv: "custom-openai", customOpenAIURLEnv: "ftp://gateway"},
		{summaryProvidersEnv: "custom-openai", customOpenAIURLEnv: "https://user:pass@gateway/v1"},
	} {
		if _, err := loadSummaryProviders(quiet, env(values)); err == nil {
			t.Fatalf("accepted %v", values)
		} else if strings.Contains(err.Error(), "pass@") {
			t.Fatalf("error echoes endpoint credentials: %v", err)
		}
	}
	var logs bytes.Buffer
	specs, err := loadSummaryProviders(slog.New(slog.NewTextHandler(&logs, nil)), env(map[string]string{
		summaryProvidersEnv: " openai , ollama,claude", ollamaURLEnv: "http://ollama.internal.example:11434",
	}))
	if err != nil || len(specs) != 3 {
		t.Fatalf("specs=%v err=%v", specs, err)
	}
	if !strings.Contains(logs.String(), "plaintext http") {
		t.Fatalf("no warning for plaintext remote endpoint: %s", logs.String())
	}
}
