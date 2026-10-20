package application

import (
	"context"
	"errors"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

// JobEnqueuer creates processing jobs (implemented by processing.Queue).
type JobEnqueuer interface {
	Enqueue(ctx context.Context, jobType domain.ProcessingJobType, asset domain.MediaAsset) (domain.ProcessingJob, error)
}

// ProcessingState is the user-facing summary of a recording's processing.
type ProcessingState string

const (
	StateNone         ProcessingState = "none"
	StatePreparing    ProcessingState = "preparing"
	StateTranscribing ProcessingState = "transcribing"
	StateReady        ProcessingState = "ready"
	StateFailed       ProcessingState = "failed"
	StateCancelled    ProcessingState = "cancelled"
	StateUnavailable  ProcessingState = "unavailable"
)

type AssetProcessing struct {
	Asset      domain.MediaAsset
	State      ProcessingState
	Jobs       []domain.ProcessingJob
	Transcript *domain.Transcript
	// LastFailure is the most recent failed or cancelled job, if it is the
	// latest job of its type.
	LastFailure *domain.ProcessingJob
}

// ProcessingService exposes processing state and controls to the API.
type ProcessingService struct {
	meetings               repository.MeetingRepository
	media                  repository.MediaRepository
	jobs                   repository.JobRepository
	transcripts            repository.TranscriptRepository
	queue                  JobEnqueuer
	transcriptionAvailable bool
	now                    func() time.Time
}

func NewProcessingService(meetings repository.MeetingRepository, media repository.MediaRepository, jobs repository.JobRepository,
	transcripts repository.TranscriptRepository, queue JobEnqueuer, transcriptionAvailable bool) *ProcessingService {
	return &ProcessingService{meetings: meetings, media: media, jobs: jobs, transcripts: transcripts, queue: queue,
		transcriptionAvailable: transcriptionAvailable, now: time.Now}
}

func (s *ProcessingService) TranscriptionAvailable() bool { return s.transcriptionAvailable }

func (s *ProcessingService) requireMeeting(ctx context.Context, op string, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) error {
	if _, err := s.meetings.Get(ctx, workspaceID, meetingID); errors.Is(err, repository.ErrNotFound) {
		return &Error{Code: NotFound, Op: op, Err: err}
	} else if err != nil {
		return storageError(op, err)
	}
	return nil
}

func (s *ProcessingService) MeetingJobs(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) ([]domain.ProcessingJob, error) {
	const op = "processing.list_jobs"
	if err := s.requireMeeting(ctx, op, workspaceID, meetingID); err != nil {
		return nil, err
	}
	jobs, err := s.jobs.ListMeetingJobs(ctx, workspaceID, meetingID)
	if err != nil {
		return nil, storageError(op, err)
	}
	return jobs, nil
}

func (s *ProcessingService) AssetProcessing(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (AssetProcessing, error) {
	const op = "processing.asset"
	asset, err := s.media.GetAsset(ctx, workspaceID, assetID)
	if errors.Is(err, repository.ErrNotFound) {
		return AssetProcessing{}, &Error{Code: NotFound, Op: op, Err: err}
	}
	if err != nil {
		return AssetProcessing{}, storageError(op, err)
	}
	return s.describe(ctx, op, asset)
}

// MeetingTranscript describes the meeting's most recent ready recording, or
// returns ok=false when the meeting has none.
func (s *ProcessingService) MeetingTranscript(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) (AssetProcessing, bool, error) {
	const op = "processing.meeting_transcript"
	if err := s.requireMeeting(ctx, op, workspaceID, meetingID); err != nil {
		return AssetProcessing{}, false, err
	}
	assets, err := s.media.ListMeetingAssets(ctx, workspaceID, meetingID)
	if err != nil {
		return AssetProcessing{}, false, storageError(op, err)
	}
	for i := len(assets) - 1; i >= 0; i-- {
		if assets[i].Status == domain.MediaAssetReady {
			processing, err := s.describe(ctx, op, assets[i])
			return processing, err == nil, err
		}
	}
	return AssetProcessing{}, false, nil
}

func (s *ProcessingService) describe(ctx context.Context, op string, asset domain.MediaAsset) (AssetProcessing, error) {
	jobs, err := s.jobs.ListAssetJobs(ctx, asset.WorkspaceID, asset.ID)
	if err != nil {
		return AssetProcessing{}, storageError(op, err)
	}
	result := AssetProcessing{Asset: asset, Jobs: jobs}
	transcript, err := s.transcripts.GetByAsset(ctx, asset.WorkspaceID, asset.ID)
	if err == nil {
		result.Transcript = &transcript
	} else if !errors.Is(err, repository.ErrNotFound) {
		return AssetProcessing{}, storageError(op, err)
	}
	result.State, result.LastFailure = s.state(asset, jobs, result.Transcript != nil)
	return result, nil
}

func latest(jobs []domain.ProcessingJob, jobType domain.ProcessingJobType) *domain.ProcessingJob {
	var found *domain.ProcessingJob
	for i := range jobs {
		if jobs[i].Type == jobType {
			found = &jobs[i]
		}
	}
	return found
}

func (s *ProcessingService) state(asset domain.MediaAsset, jobs []domain.ProcessingJob, hasTranscript bool) (ProcessingState, *domain.ProcessingJob) {
	if asset.Status != domain.MediaAssetReady {
		return StateNone, nil
	}
	remux, transcription := latest(jobs, domain.JobRemux), latest(jobs, domain.JobTranscription)
	switch {
	case remux != nil && remux.Active():
		return StatePreparing, nil
	case transcription != nil && transcription.Active():
		return StateTranscribing, nil
	case transcription != nil && transcription.Status == domain.JobFailed:
		return StateFailed, transcription
	case transcription != nil && transcription.Status == domain.JobCancelled && !hasTranscript:
		return StateCancelled, transcription
	case hasTranscript:
		return StateReady, nil
	case remux != nil && remux.Status == domain.JobFailed && remux.ErrorCode != nil && *remux.ErrorCode == "invalid_media":
		return StateFailed, remux
	case remux != nil && remux.Status == domain.JobCancelled:
		return StateCancelled, remux
	case !s.transcriptionAvailable:
		return StateUnavailable, nil
	case asset.ProcessedAt == nil:
		return StatePreparing, nil
	}
	// Processed, transcription available, but no job yet (e.g. enabled after
	// the recording was made): the user can request one.
	return StateNone, nil
}

// RequestTranscription (re)transcribes a ready recording.
func (s *ProcessingService) RequestTranscription(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (domain.ProcessingJob, error) {
	const op = "processing.request_transcription"
	if !s.transcriptionAvailable {
		return domain.ProcessingJob{}, &Error{Code: Unavailable, Op: op, Category: "transcription_disabled"}
	}
	asset, err := s.media.GetAsset(ctx, workspaceID, assetID)
	if errors.Is(err, repository.ErrNotFound) {
		return domain.ProcessingJob{}, &Error{Code: NotFound, Op: op, Err: err}
	}
	if err != nil {
		return domain.ProcessingJob{}, storageError(op, err)
	}
	if asset.Status != domain.MediaAssetReady {
		return domain.ProcessingJob{}, &Error{Code: Conflict, Op: op, Details: map[string]any{"reason": "not_ready"}}
	}
	job, err := s.queue.Enqueue(ctx, domain.JobTranscription, asset)
	if errors.Is(err, repository.ErrConflict) {
		return domain.ProcessingJob{}, &Error{Code: Conflict, Op: op, Details: map[string]any{"reason": "already_processing"}}
	}
	if err != nil {
		return domain.ProcessingJob{}, storageError(op, err)
	}
	return job, nil
}

// CancelJob stops a queued or running job. Running work stops at the
// worker's next heartbeat (about 10 s), which terminates the child process.
func (s *ProcessingService) CancelJob(ctx context.Context, workspaceID domain.WorkspaceID, jobID domain.ProcessingJobID) (domain.ProcessingJob, error) {
	const op = "processing.cancel"
	job, err := s.jobs.Cancel(ctx, workspaceID, jobID, s.now().UTC().Truncate(time.Microsecond))
	switch {
	case errors.Is(err, repository.ErrNotFound):
		return domain.ProcessingJob{}, &Error{Code: NotFound, Op: op, Err: err}
	case errors.Is(err, repository.ErrConflict):
		return domain.ProcessingJob{}, &Error{Code: Conflict, Op: op, Details: map[string]any{"reason": "already_finished"}}
	case err != nil:
		return domain.ProcessingJob{}, storageError(op, err)
	}
	if job.Type == domain.JobRemux && job.MediaAssetID != nil {
		// Keep the original for playback and stop reconciliation re-queuing it.
		_ = s.media.MarkProcessed(ctx, workspaceID, *job.MediaAssetID, nil, s.now().UTC().Truncate(time.Microsecond))
	}
	return job, nil
}
