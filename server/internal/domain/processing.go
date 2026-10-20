package domain

import (
	"encoding/json"
	"time"
)

type ProcessingJobID string
type TranscriptID string

type ProcessingJobType string

const (
	JobRemux         ProcessingJobType = "remux"
	JobTranscription ProcessingJobType = "transcription"
	JobSummary       ProcessingJobType = "summary"
)

type ProcessingJobStatus string

const (
	JobQueued    ProcessingJobStatus = "queued"
	JobRunning   ProcessingJobStatus = "running"
	JobCompleted ProcessingJobStatus = "completed"
	JobFailed    ProcessingJobStatus = "failed"
	JobCancelled ProcessingJobStatus = "cancelled"
)

// ProcessingJob is one unit of asynchronous media work. ErrorCode and
// ErrorMessage are safe to show users; InternalError is for operators only
// and never leaves the server.
type ProcessingJob struct {
	ID             ProcessingJobID
	WorkspaceID    WorkspaceID
	MeetingID      MeetingID
	MediaAssetID   *MediaAssetID
	Type           ProcessingJobType
	Status         ProcessingJobStatus
	Attempts       int
	MaxAttempts    int
	RunAfter       time.Time
	LeaseExpiresAt *time.Time
	Progress       *float64
	ErrorCode      *string
	ErrorMessage   *string
	InternalError  *string
	CreatedAt      time.Time
	StartedAt      *time.Time
	CompletedAt    *time.Time
	UpdatedAt      time.Time
	// Parameters is an immutable snapshot taken at enqueue (summary jobs:
	// transcript, provider, model, template). Never sent to clients.
	Parameters json.RawMessage
	// Checkpoint holds resumable intermediate results written by the
	// owning attempt (summary chunk results). Never sent to clients.
	Checkpoint json.RawMessage
}

func (j ProcessingJob) Active() bool { return j.Status == JobQueued || j.Status == JobRunning }

// TranscriptSegment timing is relative to the start of the recording.
type TranscriptSegment struct {
	Sequence     int
	StartSeconds float64
	EndSeconds   float64
	Text         string
	// Confidence is nil when the engine provides no meaningful confidence.
	Confidence *float64
}

type Transcript struct {
	ID              TranscriptID
	WorkspaceID     WorkspaceID
	MeetingID       MeetingID
	MediaAssetID    MediaAssetID
	ProcessingJobID *ProcessingJobID
	Language        *string
	ASREngine       string
	ASRModel        string
	Segments        []TranscriptSegment
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
