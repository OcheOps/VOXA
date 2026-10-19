package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

// bootstrapLockKey serializes first-user bootstrap transactions.
const bootstrapLockKey int64 = 0x766f78615f626f6f // "voxa_boo"

type IdentityRepository struct {
	pool *pgxpool.Pool
}

func NewIdentityRepository(pool *pgxpool.Pool) *IdentityRepository {
	return &IdentityRepository{pool: pool}
}

const userColumns = `id, issuer, subject, email, display_name, created_at, updated_at`

func scanUser(row pgx.Row) (domain.User, error) {
	var user domain.User
	var id string
	if err := row.Scan(&id, &user.Issuer, &user.Subject, &user.Email, &user.DisplayName, &user.CreatedAt, &user.UpdatedAt); err != nil {
		return domain.User{}, err
	}
	user.ID = domain.UserID(id)
	user.CreatedAt, user.UpdatedAt = user.CreatedAt.UTC(), user.UpdatedAt.UTC()
	return user, nil
}

func (r *IdentityRepository) FindUser(ctx context.Context, issuer, subject string) (domain.User, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	user, err := scanUser(r.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE issuer = $1 AND subject = $2`, issuer, subject))
	if err != nil {
		return domain.User{}, translate("find user", err)
	}
	return user, nil
}

func (r *IdentityRepository) UpdateUserProfile(ctx context.Context, userID domain.UserID, email, displayName *string, at time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tag, err := r.pool.Exec(ctx, `UPDATE users SET email = $2, display_name = $3, updated_at = $4 WHERE id = $1`, string(userID), email, displayName, at)
	if err != nil {
		return translate("update user profile", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *IdentityRepository) ListMemberships(ctx context.Context, userID domain.UserID) ([]domain.Membership, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := r.pool.Query(ctx, `SELECT m.workspace_id, w.name, m.role
FROM workspace_memberships m JOIN workspaces w ON w.id = m.workspace_id
WHERE m.user_id = $1
ORDER BY m.workspace_id`, string(userID))
	if err != nil {
		return nil, translate("list memberships", err)
	}
	memberships, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Membership, error) {
		var workspaceID, role string
		membership := domain.Membership{UserID: userID}
		err := row.Scan(&workspaceID, &membership.Workspace.Name, &role)
		membership.Workspace.ID, membership.Role = domain.WorkspaceID(workspaceID), domain.Role(role)
		return membership, err
	})
	if err != nil {
		return nil, translate("list memberships", err)
	}
	return memberships, nil
}

// Bootstrap runs in one transaction under a transaction-scoped advisory
// lock: check deployment_state, adopt-or-create the workspace, create the
// user and owner membership, then record deployment_state. Concurrent
// callers serialize on the lock; every caller after the first sees the
// recorded state and gets ErrAlreadyInitialized. The singleton primary key
// on deployment_state is a second, schema-level guarantee.
func (r *IdentityRepository) Bootstrap(ctx context.Context, user domain.User, workspace domain.Workspace) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return translate("begin bootstrap", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, bootstrapLockKey); err != nil {
		return translate("lock bootstrap", err)
	}
	var initialized bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deployment_state)`).Scan(&initialized); err != nil {
		return translate("check bootstrap", err)
	}
	if initialized {
		return repository.ErrAlreadyInitialized
	}
	// An existing workspace (e.g. data created in development mode) is
	// adopted unchanged.
	if _, err := tx.Exec(ctx, `INSERT INTO workspaces (id, name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`, string(workspace.ID), workspace.Name); err != nil {
		return translate("bootstrap workspace", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO users (`+userColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		string(user.ID), user.Issuer, user.Subject, user.Email, user.DisplayName, user.CreatedAt, user.UpdatedAt); err != nil {
		return translate("bootstrap user", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workspace_memberships (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, string(workspace.ID), string(user.ID)); err != nil {
		return translate("bootstrap membership", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deployment_state (bootstrap_user_id, bootstrap_workspace_id) VALUES ($1, $2)`, string(user.ID), string(workspace.ID)); err != nil {
		return translate("record bootstrap", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return translate("commit bootstrap", err)
	}
	return nil
}

func (r *IdentityRepository) IsInitialized(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	var initialized bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deployment_state)`).Scan(&initialized); err != nil {
		return false, translate("check bootstrap", err)
	}
	return initialized, nil
}

var _ repository.IdentityRepository = (*IdentityRepository)(nil)
