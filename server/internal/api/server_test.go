package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OcheOps/voxa/server/internal/adapters/memory"
	"github.com/OcheOps/voxa/server/internal/api"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/domain"
)

func request(t *testing.T, handler http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	handler.ServeHTTP(recorder, r)
	return recorder
}

func decode(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return result
}

func newTestHandler(workspaceID string) http.Handler {
	meetingRepository := memory.NewMeetingRepository()
	credentialRepository := memory.NewCredentialRepository()
	configurationRepository := memory.NewConfigurationRepository(application.DefaultConfiguration())
	return api.NewHandler(
		application.NewMeetingService(meetingRepository),
		application.NewConfigurationService(configurationRepository, credentialRepository),
		application.NewCredentialService(credentialRepository),
		domain.WorkspaceID(workspaceID),
		"dev",
		nil,
	)
}

func TestHealthAndCapabilitiesContracts(t *testing.T) {
	handler := newTestHandler("workspace-test")
	health := request(t, handler, http.MethodGet, "/api/v1/health", "", nil)
	if health.Code != http.StatusOK {
		t.Fatalf("health status=%d", health.Code)
	}
	body := decode(t, health)
	if body["status"] != "ok" || body["apiVersion"] != "v1" || body["serverVersion"] != "dev" {
		t.Fatalf("health shape=%#v", body)
	}
	if health.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing request ID")
	}
	capBody := decode(t, request(t, handler, http.MethodGet, "/api/v1/capabilities", "", nil))
	if capBody["apiVersion"] != "v1" {
		t.Fatalf("capabilities shape=%#v", capBody)
	}
	for _, key := range []string{"mediaStorage", "transcription", "summary", "configuration"} {
		if _, ok := capBody[key]; !ok {
			t.Fatalf("missing %s", key)
		}
	}
	if capBody["mediaStorage"].(map[string]any)["available"] != false {
		t.Fatal("media storage must be unavailable")
	}
}

func TestProtocolWireShapesAreExact(t *testing.T) {
	handler := newTestHandler("workspace-test")
	created := decode(t, request(t, handler, http.MethodPost, "/api/v1/meetings", `{"title":"Contract"}`, nil))
	assertKeys(t, created, "meeting")
	meeting := created["meeting"].(map[string]any)
	assertKeys(t, meeting, "id", "workspaceId", "title", "status", "createdAt", "updatedAt")

	listed := decode(t, request(t, handler, http.MethodGet, "/api/v1/meetings", "", nil))
	assertKeys(t, listed, "items", "page")
	assertKeys(t, listed["page"].(map[string]any), "nextCursor", "hasMore")

	capabilities := decode(t, request(t, handler, http.MethodGet, "/api/v1/capabilities", "", nil))
	assertKeys(t, capabilities, "apiVersion", "mediaStorage", "transcription", "summary", "configuration")
	assertKeys(t, capabilities["mediaStorage"].(map[string]any), "available")
	assertKeys(t, capabilities["transcription"].(map[string]any), "available", "providers")
	assertKeys(t, capabilities["summary"].(map[string]any), "available", "providers")
	assertKeys(t, capabilities["configuration"].(map[string]any), "serverManaged", "credentialManagement")

	configuration := decode(t, request(t, handler, http.MethodGet, "/api/v1/configuration", "", nil))
	assertKeys(t, configuration, "workspaceId", "transcription", "summary", "recording", "privacy", "notifications", "credentials")
	assertKeys(t, configuration["transcription"].(map[string]any), "provider", "model", "language", "translateToEnglish")
	assertKeys(t, configuration["summary"].(map[string]any), "provider", "model", "outputLanguage", "templateId", "autoGenerate")
	assertKeys(t, configuration["recording"].(map[string]any), "retainMedia")
	assertKeys(t, configuration["privacy"].(map[string]any), "analyticsEnabled")
	assertKeys(t, configuration["notifications"].(map[string]any), "processingComplete")

	credential := decode(t, request(t, handler, http.MethodPut, "/api/v1/credentials/openai", `{"value":"secret"}`, nil))["credential"].(map[string]any)
	assertKeys(t, credential, "provider", "configured", "maskedValue")
	credentialList := decode(t, request(t, handler, http.MethodGet, "/api/v1/credentials", "", nil))
	assertKeys(t, credentialList, "credentials")
	assertKeys(t, credentialList["credentials"].([]any)[0].(map[string]any), "provider", "configured", "maskedValue")

	validation := decode(t, request(t, handler, http.MethodPost, "/api/v1/meetings", `{"title":""}`, nil))["error"].(map[string]any)
	assertKeys(t, validation, "code", "message", "details", "requestId")
}

func TestMeetingCRUDAndContract(t *testing.T) {
	handler := newTestHandler("workspace-test")
	created := request(t, handler, http.MethodPost, "/api/v1/meetings", `{"title":"Planning"}`, nil)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	meeting := decode(t, created)["meeting"].(map[string]any)
	for _, key := range []string{"id", "workspaceId", "title", "status", "createdAt", "updatedAt"} {
		if _, ok := meeting[key]; !ok {
			t.Fatalf("missing meeting field %s", key)
		}
	}
	if meeting["workspaceId"] != "workspace-test" || meeting["status"] != "created" {
		t.Fatalf("meeting=%#v", meeting)
	}
	id := meeting["id"].(string)
	listed := decode(t, request(t, handler, http.MethodGet, "/api/v1/meetings", "", nil))
	if len(listed["items"].([]any)) != 1 || listed["page"].(map[string]any)["nextCursor"] != nil {
		t.Fatalf("list=%#v", listed)
	}
	got := decode(t, request(t, handler, http.MethodGet, "/api/v1/meetings/"+id, "", nil))["meeting"].(map[string]any)
	if got["title"] != "Planning" {
		t.Fatalf("get=%#v", got)
	}
	updated := decode(t, request(t, handler, http.MethodPatch, "/api/v1/meetings/"+id, `{"title":"Retrospective"}`, nil))["meeting"].(map[string]any)
	if updated["title"] != "Retrospective" {
		t.Fatalf("update=%#v", updated)
	}
	deleted := request(t, handler, http.MethodDelete, "/api/v1/meetings/"+id, "", nil)
	if deleted.Code != http.StatusNoContent || deleted.Body.Len() != 0 {
		t.Fatalf("delete status=%d body=%q", deleted.Code, deleted.Body.String())
	}
	assertAPIError(t, request(t, handler, http.MethodGet, "/api/v1/meetings/"+id, "", nil), http.StatusNotFound, "not_found")
}

func TestValidationAndMalformedJSON(t *testing.T) {
	handler := newTestHandler("workspace-test")
	for name, body := range map[string]string{"malformed": `{"title":`, "unknown": `{"title":"ok","path":"/tmp/private"}`, "blank": `{"title":"  "}`} {
		t.Run(name, func(t *testing.T) {
			assertAPIError(t, request(t, handler, http.MethodPost, "/api/v1/meetings", body, nil), http.StatusBadRequest, "validation")
		})
	}
	assertAPIError(t, request(t, handler, http.MethodGet, "/api/v1/meetings?limit=nope", "", nil), http.StatusBadRequest, "validation")
}

func TestConfigurationGetPatchAndEnumValidation(t *testing.T) {
	handler := newTestHandler("workspace-test")
	initial := decode(t, request(t, handler, http.MethodGet, "/api/v1/configuration", "", nil))
	if initial["workspaceId"] != "workspace-test" || initial["credentials"] == nil {
		t.Fatalf("configuration=%#v", initial)
	}
	patched := request(t, handler, http.MethodPatch, "/api/v1/configuration", `{"transcription":{"provider":"whisper","language":"en","translateToEnglish":true},"summary":{"provider":"ollama","autoGenerate":true},"recording":{"retainMedia":false}}`, nil)
	if patched.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patched.Code, patched.Body.String())
	}
	configuration := decode(t, patched)["configuration"].(map[string]any)
	if configuration["transcription"].(map[string]any)["provider"] != "whisper" || configuration["recording"].(map[string]any)["retainMedia"] != false {
		t.Fatalf("configuration=%#v", configuration)
	}
	assertAPIError(t, request(t, handler, http.MethodPatch, "/api/v1/configuration", `{"summary":{"provider":"not valid"}}`, nil), http.StatusBadRequest, "validation")
	assertAPIError(t, request(t, handler, http.MethodPatch, "/api/v1/configuration", `{"recordingsFolder":"/tmp"}`, nil), http.StatusBadRequest, "validation")
	for _, body := range []string{`{"transcription":{"provider":null}}`, `{"summary":{"autoGenerate":null}}`, `{"recording":{"retainMedia":null}}`} {
		assertAPIError(t, request(t, handler, http.MethodPatch, "/api/v1/configuration", body, nil), http.StatusBadRequest, "validation")
	}
	request(t, handler, http.MethodPatch, "/api/v1/configuration", `{"summary":{"templateId":"daily"}}`, nil)
	cleared := decode(t, request(t, handler, http.MethodPatch, "/api/v1/configuration", `{"summary":{"templateId":null}}`, nil))["configuration"].(map[string]any)
	if cleared["summary"].(map[string]any)["templateId"] != nil {
		t.Fatalf("templateId was not cleared: %#v", cleared)
	}
}

func TestCredentialLifecycleNeverExposesSecret(t *testing.T) {
	meetingRepository := memory.NewMeetingRepository()
	credentialRepository := memory.NewCredentialRepository()
	configurationRepository := memory.NewConfigurationRepository(application.DefaultConfiguration())
	var logOutput bytes.Buffer
	handler := api.NewHandler(application.NewMeetingService(meetingRepository), application.NewConfigurationService(configurationRepository, credentialRepository), application.NewCredentialService(credentialRepository), "workspace-test", "test", slog.New(slog.NewJSONHandler(&logOutput, nil)))
	secret := "sk-super-private"
	set := request(t, handler, http.MethodPut, "/api/v1/credentials/openai", `{"value":"`+secret+`"}`, nil)
	if set.Code != http.StatusOK || bytes.Contains(set.Body.Bytes(), []byte(secret)) {
		t.Fatalf("set response leaked secret: %s", set.Body.String())
	}
	setBody := decode(t, set)["credential"].(map[string]any)
	if setBody["configured"] != true || setBody["maskedValue"] != "••••" {
		t.Fatalf("credential=%#v", setBody)
	}
	list := request(t, handler, http.MethodGet, "/api/v1/credentials", "", nil)
	if bytes.Contains(list.Body.Bytes(), []byte(secret)) {
		t.Fatalf("list response leaked secret: %s", list.Body.String())
	}
	errorResponse := request(t, handler, http.MethodPut, "/api/v1/credentials/BAD%20PROVIDER", `{"value":"`+secret+`"}`, nil)
	if bytes.Contains(errorResponse.Body.Bytes(), []byte(secret)) {
		t.Fatalf("error leaked secret: %s", errorResponse.Body.String())
	}
	assertAPIError(t, errorResponse, http.StatusBadRequest, "validation")
	if deleted := request(t, handler, http.MethodDelete, "/api/v1/credentials/openai", "", nil); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete=%d", deleted.Code)
	}
	if got := decode(t, request(t, handler, http.MethodGet, "/api/v1/credentials", "", nil))["credentials"].([]any); len(got) != 0 {
		t.Fatalf("credentials=%#v", got)
	}
	if strings.Contains(logOutput.String(), secret) {
		t.Fatalf("access log leaked credential: %s", logOutput.String())
	}
}

func TestBodySizeLimit(t *testing.T) {
	handler := newTestHandler("workspace-test")
	oversized := `{"title":"` + strings.Repeat("x", (1<<20)+1) + `"}`
	assertAPIError(t, request(t, handler, http.MethodPost, "/api/v1/meetings", oversized, nil), http.StatusBadRequest, "validation")
}

func TestJSONContentTypeIsRequired(t *testing.T) {
	handler := newTestHandler("workspace-test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/meetings", strings.NewReader(`{"title":"Planning"}`)))
	assertAPIError(t, recorder, http.StatusBadRequest, "validation")
}

func TestPatchRejectsNullForNonNullableFields(t *testing.T) {
	handler := newTestHandler("workspace-test")
	created := decode(t, request(t, handler, http.MethodPost, "/api/v1/meetings", `{"title":"Planning"}`, nil))["meeting"].(map[string]any)
	assertAPIError(t, request(t, handler, http.MethodPatch, "/api/v1/meetings/"+created["id"].(string), `{"title":null}`, nil), http.StatusBadRequest, "validation")
	assertAPIError(t, request(t, handler, http.MethodPatch, "/api/v1/meetings/"+created["id"].(string), `null`, nil), http.StatusBadRequest, "validation")
	assertAPIError(t, request(t, handler, http.MethodPatch, "/api/v1/configuration", `null`, nil), http.StatusBadRequest, "validation")
	for _, body := range []string{`{"transcription":null}`, `{"summary":null}`, `{"recording":null}`, `{"privacy":null}`, `{"notifications":null}`} {
		assertAPIError(t, request(t, handler, http.MethodPatch, "/api/v1/configuration", body, nil), http.StatusBadRequest, "validation")
	}
}

func TestPaginationAndMalformedCursor(t *testing.T) {
	handler := newTestHandler("workspace-test")
	for _, title := range []string{"A", "B", "C"} {
		result := request(t, handler, http.MethodPost, "/api/v1/meetings", `{"title":"`+title+`"}`, nil)
		if result.Code != http.StatusCreated {
			t.Fatalf("create=%s", result.Body.String())
		}
	}
	first := decode(t, request(t, handler, http.MethodGet, "/api/v1/meetings?limit=2", "", nil))
	page := first["page"].(map[string]any)
	if page["hasMore"] != true || page["nextCursor"] == nil || len(first["items"].([]any)) != 2 {
		t.Fatalf("first page=%#v", first)
	}
	second := decode(t, request(t, handler, http.MethodGet, "/api/v1/meetings?limit=2&cursor="+page["nextCursor"].(string), "", nil))
	if len(second["items"].([]any)) != 1 || second["page"].(map[string]any)["hasMore"] != false {
		t.Fatalf("second page=%#v", second)
	}
	assertAPIError(t, request(t, handler, http.MethodGet, "/api/v1/meetings?cursor=not-valid!", "", nil), http.StatusBadRequest, "validation")
}

func TestWorkspaceIsolation(t *testing.T) {
	meetings := memory.NewMeetingRepository()
	credentials := memory.NewCredentialRepository()
	configurations := memory.NewConfigurationRepository(application.DefaultConfiguration())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	newHandler := func(workspace string) http.Handler {
		return api.NewHandler(application.NewMeetingService(meetings), application.NewConfigurationService(configurations, credentials), application.NewCredentialService(credentials), domain.WorkspaceID(workspace), "test", logger)
	}
	a, b := newHandler("workspace-a"), newHandler("workspace-b")
	created := decode(t, request(t, a, http.MethodPost, "/api/v1/meetings", `{"title":"Private"}`, nil))["meeting"].(map[string]any)
	assertAPIError(t, request(t, b, http.MethodGet, "/api/v1/meetings/"+created["id"].(string), "", nil), http.StatusNotFound, "not_found")
	if items := decode(t, request(t, b, http.MethodGet, "/api/v1/meetings", "", nil))["items"].([]any); len(items) != 0 {
		t.Fatalf("cross-workspace items=%#v", items)
	}
	request(t, a, http.MethodPatch, "/api/v1/configuration", `{"recording":{"retainMedia":false}}`, nil)
	if config := decode(t, request(t, b, http.MethodGet, "/api/v1/configuration", "", nil)); config["recording"].(map[string]any)["retainMedia"] != true {
		t.Fatalf("configuration crossed workspace boundary: %#v", config)
	}
	request(t, a, http.MethodPut, "/api/v1/credentials/openai", `{"value":"workspace-a-secret"}`, nil)
	if statuses := decode(t, request(t, b, http.MethodGet, "/api/v1/credentials", "", nil))["credentials"].([]any); len(statuses) != 0 {
		t.Fatalf("credentials crossed workspace boundary: %#v", statuses)
	}
}

func TestRequestIDBehavior(t *testing.T) {
	handler := newTestHandler("workspace-test")
	trusted := request(t, handler, http.MethodGet, "/api/v1/meetings/missing", "", map[string]string{"X-Request-ID": "client.safe-123"})
	if trusted.Header().Get("X-Request-ID") != "client.safe-123" || decode(t, trusted)["error"].(map[string]any)["requestId"] != "client.safe-123" {
		t.Fatal("safe request ID was not propagated")
	}
	unsafe := request(t, handler, http.MethodGet, "/api/v1/meetings/missing", "", map[string]string{"X-Request-ID": "unsafe value"})
	if generated := unsafe.Header().Get("X-Request-ID"); generated == "" || generated == "unsafe value" {
		t.Fatalf("unsafe request ID accepted: %q", generated)
	}
}

func assertAPIError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status=%d want=%d body=%s", recorder.Code, status, recorder.Body.String())
	}
	apiError := decode(t, recorder)["error"].(map[string]any)
	if apiError["code"] != code || apiError["message"] == "" || apiError["requestId"] == "" {
		t.Fatalf("error shape=%#v", apiError)
	}
}

func assertKeys(t *testing.T, value map[string]any, expected ...string) {
	t.Helper()
	if len(value) != len(expected) {
		t.Fatalf("keys in %#v: got %d, want %d (%v)", value, len(value), len(expected), expected)
	}
	for _, key := range expected {
		if _, ok := value[key]; !ok {
			t.Fatalf("missing key %q in %#v", key, value)
		}
	}
}
