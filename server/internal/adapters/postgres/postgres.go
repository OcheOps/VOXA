// Package postgres provides PostgreSQL implementations of the repository
// ports using pgx with explicit, parameterized SQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/puddle/v2"

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
		switch {
		case pgError.Code == "23505": // unique_violation
			return repository.ErrConflict
		case pgError.Code == "23503": // foreign_key_violation
			return fmt.Errorf("postgres %s: referenced workspace does not exist", operation)
		case unavailableSQLState(pgError.Code):
			return fmt.Errorf("postgres %s: sqlstate %s: %w", operation, pgError.Code, repository.ErrUnavailable)
		}
		return fmt.Errorf("postgres %s: sqlstate %s", operation, pgError.Code)
	}
	if unavailableTransport(err) {
		return fmt.Errorf("postgres %s: %w: %w", operation, repository.ErrUnavailable, safeTransportError(err))
	}
	return fmt.Errorf("postgres %s: %w", operation, err)
}

// unavailableSQLState reports server-sent codes that mean the database
// cannot serve requests right now, as opposed to a problem with the request.
func unavailableSQLState(code string) bool {
	switch {
	case strings.HasPrefix(code, "08"): // connection_exception class
		return true
	case code == "57P01", code == "57P02", code == "57P03": // admin/crash shutdown, cannot_connect_now
		return true
	case code == "53300": // too_many_connections
		return true
	}
	return false
}

// unavailableTransport classifies failures that never produced a server
// response: connect errors, timeouts, broken connections and a closed pool.
// Errors that cannot be classified reliably stay internal.
func unavailableTransport(err error) bool {
	if errors.Is(err, context.Canceled) {
		// The caller went away; this says nothing about database health.
		return false
	}
	var connectError *pgconn.ConnectError
	var netError net.Error
	return errors.As(err, &connectError) ||
		pgconn.Timeout(err) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &netError) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, puddle.ErrClosedPool)
}

// safeTransportError keeps the classification-relevant chain while avoiding
// connection-string details: ConnectError text names the user and database.
func safeTransportError(err error) error {
	var connectError *pgconn.ConnectError
	if errors.As(err, &connectError) {
		return errors.New("could not connect to database")
	}
	return err
}
