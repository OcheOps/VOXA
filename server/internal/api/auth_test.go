package api_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OcheOps/voxa/server/internal/adapters/memory"
	"github.com/OcheOps/voxa/server/internal/api"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/auth"
	"github.com/OcheOps/voxa/server/internal/auth/authtest"
	"github.com/OcheOps/voxa/server/internal/domain"
)

type oidcFixture struct {
	handler    http.Handler
	issuer     *authtest.Issuer
	identities *memory.IdentityRepository
	logs       *bytes.Buffer
}

func newOIDCFixture(t *testing.T) *oidcFixture {
	t.Helper()
	issuer := authtest.NewIssuer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	authenticator, err := auth.NewOIDCAuthenticator(ctx, auth.OIDCConfig{Issuer: issuer.URL, Audience: authtest.Audience})
	if err != nil {
		t.Fatal(err)
	}
	identities := memory.NewIdentityRepository()
	credentials := memory.NewCredentialRepository()
	var logs bytes.Buffer
	handler := api.NewHandler(
		application.NewMeetingService(memory.NewMeetingRepository()),
		application.NewConfigurationService(memory.NewConfigurationRepository(application.DefaultConfiguration()), credentials),
		application.NewCredentialService(credentials),
		authenticator,
		application.NewMembershipResolver(identities, application.MembershipResolverOptions{}),
		"test", slog.New(slog.NewJSONHandler(&logs, nil)),
	)
	return &oidcFixture{handler: handler, issuer: issuer, identities: identities, logs: &logs}
}

func (f *oidcFixture) do(t *testing.T, method, target, body, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	headers := map[string]string{}
	if authorization != "" {
		headers["Authorization"] = authorization
	}
	return request(t, f.handler, method, target, body, headers)
}

func (f *oidcFixture) bearer(t *testing.T, subject string) string {
	return "Bearer " + f.issuer.TokenFor(t, subject, nil)
}

func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	return decode(t, response)["error"].(map[string]any)["code"].(string)
}

func TestPublicRoutesNeedNoToken(t *testing.T) {
	f := newOIDCFixture(t)
	for _, target := range []string{"/api/v1/health", "/api/v1/capabilities"} {
		if response := f.do(t, http.MethodGet, target, "", ""); response.Code != http.StatusOK {
			t.Fatalf("%s status=%d", target, response.Code)
		}
	}
}

func TestProtectedRoutesRejectMissingAndInvalidTokens(t *testing.T) {
	f := newOIDCFixture(t)
	other := authtest.NewIssuer(t)
	claims := func(overrides map[string]any) map[string]any {
		c := f.issuer.Claims("alice")
		for key, value := range overrides {
			c[key] = value
		}
		return c
	}
	cases := map[string]string{
		"missing":        "",
		"basic":          "Basic YWxpY2U6cHc=",
		"malformed":      "Bearer",
		"invalid":        "Bearer not.a.token",
		"expired":        "Bearer " + f.issuer.Token(t, claims(map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})),
		"wrong issuer":   "Bearer " + f.issuer.Token(t, claims(map[string]any{"iss": other.URL})),
		"wrong audience": "Bearer " + f.issuer.Token(t, claims(map[string]any{"aud": "another-app"})),
		"forged":         "Bearer " + authtest.Sign(t, authtest.ForeignKey(t), "key-1", claims(nil)),
	}
	protected := []struct{ method, target, body string }{
		{http.MethodGet, "/api/v1/me", ""},
		{http.MethodGet, "/api/v1/meetings", ""},
		{http.MethodPost, "/api/v1/meetings", `{"title":"x"}`},
		{http.MethodGet, "/api/v1/meetings/mtg_1", ""},
		{http.MethodGet, "/api/v1/configuration", ""},
		{http.MethodPatch, "/api/v1/configuration", `{"privacy":{"analyticsEnabled":true}}`},
		{http.MethodGet, "/api/v1/credentials", ""},
		{http.MethodPut, "/api/v1/credentials/openai", `{"value":"` + leakCanary + `"}`},
		{http.MethodDelete, "/api/v1/credentials/openai", ""},
	}
	for name, authorization := range cases {
		for _, route := range protected {
			response := f.do(t, route.method, route.target, route.body, authorization)
			if response.Code != http.StatusUnauthorized || errorCode(t, response) != "unauthorized" {
				t.Fatalf("%s %s %s: status=%d body=%s", name, route.method, route.target, response.Code, response.Body.String())
			}
			if response.Header().Get("WWW-Authenticate") != `Bearer realm="voxa"` {
				t.Fatalf("%s: missing WWW-Authenticate", name)
			}
			if details := decode(t, response)["error"].(map[string]any)["details"]; details != nil {
				t.Fatalf("%s: 401 exposed details %v", name, details)
			}
		}
	}
	// Nothing was bootstrapped by rejected requests.
	if initialized, _ := f.identities.IsInitialized(context.Background()); initialized {
		t.Fatal("rejected token initialized the deployment")
	}
	logs := f.logs.String()
	for name, authorization := range cases {
		token := strings.TrimPrefix(authorization, "Bearer ")
		if len(token) > 10 && strings.Contains(logs, token) {
			t.Fatalf("%s: token appeared in logs", name)
		}
	}
	for _, category := range []string{"missing_bearer", "malformed_authorization", "malformed_token", "expired", "issuer_mismatch", "audience_mismatch", "signature_invalid"} {
		if !strings.Contains(logs, `"category":"`+category+`"`) {
			t.Fatalf("category %s not logged", category)
		}
	}
	assertNoLeak(t, "logs", logs)
}

func TestBootstrapThenUnknownIdentityIsForbidden(t *testing.T) {
	f := newOIDCFixture(t)
	alice := f.bearer(t, "alice")

	me := f.do(t, http.MethodGet, "/api/v1/me", "", alice)
	if me.Code != http.StatusOK {
		t.Fatalf("me status=%d body=%s", me.Code, me.Body.String())
	}
	body := decode(t, me)
	assertKeys(t, body, "user", "workspace", "role")
	assertKeys(t, body["user"].(map[string]any), "id", "email", "displayName")
	assertKeys(t, body["workspace"].(map[string]any), "id", "name")
	if body["role"] != "owner" || body["workspace"].(map[string]any)["name"] != application.DefaultWorkspaceName {
		t.Fatalf("me = %v", body)
	}
	for _, forbidden := range []string{"issuer", "subject", "token", "claims", "aud"} {
		if strings.Contains(me.Body.String(), `"`+forbidden) {
			t.Fatalf("/me exposed %s: %s", forbidden, me.Body.String())
		}
	}

	created := f.do(t, http.MethodPost, "/api/v1/meetings", `{"title":"Owner meeting"}`, alice)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d", created.Code)
	}

	mallory := f.bearer(t, "mallory")
	for _, target := range []string{"/api/v1/me", "/api/v1/meetings", "/api/v1/configuration", "/api/v1/credentials"} {
		response := f.do(t, http.MethodGet, target, "", mallory)
		if response.Code != http.StatusForbidden || errorCode(t, response) != "forbidden" {
			t.Fatalf("mallory %s status=%d", target, response.Code)
		}
		if strings.Contains(response.Body.String(), "Owner meeting") || strings.Contains(response.Body.String(), "wsp_") || strings.Contains(response.Body.String(), "usr_") {
			t.Fatalf("forbidden response leaked data: %s", response.Body.String())
		}
	}
	if !strings.Contains(f.logs.String(), `"category":"unknown_identity"`) {
		t.Fatal("unknown identity denial not logged")
	}
}

func TestWorkspaceIsolationBetweenMembers(t *testing.T) {
	f := newOIDCFixture(t)
	alice := f.bearer(t, "alice")
	if response := f.do(t, http.MethodGet, "/api/v1/me", "", alice); response.Code != http.StatusOK {
		t.Fatal("bootstrap failed")
	}
	// bob is a member of a different workspace; carol is a member of alice's.
	bob := domain.User{ID: "usr_bob", Issuer: f.issuer.URL, Subject: "bob"}
	f.identities.AddUser(bob)
	f.identities.AddMembership(bob.ID, domain.Workspace{ID: "wsp_bob", Name: "Bob's"}, domain.RoleOwner)
	aliceWorkspace := decode(t, f.do(t, http.MethodGet, "/api/v1/me", "", alice))["workspace"].(map[string]any)["id"].(string)
	carol := domain.User{ID: "usr_carol", Issuer: f.issuer.URL, Subject: "carol"}
	f.identities.AddUser(carol)
	f.identities.AddMembership(carol.ID, domain.Workspace{ID: domain.WorkspaceID(aliceWorkspace)}, domain.RoleMember)
	bobToken, carolToken := f.bearer(t, "bob"), f.bearer(t, "carol")

	meeting := decode(t, f.do(t, http.MethodPost, "/api/v1/meetings", `{"title":"Alice private"}`, alice))["meeting"].(map[string]any)
	meetingPath := "/api/v1/meetings/" + meeting["id"].(string)
	f.do(t, http.MethodPut, "/api/v1/credentials/openai", `{"value":"`+leakCanary+`"}`, alice)
	f.do(t, http.MethodPatch, "/api/v1/configuration", `{"summary":{"model":"alice-model"}}`, alice)

	// Bob (other workspace) sees none of it and cannot touch it.
	if response := f.do(t, http.MethodGet, meetingPath, "", bobToken); response.Code != http.StatusNotFound {
		t.Fatalf("bob get status=%d", response.Code)
	}
	if response := f.do(t, http.MethodDelete, meetingPath, "", bobToken); response.Code != http.StatusNotFound {
		t.Fatalf("bob delete status=%d", response.Code)
	}
	if items := decode(t, f.do(t, http.MethodGet, "/api/v1/meetings", "", bobToken))["items"].([]any); len(items) != 0 {
		t.Fatalf("bob listed %v", items)
	}
	if credentials := decode(t, f.do(t, http.MethodGet, "/api/v1/credentials", "", bobToken))["credentials"].([]any); len(credentials) != 0 {
		t.Fatalf("bob saw credentials %v", credentials)
	}
	if config := decode(t, f.do(t, http.MethodGet, "/api/v1/configuration", "", bobToken)); config["workspaceId"] != "wsp_bob" || strings.Contains(f.do(t, http.MethodGet, "/api/v1/configuration", "", bobToken).Body.String(), "alice-model") {
		t.Fatalf("bob configuration = %v", config)
	}
	// A client-supplied workspace header is ignored.
	spoofed := request(t, f.handler, http.MethodGet, meetingPath, "", map[string]string{"Authorization": bobToken, "X-Workspace-ID": aliceWorkspace})
	if spoofed.Code != http.StatusNotFound {
		t.Fatalf("workspace header honored: %d", spoofed.Code)
	}

	// Carol (member role) has normal access to alice's workspace.
	if response := f.do(t, http.MethodGet, meetingPath, "", carolToken); response.Code != http.StatusOK {
		t.Fatalf("member get status=%d", response.Code)
	}
	if me := decode(t, f.do(t, http.MethodGet, "/api/v1/me", "", carolToken)); me["role"] != "member" {
		t.Fatalf("carol me = %v", me)
	}
	if response := f.do(t, http.MethodPatch, meetingPath, `{"title":"Edited by member"}`, carolToken); response.Code != http.StatusOK {
		t.Fatalf("member update status=%d", response.Code)
	}

	// An ambiguous multi-workspace user is refused rather than guessed.
	f.identities.AddMembership(carol.ID, domain.Workspace{ID: "wsp_bob", Name: "Bob's"}, domain.RoleMember)
	ambiguous := f.do(t, http.MethodGet, "/api/v1/meetings", "", carolToken)
	if ambiguous.Code != http.StatusConflict || decode(t, ambiguous)["error"].(map[string]any)["details"].(map[string]any)["reason"] != "workspace_selection_required" {
		t.Fatalf("ambiguous status=%d body=%s", ambiguous.Code, ambiguous.Body.String())
	}
	assertNoLeak(t, "logs", f.logs.String())
}

func TestAccessLogCarriesPrincipalButNoToken(t *testing.T) {
	f := newOIDCFixture(t)
	token := f.issuer.TokenFor(t, "alice", map[string]any{"email": "alice@example.test"})
	f.do(t, http.MethodGet, "/api/v1/me", "", "Bearer "+token)
	var access logEntry
	for _, entry := range logEntries(t, f.logs) {
		if entry["msg"] == "http request" {
			access = entry
		}
	}
	if access == nil || !strings.HasPrefix(access["user_id"].(string), "usr_") || !strings.HasPrefix(access["workspace_id"].(string), "wsp_") || access["role"] != "owner" {
		t.Fatalf("access log = %v", access)
	}
	if strings.Contains(f.logs.String(), token) || strings.Contains(f.logs.String(), "alice@example.test") || strings.Contains(f.logs.String(), "Bearer") {
		t.Fatalf("logs leaked token or email: %s", f.logs.String())
	}
}

func TestIdentityProviderOutageIsUnavailable(t *testing.T) {
	f := newOIDCFixture(t)
	if response := f.do(t, http.MethodGet, "/api/v1/me", "", f.bearer(t, "alice")); response.Code != http.StatusOK {
		t.Fatal("bootstrap failed")
	}
	f.issuer.SetDown(true)
	// Cached keys still verify existing-key tokens during the outage.
	if response := f.do(t, http.MethodGet, "/api/v1/me", "", f.bearer(t, "alice")); response.Code != http.StatusOK {
		t.Fatalf("cached-key request status=%d", response.Code)
	}
	f.issuer.Rotate(t, "key-2", false)
	response := f.do(t, http.MethodGet, "/api/v1/me", "", f.bearer(t, "alice"))
	if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "unavailable" {
		t.Fatalf("new-key request during outage status=%d", response.Code)
	}
}

func TestSecurityHeadersAndNoCORS(t *testing.T) {
	f := newOIDCFixture(t)
	for _, response := range []*httptest.ResponseRecorder{
		request(t, f.handler, http.MethodGet, "/api/v1/health", "", map[string]string{"Origin": "https://evil.example.test"}),
		request(t, f.handler, http.MethodGet, "/api/v1/meetings", "", map[string]string{"Origin": "https://evil.example.test"}),
		request(t, f.handler, http.MethodGet, "/api/v1/me", "", map[string]string{"Authorization": f.bearer(t, "alice")}),
	} {
		header := response.Header()
		if header.Get("X-Content-Type-Options") != "nosniff" || header.Get("Cache-Control") != "no-store" {
			t.Fatalf("status %d headers = %v", response.Code, header)
		}
		for key := range header {
			if strings.HasPrefix(strings.ToLower(key), "access-control-") {
				t.Fatalf("CORS header emitted: %s", key)
			}
		}
	}
}
