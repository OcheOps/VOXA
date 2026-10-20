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
	"github.com/OcheOps/voxa/server/internal/summary"
)

const (
	whisperModelEnv      = "VOXA_WHISPER_MODEL"
	whisperBinEnv        = "VOXA_WHISPER_BIN"
	processingWorkersEnv = "VOXA_PROCESSING_WORKERS"
	summaryProvidersEnv  = "VOXA_SUMMARY_PROVIDERS"
	ollamaURLEnv         = "VOXA_OLLAMA_URL"
	customOpenAIURLEnv   = "VOXA_CUSTOM_OPENAI_URL"
)

// loadSummaryProviders reads the operator's provider allow-list. Nothing is
// enabled by default: transcripts leave the server only when an operator
// opts in to a provider.
func loadSummaryProviders(logger *slog.Logger, getenv func(string) string) ([]summary.Spec, error) {
	var enabled []string
	for _, name := range strings.Split(getenv(summaryProvidersEnv), ",") {
		if name = strings.TrimSpace(name); name != "" {
			enabled = append(enabled, name)
		}
	}
	specs, warnings, err := summary.Specs(summary.Settings{Enabled: enabled, OllamaURL: getenv(ollamaURLEnv), CustomOpenAIURL: getenv(customOpenAIURLEnv)})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", summaryProvidersEnv, err)
	}
	for _, warning := range warnings {
		logger.Warn("summary provider endpoint warning", "warning", warning)
	}
	return specs, nil
}

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
func openProcessing(ctx context.Context, logger *slog.Logger, getenv func(string) string, store *storage, files mediastore.Store, media *application.MediaService) ([]api.Option, func(), error) {
	noWait := func() {}
	workers, err := loadWorkerCount(getenv)
	if err != nil {
		return nil, noWait, err
	}
	specs, err := loadSummaryProviders(logger, getenv)
	if err != nil {
		return nil, noWait, err
	}
	ffmpeg, ffmpegErr := exec.LookPath("ffmpeg")
	ffprobe, ffprobeErr := exec.LookPath("ffprobe")
	if ffmpegErr != nil || ffprobeErr != nil {
		logger.Warn("media processing disabled: ffmpeg and ffprobe must be on PATH; recordings still save and play, without duration fix-ups, transcription or summaries")
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
	var providers application.SummaryProviders
	var registry *summary.Registry
	if len(specs) > 0 {
		registry = summary.NewRegistry(specs, summary.NewHTTPClient(), store.secrets)
		providers = registry
		handlers[domain.JobSummary] = &processing.SummaryHandler{Jobs: store.jobs, Transcripts: store.transcripts, Summaries: store.summaries,
			Providers: registry, Now: now}
		logger.Info("summaries enabled", "providers", strings.Join(registry.Enabled(), ","))
	} else {
		logger.Info("summaries disabled (VOXA_SUMMARY_PROVIDERS not set)")
	}
	summaries := application.NewSummaryService(store.meetings, store.media, store.jobs, store.transcripts, store.summaries,
		store.configurations, store.credentials, providers)
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
	worker.OnCompleted = func(ctx context.Context, job domain.ProcessingJob) error {
		_, err := summaries.AfterTranscription(ctx, job)
		return err
	}
	var running sync.WaitGroup
	running.Add(1)
	go func() {
		defer running.Done()
		worker.Run(ctx)
	}()
	logger.Info("media processing enabled", "workers", workers, "remux", true, "transcription", engine != nil, "queue", store.backend)
	service := application.NewProcessingService(store.meetings, store.media, store.jobs, store.transcripts, queue, engine != nil)
	return []api.Option{api.WithProcessing(service), api.WithSummaries(summaries)}, running.Wait, nil
}
