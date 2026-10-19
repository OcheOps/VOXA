package postgres_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OcheOps/voxa/server/internal/adapters/postgres"
	"github.com/OcheOps/voxa/server/internal/api"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/repository/repositorytest"
)

// serverOn builds a full HTTP handler over PostgreSQL, as startup would.
func serverOn(t *testing.T, pool *pgxpool.Pool, key []byte, logs *bytes.Buffer) http.Handler {
	t.Helper()
	credentials := postgres.NewCredentialRepository(pool, credentialCipher(t, "v1", map[string][]byte{"v1": key}))
	return api.NewHandler(
		application.NewMeetingService(postgres.NewMeetingRepository(pool)),
		application.NewConfigurationService(postgres.NewConfigurationRepository(pool, application.DefaultConfiguration()), credentials),
		application.NewCredentialService(credentials),
		repositorytest.WorkspaceA, "test", slog.New(slog.NewJSONHandler(logs, nil)),
	)
}

func call(handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	handler.ServeHTTP(recorder, r)
	return recorder
}

func TestCredentialHTTPLifecycleSurvivesRestartWithoutLeaking(t *testing.T) {
	pool := migratedPool(t)
	key := testKey(t)
	var logs bytes.Buffer
	var bodies []string

	first := serverOn(t, pool, key, &logs)
	put := call(first, http.MethodPut, "/api/v1/credentials/openai", `{"value":"`+repositorytest.FakeSecret+`"}`)
	if put.Code != http.StatusOK || !strings.Contains(put.Body.String(), `"configured":true`) {
		t.Fatalf("put status=%d body=%s", put.Code, put.Body.String())
	}
	bodies = append(bodies, put.Body.String())

	restarted := serverOn(t, pool, key, &logs)
	for _, target := range []string{"/api/v1/credentials", "/api/v1/configuration"} {
		response := call(restarted, http.MethodGet, target, "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"provider":"openai","configured":true,"maskedValue":"••••"`) {
			t.Fatalf("GET %s after restart status=%d body=%s", target, response.Code, response.Body.String())
		}
		bodies = append(bodies, response.Body.String())
	}

	if response := call(restarted, http.MethodDelete, "/api/v1/credentials/openai", ""); response.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d", response.Code)
	}
	list := call(restarted, http.MethodGet, "/api/v1/credentials", "")
	if !strings.Contains(list.Body.String(), `"credentials":[]`) {
		t.Fatalf("list after delete = %s", list.Body.String())
	}
	bodies = append(bodies, list.Body.String())

	for _, haystack := range append(bodies, logs.String()) {
		if strings.Contains(haystack, repositorytest.FakeSecret) {
			t.Fatalf("secret leaked: %s", haystack)
		}
	}
}
