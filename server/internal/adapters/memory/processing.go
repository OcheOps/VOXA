package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

// JobRepository is an in-process processing queue for tests and
// development. It provides the same claim/lease/fencing semantics as the
// PostgreSQL queue within one process.
type JobRepository struct {
	mu   sync.Mutex
	jobs map[domain.ProcessingJobID]domain.ProcessingJob
}

func NewJobRepository() *JobRepository {
	return &JobRepository{jobs: map[domain.ProcessingJobID]domain.ProcessingJob{}}
}

func (r *JobRepository) Enqueue(_ context.Context, job domain.ProcessingJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.jobs[job.ID]; exists {
		return repository.ErrConflict
	}
	for _, existing := range r.jobs {
		if existing.Active() && existing.Type == job.Type && existing.MediaAssetID != nil && job.MediaAssetID != nil && *existing.MediaAssetID == *job.MediaAssetID {
			return repository.ErrConflict
		}
	}
	r.jobs[job.ID] = job
	return nil
}

func (r *JobRepository) Claim(_ context.Context, types []domain.ProcessingJobType, limit int, lease time.Duration, now time.Time) ([]domain.ProcessingJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	wanted := map[domain.ProcessingJobType]bool{}
	for _, t := range types {
		wanted[t] = true
	}
	var candidates []domain.ProcessingJob
	for _, job := range r.jobs {
		if !wanted[job.Type] || job.Attempts >= job.MaxAttempts {
			continue
		}
		runnable := job.Status == domain.JobQueued && !job.RunAfter.After(now)
		abandoned := job.Status == domain.JobRunning && job.LeaseExpiresAt != nil && job.LeaseExpiresAt.Before(now)
		if runnable || abandoned {
			candidates = append(candidates, job)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].RunAfter.Equal(candidates[j].RunAfter) {
			return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
		}
		return candidates[i].RunAfter.Before(candidates[j].RunAfter)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	for i := range candidates {
		job := candidates[i]
		expires := now.Add(lease)
		job.Status, job.Attempts, job.LeaseExpiresAt, job.UpdatedAt = domain.JobRunning, job.Attempts+1, &expires, now
		if job.StartedAt == nil {
			job.StartedAt = &now
		}
		r.jobs[job.ID] = job
		candidates[i] = job
	}
	return candidates, nil
}

func (r *JobRepository) owned(id domain.ProcessingJobID, attempt int) (domain.ProcessingJob, bool) {
	job, ok := r.jobs[id]
	return job, ok && job.Status == domain.JobRunning && job.Attempts == attempt
}

func (r *JobRepository) Heartbeat(_ context.Context, id domain.ProcessingJobID, attempt int, lease time.Duration, progress *float64, now time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.owned(id, attempt)
	if !ok {
		return false, nil
	}
	expires := now.Add(lease)
	job.LeaseExpiresAt, job.UpdatedAt = &expires, now
	if progress != nil {
		job.Progress = progress
	}
	r.jobs[id] = job
	return true, nil
}

func (r *JobRepository) Complete(_ context.Context, id domain.ProcessingJobID, attempt int, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.owned(id, attempt)
	if !ok {
		return repository.ErrConflict
	}
	done := 1.0
	job.Status, job.CompletedAt, job.LeaseExpiresAt, job.UpdatedAt, job.Progress = domain.JobCompleted, &now, nil, now, &done
	job.ErrorCode, job.ErrorMessage = nil, nil
	r.jobs[id] = job
	return nil
}

func (r *JobRepository) Fail(_ context.Context, id domain.ProcessingJobID, attempt int, code, message, internal string, retryAt *time.Time, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.owned(id, attempt)
	if !ok {
		return repository.ErrConflict
	}
	job.ErrorCode, job.ErrorMessage, job.InternalError, job.LeaseExpiresAt, job.UpdatedAt = &code, &message, &internal, nil, now
	if retryAt != nil && job.Attempts < job.MaxAttempts {
		job.Status, job.RunAfter = domain.JobQueued, *retryAt
	} else {
		job.Status, job.CompletedAt = domain.JobFailed, &now
	}
	r.jobs[id] = job
	return nil
}

func (r *JobRepository) Release(_ context.Context, id domain.ProcessingJobID, attempt int, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.owned(id, attempt)
	if !ok {
		return nil
	}
	job.Status, job.Attempts, job.LeaseExpiresAt, job.RunAfter, job.UpdatedAt = domain.JobQueued, job.Attempts-1, nil, now, now
	r.jobs[id] = job
	return nil
}

func (r *JobRepository) Cancel(_ context.Context, workspaceID domain.WorkspaceID, id domain.ProcessingJobID, now time.Time) (domain.ProcessingJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok || job.WorkspaceID != workspaceID {
		return domain.ProcessingJob{}, repository.ErrNotFound
	}
	if !job.Active() {
		return domain.ProcessingJob{}, repository.ErrConflict
	}
	job.Status, job.CompletedAt, job.LeaseExpiresAt, job.UpdatedAt = domain.JobCancelled, &now, nil, now
	r.jobs[id] = job
	return job, nil
}

func (r *JobRepository) GetJob(_ context.Context, workspaceID domain.WorkspaceID, id domain.ProcessingJobID) (domain.ProcessingJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok || job.WorkspaceID != workspaceID {
		return domain.ProcessingJob{}, repository.ErrNotFound
	}
	return job, nil
}

func (r *JobRepository) list(match func(domain.ProcessingJob) bool) []domain.ProcessingJob {
	items := []domain.ProcessingJob{}
	for _, job := range r.jobs {
		if match(job) {
			items = append(items, job)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items
}

func (r *JobRepository) ListMeetingJobs(_ context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) ([]domain.ProcessingJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.list(func(j domain.ProcessingJob) bool { return j.WorkspaceID == workspaceID && j.MeetingID == meetingID }), nil
}

func (r *JobRepository) ListAssetJobs(_ context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) ([]domain.ProcessingJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.list(func(j domain.ProcessingJob) bool {
		return j.WorkspaceID == workspaceID && j.MediaAssetID != nil && *j.MediaAssetID == assetID
	}), nil
}

func (r *JobRepository) FailExhausted(_ context.Context, now time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	failed := 0
	for id, job := range r.jobs {
		if job.Status == domain.JobRunning && job.LeaseExpiresAt != nil && job.LeaseExpiresAt.Before(now) && job.Attempts >= job.MaxAttempts {
			code, message := "abandoned", "Processing stopped unexpectedly and was not retried."
			job.Status, job.CompletedAt, job.LeaseExpiresAt, job.UpdatedAt, job.ErrorCode, job.ErrorMessage = domain.JobFailed, &now, nil, now, &code, &message
			r.jobs[id] = job
			failed++
		}
	}
	return failed, nil
}

// TranscriptRepository keeps one transcript per media asset in memory.
type TranscriptRepository struct {
	mu          sync.Mutex
	transcripts map[domain.MediaAssetID]domain.Transcript
}

func NewTranscriptRepository() *TranscriptRepository {
	return &TranscriptRepository{transcripts: map[domain.MediaAssetID]domain.Transcript{}}
}

func (r *TranscriptRepository) Replace(_ context.Context, transcript domain.Transcript) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	transcript.Segments = append([]domain.TranscriptSegment(nil), transcript.Segments...)
	r.transcripts[transcript.MediaAssetID] = transcript
	return nil
}

func (r *TranscriptRepository) GetByAsset(_ context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (domain.Transcript, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	transcript, ok := r.transcripts[assetID]
	if !ok || transcript.WorkspaceID != workspaceID {
		return domain.Transcript{}, repository.ErrNotFound
	}
	transcript.Segments = append([]domain.TranscriptSegment(nil), transcript.Segments...)
	return transcript, nil
}

var _ repository.JobRepository = (*JobRepository)(nil)
var _ repository.TranscriptRepository = (*TranscriptRepository)(nil)
