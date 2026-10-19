package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

// DefaultWorkspaceName names the workspace created by first-user bootstrap.
const DefaultWorkspaceName = "Default workspace"

const (
	maxIssuerLength      = 2048
	maxSubjectLength     = 255
	maxEmailLength       = 320
	maxDisplayNameLength = 200
)

// MembershipResolver maps an authenticated identity to the single workspace
// that identity may act in. It owns first-user bootstrap and every
// membership decision; HTTP code never queries memberships directly.
type MembershipResolver struct {
	repository repository.IdentityRepository
	now        func() time.Time
	newID      func(prefix string) (string, error)
	// bootstrapSubject, when set, is the only subject allowed to bootstrap.
	bootstrapSubject string
	// bootstrapWorkspace, when set, is adopted (or created) by bootstrap
	// instead of a freshly generated workspace.
	bootstrapWorkspace domain.WorkspaceID
}

type MembershipResolverOptions struct {
	BootstrapSubject     string
	BootstrapWorkspaceID domain.WorkspaceID
}

func NewMembershipResolver(repo repository.IdentityRepository, options MembershipResolverOptions) *MembershipResolver {
	return &MembershipResolver{
		repository:         repo,
		now:                time.Now,
		newID:              randomID,
		bootstrapSubject:   options.BootstrapSubject,
		bootstrapWorkspace: options.BootstrapWorkspaceID,
	}
}

func randomID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}

func accessError(code ErrorCode, category string, err error) error {
	return &Error{Code: code, Op: "auth.resolve", Category: category, Err: err}
}

// Resolve returns the authorized principal for identity:
//
//   - known user with exactly one membership -> that workspace and role
//   - known user with no membership          -> forbidden
//   - known user with several memberships    -> conflict (selection required)
//   - unknown identity, deployment not yet bootstrapped -> bootstrap as owner
//   - unknown identity otherwise             -> forbidden (no auto-join)
func (r *MembershipResolver) Resolve(ctx context.Context, identity domain.Identity) (domain.Principal, error) {
	if identity.Issuer == "" || identity.Subject == "" || len(identity.Issuer) > maxIssuerLength || len(identity.Subject) > maxSubjectLength {
		return domain.Principal{}, &Error{Code: Unauthorized, Op: "auth.resolve", Category: "identity_invalid"}
	}
	email := sanitizedProfile(identity.Email, maxEmailLength)
	displayName := sanitizedProfile(identity.DisplayName, maxDisplayNameLength)

	user, err := r.repository.FindUser(ctx, identity.Issuer, identity.Subject)
	if errors.Is(err, repository.ErrNotFound) {
		user, err = r.bootstrap(ctx, identity, email, displayName)
	} else if err == nil {
		user, err = r.syncProfile(ctx, user, email, displayName)
	} else {
		err = storageError("auth.resolve", err)
	}
	if err != nil {
		return domain.Principal{}, err
	}

	memberships, err := r.repository.ListMemberships(ctx, user.ID)
	if err != nil {
		return domain.Principal{}, storageError("auth.resolve", err)
	}
	switch len(memberships) {
	case 0:
		return domain.Principal{}, accessError(Forbidden, "not_a_member", nil)
	case 1:
		return domain.Principal{User: user, Workspace: memberships[0].Workspace, Role: memberships[0].Role}, nil
	default:
		// Selection is not implemented yet; never pick one arbitrarily.
		return domain.Principal{}, &Error{Code: Conflict, Op: "auth.resolve", Category: "workspace_selection_required",
			Details: map[string]any{"reason": "workspace_selection_required"}}
	}
}

// bootstrap creates the first user, default workspace and owner membership
// when the deployment is uninitialized. Losing a concurrent bootstrap race
// is not an error: the identity is looked up again, so a retry by the
// winner's identity succeeds and any other identity is forbidden.
func (r *MembershipResolver) bootstrap(ctx context.Context, identity domain.Identity, email, displayName *string) (domain.User, error) {
	initialized, err := r.repository.IsInitialized(ctx)
	if err != nil {
		return domain.User{}, storageError("auth.bootstrap", err)
	}
	if initialized {
		// Another request may have just bootstrapped this same identity
		// between our lookup and this check; look again before denying.
		return r.findExisting(ctx, identity)
	}
	if r.bootstrapSubject != "" && identity.Subject != r.bootstrapSubject {
		return domain.User{}, accessError(Forbidden, "bootstrap_not_permitted", nil)
	}

	userID, err := r.newID("usr_")
	if err != nil {
		return domain.User{}, &Error{Code: Internal, Op: "auth.bootstrap", Err: err}
	}
	workspace := domain.Workspace{ID: r.bootstrapWorkspace, Name: DefaultWorkspaceName}
	if workspace.ID == "" {
		generated, err := r.newID("wsp_")
		if err != nil {
			return domain.User{}, &Error{Code: Internal, Op: "auth.bootstrap", Err: err}
		}
		workspace.ID = domain.WorkspaceID(generated)
	}
	now := r.now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: domain.UserID(userID), Issuer: identity.Issuer, Subject: identity.Subject, Email: email, DisplayName: displayName, CreatedAt: now, UpdatedAt: now}

	err = r.repository.Bootstrap(ctx, user, workspace)
	if errors.Is(err, repository.ErrAlreadyInitialized) {
		return r.findExisting(ctx, identity)
	}
	if err != nil {
		return domain.User{}, storageError("auth.bootstrap", err)
	}
	return user, nil
}

// findExisting is used once the deployment is known to be initialized: the
// identity either exists (it won a concurrent bootstrap) or is unknown and
// must not gain access.
func (r *MembershipResolver) findExisting(ctx context.Context, identity domain.Identity) (domain.User, error) {
	user, err := r.repository.FindUser(ctx, identity.Issuer, identity.Subject)
	if errors.Is(err, repository.ErrNotFound) {
		return domain.User{}, accessError(Forbidden, "unknown_identity", nil)
	}
	if err != nil {
		return domain.User{}, storageError("auth.bootstrap", err)
	}
	return user, nil
}

// syncProfile refreshes mutable profile hints. It never changes the user ID
// or memberships.
func (r *MembershipResolver) syncProfile(ctx context.Context, user domain.User, email, displayName *string) (domain.User, error) {
	if equalOptional(user.Email, email) && equalOptional(user.DisplayName, displayName) {
		return user, nil
	}
	now := r.now().UTC().Truncate(time.Microsecond)
	if err := r.repository.UpdateUserProfile(ctx, user.ID, email, displayName, now); err != nil {
		return domain.User{}, storageError("auth.sync_profile", err)
	}
	user.Email, user.DisplayName, user.UpdatedAt = email, displayName, now
	return user, nil
}

func equalOptional(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// sanitizedProfile drops blank, oversized or invalid profile hints instead
// of storing untrusted claim values verbatim.
func sanitizedProfile(value *string, maxLength int) *string {
	if value == nil {
		return nil
	}
	clean := strings.TrimSpace(*value)
	if clean == "" || len(clean) > maxLength || !utf8.ValidString(clean) || strings.ContainsFunc(clean, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return nil
	}
	return &clean
}

// DevelopmentResolver authorizes every identity as the owner of one fixed
// development workspace. It exists only for VOXA_AUTH_MODE=development.
type DevelopmentResolver struct {
	principal domain.Principal
}

// DevelopmentUserID is the deterministic user used in development mode.
const DevelopmentUserID domain.UserID = "usr_development"

func NewDevelopmentResolver(workspaceID domain.WorkspaceID) *DevelopmentResolver {
	name := "Development user"
	return &DevelopmentResolver{principal: domain.Principal{
		User:      domain.User{ID: DevelopmentUserID, Issuer: "voxa-development", Subject: "developer", DisplayName: &name},
		Workspace: domain.Workspace{ID: workspaceID, Name: "Development workspace"},
		Role:      domain.RoleOwner,
	}}
}

func (r *DevelopmentResolver) Resolve(context.Context, domain.Identity) (domain.Principal, error) {
	return r.principal, nil
}
