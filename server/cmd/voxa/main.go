package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/OcheOps/voxa/server/internal/adapters/memory"
	"github.com/OcheOps/voxa/server/internal/api"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/domain"
)

const serverVersion = "0.1.0-dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	listenAddress := environment("VOXA_LISTEN_ADDR", ":8080")
	workspaceValue := strings.TrimSpace(environment("VOXA_DEV_WORKSPACE_ID", "workspace-dev"))
	if workspaceValue == "" {
		logger.Error("VOXA_DEV_WORKSPACE_ID must not be blank")
		os.Exit(2)
	}
	workspaceID := domain.WorkspaceID(workspaceValue)

	meetingRepository := memory.NewMeetingRepository()
	credentialRepository := memory.NewCredentialRepository()
	configurationRepository := memory.NewConfigurationRepository(application.DefaultConfiguration())
	handler := api.NewHandler(
		application.NewMeetingService(meetingRepository),
		application.NewConfigurationService(configurationRepository, credentialRepository),
		application.NewCredentialService(credentialRepository),
		workspaceID,
		serverVersion,
		logger,
	)

	server := &http.Server{
		Addr:              listenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		logger.Info("shutting down Voxa server")
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
	}()

	logger.Info("starting Voxa server", "address", listenAddress, "development_workspace_id", workspaceID, "version", serverVersion)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped unexpectedly", "error", err)
		os.Exit(1)
	}
	logger.Info("Voxa server stopped")
}

func environment(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
