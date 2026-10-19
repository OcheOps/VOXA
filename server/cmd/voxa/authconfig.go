package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/OcheOps/voxa/server/internal/auth"
	"github.com/OcheOps/voxa/server/internal/domain"
)

const (
	authModeEnv           = "VOXA_AUTH_MODE"
	oidcIssuerEnv         = "VOXA_OIDC_ISSUER"
	oidcAudienceEnv       = "VOXA_OIDC_AUDIENCE"
	bootstrapSubjectEnv   = "VOXA_BOOTSTRAP_OIDC_SUBJECT"
	bootstrapWorkspaceEnv = "VOXA_BOOTSTRAP_WORKSPACE_ID"
	devWorkspaceEnv       = "VOXA_DEV_WORKSPACE_ID"

	authModeOIDC        = "oidc"
	authModeDevelopment = "development"
)

var workspaceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type authConfig struct {
	mode string
	// oidc
	issuer             string
	audience           string
	bootstrapSubject   string
	bootstrapWorkspace domain.WorkspaceID
	// development
	devWorkspace domain.WorkspaceID
}

// loadAuthConfig validates authentication settings. The mode must be chosen
// explicitly: there is no default, so a deployment can never silently run
// with development identity.
func loadAuthConfig(getenv func(string) string) (authConfig, error) {
	config := authConfig{mode: strings.TrimSpace(getenv(authModeEnv))}
	switch config.mode {
	case authModeOIDC:
		config.issuer = strings.TrimSpace(getenv(oidcIssuerEnv))
		config.audience = strings.TrimSpace(getenv(oidcAudienceEnv))
		if config.issuer == "" || config.audience == "" {
			return authConfig{}, fmt.Errorf("%s=oidc requires %s and %s", authModeEnv, oidcIssuerEnv, oidcAudienceEnv)
		}
		if err := auth.ValidateIssuerURL(config.issuer); err != nil {
			return authConfig{}, fmt.Errorf("%s: %w", oidcIssuerEnv, err)
		}
		config.bootstrapSubject = strings.TrimSpace(getenv(bootstrapSubjectEnv))
		if workspace := strings.TrimSpace(getenv(bootstrapWorkspaceEnv)); workspace != "" {
			if !workspaceIDPattern.MatchString(workspace) {
				return authConfig{}, fmt.Errorf("%s must match %s", bootstrapWorkspaceEnv, workspaceIDPattern)
			}
			config.bootstrapWorkspace = domain.WorkspaceID(workspace)
		}
	case authModeDevelopment:
		workspace := strings.TrimSpace(getenv(devWorkspaceEnv))
		if workspace == "" {
			workspace = "workspace-dev"
		}
		if !workspaceIDPattern.MatchString(workspace) {
			return authConfig{}, fmt.Errorf("%s must match %s", devWorkspaceEnv, workspaceIDPattern)
		}
		config.devWorkspace = domain.WorkspaceID(workspace)
	case "":
		return authConfig{}, fmt.Errorf("%s must be set to %q (production) or %q (local development only)", authModeEnv, authModeOIDC, authModeDevelopment)
	default:
		return authConfig{}, errors.New(authModeEnv + ` must be "oidc" or "development"`)
	}
	return config, nil
}
