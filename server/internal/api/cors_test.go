package api_test

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/OcheOps/voxa/server/internal/api"
)

func TestParseAllowedOrigins(t *testing.T) {
	policy, err := api.ParseAllowedOrigins(" https://Voxa.Example.com ,http://localhost:3118, https://app.example.com:443/ ,,")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(sortedStrings(policy.Origins()), " ")
	if got != "http://localhost:3118 https://app.example.com https://voxa.example.com" {
		t.Fatalf("origins = %s", got)
	}
	if empty, err := api.ParseAllowedOrigins(""); err != nil || empty.Enabled() {
		t.Fatalf("empty policy enabled=%v err=%v", empty.Enabled(), err)
	}
	for _, bad := range []string{"*", "null", "https://*.example.com", "https://example.com/app", "https://example.com?x=1", "https://u:p@example.com", "ftp://example.com", "example.com", "https://ok.example.com,*"} {
		if _, err := api.ParseAllowedOrigins(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func sortedStrings(values []string) []string {
	sort.Strings(values)
	return values
}

func corsHandler(t *testing.T, origins string) http.Handler {
	t.Helper()
	policy, err := api.ParseAllowedOrigins(origins)
	if err != nil {
		t.Fatal(err)
	}
	return api.WithCORS(policy, newOIDCFixture(t).handler)
}

func preflight(handler http.Handler, origin, method, headers string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodOptions, "/api/v1/meetings", nil)
	r.Header.Set("Origin", origin)
	r.Header.Set("Access-Control-Request-Method", method)
	if headers != "" {
		r.Header.Set("Access-Control-Request-Headers", headers)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, r)
	return recorder
}

func TestCORSPreflight(t *testing.T) {
	handler := corsHandler(t, "http://localhost:3118")
	ok := preflight(handler, "http://localhost:3118", "POST", "authorization, content-type")
	if ok.Code != http.StatusNoContent ||
		ok.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3118" ||
		!strings.Contains(ok.Header().Get("Access-Control-Allow-Headers"), "Authorization") ||
		ok.Header().Get("Access-Control-Allow-Methods") != "GET, POST, PATCH, PUT, DELETE" ||
		ok.Header().Get("Access-Control-Allow-Credentials") != "" ||
		!strings.Contains(strings.Join(ok.Header().Values("Vary"), ","), "Origin") {
		t.Fatalf("allowed preflight: %d %v", ok.Code, ok.Header())
	}
	for name, response := range map[string]*httptest.ResponseRecorder{
		"unknown origin":    preflight(handler, "https://evil.example", "GET", ""),
		"origin prefix":     preflight(handler, "http://localhost:31180", "GET", ""),
		"disallowed method": preflight(handler, "http://localhost:3118", "TRACE", ""),
		"disallowed header": preflight(handler, "http://localhost:3118", "GET", "authorization, x-evil"),
	} {
		if response.Code != http.StatusForbidden || response.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("%s: %d %v", name, response.Code, response.Header())
		}
	}
}

func TestCORSActualRequests(t *testing.T) {
	handler := corsHandler(t, "http://localhost:3118")
	request := func(origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/meetings", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		return recorder
	}
	allowed := request("http://localhost:3118")
	// The 401 is readable by the allowed browser origin, with request id and challenge exposed.
	if allowed.Code != http.StatusUnauthorized || allowed.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3118" ||
		!strings.Contains(allowed.Header().Get("Access-Control-Expose-Headers"), "X-Request-ID") ||
		allowed.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("allowed origin: %d %v", allowed.Code, allowed.Header())
	}
	if denied := request("https://evil.example"); denied.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("disallowed origin received CORS headers: %v", denied.Header())
	}
	if none := request(""); none.Header().Get("Access-Control-Allow-Origin") != "" || !strings.Contains(strings.Join(none.Header().Values("Vary"), ","), "Origin") {
		t.Fatalf("same-origin/non-browser request headers: %v", none.Header())
	}
}

func TestCORSDisabledByDefault(t *testing.T) {
	handler := corsHandler(t, "")
	response := preflight(handler, "http://localhost:3118", "GET", "authorization")
	if response.Header().Get("Access-Control-Allow-Origin") != "" || response.Code == http.StatusNoContent {
		t.Fatalf("CORS active without configuration: %d %v", response.Code, response.Header())
	}
}
