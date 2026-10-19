package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

type externalIdentity struct{ issuer, subject string }

// IdentityRepository is an in-memory users/memberships/bootstrap store for
// tests and development.
type IdentityRepository struct {
	mu          sync.Mutex
	users       map[domain.UserID]domain.User
	byIdentity  map[externalIdentity]domain.UserID
	workspaces  map[domain.WorkspaceID]domain.Workspace
	memberships map[domain.UserID]map[domain.WorkspaceID]domain.Role
	initialized bool
}

func NewIdentityRepository() *IdentityRepository {
	return &IdentityRepository{
		users:       make(map[domain.UserID]domain.User),
		byIdentity:  make(map[externalIdentity]domain.UserID),
		workspaces:  make(map[domain.WorkspaceID]domain.Workspace),
		memberships: make(map[domain.UserID]map[domain.WorkspaceID]domain.Role),
	}
}

func (r *IdentityRepository) FindUser(_ context.Context, issuer, subject string) (domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byIdentity[externalIdentity{issuer, subject}]
	if !ok {
		return domain.User{}, repository.ErrNotFound
	}
	return r.users[id], nil
}

func (r *IdentityRepository) UpdateUserProfile(_ context.Context, userID domain.UserID, email, displayName *string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	user, ok := r.users[userID]
	if !ok {
		return repository.ErrNotFound
	}
	user.Email, user.DisplayName, user.UpdatedAt = email, displayName, at
	r.users[userID] = user
	return nil
}

func (r *IdentityRepository) ListMemberships(_ context.Context, userID domain.UserID) ([]domain.Membership, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	memberships := make([]domain.Membership, 0, len(r.memberships[userID]))
	for workspaceID, role := range r.memberships[userID] {
		memberships = append(memberships, domain.Membership{Workspace: r.workspaces[workspaceID], UserID: userID, Role: role})
	}
	sort.Slice(memberships, func(i, j int) bool { return memberships[i].Workspace.ID < memberships[j].Workspace.ID })
	return memberships, nil
}

func (r *IdentityRepository) Bootstrap(_ context.Context, user domain.User, workspace domain.Workspace) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.initialized {
		return repository.ErrAlreadyInitialized
	}
	key := externalIdentity{user.Issuer, user.Subject}
	if _, exists := r.byIdentity[key]; exists {
		return repository.ErrConflict
	}
	if _, exists := r.workspaces[workspace.ID]; !exists {
		r.workspaces[workspace.ID] = workspace
	}
	r.users[user.ID] = user
	r.byIdentity[key] = user.ID
	r.memberships[user.ID] = map[domain.WorkspaceID]domain.Role{workspace.ID: domain.RoleOwner}
	r.initialized = true
	return nil
}

func (r *IdentityRepository) IsInitialized(context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.initialized, nil
}

// AddMembership grants userID a role in an existing or new workspace. It is a
// test/development helper; no API exposes membership management yet.
func (r *IdentityRepository) AddMembership(userID domain.UserID, workspace domain.Workspace, role domain.Role) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.workspaces[workspace.ID]; !exists {
		r.workspaces[workspace.ID] = workspace
	}
	if r.memberships[userID] == nil {
		r.memberships[userID] = make(map[domain.WorkspaceID]domain.Role)
	}
	r.memberships[userID][workspace.ID] = role
}

// AddUser registers a user without memberships (test/development helper).
func (r *IdentityRepository) AddUser(user domain.User) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.users[user.ID] = user
	r.byIdentity[externalIdentity{user.Issuer, user.Subject}] = user.ID
}
