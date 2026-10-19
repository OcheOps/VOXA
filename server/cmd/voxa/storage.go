package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/OcheOps/voxa/server/internal/adapters/memory"
	"github.com/OcheOps/voxa/server/internal/adapters/postgres"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/credentialcrypto"
	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
	"github.com/OcheOps/voxa/server/migrations"
)

// startupTimeout bounds connecting, migrating, and bootstrapping PostgreSQL.
const startupTimeout = 30 * time.Second

// storage is the repository set chosen at startup. Selection happens only
// here; handlers and services see repository interfaces.
type storage struct {
	backend        string
	meetings       repository.MeetingRepository
	configurations repository.ConfigurationRepository
	credentials    repository.CredentialRepository
	ready          func(context.Context) error
	close          func()
}

func openStorage(ctx context.Context, logger *slog.Logger, getenv func(string) string, databaseURL string, workspaceID domain.WorkspaceID) (*storage, error) {
	defaults := application.DefaultConfiguration()

	if databaseURL == "" {
		logger.Warn("using in-memory storage; meetings, configuration, and plaintext credentials are lost when the process exits (development only)",
			"storage_backend", "memory", "credential_backend", "memory-plaintext")
		if getenv(credentialKeyEnv) != "" {
			logger.Warn("credential encryption key is configured but unused in memory mode", "credential_backend", "memory-plaintext")
		}
		return &storage{
			backend:        "memory",
			meetings:       memory.NewMeetingRepository(),
			configurations: memory.NewConfigurationRepository(defaults),
			credentials:    memory.NewCredentialRepository(),
			ready:          func(context.Context) error { return nil },
			close:          func() {},
		}, nil
	}

	// Validate encryption keys before touching the database. There is no
	// fallback to plaintext in-memory credentials in PostgreSQL mode.
	keyRing, err := loadCredentialKeyRing(getenv)
	if err != nil {
		return nil, err
	}

	startupCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()
	pool, err := postgres.Connect(startupCtx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	connConfig := pool.Config().ConnConfig
	logger.Info("connected to PostgreSQL", "storage_backend", "postgres", "database_host", connConfig.Host, "database_name", connConfig.Database)

	applied, err := postgres.Migrate(startupCtx, pool, migrations.Files)
	for _, migration := range applied {
		logger.Info("applied database migration", "version", migration.Version, "name", migration.Name)
	}
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	logger.Info("database schema is up to date", "applied_this_start", len(applied))

	created, err := postgres.EnsureWorkspace(startupCtx, pool, workspaceID)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("bootstrap development workspace: %w", err)
	}
	logger.Info("development workspace ready", "development_workspace_id", workspaceID, "created", created)

	// Key versions are non-secret metadata (stored in plaintext beside each
	// row), so they are safe to log.
	stored, err := postgres.CredentialKeyVersions(startupCtx, pool)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("inspect stored credential key versions: %w", err)
	}
	loaded := keyRing.Versions()
	for version, rows := range stored {
		if !slices.Contains(loaded, version) {
			logger.Warn("stored credentials use a key version that is not configured; they cannot be decrypted until that key is supplied",
				"key_version", version, "credential_rows", rows)
		}
	}
	logger.Info("credential storage ready", "credential_backend", "postgres-encrypted", "cipher", "AES-256-GCM",
		"active_key_version", keyRing.ActiveVersion(), "loaded_key_versions", loaded)

	return &storage{
		backend:        "postgres",
		meetings:       postgres.NewMeetingRepository(pool),
		configurations: postgres.NewConfigurationRepository(pool, defaults),
		credentials:    postgres.NewCredentialRepository(pool, credentialcrypto.NewCipher(keyRing)),
		ready:          pool.Ping,
		close:          pool.Close,
	}, nil
}
