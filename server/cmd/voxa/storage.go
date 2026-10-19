package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/OcheOps/voxa/server/internal/adapters/memory"
	"github.com/OcheOps/voxa/server/internal/adapters/postgres"
	"github.com/OcheOps/voxa/server/internal/application"
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

func openStorage(ctx context.Context, logger *slog.Logger, databaseURL string, workspaceID domain.WorkspaceID) (*storage, error) {
	// Credentials stay in process memory for every backend until encrypted
	// credential storage exists.
	credentials := memory.NewCredentialRepository()
	defaults := application.DefaultConfiguration()

	if databaseURL == "" {
		logger.Warn("using in-memory storage; meetings, configuration, and credentials are lost when the process exits",
			"storage_backend", "memory", "credential_backend", "memory")
		return &storage{
			backend:        "memory",
			meetings:       memory.NewMeetingRepository(),
			configurations: memory.NewConfigurationRepository(defaults),
			credentials:    credentials,
			ready:          func(context.Context) error { return nil },
			close:          func() {},
		}, nil
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

	logger.Warn("credentials are NOT persisted: provider secrets live only in process memory, are lost on restart, and are not production credential storage",
		"storage_backend", "postgres", "credential_backend", "memory")

	return &storage{
		backend:        "postgres",
		meetings:       postgres.NewMeetingRepository(pool),
		configurations: postgres.NewConfigurationRepository(pool, defaults),
		credentials:    credentials,
		ready:          pool.Ping,
		close:          pool.Close,
	}, nil
}
