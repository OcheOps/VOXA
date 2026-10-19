package api_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OcheOps/voxa/server/internal/adapters/memory"
	"github.com/OcheOps/voxa/server/internal/adapters/postgres"
	"github.com/OcheOps/voxa/server/internal/api"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/auth"
	"github.com/OcheOps/voxa/server/internal/credentialcrypto"
	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

const leakCanary = "sk-VOXA-HTTP-CANARY-7f3a91"

// assertNoLeak fails if any canary appears in the haystacks.
func assertNoLeak(t *testing.T, label string, haystacks ...string) {
	t.Helper()
	for _, haystack := range haystacks {
		for _, canary := range []string{leakCanary, "DEAD-POOL-PASSWORD"} {
			if strings.Contains(haystack, canary) {
				t.Fatalf("%s leaked %q: %s", label, canary, haystack)
			}
		}
	}
}

type logEntry map[string]any

func logEntries(t *testing.T, output *bytes.Buffer) []logEntry {
	t.Helper()
	var entries []logEntry
	for _, line := range bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry logEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func failureEntries(t *testing.T, output *bytes.Buffer) []logEntry {
	var failures []logEntry
	for _, entry := range logEntries(t, output) {
		if entry["msg"] == "request failed" {
			failures = append(failures, entry)
		}
	}
	return failures
}

// outageHandler wires PostgreSQL adapters to a pool that can never connect,
// simulating a database outage without needing a database.
func outageHandler(t *testing.T, logs *bytes.Buffer) http.Handler {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	config, err := pgxpool.ParseConfig("postgres://voxa:DEAD-POOL-PASSWORD@" + address + "/voxa?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	key := make([]byte, credentialcrypto.KeySize)
	_, _ = rand.Read(key)
	ring, err := credentialcrypto.NewStaticKeyRing("v1", map[string][]byte{"v1": key})
	if err != nil {
		t.Fatal(err)
	}
	credentials := postgres.NewCredentialRepository(pool, credentialcrypto.NewCipher(ring))
	return api.NewHandler(
		application.NewMeetingService(postgres.NewMeetingRepository(pool)),
		application.NewConfigurationService(postgres.NewConfigurationRepository(pool, application.DefaultConfiguration()), credentials),
		application.NewCredentialService(credentials),
		auth.DevelopmentAuthenticator{}, application.NewDevelopmentResolver("workspace-test"), "test", slog.New(slog.NewJSONHandler(logs, nil)),
	)
}

func TestDatabaseOutageMapsToUnavailableWithoutLeaking(t *testing.T) {
	var logs bytes.Buffer
	handler := outageHandler(t, &logs)

	responses := map[string]string{}
	for _, call := range []struct{ method, target, body string }{
		{http.MethodPut, "/api/v1/credentials/openai", `{"value":"` + leakCanary + `"}`},
		{http.MethodGet, "/api/v1/credentials", ""},
		{http.MethodGet, "/api/v1/meetings", ""},
		{http.MethodGet, "/api/v1/configuration", ""},
	} {
		response := request(t, handler, call.method, call.target, call.body, nil)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s status=%d body=%s", call.method, call.target, response.Code, response.Body.String())
		}
		body := decode(t, response)["error"].(map[string]any)
		if body["code"] != "unavailable" || body["message"] != "Service unavailable" {
			t.Fatalf("%s %s error body = %#v", call.method, call.target, body)
		}
		responses[call.method+" "+call.target] = response.Body.String()
		assertNoLeak(t, "response body", response.Body.String())
	}

	failures := failureEntries(t, &logs)
	if len(failures) != 4 {
		t.Fatalf("logged %d failures, want 4: %s", len(failures), logs.String())
	}
	put := failures[0]
	want := map[string]any{"level": "WARN", "code": "unavailable", "category": "storage_unavailable", "operation": "credentials.set",
		"route": "PUT /api/v1/credentials/{provider}", "provider": "openai", "workspace_id": "workspace-test", "method": "PUT"}
	for key, value := range want {
		if put[key] != value {
			t.Fatalf("failure log %s = %v, want %v (entry %v)", key, put[key], value, put)
		}
	}
	if put["request_id"] == "" || put["error"] == nil {
		t.Fatalf("failure log missing request_id/error: %v", put)
	}
	for _, field := range []string{"user=", "voxa?sslmode"} {
		if strings.Contains(logs.String(), field) {
			t.Fatalf("log leaked connection detail %q: %s", field, logs.String())
		}
	}
	assertNoLeak(t, "logs", logs.String())
}

func TestClientErrorsDuringOutageAreNotUnavailable(t *testing.T) {
	var logs bytes.Buffer
	handler := outageHandler(t, &logs)
	cases := []struct{ method, target, body string }{
		{http.MethodGet, "/api/v1/meetings?cursor=not-a-cursor", ""},
		{http.MethodGet, "/api/v1/meetings?limit=1000", ""},
		{http.MethodPut, "/api/v1/credentials/bad%20provider", `{"value":"` + leakCanary + `"}`},
		{http.MethodPost, "/api/v1/meetings", `{"title":"   "}`},
	}
	for _, call := range cases {
		response := request(t, handler, call.method, call.target, call.body, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s %s status=%d, want 400", call.method, call.target, response.Code)
		}
		assertNoLeak(t, "response body", response.Body.String())
	}
	if failures := failureEntries(t, &logs); len(failures) != 0 {
		t.Fatalf("client errors logged as failures: %v", failures)
	}
}

func TestMalformedCredentialRequestsAreValidationErrors(t *testing.T) {
	var logs bytes.Buffer
	credentials := memory.NewCredentialRepository()
	handler := api.NewHandler(application.NewMeetingService(memory.NewMeetingRepository()),
		application.NewConfigurationService(memory.NewConfigurationRepository(application.DefaultConfiguration()), credentials),
		application.NewCredentialService(credentials), auth.DevelopmentAuthenticator{}, application.NewDevelopmentResolver("workspace-test"), "test", slog.New(slog.NewJSONHandler(&logs, nil)))

	for name, body := range map[string]string{
		"number value":  `{"value":12345}`,
		"null value":    `{"value":null}`,
		"missing value": `{}`,
		"blank value":   `{"value":"   "}`,
		"unknown field": `{"value":"` + leakCanary + `","echo":"` + leakCanary + `"}`,
		"trailing data": `{"value":"` + leakCanary + `"} {"value":"` + leakCanary + `"}`,
		"not json":      `value=` + leakCanary,
	} {
		response := request(t, handler, http.MethodPut, "/api/v1/credentials/openai", body, nil)
		if response.Code != http.StatusBadRequest || decode(t, response)["error"].(map[string]any)["code"] != "validation" {
			t.Fatalf("%s: status=%d body=%s", name, response.Code, response.Body.String())
		}
		assertNoLeak(t, name+" error body", response.Body.String())
	}
	list := request(t, handler, http.MethodGet, "/api/v1/credentials", "", nil)
	if got := decode(t, list)["credentials"].([]any); len(got) != 0 {
		t.Fatalf("malformed requests stored credentials: %v", got)
	}
	assertNoLeak(t, "logs", logs.String())
}

type failingCredentials struct{ *memory.CredentialRepository }

func (failingCredentials) Set(context.Context, domain.WorkspaceID, string, domain.CredentialSecret) error {
	return errors.New("adapter exploded")
}

func TestInternalErrorsAreLoggedWithContextButNotSecrets(t *testing.T) {
	var logs bytes.Buffer
	credentials := failingCredentials{memory.NewCredentialRepository()}
	handler := api.NewHandler(application.NewMeetingService(memory.NewMeetingRepository()),
		application.NewConfigurationService(memory.NewConfigurationRepository(application.DefaultConfiguration()), credentials),
		application.NewCredentialService(credentials), auth.DevelopmentAuthenticator{}, application.NewDevelopmentResolver("workspace-test"), "test", slog.New(slog.NewJSONHandler(&logs, nil)))

	response := request(t, handler, http.MethodPut, "/api/v1/credentials/openai", `{"value":"`+leakCanary+`"}`, map[string]string{"Authorization": "Bearer " + leakCanary, "X-Request-ID": "req-internal-1"})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", response.Code)
	}
	body := decode(t, response)["error"].(map[string]any)
	if body["code"] != "internal" || body["message"] != "Internal server error" || body["requestId"] != "req-internal-1" {
		t.Fatalf("error body = %#v", body)
	}
	if strings.Contains(response.Body.String(), "exploded") {
		t.Fatal("internal error detail reached the client")
	}
	failures := failureEntries(t, &logs)
	if len(failures) != 1 {
		t.Fatalf("failures = %v", failures)
	}
	entry := failures[0]
	if entry["level"] != "ERROR" || entry["request_id"] != "req-internal-1" || entry["operation"] != "credentials.set" ||
		entry["category"] != "internal" || entry["error"] != "adapter exploded" || entry["provider"] != "openai" {
		t.Fatalf("failure entry = %v", entry)
	}
	assertNoLeak(t, "logs", logs.String())
}

func TestUnreadableCredentialCategory(t *testing.T) {
	// The decrypt path is internal-only today; this guards the category and
	// the structured metadata it would log.
	err := &repository.CredentialUnreadableError{WorkspaceID: "workspace-test", Provider: "openai", KeyVersion: "v9", Reason: credentialcrypto.ErrUnknownKeyVersion}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Error("request failed", "error", err)
	for _, field := range []string{`"key_version":"v9"`, `"provider":"openai"`, `"workspace_id":"workspace-test"`} {
		if !strings.Contains(logs.String(), field) {
			t.Fatalf("log missing %s: %s", field, logs.String())
		}
	}
}
