package repository

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")

// ErrAlreadyInitialized is returned by IdentityRepository.Bootstrap when the
// deployment has already been bootstrapped.
var ErrAlreadyInitialized = errors.New("deployment already initialized")

// ErrUnavailable means the storage backend could not be reached or refused
// service (connection failure, timeout, shutdown). Callers may retry.
var ErrUnavailable = errors.New("storage unavailable")

type MeetingPosition struct {
	CreatedAt time.Time
	ID        domain.MeetingID
}

type MeetingRepository interface {
	List(context.Context, domain.WorkspaceID, *MeetingPosition, int) ([]domain.Meeting, bool, error)
	Create(context.Context, domain.Meeting) error
	Get(context.Context, domain.WorkspaceID, domain.MeetingID) (domain.Meeting, error)
	Update(context.Context, domain.Meeting) error
	Delete(context.Context, domain.WorkspaceID, domain.MeetingID) error
}

type ConfigurationRepository interface {
	Get(context.Context, domain.WorkspaceID) (domain.Configuration, error)
	Update(context.Context, domain.WorkspaceID, func(domain.Configuration) (domain.Configuration, error)) (domain.Configuration, error)
}

// MediaRepository stores recording sessions and media asset metadata. Media
// bytes live in a mediastore.Store; StorageKey links the two internally.
type MediaRepository interface {
	// CreateRecording atomically stores a new session and its uploading asset.
	CreateRecording(ctx context.Context, session domain.RecordingSession, asset domain.MediaAsset) error
	GetAsset(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (domain.MediaAsset, error)
	// ListMeetingAssets returns a meeting's assets ordered by (created_at, id).
	ListMeetingAssets(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) ([]domain.MediaAsset, error)
	// TouchUpload records upload activity on an uploading asset.
	TouchUpload(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, at time.Time) error
	// CompleteUpload marks an uploading asset ready and its session completed.
	// It returns ErrConflict when the asset is no longer uploading.
	CompleteUpload(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, sizeBytes int64, chunkCount int, durationSeconds *float64, at time.Time) (domain.MediaAsset, error)
	// FailUpload marks an uploading asset (and its session) failed; a no-op otherwise.
	FailUpload(ctx context.Context, assetID domain.MediaAssetID, at time.Time) error
	DeleteAsset(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (domain.MediaAsset, error)
	// ListStaleUploads returns uploading assets (any workspace) with no activity since before.
	ListStaleUploads(ctx context.Context, before time.Time) ([]domain.MediaAsset, error)
	// AssetStatusByStorageKey supports orphan cleanup: it matches the current
	// or original storage key; ErrNotFound when no asset uses the key.
	AssetStatusByStorageKey(ctx context.Context, storageKey string) (domain.MediaAssetStatus, error)
	// SetProcessedMedia switches a ready asset to a processed playback object,
	// keeping the previous object as the original upload.
	SetProcessedMedia(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, storageKey, mimeType string, sizeBytes int64, durationSeconds *float64, at time.Time) (domain.MediaAsset, error)
	// MarkProcessed records that processing finished without a new object
	// (e.g. remux failed permanently); the asset keeps its current object.
	MarkProcessed(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, durationSeconds *float64, at time.Time) error
	// ListUnprocessedReady returns ready assets (any workspace) with no
	// processing record, for queue reconciliation.
	ListUnprocessedReady(ctx context.Context, limit int) ([]domain.MediaAsset, error)
}

// JobRepository is the processing queue. Claims are exclusive: a claimed
// job carries its attempt number, which acts as a fencing token for
// Heartbeat/Complete/Fail/Release so a worker whose lease expired cannot
// overwrite a newer attempt.
type JobRepository interface {
	// Enqueue adds a job; ErrConflict when an active job of the same type
	// already exists for the media asset (or, for summaries, the meeting).
	Enqueue(ctx context.Context, job domain.ProcessingJob) error
	// Claim leases up to limit runnable jobs: queued jobs whose run_after has
	// passed, and running jobs whose lease expired (abandoned) with attempts
	// remaining. Each claim increments Attempts.
	Claim(ctx context.Context, types []domain.ProcessingJobType, limit int, lease time.Duration, now time.Time) ([]domain.ProcessingJob, error)
	// Heartbeat extends the lease and records progress. It reports false
	// when the job is no longer running under this attempt (cancelled,
	// reclaimed), which tells the worker to stop.
	Heartbeat(ctx context.Context, id domain.ProcessingJobID, attempt int, lease time.Duration, progress *float64, now time.Time) (bool, error)
	Complete(ctx context.Context, id domain.ProcessingJobID, attempt int, now time.Time) error
	// Fail records an error; with retryAt set (and attempts remaining) the
	// job is queued again at that time, otherwise it becomes failed.
	Fail(ctx context.Context, id domain.ProcessingJobID, attempt int, code, message, internal string, retryAt *time.Time, now time.Time) error
	// Release returns a job to the queue without consuming the attempt
	// (graceful worker shutdown).
	Release(ctx context.Context, id domain.ProcessingJobID, attempt int, now time.Time) error
	// Cancel stops a queued or running job; ErrConflict when it already ended.
	Cancel(ctx context.Context, workspaceID domain.WorkspaceID, id domain.ProcessingJobID, now time.Time) (domain.ProcessingJob, error)
	GetJob(ctx context.Context, workspaceID domain.WorkspaceID, id domain.ProcessingJobID) (domain.ProcessingJob, error)
	ListMeetingJobs(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) ([]domain.ProcessingJob, error)
	ListAssetJobs(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) ([]domain.ProcessingJob, error)
	// FailExhausted marks abandoned running jobs with no attempts left as failed.
	FailExhausted(ctx context.Context, now time.Time) (int, error)
	// SaveCheckpoint stores resumable intermediate results for the running
	// attempt; false when the job is no longer owned by it.
	SaveCheckpoint(ctx context.Context, id domain.ProcessingJobID, attempt int, checkpoint []byte, now time.Time) (bool, error)
}

// SummaryRepository stores one current summary per meeting.
type SummaryRepository interface {
	// Promote stores summary as the meeting's summary, replacing any previous
	// one, only while job jobID is still running under attempt (otherwise
	// ErrConflict and nothing changes). The previous summary therefore stays
	// until a newer job succeeds, and a stale or cancelled worker cannot
	// overwrite a newer result.
	Promote(ctx context.Context, summary domain.Summary, jobID domain.ProcessingJobID, attempt int) error
	GetByMeeting(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) (domain.Summary, error)
}

// TranscriptRepository stores one current transcript per media asset.
type TranscriptRepository interface {
	// Replace atomically stores transcript as the asset's transcript,
	// replacing any previous one (retranscription).
	Replace(ctx context.Context, transcript domain.Transcript) error
	GetByAsset(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (domain.Transcript, error)
}

// IdentityRepository stores Voxa users, workspace memberships and the
// one-time deployment bootstrap.
type IdentityRepository interface {
	// FindUser returns the user for an external identity or ErrNotFound.
	FindUser(ctx context.Context, issuer, subject string) (domain.User, error)
	// UpdateUserProfile replaces mutable profile fields. Identity fields and
	// the user ID never change.
	UpdateUserProfile(ctx context.Context, userID domain.UserID, email, displayName *string, at time.Time) error
	// ListMemberships returns the user's memberships ordered by workspace ID.
	ListMemberships(ctx context.Context, userID domain.UserID) ([]domain.Membership, error)
	// Bootstrap atomically creates the first user, the workspace (kept if it
	// already exists) and an owner membership, and marks the deployment
	// initialized. It returns ErrAlreadyInitialized if that already happened;
	// concurrent callers can never both succeed.
	Bootstrap(ctx context.Context, user domain.User, workspace domain.Workspace) error
	// IsInitialized reports whether Bootstrap has completed.
	IsInitialized(ctx context.Context) (bool, error)
}

// CredentialRepository manages credential metadata and write-only secret
// storage. Nothing in it returns secret material; HTTP-facing services
// depend only on this interface.
type CredentialRepository interface {
	List(context.Context, domain.WorkspaceID) ([]domain.CredentialStatus, error)
	IsConfigured(context.Context, domain.WorkspaceID, string) (bool, error)
	Set(context.Context, domain.WorkspaceID, string, domain.CredentialSecret) error
	Delete(context.Context, domain.WorkspaceID, string) error
}

// CredentialSecretReader returns decrypted credential material for internal
// provider execution. It is deliberately separate from CredentialRepository
// so metadata/HTTP code paths cannot reach it. Callers should Wipe the secret
// once the provider call is done.
type CredentialSecretReader interface {
	RevealCredential(context.Context, domain.WorkspaceID, string) (domain.CredentialSecret, error)
}

// CredentialUnreadableError reports a stored credential that exists but
// cannot be decrypted (unknown key version, tampering, corruption). It
// carries only non-secret metadata for logs.
type CredentialUnreadableError struct {
	WorkspaceID domain.WorkspaceID
	Provider    string
	KeyVersion  string
	Reason      error
}

func (e *CredentialUnreadableError) Error() string {
	return "stored credential cannot be decrypted: " + e.Reason.Error()
}
func (e *CredentialUnreadableError) Unwrap() error { return e.Reason }
func (e *CredentialUnreadableError) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("workspace_id", string(e.WorkspaceID)),
		slog.String("provider", e.Provider),
		slog.String("key_version", e.KeyVersion),
		slog.String("reason", e.Reason.Error()),
	)
}
