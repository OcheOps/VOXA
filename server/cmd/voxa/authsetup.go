package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/OcheOps/voxa/server/internal/api"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/auth"
)

// oidcDiscoveryTimeout bounds provider discovery at startup.
const oidcDiscoveryTimeout = 15 * time.Second

// openAuth builds the authenticator and workspace resolver for the chosen
// mode. Handlers and services never learn which mode is active.
func openAuth(ctx context.Context, logger *slog.Logger, config authConfig, store *storage) (api.Authenticator, api.WorkspaceResolver, error) {
	if config.mode == authModeDevelopment {
		logger.Warn("DEVELOPMENT AUTHENTICATION: every request is accepted as the owner of a fixed workspace without credentials; never expose this server",
			"auth_mode", authModeDevelopment, "development_workspace_id", config.devWorkspace)
		return auth.DevelopmentAuthenticator{}, application.NewDevelopmentResolver(config.devWorkspace), nil
	}

	if os.Getenv(devWorkspaceEnv) != "" {
		logger.Warn("VOXA_DEV_WORKSPACE_ID is ignored in oidc mode; workspaces come from memberships")
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, oidcDiscoveryTimeout)
	defer cancel()
	authenticator, err := auth.NewOIDCAuthenticator(discoveryCtx, auth.OIDCConfig{Issuer: config.issuer, Audience: config.audience})
	if err != nil {
		return nil, nil, err
	}
	initialized, err := store.identities.IsInitialized(discoveryCtx)
	if err != nil {
		return nil, nil, fmt.Errorf("read deployment state: %w", err)
	}
	// Issuer and audience are public OIDC metadata, safe to log.
	logger.Info("OIDC authentication ready", "auth_mode", authModeOIDC, "oidc_issuer", config.issuer, "oidc_audience", config.audience,
		"deployment_initialized", initialized)
	if !initialized {
		logger.Warn("deployment is not initialized: the first identity to authenticate becomes owner of the default workspace",
			"bootstrap_restricted_to_subject", config.bootstrapSubject != "", "bootstrap_workspace_id", string(config.bootstrapWorkspace))
	}
	resolver := application.NewMembershipResolver(store.identities, application.MembershipResolverOptions{
		BootstrapSubject:     config.bootstrapSubject,
		BootstrapWorkspaceID: config.bootstrapWorkspace,
	})
	return authenticator, resolver, nil
}
