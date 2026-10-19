package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/OcheOps/voxa/server/internal/api"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/domain"
)

const serverVersion = "0.1.0-dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	databaseURL := strings.TrimSpace(os.Getenv("VOXA_DATABASE_URL"))
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "migrate" {
		os.Exit(runMigrate(args[1:], databaseURL, logger, os.Stdout))
	}
	if len(args) > 0 {
		logger.Error("unknown command; usage: voxa [migrate [up|status]]", "command", args[0])
		os.Exit(2)
	}
	os.Exit(serve(logger, databaseURL))
}

func serve(logger *slog.Logger, databaseURL string) int {
	listenAddress := environment("VOXA_LISTEN_ADDR", ":8080")
	workspaceValue := strings.TrimSpace(environment("VOXA_DEV_WORKSPACE_ID", "workspace-dev"))
	if workspaceValue == "" {
		logger.Error("VOXA_DEV_WORKSPACE_ID must not be blank")
		return 2
	}
	workspaceID := domain.WorkspaceID(workspaceValue)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := openStorage(ctx, logger, databaseURL, workspaceID)
	if err != nil {
		logger.Error("storage startup failed", "error", err)
		return 1
	}
	defer store.close()

	apiHandler := api.NewHandler(
		application.NewMeetingService(store.meetings),
		application.NewConfigurationService(store.configurations, store.credentials),
		application.NewCredentialService(store.credentials),
		workspaceID,
		serverVersion,
		logger,
	)
	root := http.NewServeMux()
	root.Handle("/", apiHandler)
	// Operational readiness probe, intentionally outside the /api/v1
	// protocol. /api/v1/health remains process liveness only.
	root.HandleFunc("GET /readyz", readiness(store, logger))

	server := &http.Server{
		Addr:              listenAddress,
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		logger.Info("shutting down Voxa server")
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
	}()

	logger.Info("starting Voxa server", "address", listenAddress, "development_workspace_id", workspaceID, "storage_backend", store.backend, "version", serverVersion)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped unexpectedly", "error", err)
		return 1
	}
	// Let in-flight requests drain before the deferred pool close.
	<-shutdownDone
	logger.Info("Voxa server stopped")
	return 0
}

func readiness(store *storage, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		status, body := http.StatusOK, map[string]string{"status": "ready", "storage": store.backend}
		if err := store.ready(ctx); err != nil {
			logger.Warn("readiness check failed", "storage_backend", store.backend, "error", err)
			status, body = http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "storage": store.backend}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
}

func environment(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
