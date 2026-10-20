package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

// JobRepository is the PostgreSQL processing queue (see 0005_processing.sql).
type JobRepository struct {
	pool *pgxpool.Pool
}

func NewJobRepository(pool *pgxpool.Pool) *JobRepository {
	return &JobRepository{pool: pool}
}

const jobColumns = `id, workspace_id, meeting_id, media_asset_id, type, status, attempts, max_attempts, run_after, lease_expires_at, progress,
error_code, error_message, internal_error, created_at, started_at, completed_at, updated_at`

func scanJob(row pgx.Row) (domain.ProcessingJob, error) {
	var job domain.ProcessingJob
	var id, workspaceID, meetingID, jobType, status string
	var assetID *string
	if err := row.Scan(&id, &workspaceID, &meetingID, &assetID, &jobType, &status, &job.Attempts, &job.MaxAttempts, &job.RunAfter,
		&job.LeaseExpiresAt, &job.Progress, &job.ErrorCode, &job.ErrorMessage, &job.InternalError, &job.CreatedAt, &job.StartedAt,
		&job.CompletedAt, &job.UpdatedAt); err != nil {
		return domain.ProcessingJob{}, err
	}
	job.ID, job.WorkspaceID, job.MeetingID = domain.ProcessingJobID(id), domain.WorkspaceID(workspaceID), domain.MeetingID(meetingID)
	job.Type, job.Status = domain.ProcessingJobType(jobType), domain.ProcessingJobStatus(status)
	if assetID != nil {
		value := domain.MediaAssetID(*assetID)
		job.MediaAssetID = &value
	}
	return job, nil
}

func collectJobs(rows pgx.Rows) ([]domain.ProcessingJob, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.ProcessingJob, error) { return scanJob(row) })
}

func (r *JobRepository) Enqueue(ctx context.Context, job domain.ProcessingJob) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	var assetID *string
	if job.MediaAssetID != nil {
		value := string(*job.MediaAssetID)
		assetID = &value
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO processing_jobs (id, workspace_id, meeting_id, media_asset_id, type, status, attempts, max_attempts, run_after, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'queued', 0, $6, $7, $8, $8)`,
		string(job.ID), string(job.WorkspaceID), string(job.MeetingID), assetID, string(job.Type), job.MaxAttempts, job.RunAfter, job.CreatedAt)
	if err != nil {
		return translate("enqueue job", err)
	}
	return nil
}

// Claim atomically leases runnable jobs. SKIP LOCKED lets concurrent workers
// (goroutines or processes) each take different rows without blocking.
func (r *JobRepository) Claim(ctx context.Context, types []domain.ProcessingJobType, limit int, lease time.Duration, now time.Time) ([]domain.ProcessingJob, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = string(t)
	}
	rows, err := r.pool.Query(ctx, `WITH next AS (
  SELECT id FROM processing_jobs
  WHERE type = ANY($1) AND attempts < max_attempts
    AND ((status = 'queued' AND run_after <= $3) OR (status = 'running' AND lease_expires_at < $3))
  ORDER BY run_after, created_at
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)
UPDATE processing_jobs j
SET status = 'running', attempts = j.attempts + 1, lease_expires_at = $4, started_at = COALESCE(j.started_at, $3), updated_at = $3
FROM next WHERE j.id = next.id
RETURNING `+prefixed("j.", jobColumns), names, limit, now, now.Add(lease))
	if err != nil {
		return nil, translate("claim jobs", err)
	}
	jobs, err := collectJobs(rows)
	if err != nil {
		return nil, translate("claim jobs", err)
	}
	return jobs, nil
}

func (r *JobRepository) Heartbeat(ctx context.Context, id domain.ProcessingJobID, attempt int, lease time.Duration, progress *float64, now time.Time) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tag, err := r.pool.Exec(ctx, `UPDATE processing_jobs SET lease_expires_at = $3, progress = COALESCE($4, progress), updated_at = $5
WHERE id = $1 AND attempts = $2 AND status = 'running'`, string(id), attempt, now.Add(lease), progress, now)
	if err != nil {
		return false, translate("job heartbeat", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *JobRepository) Complete(ctx context.Context, id domain.ProcessingJobID, attempt int, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tag, err := r.pool.Exec(ctx, `UPDATE processing_jobs
SET status = 'completed', completed_at = $3, lease_expires_at = NULL, progress = 1, error_code = NULL, error_message = NULL, updated_at = $3
WHERE id = $1 AND attempts = $2 AND status = 'running'`, string(id), attempt, now)
	if err != nil {
		return translate("complete job", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrConflict
	}
	return nil
}

func (r *JobRepository) Fail(ctx context.Context, id domain.ProcessingJobID, attempt int, code, message, internal string, retryAt *time.Time, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tag, err := r.pool.Exec(ctx, `UPDATE processing_jobs
SET status = CASE WHEN $6::timestamptz IS NOT NULL AND attempts < max_attempts THEN 'queued' ELSE 'failed' END,
    run_after = CASE WHEN $6::timestamptz IS NOT NULL AND attempts < max_attempts THEN $6::timestamptz ELSE run_after END,
    completed_at = CASE WHEN $6::timestamptz IS NOT NULL AND attempts < max_attempts THEN NULL::timestamptz ELSE $7::timestamptz END,
    error_code = $3::text, error_message = $4::text, internal_error = left($5::text, 4000), lease_expires_at = NULL, updated_at = $7::timestamptz
WHERE id = $1 AND attempts = $2 AND status = 'running'`, string(id), attempt, code, message, internal, retryAt, now)
	if err != nil {
		return translate("fail job", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrConflict
	}
	return nil
}

func (r *JobRepository) Release(ctx context.Context, id domain.ProcessingJobID, attempt int, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	if _, err := r.pool.Exec(ctx, `UPDATE processing_jobs SET status = 'queued', attempts = attempts - 1, lease_expires_at = NULL, run_after = $3, updated_at = $3
WHERE id = $1 AND attempts = $2 AND status = 'running'`, string(id), attempt, now); err != nil {
		return translate("release job", err)
	}
	return nil
}

func (r *JobRepository) Cancel(ctx context.Context, workspaceID domain.WorkspaceID, id domain.ProcessingJobID, now time.Time) (domain.ProcessingJob, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	job, err := scanJob(r.pool.QueryRow(ctx, `UPDATE processing_jobs SET status = 'cancelled', completed_at = $3, lease_expires_at = NULL, updated_at = $3
WHERE workspace_id = $1 AND id = $2 AND status IN ('queued', 'running') RETURNING `+jobColumns, string(workspaceID), string(id), now))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := r.GetJob(ctx, workspaceID, id); getErr != nil {
			return domain.ProcessingJob{}, getErr
		}
		return domain.ProcessingJob{}, repository.ErrConflict
	}
	if err != nil {
		return domain.ProcessingJob{}, translate("cancel job", err)
	}
	return job, nil
}

func (r *JobRepository) GetJob(ctx context.Context, workspaceID domain.WorkspaceID, id domain.ProcessingJobID) (domain.ProcessingJob, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	job, err := scanJob(r.pool.QueryRow(ctx, `SELECT `+jobColumns+` FROM processing_jobs WHERE workspace_id = $1 AND id = $2`, string(workspaceID), string(id)))
	if err != nil {
		return domain.ProcessingJob{}, translate("get job", err)
	}
	return job, nil
}

func (r *JobRepository) ListMeetingJobs(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) ([]domain.ProcessingJob, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := r.pool.Query(ctx, `SELECT `+jobColumns+` FROM processing_jobs WHERE workspace_id = $1 AND meeting_id = $2 ORDER BY created_at, id`, string(workspaceID), string(meetingID))
	if err != nil {
		return nil, translate("list meeting jobs", err)
	}
	jobs, err := collectJobs(rows)
	if err != nil {
		return nil, translate("list meeting jobs", err)
	}
	return jobs, nil
}

func (r *JobRepository) ListAssetJobs(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) ([]domain.ProcessingJob, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := r.pool.Query(ctx, `SELECT `+jobColumns+` FROM processing_jobs WHERE workspace_id = $1 AND media_asset_id = $2 ORDER BY created_at, id`, string(workspaceID), string(assetID))
	if err != nil {
		return nil, translate("list asset jobs", err)
	}
	jobs, err := collectJobs(rows)
	if err != nil {
		return nil, translate("list asset jobs", err)
	}
	return jobs, nil
}

func (r *JobRepository) FailExhausted(ctx context.Context, now time.Time) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tag, err := r.pool.Exec(ctx, `UPDATE processing_jobs
SET status = 'failed', completed_at = $1, lease_expires_at = NULL, updated_at = $1,
    error_code = 'abandoned', error_message = 'Processing stopped unexpectedly and was not retried.'
WHERE status = 'running' AND lease_expires_at < $1 AND attempts >= max_attempts`, now)
	if err != nil {
		return 0, translate("fail exhausted jobs", err)
	}
	return int(tag.RowsAffected()), nil
}

// prefixed qualifies a column list with a table alias.
func prefixed(prefix, columns string) string {
	fields := strings.FieldsFunc(columns, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
	for i, field := range fields {
		fields[i] = prefix + field
	}
	return strings.Join(fields, ", ")
}

// TranscriptRepository stores transcripts and their segments.
type TranscriptRepository struct {
	pool *pgxpool.Pool
}

func NewTranscriptRepository(pool *pgxpool.Pool) *TranscriptRepository {
	return &TranscriptRepository{pool: pool}
}

// Replace swaps the asset's transcript in one transaction; readers see the
// old or the new transcript, never a partial one.
func (r *TranscriptRepository) Replace(ctx context.Context, transcript domain.Transcript) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return translate("begin replace transcript", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `DELETE FROM transcripts WHERE workspace_id = $1 AND media_asset_id = $2`, string(transcript.WorkspaceID), string(transcript.MediaAssetID)); err != nil {
		return translate("replace transcript", err)
	}
	var jobID *string
	if transcript.ProcessingJobID != nil {
		value := string(*transcript.ProcessingJobID)
		jobID = &value
	}
	if _, err := tx.Exec(ctx, `INSERT INTO transcripts (id, workspace_id, meeting_id, media_asset_id, processing_job_id, language, asr_engine, asr_model, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		string(transcript.ID), string(transcript.WorkspaceID), string(transcript.MeetingID), string(transcript.MediaAssetID), jobID,
		transcript.Language, transcript.ASREngine, transcript.ASRModel, transcript.CreatedAt, transcript.UpdatedAt); err != nil {
		return translate("insert transcript", err)
	}
	rows := make([][]any, len(transcript.Segments))
	for i, segment := range transcript.Segments {
		rows[i] = []any{string(transcript.ID), segment.Sequence, segment.StartSeconds, segment.EndSeconds, segment.Text, segment.Confidence}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"transcript_segments"}, []string{"transcript_id", "sequence", "start_seconds", "end_seconds", "text", "confidence"}, pgx.CopyFromRows(rows)); err != nil {
		return translate("insert transcript segments", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return translate("commit transcript", err)
	}
	return nil
}

func (r *TranscriptRepository) GetByAsset(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (domain.Transcript, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	var t domain.Transcript
	var id, ws, meeting, asset string
	var jobID *string
	err := r.pool.QueryRow(ctx, `SELECT id, workspace_id, meeting_id, media_asset_id, processing_job_id, language, asr_engine, asr_model, created_at, updated_at
FROM transcripts WHERE workspace_id = $1 AND media_asset_id = $2`, string(workspaceID), string(assetID)).
		Scan(&id, &ws, &meeting, &asset, &jobID, &t.Language, &t.ASREngine, &t.ASRModel, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return domain.Transcript{}, translate("get transcript", err)
	}
	t.ID, t.WorkspaceID, t.MeetingID, t.MediaAssetID = domain.TranscriptID(id), domain.WorkspaceID(ws), domain.MeetingID(meeting), domain.MediaAssetID(asset)
	if jobID != nil {
		value := domain.ProcessingJobID(*jobID)
		t.ProcessingJobID = &value
	}
	rows, err := r.pool.Query(ctx, `SELECT sequence, start_seconds, end_seconds, text, confidence FROM transcript_segments WHERE transcript_id = $1 ORDER BY sequence`, id)
	if err != nil {
		return domain.Transcript{}, translate("get transcript segments", err)
	}
	t.Segments, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.TranscriptSegment, error) {
		var s domain.TranscriptSegment
		return s, row.Scan(&s.Sequence, &s.StartSeconds, &s.EndSeconds, &s.Text, &s.Confidence)
	})
	if err != nil {
		return domain.Transcript{}, translate("get transcript segments", err)
	}
	return t, nil
}

var _ repository.JobRepository = (*JobRepository)(nil)
var _ repository.TranscriptRepository = (*TranscriptRepository)(nil)
