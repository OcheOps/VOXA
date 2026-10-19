package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// CORS for browser clients (Voxa Web). Only exact, configured origins are
// allowed; there is no wildcard. Authentication uses bearer tokens, so
// credentialed CORS (cookies) is never enabled. With no origins configured
// the middleware is a no-op and cross-origin browser access stays denied.

const (
	corsAllowedMethods = "GET, POST, PATCH, PUT, DELETE"
	corsAllowedHeaders = "Authorization, Content-Type, X-Request-ID"
	corsExposedHeaders = "X-Request-ID, WWW-Authenticate"
	corsMaxAgeSeconds  = "600"
)

var corsMethods = map[string]bool{"GET": true, "POST": true, "PATCH": true, "PUT": true, "DELETE": true}
var corsHeaders = map[string]bool{"authorization": true, "content-type": true, "x-request-id": true}

type CORSPolicy struct {
	origins map[string]bool
}

// ParseAllowedOrigins parses a comma-separated list of exact origins such as
// "https://voxa.example.com,http://localhost:3118".
func ParseAllowedOrigins(value string) (CORSPolicy, error) {
	policy := CORSPolicy{origins: map[string]bool{}}
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		origin, err := normalizeOrigin(entry)
		if err != nil {
			return CORSPolicy{}, err
		}
		policy.origins[origin] = true
	}
	return policy, nil
}

func normalizeOrigin(value string) (string, error) {
	invalid := errors.New("allowed origin must be an exact scheme://host[:port] (no wildcard, path, query or credentials)")
	if value == "*" || strings.EqualFold(value, "null") {
		return "", invalid
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || strings.Contains(parsed.Host, "*") {
		return "", invalid
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", invalid
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port == "" || (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		return scheme + "://" + bracketIPv6(host), nil
	}
	return scheme + "://" + bracketIPv6(host) + ":" + port, nil
}

func bracketIPv6(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func (p CORSPolicy) Enabled() bool { return len(p.origins) > 0 }

// Origins returns the configured origins (not secret; safe to log).
func (p CORSPolicy) Origins() []string {
	origins := make([]string, 0, len(p.origins))
	for origin := range p.origins {
		origins = append(origins, origin)
	}
	return origins
}

func (p CORSPolicy) allows(origin string) bool {
	normalized, err := normalizeOrigin(origin)
	return err == nil && p.origins[normalized]
}

// WithCORS answers preflight requests before authentication and adds CORS
// response headers for allowed origins, including on error responses so the
// browser client can read 401/403 bodies.
func WithCORS(policy CORSPolicy, next http.Handler) http.Handler {
	if !policy.Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		allowed := origin != "" && policy.allows(origin)

		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			header.Add("Vary", "Access-Control-Request-Method")
			header.Add("Vary", "Access-Control-Request-Headers")
			header.Set("X-Content-Type-Options", "nosniff")
			if !allowed || !corsMethods[r.Header.Get("Access-Control-Request-Method")] || !allowedRequestHeaders(r.Header.Values("Access-Control-Request-Headers")) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			header.Set("Access-Control-Allow-Origin", origin)
			header.Set("Access-Control-Allow-Methods", corsAllowedMethods)
			header.Set("Access-Control-Allow-Headers", corsAllowedHeaders)
			header.Set("Access-Control-Max-Age", corsMaxAgeSeconds)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if allowed {
			header.Set("Access-Control-Allow-Origin", origin)
			header.Set("Access-Control-Expose-Headers", corsExposedHeaders)
		}
		next.ServeHTTP(w, r)
	})
}

func allowedRequestHeaders(values []string) bool {
	for _, value := range values {
		for _, name := range strings.Split(value, ",") {
			name = strings.ToLower(strings.TrimSpace(name))
			if name != "" && !corsHeaders[name] {
				return false
			}
		}
	}
	return true
}
