package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

// SummaryRepository stores the current summary of each meeting (see
// 0006_summaries.sql).
type SummaryRepository struct {
	pool *pgxpool.Pool
}

func NewSummaryRepository(pool *pgxpool.Pool) *SummaryRepository {
	return &SummaryRepository{pool: pool}
}

const summaryColumns = `id, workspace_id, meeting_id, transcript_id, media_asset_id, processing_job_id, provider, model, template_id,
output_language, content, usage, created_at, updated_at`

// Promote locks the job row, verifies the caller still owns the running
// attempt, and upserts the summary in the same transaction. A cancel or
// reclaim racing with it either happens first (Promote fails) or waits for
// the commit.
func (r *SummaryRepository) Promote(ctx context.Context, summary domain.Summary, jobID domain.ProcessingJobID, attempt int) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return translate("begin promote summary", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var owned int
	err = tx.QueryRow(ctx, `SELECT 1 FROM processing_jobs
WHERE id = $1 AND attempts = $2 AND status = 'running' AND workspace_id = $3 AND meeting_id = $4 FOR UPDATE`,
		string(jobID), attempt, string(summary.WorkspaceID), string(summary.MeetingID)).Scan(&owned)
	if errors.Is(err, pgx.ErrNoRows) {
		return repository.ErrConflict
	}
	if err != nil {
		return translate("lock summary job", err)
	}
	var assetID, processingJobID *string
	if summary.MediaAssetID != nil {
		value := string(*summary.MediaAssetID)
		assetID = &value
	}
	if summary.ProcessingJobID != nil {
		value := string(*summary.ProcessingJobID)
		processingJobID = &value
	}
	var usage any
	if summary.Usage != nil {
		encoded, err := json.Marshal(summary.Usage)
		if err != nil {
			return err
		}
		usage = string(encoded)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO summaries (id, workspace_id, meeting_id, transcript_id, media_asset_id, processing_job_id, provider, model,
    template_id, output_language, content, usage, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12::jsonb, $13, $13)
ON CONFLICT (workspace_id, meeting_id) DO UPDATE SET
    id = EXCLUDED.id, transcript_id = EXCLUDED.transcript_id, media_asset_id = EXCLUDED.media_asset_id,
    processing_job_id = EXCLUDED.processing_job_id, provider = EXCLUDED.provider, model = EXCLUDED.model,
    template_id = EXCLUDED.template_id, output_language = EXCLUDED.output_language, content = EXCLUDED.content,
    usage = EXCLUDED.usage, updated_at = EXCLUDED.updated_at`,
		string(summary.ID), string(summary.WorkspaceID), string(summary.MeetingID), string(summary.TranscriptID), assetID, processingJobID,
		summary.Provider, summary.Model, summary.TemplateID, summary.OutputLanguage, jsonParam(summary.Content), usage, summary.UpdatedAt); err != nil {
		return translate("store summary", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return translate("commit summary", err)
	}
	return nil
}

func (r *SummaryRepository) GetByMeeting(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) (domain.Summary, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	var summary domain.Summary
	var id, ws, meeting, transcript string
	var assetID, jobID *string
	var usage []byte
	err := r.pool.QueryRow(ctx, `SELECT `+summaryColumns+` FROM summaries WHERE workspace_id = $1 AND meeting_id = $2`,
		string(workspaceID), string(meetingID)).Scan(&id, &ws, &meeting, &transcript, &assetID, &jobID, &summary.Provider, &summary.Model,
		&summary.TemplateID, &summary.OutputLanguage, &summary.Content, &usage, &summary.CreatedAt, &summary.UpdatedAt)
	if err != nil {
		return domain.Summary{}, translate("get summary", err)
	}
	summary.ID, summary.WorkspaceID, summary.MeetingID, summary.TranscriptID = domain.SummaryID(id), domain.WorkspaceID(ws), domain.MeetingID(meeting), domain.TranscriptID(transcript)
	if assetID != nil {
		value := domain.MediaAssetID(*assetID)
		summary.MediaAssetID = &value
	}
	if jobID != nil {
		value := domain.ProcessingJobID(*jobID)
		summary.ProcessingJobID = &value
	}
	if len(usage) > 0 {
		summary.Usage = &domain.SummaryUsage{}
		if err := json.Unmarshal(usage, summary.Usage); err != nil {
			summary.Usage = nil
		}
	}
	return summary, nil
}

var _ repository.SummaryRepository = (*SummaryRepository)(nil)
