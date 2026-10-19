package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthModeMustBeExplicit(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"unset":                          {},
		"unknown mode":                   {authModeEnv: "none"},
		"mixed case":                     {authModeEnv: "OIDC"},
		"oidc without issuer":            {authModeEnv: "oidc", oidcAudienceEnv: "voxa"},
		"oidc without audience":          {authModeEnv: "oidc", oidcIssuerEnv: "https://idp.example.test"},
		"oidc plain http issuer":         {authModeEnv: "oidc", oidcIssuerEnv: "http://idp.example.test", oidcAudienceEnv: "voxa"},
		"oidc bad bootstrap workspace":   {authModeEnv: "oidc", oidcIssuerEnv: "https://idp.example.test", oidcAudienceEnv: "voxa", bootstrapWorkspaceEnv: "bad workspace"},
		"oidc ignores dev fallback":      {authModeEnv: "oidc", devWorkspaceEnv: "workspace-dev"},
		"development with bad workspace": {authModeEnv: "development", devWorkspaceEnv: "bad/id"},
	} {
		if _, err := loadAuthConfig(env(values)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAuthConfigModes(t *testing.T) {
	oidc, err := loadAuthConfig(env(map[string]string{
		authModeEnv: "oidc", oidcIssuerEnv: "https://idp.example.test/realms/voxa", oidcAudienceEnv: "voxa-web",
		bootstrapSubjectEnv: "admin-sub", bootstrapWorkspaceEnv: "workspace-dev",
	}))
	if err != nil || oidc.mode != authModeOIDC || oidc.devWorkspace != "" || oidc.bootstrapWorkspace != "workspace-dev" || oidc.bootstrapSubject != "admin-sub" {
		t.Fatalf("oidc config = %#v err=%v", oidc, err)
	}
	development, err := loadAuthConfig(env(map[string]string{authModeEnv: "development"}))
	if err != nil || development.devWorkspace != "workspace-dev" || development.issuer != "" {
		t.Fatalf("development config = %#v err=%v", development, err)
	}
}

func TestReadinessIsPublicAndOpaque(t *testing.T) {
	store := &storage{backend: "postgres", ready: func(context.Context) error { return context.DeadlineExceeded }}
	recorder := httptest.NewRecorder()
	readiness(store, slog.New(slog.NewTextHandler(io.Discard, nil)))(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "deadline") {
		t.Fatalf("readiness status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
