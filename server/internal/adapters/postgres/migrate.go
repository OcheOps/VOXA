package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLockKey is the pg_advisory_lock key that serializes migration runs
// from concurrently starting server processes against one database.
const migrationLockKey int64 = 0x766f78615f6d6967 // "voxa_mig"

var migrationFilePattern = regexp.MustCompile(`^(\d{4,})_([a-z0-9_]+)\.sql$`)

type Migration struct {
	Version  int64
	Name     string
	SQL      string
	Checksum string
}

type MigrationStatus struct {
	Version   int64
	Name      string
	Applied   bool
	AppliedAt *time.Time
}

// LoadMigrations reads NNNN_name.sql files from fsys in version order.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var migrations []Migration
	seen := make(map[int64]string)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		match := migrationFilePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("migration %q: name must match NNNN_description.sql", entry.Name())
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || version < 1 {
			return nil, fmt.Errorf("migration %q: invalid version", entry.Name())
		}
		if previous, exists := seen[version]; exists {
			return nil, fmt.Errorf("migrations %q and %q share version %d", previous, entry.Name(), version)
		}
		seen[version] = entry.Name()
		content, err := fs.ReadFile(fsys, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(content)
		migrations = append(migrations, Migration{Version: version, Name: match[2], SQL: string(content), Checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

type appliedMigration struct {
	name      string
	checksum  string
	appliedAt time.Time
}

// Migrate applies pending migrations in version order and returns the ones it
// applied. Each migration and its schema_migrations row commit in a single
// transaction, so a failure leaves earlier migrations applied, rolls back the
// failing one, and stops. Applied migrations whose checksum changed, or
// applied versions unknown to this binary, are rejected before anything runs.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) ([]Migration, error) {
	migrations, err := LoadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	conn, err := lockedConnection(ctx, pool)
	if err != nil {
		return nil, err
	}
	// Closing the dedicated session releases the advisory lock even if the
	// context was cancelled mid-run.
	defer conn.Close(context.Background())

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
    version    bigint PRIMARY KEY,
    name       text NOT NULL,
    checksum   text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}
	applied, err := readApplied(ctx, conn)
	if err != nil {
		return nil, err
	}
	if err := verifyApplied(migrations, applied); err != nil {
		return nil, err
	}

	var ran []Migration
	for _, migration := range migrations {
		if _, done := applied[migration.Version]; done {
			continue
		}
		if err := applyMigration(ctx, conn, migration); err != nil {
			return ran, err
		}
		ran = append(ran, migration)
	}
	return ran, nil
}

// MigrationState reports every known migration and whether it is applied.
// It is read-only and does not create the tracking table.
func MigrationState(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) ([]MigrationStatus, error) {
	migrations, err := LoadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, fmt.Errorf("inspect schema_migrations: %w", err)
	}
	applied := map[int64]appliedMigration{}
	if exists {
		if applied, err = readApplied(ctx, conn.Conn()); err != nil {
			return nil, err
		}
		if err := verifyApplied(migrations, applied); err != nil {
			return nil, err
		}
	}
	statuses := make([]MigrationStatus, 0, len(migrations))
	for _, migration := range migrations {
		status := MigrationStatus{Version: migration.Version, Name: migration.Name}
		if row, ok := applied[migration.Version]; ok {
			appliedAt := row.appliedAt
			status.Applied = true
			status.AppliedAt = &appliedAt
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func lockedConnection(ctx context.Context, pool *pgxpool.Pool) (*pgx.Conn, error) {
	pooled, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire migration connection: %w", err)
	}
	// Hijack so the session-level advisory lock can never leak back into the pool.
	conn := pooled.Hijack()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		conn.Close(context.Background())
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	return conn, nil
}

func readApplied(ctx context.Context, conn *pgx.Conn) (map[int64]appliedMigration, error) {
	rows, err := conn.Query(ctx, `SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[int64]appliedMigration)
	for rows.Next() {
		var version int64
		var row appliedMigration
		if err := rows.Scan(&version, &row.name, &row.checksum, &row.appliedAt); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[version] = row
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	return applied, nil
}

func verifyApplied(migrations []Migration, applied map[int64]appliedMigration) error {
	known := make(map[int64]Migration, len(migrations))
	for _, migration := range migrations {
		known[migration.Version] = migration
	}
	versions := make([]int64, 0, len(applied))
	for version := range applied {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	for _, version := range versions {
		migration, ok := known[version]
		if !ok {
			return fmt.Errorf("database has migration %d applied that this server does not know; refusing to run an older binary against a newer schema", version)
		}
		if applied[version].checksum != migration.Checksum {
			return fmt.Errorf("migration %d_%s was modified after it was applied (checksum mismatch)", version, migration.Name)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, conn *pgx.Conn, migration Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migration %d_%s: begin: %w", migration.Version, migration.Name, err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Exec without arguments uses the simple protocol, which allows a file
	// to contain multiple statements.
	if _, err := tx.Exec(ctx, migration.SQL); err != nil {
		return fmt.Errorf("migration %d_%s failed: %w", migration.Version, migration.Name, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`, migration.Version, migration.Name, migration.Checksum); err != nil {
		return fmt.Errorf("migration %d_%s: record: %w", migration.Version, migration.Name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migration %d_%s: commit: %w", migration.Version, migration.Name, err)
	}
	return nil
}
