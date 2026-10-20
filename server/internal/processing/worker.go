package processing

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

// Handler performs one job type. It must stop promptly when ctx is cancelled
// and report failures as *JobError (anything else is treated as transient).
type Handler interface {
	Handle(ctx context.Context, job domain.ProcessingJob, progress func(float64)) error
}

type WorkerConfig struct {
	Concurrency int
	// Lease is how long a claim stays valid without a heartbeat; an expired
	// lease makes the job reclaimable (crash recovery).
	Lease     time.Duration
	Heartbeat time.Duration
	Poll      time.Duration
	Logger    *slog.Logger
}

func DefaultWorkerConfig(logger *slog.Logger) WorkerConfig {
	return WorkerConfig{Concurrency: 1, Lease: 60 * time.Second, Heartbeat: 10 * time.Second, Poll: 2 * time.Second, Logger: logger}
}

// Worker runs jobs inside the server process. Several workers (goroutines or
// server processes) may share one queue: claims are exclusive.
type Worker struct {
	jobs      repository.JobRepository
	handlers  map[domain.ProcessingJobType]Handler
	config    WorkerConfig
	reconcile func(context.Context)
	now       func() time.Time
}

var errLeaseLost = errors.New("job no longer owned by this worker")

func NewWorker(jobs repository.JobRepository, handlers map[domain.ProcessingJobType]Handler, config WorkerConfig, reconcile func(context.Context)) *Worker {
	return &Worker{jobs: jobs, handlers: handlers, config: config, reconcile: reconcile, now: time.Now}
}

func (w *Worker) types() []domain.ProcessingJobType {
	types := make([]domain.ProcessingJobType, 0, len(w.handlers))
	for t := range w.handlers {
		types = append(types, t)
	}
	return types
}

// Run blocks until ctx is cancelled and all in-flight jobs have stopped.
func (w *Worker) Run(ctx context.Context) {
	var wait sync.WaitGroup
	for i := 0; i < max(1, w.config.Concurrency); i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			w.loop(ctx)
		}()
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		w.maintenance(ctx)
	}()
	wait.Wait()
}

func (w *Worker) maintenance(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if w.reconcile != nil {
			w.reconcile(ctx)
		}
		if n, err := w.jobs.FailExhausted(ctx, w.now().UTC()); err == nil && n > 0 {
			w.config.Logger.Warn("processing jobs abandoned after their last attempt", "jobs", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) loop(ctx context.Context) {
	for ctx.Err() == nil {
		jobs, err := w.jobs.Claim(ctx, w.types(), 1, w.config.Lease, w.now().UTC())
		if err != nil && ctx.Err() == nil {
			w.config.Logger.Warn("claiming processing jobs failed", "error", err)
		}
		if len(jobs) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(w.config.Poll):
			}
			continue
		}
		for _, job := range jobs {
			w.process(ctx, job)
		}
	}
}

func jobAttrs(job domain.ProcessingJob) []any {
	attrs := []any{"job_id", job.ID, "job_type", job.Type, "workspace_id", job.WorkspaceID, "meeting_id", job.MeetingID, "attempt", job.Attempts}
	if job.MediaAssetID != nil {
		attrs = append(attrs, "media_asset_id", *job.MediaAssetID)
	}
	return attrs
}

func (w *Worker) process(parent context.Context, job domain.ProcessingJob) {
	logger := w.config.Logger.With(jobAttrs(job)...)
	started := time.Now()
	handler := w.handlers[job.Type]
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)

	var progress atomic.Value
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(w.config.Heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeat:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				var value *float64
				if p, ok := progress.Load().(float64); ok {
					value = &p
				}
				owned, err := w.jobs.Heartbeat(parent, job.ID, job.Attempts, w.config.Lease, value, w.now().UTC())
				if err == nil && !owned {
					// Cancelled by a user or reclaimed after a lease expiry.
					cancel(errLeaseLost)
					return
				}
			}
		}
	}()

	logger.Info("processing job started")
	err := handler.Handle(ctx, job, func(value float64) { progress.Store(min(max(value, 0), 1)) })
	close(stopHeartbeat)
	<-heartbeatDone
	elapsed := time.Since(started)
	now := w.now().UTC()
	// Writes after the run use a fresh context so a shutdown still records the outcome.
	writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
	defer writeCancel()

	switch {
	case errors.Is(context.Cause(ctx), errLeaseLost):
		logger.Info("processing job stopped: cancelled or reclaimed", "duration", elapsed)
	case err == nil:
		if completeErr := w.jobs.Complete(writeCtx, job.ID, job.Attempts, now); completeErr != nil {
			logger.Warn("processing job finished but could not be marked complete", "duration", elapsed, "error", completeErr)
			return
		}
		logger.Info("processing job completed", "duration", elapsed)
	case parent.Err() != nil:
		// Server shutting down: hand the job back without spending an attempt.
		_ = w.jobs.Release(writeCtx, job.ID, job.Attempts, now)
		logger.Info("processing job released for shutdown", "duration", elapsed)
	default:
		var jobErr *JobError
		if !errors.As(err, &jobErr) {
			jobErr = transient("processing_failed", msgTranscription, err)
		}
		var retryAt *time.Time
		if jobErr.Retryable && job.Attempts < job.MaxAttempts {
			at := now.Add(policies[job.Type].delay(job.Attempts))
			retryAt = &at
		}
		internal := ""
		if jobErr.Err != nil {
			internal = jobErr.Err.Error()
		}
		if failErr := w.jobs.Fail(writeCtx, job.ID, job.Attempts, jobErr.Code, jobErr.Message, internal, retryAt, now); failErr != nil {
			logger.Warn("recording job failure failed", "error", failErr)
		}
		// The internal error may contain server paths; it is stored for
		// operators but logged only as a category here.
		logger.Warn("processing job failed", "duration", elapsed, "error_code", jobErr.Code, "will_retry", retryAt != nil)
	}
}
