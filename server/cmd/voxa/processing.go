package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OcheOps/voxa/server/internal/api"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/mediastore"
	"github.com/OcheOps/voxa/server/internal/processing"
)

const (
	whisperModelEnv      = "VOXA_WHISPER_MODEL"
	whisperBinEnv        = "VOXA_WHISPER_BIN"
	processingWorkersEnv = "VOXA_PROCESSING_WORKERS"
)

func loadWorkerCount(getenv func(string) string) (int, error) {
	value := strings.TrimSpace(getenv(processingWorkersEnv))
	if value == "" {
		return 1, nil
	}
	workers, err := strconv.Atoi(value)
	if err != nil || workers < 1 || workers > 16 {
		return 0, fmt.Errorf("%s must be an integer between 1 and 16", processingWorkersEnv)
	}
	return workers, nil
}

// openProcessing starts remux (and, when a usable Whisper engine is
// configured, transcription) workers. Missing tools disable the feature with
// an operator log; recordings keep saving and playing either way.
func openProcessing(ctx context.Context, logger *slog.Logger, getenv func(string) string, store *storage, files mediastore.Store, media *application.MediaService) (api.Option, func(), error) {
	noWait := func() {}
	workers, err := loadWorkerCount(getenv)
	if err != nil {
		return nil, noWait, err
	}
	ffmpeg, ffmpegErr := exec.LookPath("ffmpeg")
	ffprobe, ffprobeErr := exec.LookPath("ffprobe")
	if ffmpegErr != nil || ffprobeErr != nil {
		logger.Warn("media processing disabled: ffmpeg and ffprobe must be on PATH; recordings still save and play, without duration fix-ups or transcription")
		return nil, noWait, nil
	}
	tools := processing.Tools{FFmpeg: ffmpeg, FFprobe: ffprobe}

	var engine processing.Engine
	if modelPath := strings.TrimSpace(getenv(whisperModelEnv)); modelPath != "" {
		binary := strings.TrimSpace(getenv(whisperBinEnv))
		if binary == "" {
			binary = "whisper-cli"
		}
		resolved, lookErr := exec.LookPath(binary)
		whisper := processing.WhisperCLI{Binary: resolved, ModelPath: modelPath, Threads: max(1, min(8, runtime.NumCPU()/workers))}
		checkCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		checkErr := lookErr
		if checkErr == nil {
			checkErr = processing.CheckWhisper(checkCtx, whisper)
		}
		cancel()
		if checkErr != nil {
			logger.Error("transcription unavailable: the Whisper engine is not usable; recordings still save and play",
				"whisper_bin", binary, "whisper_model", filepath.Base(modelPath), "reason", checkErr.Error())
		} else {
			engine = whisper
			logger.Info("transcription enabled", "engine", whisper.Name(), "model", whisper.Model(), "threads", whisper.Threads)
		}
	} else {
		logger.Info("transcription disabled (VOXA_WHISPER_MODEL not set)")
	}

	queue := processing.NewQueue(store.jobs, engine != nil)
	media.SetProcessing(queue)
	now := time.Now
	handlers := map[domain.ProcessingJobType]processing.Handler{
		domain.JobRemux: &processing.RemuxHandler{Media: store.media, Store: files, Tools: tools, Queue: queue, Now: now},
	}
	if engine != nil {
		handlers[domain.JobTranscription] = &processing.TranscriptionHandler{Media: store.media, Transcripts: store.transcripts,
			Configurations: store.configurations, Store: files, Tools: tools, Engine: engine, Now: now}
	}
	reconcile := func(ctx context.Context) {
		assets, err := store.media.ListUnprocessedReady(ctx, 100)
		if err != nil {
			return
		}
		for _, asset := range assets {
			_ = queue.EnqueueRemux(ctx, asset)
		}
	}
	config := processing.DefaultWorkerConfig(logger)
	config.Concurrency = workers
	worker := processing.NewWorker(store.jobs, handlers, config, reconcile)
	var running sync.WaitGroup
	running.Add(1)
	go func() {
		defer running.Done()
		worker.Run(ctx)
	}()
	logger.Info("media processing enabled", "workers", workers, "remux", true, "transcription", engine != nil, "queue", store.backend)
	service := application.NewProcessingService(store.meetings, store.media, store.jobs, store.transcripts, queue, engine != nil)
	return api.WithProcessing(service), running.Wait, nil
}
