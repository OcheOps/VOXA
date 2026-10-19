// Package postgres provides PostgreSQL implementations of the repository
// ports using pgx with explicit, parameterized SQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

const (
	defaultConnectTimeout = 5 * time.Second
	queryTimeout          = 5 * time.Second
)

// ErrInvalidDatabaseURL is returned instead of the parser error, which may
// echo the connection string including its password.
var ErrInvalidDatabaseURL = errors.New("database URL is not a valid PostgreSQL connection string")

// Connect builds a pgx pool with library-default sizing, a bounded per-dial
// timeout, and verifies connectivity before returning.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, ErrInvalidDatabaseURL
	}
	if config.ConnConfig.ConnectTimeout == 0 {
		config.ConnConfig.ConnectTimeout = defaultConnectTimeout
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("verify database connection: %w", err)
	}
	return pool, nil
}

// EnsureWorkspace creates the workspace row if it is missing and reports
// whether it was created. It is the temporary bootstrap for the configured
// development workspace until authentication and membership exist.
func EnsureWorkspace(ctx context.Context, pool *pgxpool.Pool, workspaceID domain.WorkspaceID) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tag, err := pool.Exec(ctx, `INSERT INTO workspaces (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, string(workspaceID))
	if err != nil {
		return false, translate("ensure workspace", err)
	}
	return tag.RowsAffected() == 1, nil
}

// translate maps driver outcomes to repository errors. PostgreSQL messages
// and details are dropped because they can echo row values.
func translate(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return repository.ErrNotFound
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "23505": // unique_violation
			return repository.ErrConflict
		case "23503": // foreign_key_violation
			return fmt.Errorf("postgres %s: referenced workspace does not exist", operation)
		}
		return fmt.Errorf("postgres %s: sqlstate %s", operation, pgError.Code)
	}
	return fmt.Errorf("postgres %s: %w", operation, err)
}
