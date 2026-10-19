package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/auth"
	"github.com/OcheOps/voxa/server/internal/domain"
)

// Authenticator verifies the Authorization header and returns the caller's
// external identity (implemented by auth.OIDCAuthenticator and
// auth.DevelopmentAuthenticator).
type Authenticator interface {
	Authenticate(ctx context.Context, authorization string) (domain.Identity, error)
}

// WorkspaceResolver authorizes an identity into exactly one workspace
// (implemented by application.MembershipResolver and
// application.DevelopmentResolver).
type WorkspaceResolver interface {
	Resolve(ctx context.Context, identity domain.Identity) (domain.Principal, error)
}

// requestState is shared by the outer middleware (access log, failure log)
// and the auth middleware, which fills in the principal once resolved.
type requestState struct {
	principal *domain.Principal
}

func stateFrom(ctx context.Context) *requestState {
	state, _ := ctx.Value(stateKey).(*requestState)
	return state
}

func principalFrom(ctx context.Context) (domain.Principal, bool) {
	if state := stateFrom(ctx); state != nil && state.principal != nil {
		return *state.principal, true
	}
	return domain.Principal{}, false
}

// workspaceID returns the authorized workspace. Protected handlers only run
// after authorize succeeded, so a missing principal is a programming error.
func workspaceID(r *http.Request) domain.WorkspaceID {
	principal, ok := principalFrom(r.Context())
	if !ok {
		panic("workspace-scoped handler reached without an authorized principal")
	}
	return principal.Workspace.ID
}

// authorize authenticates the request, resolves the caller's workspace
// membership and only then runs next. Services and repositories still
// filter every operation by the resolved workspace (defense in depth).
func (s *Server) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		identity, err := s.authenticator.Authenticate(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			s.writeError(w, r, authenticationError(err))
			return
		}
		principal, err := s.resolver.Resolve(r.Context(), identity)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if state := stateFrom(r.Context()); state != nil {
			state.principal = &principal
		}
		next(w, r)
	}
}

// authenticationError maps an authenticator outcome to the public error
// contract: bad or missing credentials are 401, provider outages are 503.
// Only the fixed failure category is kept; library errors are dropped.
func authenticationError(err error) error {
	var failure *auth.Failure
	if !errors.As(err, &failure) {
		return &application.Error{Code: application.Unauthorized, Op: "auth.authenticate", Category: auth.CategoryVerificationError}
	}
	if failure.Unavailable {
		return &application.Error{Code: application.Unavailable, Op: "auth.authenticate", Category: failure.Category}
	}
	return &application.Error{Code: application.Unauthorized, Op: "auth.authenticate", Category: failure.Category}
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r.Context())
	writeJSON(w, http.StatusOK, meResponse{
		User:      currentUserDTO{ID: string(principal.User.ID), Email: principal.User.Email, DisplayName: principal.User.DisplayName},
		Workspace: currentWorkspaceDTO{ID: string(principal.Workspace.ID), Name: principal.Workspace.Name},
		Role:      string(principal.Role),
	})
}
