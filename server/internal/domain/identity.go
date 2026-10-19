package domain

import "time"

type UserID string

// Role is a workspace membership role. Both roles currently have full
// access to existing workspace features; owner is reserved for future
// administration (invitations, membership management).
type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
)

// Identity is an authenticated external principal. Issuer+Subject is the
// stable identity key; Email and DisplayName are optional, mutable profile
// hints and are never used for identity or authorization.
type Identity struct {
	Issuer      string
	Subject     string
	Email       *string
	DisplayName *string
}

type User struct {
	ID          UserID
	Issuer      string
	Subject     string
	Email       *string
	DisplayName *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Workspace struct {
	ID   WorkspaceID
	Name string
}

type Membership struct {
	Workspace Workspace
	UserID    UserID
	Role      Role
}

// Principal is the authorized subject of a request: a Voxa user acting in
// exactly one workspace with a role. Handlers only ever see this, never
// tokens or claims.
type Principal struct {
	User      User
	Workspace Workspace
	Role      Role
}
