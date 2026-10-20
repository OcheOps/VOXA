package summary

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

// Provider names match the desktop app and the workspace summary
// configuration. The credential for a provider is stored under the same name.
const (
	ProviderOpenAI       = "openai"
	ProviderClaude       = "claude"
	ProviderGroq         = "groq"
	ProviderOpenRouter   = "openrouter"
	ProviderOllama       = "ollama"
	ProviderCustomOpenAI = "custom-openai"
)

// KnownProviders are the providers the server can talk to.
var KnownProviders = []string{ProviderClaude, ProviderCustomOpenAI, ProviderGroq, ProviderOllama, ProviderOpenAI, ProviderOpenRouter}

// Fixed public endpoints. Only operator configuration can change where a
// request goes; workspace users choose a provider and model, never a URL.
var cloudBaseURLs = map[string]string{
	ProviderOpenAI:     "https://api.openai.com/v1",
	ProviderGroq:       "https://api.groq.com/openai/v1",
	ProviderOpenRouter: "https://openrouter.ai/api/v1",
	ProviderClaude:     "https://api.anthropic.com/v1",
}

// Spec is one enabled provider.
type Spec struct {
	Name string
	// BaseURL is the API root the client appends its path to.
	BaseURL string
	// CredentialRequired: the workspace must store an API key under Name.
	// Otherwise a stored key is sent if present (e.g. Ollama behind a proxy).
	CredentialRequired bool
}

// Settings is the operator configuration (environment variables).
type Settings struct {
	Enabled         []string
	OllamaURL       string
	CustomOpenAIURL string
}

// ValidateEndpoint checks an operator-supplied API root: http or https, a
// host, no credentials, query or fragment. Loopback and private addresses
// are allowed on purpose (self-hosted Ollama or gateways); plaintext http to
// a non-local host is reported as a warning because API keys would cross the
// network unencrypted.
func ValidateEndpoint(raw string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" {
		return "", "", errors.New("must be an absolute http(s) URL with a host")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", errors.New("scheme must be http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", errors.New("must not contain credentials, a query or a fragment")
	}
	warning := ""
	if parsed.Scheme == "http" && !isLocalHost(parsed.Hostname()) {
		warning = "plaintext http to a non-local host: credentials and transcripts are sent unencrypted"
	}
	return strings.TrimRight(parsed.String(), "/"), warning, nil
}

func isLocalHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// Specs builds the enabled provider list from operator settings.
func Specs(settings Settings) ([]Spec, []string, error) {
	var specs []Spec
	var warnings []string
	seen := map[string]bool{}
	for _, raw := range settings.Enabled {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		switch name {
		case ProviderOpenAI, ProviderGroq, ProviderOpenRouter, ProviderClaude:
			specs = append(specs, Spec{Name: name, BaseURL: cloudBaseURLs[name], CredentialRequired: true})
		case ProviderOllama:
			if strings.TrimSpace(settings.OllamaURL) == "" {
				return nil, nil, errors.New("ollama is enabled but VOXA_OLLAMA_URL is not set")
			}
			base, warning, err := ValidateEndpoint(settings.OllamaURL)
			if err != nil {
				return nil, nil, fmt.Errorf("VOXA_OLLAMA_URL %w", err)
			}
			if warning != "" {
				warnings = append(warnings, "ollama: "+warning)
			}
			specs = append(specs, Spec{Name: name, BaseURL: base})
		case ProviderCustomOpenAI:
			if strings.TrimSpace(settings.CustomOpenAIURL) == "" {
				return nil, nil, errors.New("custom-openai is enabled but VOXA_CUSTOM_OPENAI_URL is not set")
			}
			base, warning, err := ValidateEndpoint(settings.CustomOpenAIURL)
			if err != nil {
				return nil, nil, fmt.Errorf("VOXA_CUSTOM_OPENAI_URL %w", err)
			}
			if warning != "" {
				warnings = append(warnings, "custom-openai: "+warning)
			}
			specs = append(specs, Spec{Name: name, BaseURL: base})
		default:
			return nil, nil, fmt.Errorf("unknown summary provider %q (known: %s)", name, strings.Join(KnownProviders, ", "))
		}
	}
	return specs, warnings, nil
}

// Registry resolves enabled providers and their per-workspace credentials.
type Registry struct {
	specs   map[string]Spec
	client  *http.Client
	secrets repository.CredentialSecretReader
}

func NewRegistry(specs []Spec, client *http.Client, secrets repository.CredentialSecretReader) *Registry {
	registry := &Registry{specs: map[string]Spec{}, client: client, secrets: secrets}
	for _, spec := range specs {
		registry.specs[spec.Name] = spec
	}
	return registry
}

// Enabled lists enabled provider names, sorted.
func (r *Registry) Enabled() []string {
	names := make([]string, 0, len(r.specs))
	for name := range r.specs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) Spec(name string) (Spec, bool) {
	spec, ok := r.specs[name]
	return spec, ok
}

// Readiness reasons for a workspace's summary configuration.
const (
	ReasonNotConfigured      = "not_configured"
	ReasonProviderNotEnabled = "provider_not_enabled"
	ReasonModelMissing       = "model_missing"
	ReasonCredentialMissing  = "credential_missing"
)

// Readiness reports whether a workspace's configuration can run summaries,
// using only local state (no provider requests).
func (r *Registry) Readiness(config domain.SummaryDefaults, credentialConfigured func(provider string) (bool, error)) (string, error) {
	provider := strings.TrimSpace(config.Provider)
	if provider == "" || provider == "none" {
		return ReasonNotConfigured, nil
	}
	spec, ok := r.specs[provider]
	if !ok {
		return ReasonProviderNotEnabled, nil
	}
	if strings.TrimSpace(config.Model) == "" {
		return ReasonModelMissing, nil
	}
	if spec.CredentialRequired {
		configured, err := credentialConfigured(provider)
		if err != nil {
			return "", err
		}
		if !configured {
			return ReasonCredentialMissing, nil
		}
	}
	return "", nil
}

// Open returns a provider for one job, using the workspace's own credential
// (never another workspace's). The returned release wipes the decrypted key
// copy held by Voxa; call it when the job's provider calls are done.
func (r *Registry) Open(ctx context.Context, workspaceID domain.WorkspaceID, name string) (Provider, func(), error) {
	spec, ok := r.specs[name]
	if !ok {
		return nil, nil, &Error{Kind: KindConfig, Detail: "provider not enabled on this server"}
	}
	key := ""
	release := func() {}
	if r.secrets != nil {
		secret, err := r.secrets.RevealCredential(ctx, workspaceID, name)
		switch {
		case err == nil && !secret.IsBlank():
			key = strings.TrimSpace(string(secret.Expose()))
			release = func() { secret.Wipe() }
		case err == nil, errors.Is(err, repository.ErrNotFound):
		default:
			var unreadable *repository.CredentialUnreadableError
			if errors.As(err, &unreadable) {
				return nil, nil, &Error{Kind: KindConfig, Detail: "stored credential cannot be decrypted"}
			}
			return nil, nil, &Error{Kind: KindUnavailable, Detail: "credential storage unavailable"}
		}
	}
	if spec.CredentialRequired && key == "" {
		release()
		return nil, nil, &Error{Kind: KindConfig, Detail: "no credential configured for provider"}
	}
	if name == ProviderClaude {
		return &Anthropic{BaseURL: spec.BaseURL, APIKey: key, Client: r.client}, release, nil
	}
	base := spec.BaseURL
	if name == ProviderOllama {
		base += "/v1"
	}
	return &OpenAICompatible{ProviderName: name, BaseURL: base, APIKey: key, DisableReasoning: name == ProviderOllama, Client: r.client}, release, nil
}

const (
	// cloudChunkTokens matches the desktop threshold for hosted providers
	// (effectively single pass) but, unlike the desktop, still chunks above
	// it so no request is unbounded.
	cloudChunkTokens = 100000
	// ollamaFallbackTokens is the desktop fallback when the context size
	// cannot be read.
	ollamaFallbackTokens = 4000
	promptReserveTokens  = 300
)

// ChunkThreshold returns the token threshold above which a transcript is
// summarized in chunks (desktop: context size minus a 300-token reserve).
func (r *Registry) ChunkThreshold(ctx context.Context, name, model string) int {
	if name != ProviderOllama {
		return cloudChunkTokens
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if tokens, ok := ollamaContextTokens(ctx, r.client, r.specs[name].BaseURL, model); ok && tokens > promptReserveTokens+500 {
		return tokens - promptReserveTokens
	}
	return ollamaFallbackTokens
}
