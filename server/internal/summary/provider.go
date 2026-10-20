package summary

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Message is one chat completion request: a system and a user prompt.
type Message struct {
	System string
	User   string
}

// Completion is the visible text of a provider response. Token counts are
// nil when the provider does not report them.
type Completion struct {
	Text              string
	ReasoningStripped bool
	InputTokens       *int
	OutputTokens      *int
}

// Provider generates one completion. Implementations must honour ctx
// (cancellation and deadline) and return *Error for classified failures.
type Provider interface {
	Name() string
	Complete(ctx context.Context, model string, message Message) (Completion, error)
}

// ErrorKind classifies a provider failure for retry decisions.
type ErrorKind string

const (
	KindAuth          ErrorKind = "auth"           // 401/403: permanent
	KindRejected      ErrorKind = "rejected"       // other 4xx: permanent (bad model, bad request)
	KindModel         ErrorKind = "model"          // 404: model or endpoint not found: permanent
	KindRateLimited   ErrorKind = "rate_limited"   // 429: transient, honours Retry-After
	KindUnavailable   ErrorKind = "unavailable"    // 5xx, 408, connection refused: transient
	KindTimeout       ErrorKind = "timeout"        // request deadline: transient
	KindRedirect      ErrorKind = "redirect"       // 3xx: permanent; never followed
	KindTooLarge      ErrorKind = "too_large"      // response over the size limit: permanent
	KindInvalidOutput ErrorKind = "invalid_output" // malformed envelope or unusable text: transient (bounded by job attempts)
	KindConfig        ErrorKind = "config"         // missing credential/model/endpoint: permanent
)

// Error is a classified provider failure. Detail is bounded and sanitized
// (credentials redacted, control characters removed) and is for operators
// only; it never reaches API clients.
type Error struct {
	Kind       ErrorKind
	Status     int
	RetryAfter time.Duration
	Detail     string
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("summary provider %s (HTTP %d): %s", e.Kind, e.Status, e.Detail)
	}
	return fmt.Sprintf("summary provider %s: %s", e.Kind, e.Detail)
}

// Retryable reports whether a later attempt may succeed.
func (e *Error) Retryable() bool {
	switch e.Kind {
	case KindRateLimited, KindUnavailable, KindTimeout, KindInvalidOutput:
		return true
	}
	return false
}

func invalidOutput(detail string) error { return &Error{Kind: KindInvalidOutput, Detail: detail} }

const (
	// RequestTimeout matches the desktop client (300 s per request).
	RequestTimeout = 300 * time.Second
	// MaxResponseBytes bounds a provider response body.
	MaxResponseBytes = 4 << 20
	// maxErrorBodyBytes bounds how much of an error body is read.
	maxErrorBodyBytes = 4096
	// maxDetailChars bounds the sanitized detail kept for operators.
	maxDetailChars = 300
	maxRetryAfter  = time.Hour
)

// NewHTTPClient returns the client used for provider calls: redirects are
// never followed (a redirect could carry credentials to another host), and
// per-request deadlines come from the request context.
func NewHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = RequestTimeout
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// sanitize bounds text and removes credentials and control characters so a
// provider error body is safe to keep in operator logs and internal errors.
func sanitize(text string, secrets ...string) string {
	for _, secret := range secrets {
		if len(secret) >= 4 {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	text = bearerPattern.ReplaceAllString(text, "Bearer [redacted]")
	text = keyPattern.ReplaceAllString(text, "[redacted]")
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > maxDetailChars {
		text = string(runes[:maxDetailChars]) + "…"
	}
	return text
}

// parseRetryAfter reads delta-seconds or an HTTP date, capped at an hour.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	var delay time.Duration
	if seconds, err := strconv.Atoi(value); err == nil {
		delay = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(value); err == nil {
		delay = at.Sub(now)
	}
	return min(max(delay, 0), maxRetryAfter)
}

// postJSON sends body to url with headers and decodes a successful response
// into out. Failures are classified; secrets are redacted from any detail.
func postJSON(ctx context.Context, client *http.Client, url string, headers http.Header, body any, out any, secrets ...string) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return &Error{Kind: KindConfig, Detail: "encode request"}
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return &Error{Kind: KindConfig, Detail: "invalid provider endpoint"}
	}
	for name, values := range headers {
		request.Header[name] = values
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		if parent := context.Cause(ctx); errors.Is(parent, context.Canceled) {
			return err // cancelled by the caller: not a provider failure
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return &Error{Kind: KindTimeout, Detail: "request timed out"}
		}
		// Transport errors can include the URL but never headers.
		return &Error{Kind: KindUnavailable, Detail: sanitize(err.Error(), secrets...)}
	}
	defer response.Body.Close()
	status := response.StatusCode
	if status >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		detail := sanitize(string(raw), secrets...)
		switch {
		case status < 400:
			return &Error{Kind: KindRedirect, Status: status, Detail: "provider redirected; redirects are not followed"}
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return &Error{Kind: KindAuth, Status: status, Detail: detail}
		case status == http.StatusTooManyRequests:
			return &Error{Kind: KindRateLimited, Status: status, Detail: detail, RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now())}
		case status == http.StatusRequestTimeout || status >= 500:
			return &Error{Kind: KindUnavailable, Status: status, Detail: detail, RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now())}
		case status == http.StatusNotFound:
			return &Error{Kind: KindModel, Status: status, Detail: detail}
		default:
			return &Error{Kind: KindRejected, Status: status, Detail: detail}
		}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil && errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			return &Error{Kind: KindTimeout, Detail: "response timed out"}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &Error{Kind: KindUnavailable, Detail: "reading response failed"}
	}
	if len(raw) > MaxResponseBytes {
		return &Error{Kind: KindTooLarge, Status: status, Detail: fmt.Sprintf("response exceeded %d bytes", MaxResponseBytes)}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &Error{Kind: KindInvalidOutput, Status: status, Detail: "response is not valid JSON"}
	}
	return nil
}
