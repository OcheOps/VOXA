package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OcheOps/voxa/server/internal/api"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/mediastore"
)

const (
	mediaDirEnv          = "VOXA_MEDIA_DIR"
	mediaMaxRecordingEnv = "VOXA_MEDIA_MAX_RECORDING_MB"
	mediaCleanupInterval = time.Hour
)

// loadMediaLimits reads the one tunable limit; the others are fixed,
// documented defaults (8 MiB chunks, 48 h stale-upload lifetime).
func loadMediaLimits(getenv func(string) string) (application.MediaLimits, error) {
	limits := application.DefaultMediaLimits()
	if value := strings.TrimSpace(getenv(mediaMaxRecordingEnv)); value != "" {
		megabytes, err := strconv.Atoi(value)
		if err != nil || megabytes < 1 || megabytes > 1<<20 {
			return limits, fmt.Errorf("%s must be an integer between 1 and 1048576", mediaMaxRecordingEnv)
		}
		limits.MaxRecordingBytes = int64(megabytes) << 20
	}
	return limits, nil
}

// openMedia enables the media API when VOXA_MEDIA_DIR is set, starts the
// cleanup loop and (when ffmpeg is available) the processing workers. The
// returned wait function blocks until workers have stopped after ctx ends.
func openMedia(ctx context.Context, logger *slog.Logger, getenv func(string) string, store *storage) ([]api.Option, func(), error) {
	noWait := func() {}
	dir := strings.TrimSpace(getenv(mediaDirEnv))
	if dir == "" {
		logger.Info("media storage disabled (VOXA_MEDIA_DIR not set)")
		return nil, noWait, nil
	}
	limits, err := loadMediaLimits(getenv)
	if err != nil {
		return nil, noWait, err
	}
	files, err := mediastore.NewFilesystem(dir)
	if err != nil {
		return nil, noWait, err
	}
	signer, err := api.NewPlaybackSigner(api.DefaultPlaybackTTL)
	if err != nil {
		return nil, noWait, err
	}
	service := application.NewMediaService(store.meetings, store.media, files, limits)
	absolute, _ := filepath.Abs(dir)
	logger.Info("media storage enabled", "media_backend", "filesystem", "media_dir", absolute,
		"max_chunk_bytes", limits.MaxChunkBytes, "max_recording_bytes", limits.MaxRecordingBytes, "stale_upload_age", limits.StaleUploadAge.String())
	if store.backend == "memory" {
		logger.Warn("media metadata is in memory: recordings stay on disk but are not listed after a restart; use PostgreSQL for real use")
	}

	runCleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		report, err := service.Cleanup(cleanupCtx)
		if err != nil {
			logger.Warn("media cleanup failed", "error", err)
			return
		}
		if report != (application.CleanupReport{}) {
			logger.Info("media cleanup", "failed_uploads", report.FailedUploads, "removed_staging", report.RemovedStaging, "removed_orphans", report.RemovedOrphans)
		}
	}
	runCleanup()
	go func() {
		ticker := time.NewTicker(mediaCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCleanup()
			}
		}
	}()
	options := []api.Option{api.WithMedia(service, signer)}
	processingOptions, wait, err := openProcessing(ctx, logger, getenv, store, files, service)
	if err != nil {
		return nil, noWait, err
	}
	return append(options, processingOptions...), wait, nil
}
