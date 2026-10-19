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
	authSettings, err := loadAuthConfig(os.Getenv)
	if err != nil {
		logger.Error("invalid authentication configuration", "error", err)
		return 2
	}
	if authSettings.mode == authModeOIDC && databaseURL == "" {
		logger.Error("VOXA_AUTH_MODE=oidc requires VOXA_DATABASE_URL: users, memberships and bootstrap state must persist")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := openStorage(ctx, logger, os.Getenv, databaseURL, authSettings.devWorkspace)
	if err != nil {
		logger.Error("storage startup failed", "error", err)
		return 1
	}
	defer store.close()

	authenticator, resolver, err := openAuth(ctx, logger, authSettings, store)
	if err != nil {
		logger.Error("authentication startup failed", "error", err)
		return 1
	}

	apiHandler := api.NewHandler(
		application.NewMeetingService(store.meetings),
		application.NewConfigurationService(store.configurations, store.credentials),
		application.NewCredentialService(store.credentials),
		authenticator,
		resolver,
		serverVersion,
		logger,
	)
	corsPolicy, err := api.ParseAllowedOrigins(os.Getenv("VOXA_ALLOWED_ORIGINS"))
	if err != nil {
		logger.Error("invalid VOXA_ALLOWED_ORIGINS", "error", err)
		return 2
	}
	if corsPolicy.Enabled() {
		logger.Info("browser origins allowed for cross-origin API access", "allowed_origins", corsPolicy.Origins())
	} else {
		logger.Info("cross-origin browser access disabled (VOXA_ALLOWED_ORIGINS not set)")
	}

	root := http.NewServeMux()
	root.Handle("/", api.WithCORS(corsPolicy, apiHandler))
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

	logger.Info("starting Voxa server", "address", listenAddress, "auth_mode", authSettings.mode, "storage_backend", store.backend, "version", serverVersion)
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
