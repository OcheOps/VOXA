package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/OcheOps/voxa/server/internal/adapters/postgres"
	"github.com/OcheOps/voxa/server/migrations"
)

// runMigrate implements `voxa migrate [up|status]`.
func runMigrate(args []string, databaseURL string, logger *slog.Logger, stdout io.Writer) int {
	command := "up"
	if len(args) > 0 {
		command = args[0]
	}
	if len(args) > 1 || command != "up" && command != "status" {
		logger.Error("usage: voxa migrate [up|status]")
		return 2
	}
	if databaseURL == "" {
		logger.Error("VOXA_DATABASE_URL must be set to run migrations")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()
	pool, err := postgres.Connect(ctx, databaseURL)
	if err != nil {
		logger.Error("connect to PostgreSQL failed", "error", err)
		return 1
	}
	defer pool.Close()

	if command == "status" {
		statuses, err := postgres.MigrationState(ctx, pool, migrations.Files)
		if err != nil {
			logger.Error("read migration state failed", "error", err)
			return 1
		}
		for _, status := range statuses {
			state := "pending"
			if status.Applied {
				state = "applied " + status.AppliedAt.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(stdout, "%04d_%s\t%s\n", status.Version, status.Name, state)
		}
		return 0
	}

	applied, err := postgres.Migrate(ctx, pool, migrations.Files)
	for _, migration := range applied {
		logger.Info("applied database migration", "version", migration.Version, "name", migration.Name)
	}
	if err != nil {
		logger.Error("migration failed", "error", err)
		return 1
	}
	logger.Info("database schema is up to date", "applied", len(applied))
	return 0
}
