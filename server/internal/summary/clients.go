package summary

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

var (
	bearerPattern = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`)
	// Common API key shapes (sk-…, sk-ant-…, gsk_…), redacted from any
	// provider text kept for operators.
	keyPattern = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{8,}|gsk_[A-Za-z0-9]{8,}|sk-ant-[A-Za-z0-9_-]{8,})`)
)

// OpenAICompatible calls POST {BaseURL}/chat/completions with the OpenAI chat
// schema. It serves OpenAI, Groq, OpenRouter, operator-configured
// OpenAI-compatible endpoints and Ollama (via its /v1 compatibility API).
type OpenAICompatible struct {
	ProviderName string
	BaseURL      string // e.g. https://api.openai.com/v1
	APIKey       string // empty: no Authorization header (e.g. local Ollama)
	// DisableReasoning sends reasoning_effort "none" (desktop Ollama
	// behaviour), retrying once without it when the server rejects it.
	DisableReasoning bool
	Client           *http.Client
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model           string        `json:"model"`
	Messages        []chatMessage `json:"messages"`
	ReasoningEffort *string       `json:"reasoning_effort,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content          *string `json:"content"`
			Reasoning        *string `json:"reasoning"`
			ReasoningContent *string `json:"reasoning_content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     *int `json:"prompt_tokens"`
		CompletionTokens *int `json:"completion_tokens"`
	} `json:"usage"`
}

func (p *OpenAICompatible) Name() string { return p.ProviderName }

func (p *OpenAICompatible) Complete(ctx context.Context, model string, message Message) (Completion, error) {
	headers := http.Header{}
	if p.APIKey != "" {
		headers.Set("Authorization", "Bearer "+p.APIKey)
	}
	if p.ProviderName == "openrouter" {
		headers.Set("X-Title", "Voxa")
	}
	request := chatRequest{Model: model, Messages: []chatMessage{{Role: "system", Content: message.System}, {Role: "user", Content: message.User}}}
	if p.DisableReasoning {
		none := "none"
		request.ReasoningEffort = &none
	}
	url := strings.TrimRight(p.BaseURL, "/") + "/chat/completions"
	var response chatResponse
	err := postJSON(ctx, p.Client, url, headers, request, &response, p.APIKey)
	if providerErr, ok := err.(*Error); ok && p.DisableReasoning && rejectsReasoningEffort(providerErr) {
		request.ReasoningEffort = nil
		response = chatResponse{}
		err = postJSON(ctx, p.Client, url, headers, request, &response, p.APIKey)
	}
	if err != nil {
		return Completion{}, err
	}
	if len(response.Choices) == 0 {
		return Completion{}, invalidOutput("response has no choices")
	}
	choice := response.Choices[0].Message
	completion := Completion{}
	if choice.Content != nil {
		completion.Text = strings.TrimSpace(*choice.Content)
	}
	for _, reasoning := range []*string{choice.Reasoning, choice.ReasoningContent} {
		if reasoning != nil && strings.TrimSpace(*reasoning) != "" {
			completion.ReasoningStripped = true
		}
	}
	if response.Usage != nil {
		completion.InputTokens, completion.OutputTokens = response.Usage.PromptTokens, response.Usage.CompletionTokens
	}
	return completion, nil
}

// rejectsReasoningEffort mirrors ollama_rejects_reasoning_effort on the
// sanitized error detail.
func rejectsReasoningEffort(err *Error) bool {
	if err.Status != http.StatusBadRequest && err.Status != http.StatusUnprocessableEntity {
		return false
	}
	detail := strings.ToLower(err.Detail)
	return strings.Contains(detail, "reasoning_effort") ||
		(strings.Contains(detail, "think value") && strings.Contains(detail, "none") && (strings.Contains(detail, "not supported") || strings.Contains(detail, "invalid")))
}

// Anthropic calls the Messages API (POST {BaseURL}/messages).
type Anthropic struct {
	BaseURL string // https://api.anthropic.com/v1
	APIKey  string
	Client  *http.Client
}

type anthropicRequest struct {
	Model     string        `json:"model"`
	MaxTokens int           `json:"max_tokens"`
	System    string        `json:"system"`
	Messages  []chatMessage `json:"messages"`
}

type anthropicResponse struct {
	Content []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	} `json:"content"`
	Usage *struct {
		InputTokens  *int `json:"input_tokens"`
		OutputTokens *int `json:"output_tokens"`
	} `json:"usage"`
}

func (p *Anthropic) Name() string { return "claude" }

func (p *Anthropic) Complete(ctx context.Context, model string, message Message) (Completion, error) {
	headers := http.Header{}
	headers.Set("x-api-key", p.APIKey)
	headers.Set("anthropic-version", "2023-06-01")
	// 8192 matches the desktop client (shared with thinking on models that think by default).
	request := anthropicRequest{Model: model, MaxTokens: 8192, System: message.System, Messages: []chatMessage{{Role: "user", Content: message.User}}}
	var response anthropicResponse
	if err := postJSON(ctx, p.Client, strings.TrimRight(p.BaseURL, "/")+"/messages", headers, request, &response, p.APIKey); err != nil {
		return Completion{}, err
	}
	completion := Completion{}
	found := false
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			if !found && block.Text != nil {
				completion.Text, found = strings.TrimSpace(*block.Text), true
			}
		case "thinking", "redacted_thinking":
			completion.ReasoningStripped = true
		}
	}
	if !found {
		return Completion{}, invalidOutput("response has no text content")
	}
	if response.Usage != nil {
		completion.InputTokens, completion.OutputTokens = response.Usage.InputTokens, response.Usage.OutputTokens
	}
	return completion, nil
}

// ollamaContextTokens asks Ollama for the model's context length
// (POST {root}/api/show), like the desktop metadata cache. ok=false when it
// cannot be determined.
func ollamaContextTokens(ctx context.Context, client *http.Client, root, model string) (int, bool) {
	var response struct {
		ModelInfo map[string]json.RawMessage `json:"model_info"`
	}
	if err := postJSON(ctx, client, strings.TrimRight(root, "/")+"/api/show", nil, map[string]string{"model": model}, &response); err != nil {
		return 0, false
	}
	for key, value := range response.ModelInfo {
		if strings.HasSuffix(key, ".context_length") {
			var tokens int
			if json.Unmarshal(value, &tokens) == nil && tokens > 0 {
				return tokens, true
			}
		}
	}
	return 0, false
}
