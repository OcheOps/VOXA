package processing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

// JobError is a classified job failure. Code and Message are user-safe;
// Err is internal detail for operator logs only.
type JobError struct {
	Code      string
	Message   string
	Retryable bool
	// RetryAfter is a minimum delay requested by an external service.
	RetryAfter time.Duration
	Err        error
}

func (e *JobError) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Err.Error()
	}
	return e.Code
}

func (e *JobError) Unwrap() error { return e.Err }

func permanent(code, message string, err error) *JobError {
	return &JobError{Code: code, Message: message, Err: err}
}

func transient(code, message string, err error) *JobError {
	return &JobError{Code: code, Message: message, Retryable: true, Err: err}
}

const (
	msgInvalidMedia  = "This recording could not be read as audio."
	msgRemuxFailed   = "Preparing the recording for playback failed; the original recording is kept."
	msgTranscription = "Transcription failed."
	msgTimeout       = "Processing took too long."
	msgUnavailable   = "Transcription is not available on this server."
)

// Policy bounds retries per job type. Permanent errors never retry.
type Policy struct {
	MaxAttempts int
	Backoff     []time.Duration
}

var policies = map[domain.ProcessingJobType]Policy{
	domain.JobRemux:         {MaxAttempts: 3, Backoff: []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}},
	domain.JobTranscription: {MaxAttempts: 3, Backoff: []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute}},
	// Summaries call external providers: back off longer so a struggling
	// provider is not hammered (Retry-After can extend this).
	domain.JobSummary: {MaxAttempts: 3, Backoff: []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}},
}

func (p Policy) delay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	return p.Backoff[min(attempt, len(p.Backoff))-1]
}

func newID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(raw[:]), nil
}

// Queue creates jobs. Remux always runs for finalized recordings;
// transcription is enqueued only when the server can transcribe.
type Queue struct {
	jobs          repository.JobRepository
	now           func() time.Time
	Transcription bool
}

func NewQueue(jobs repository.JobRepository, transcription bool) *Queue {
	return &Queue{jobs: jobs, now: time.Now, Transcription: transcription}
}

// Enqueue adds a job for a ready asset. repository.ErrConflict means an
// equivalent job is already queued or running.
func (q *Queue) Enqueue(ctx context.Context, jobType domain.ProcessingJobType, asset domain.MediaAsset) (domain.ProcessingJob, error) {
	if asset.Status != domain.MediaAssetReady {
		return domain.ProcessingJob{}, fmt.Errorf("asset is not ready")
	}
	id, err := newID("job_")
	if err != nil {
		return domain.ProcessingJob{}, err
	}
	now := q.now().UTC().Truncate(time.Microsecond)
	assetID := asset.ID
	job := domain.ProcessingJob{ID: domain.ProcessingJobID(id), WorkspaceID: asset.WorkspaceID, MeetingID: asset.MeetingID, MediaAssetID: &assetID,
		Type: jobType, Status: domain.JobQueued, MaxAttempts: policies[jobType].MaxAttempts, RunAfter: now, CreatedAt: now, UpdatedAt: now}
	if err := q.jobs.Enqueue(ctx, job); err != nil {
		return domain.ProcessingJob{}, err
	}
	return job, nil
}

// EnqueueRemux is called when an upload is finalized. Duplicates are not errors.
func (q *Queue) EnqueueRemux(ctx context.Context, asset domain.MediaAsset) error {
	_, err := q.Enqueue(ctx, domain.JobRemux, asset)
	if errors.Is(err, repository.ErrConflict) {
		return nil
	}
	return err
}

func (q *Queue) enqueueTranscription(ctx context.Context, asset domain.MediaAsset) error {
	if !q.Transcription {
		return nil
	}
	_, err := q.Enqueue(ctx, domain.JobTranscription, asset)
	if errors.Is(err, repository.ErrConflict) {
		return nil
	}
	return err
}
