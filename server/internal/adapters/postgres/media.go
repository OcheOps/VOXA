package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

type MediaRepository struct {
	pool *pgxpool.Pool
}

func NewMediaRepository(pool *pgxpool.Pool) *MediaRepository {
	return &MediaRepository{pool: pool}
}

const assetColumns = `id, workspace_id, meeting_id, recording_session_id, kind, mime_type, status, size_bytes, duration_seconds, chunk_count, storage_key, created_at, updated_at, original_storage_key, processed_at`

func scanAsset(row pgx.Row) (domain.MediaAsset, error) {
	var asset domain.MediaAsset
	var id, workspaceID, meetingID, kind, status string
	var sessionID *string
	if err := row.Scan(&id, &workspaceID, &meetingID, &sessionID, &kind, &asset.MimeType, &status, &asset.SizeBytes,
		&asset.DurationSeconds, &asset.ChunkCount, &asset.StorageKey, &asset.CreatedAt, &asset.UpdatedAt, &asset.OriginalStorageKey, &asset.ProcessedAt); err != nil {
		return domain.MediaAsset{}, err
	}
	asset.ID, asset.WorkspaceID, asset.MeetingID = domain.MediaAssetID(id), domain.WorkspaceID(workspaceID), domain.MeetingID(meetingID)
	asset.Kind, asset.Status = domain.MediaAssetKind(kind), domain.MediaAssetStatus(status)
	if sessionID != nil {
		value := domain.RecordingSessionID(*sessionID)
		asset.RecordingSessionID = &value
	}
	asset.CreatedAt, asset.UpdatedAt = asset.CreatedAt.UTC(), asset.UpdatedAt.UTC()
	return asset, nil
}

func (r *MediaRepository) CreateRecording(ctx context.Context, session domain.RecordingSession, asset domain.MediaAsset) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return translate("begin create recording", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `INSERT INTO recording_sessions (id, workspace_id, meeting_id, status, started_at, ended_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		string(session.ID), string(session.WorkspaceID), string(session.MeetingID), string(session.Status),
		session.StartedAt, session.EndedAt, session.CreatedAt, session.UpdatedAt); err != nil {
		return translate("create recording session", err)
	}
	var sessionID *string
	if asset.RecordingSessionID != nil {
		value := string(*asset.RecordingSessionID)
		sessionID = &value
	}
	if _, err := tx.Exec(ctx, `INSERT INTO media_assets (`+assetColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		string(asset.ID), string(asset.WorkspaceID), string(asset.MeetingID), sessionID, string(asset.Kind), asset.MimeType, string(asset.Status),
		asset.SizeBytes, asset.DurationSeconds, asset.ChunkCount, asset.StorageKey, asset.CreatedAt, asset.UpdatedAt, asset.OriginalStorageKey, asset.ProcessedAt); err != nil {
		return translate("create media asset", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return translate("commit create recording", err)
	}
	return nil
}

func (r *MediaRepository) GetAsset(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (domain.MediaAsset, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	asset, err := scanAsset(r.pool.QueryRow(ctx, `SELECT `+assetColumns+` FROM media_assets WHERE workspace_id = $1 AND id = $2`, string(workspaceID), string(assetID)))
	if err != nil {
		return domain.MediaAsset{}, translate("get media asset", err)
	}
	return asset, nil
}

func (r *MediaRepository) ListMeetingAssets(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) ([]domain.MediaAsset, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := r.pool.Query(ctx, `SELECT `+assetColumns+` FROM media_assets WHERE workspace_id = $1 AND meeting_id = $2 ORDER BY created_at, id`,
		string(workspaceID), string(meetingID))
	if err != nil {
		return nil, translate("list media assets", err)
	}
	assets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.MediaAsset, error) { return scanAsset(row) })
	if err != nil {
		return nil, translate("list media assets", err)
	}
	return assets, nil
}

func (r *MediaRepository) TouchUpload(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, at time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tag, err := r.pool.Exec(ctx, `UPDATE media_assets SET updated_at = $3 WHERE workspace_id = $1 AND id = $2 AND status = 'uploading'`,
		string(workspaceID), string(assetID), at)
	if err != nil {
		return translate("touch upload", err)
	}
	if tag.RowsAffected() == 0 {
		if _, err := r.GetAsset(ctx, workspaceID, assetID); err != nil {
			return err
		}
	}
	return nil
}

// CompleteUpload transitions uploading -> ready and closes the session in
// one transaction; the status predicate makes concurrent finalizations safe.
func (r *MediaRepository) CompleteUpload(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, sizeBytes int64, chunkCount int, durationSeconds *float64, at time.Time) (domain.MediaAsset, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.MediaAsset{}, translate("begin complete upload", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	asset, err := scanAsset(tx.QueryRow(ctx, `UPDATE media_assets
SET status = 'ready', size_bytes = $3, chunk_count = $4, duration_seconds = $5, updated_at = $6
WHERE workspace_id = $1 AND id = $2 AND status = 'uploading'
RETURNING `+assetColumns, string(workspaceID), string(assetID), sizeBytes, chunkCount, durationSeconds, at))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := r.GetAsset(ctx, workspaceID, assetID); getErr != nil {
			return domain.MediaAsset{}, getErr
		}
		return domain.MediaAsset{}, repository.ErrConflict
	}
	if err != nil {
		return domain.MediaAsset{}, translate("complete upload", err)
	}
	if asset.RecordingSessionID != nil {
		if _, err := tx.Exec(ctx, `UPDATE recording_sessions SET status = 'completed', ended_at = $2, updated_at = $2 WHERE id = $1 AND status = 'recording'`,
			string(*asset.RecordingSessionID), at); err != nil {
			return domain.MediaAsset{}, translate("complete recording session", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.MediaAsset{}, translate("commit complete upload", err)
	}
	return asset, nil
}

func (r *MediaRepository) FailUpload(ctx context.Context, assetID domain.MediaAssetID, at time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	_, err := r.pool.Exec(ctx, `WITH failed AS (
  UPDATE media_assets SET status = 'failed', updated_at = $2 WHERE id = $1 AND status = 'uploading' RETURNING recording_session_id
)
UPDATE recording_sessions SET status = 'failed', ended_at = $2, updated_at = $2
WHERE id IN (SELECT recording_session_id FROM failed) AND status = 'recording'`, string(assetID), at)
	if err != nil {
		return translate("fail upload", err)
	}
	return nil
}

func (r *MediaRepository) DeleteAsset(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (domain.MediaAsset, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	asset, err := scanAsset(r.pool.QueryRow(ctx, `DELETE FROM media_assets WHERE workspace_id = $1 AND id = $2 RETURNING `+assetColumns,
		string(workspaceID), string(assetID)))
	if err != nil {
		return domain.MediaAsset{}, translate("delete media asset", err)
	}
	return asset, nil
}

func (r *MediaRepository) ListStaleUploads(ctx context.Context, before time.Time) ([]domain.MediaAsset, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := r.pool.Query(ctx, `SELECT `+assetColumns+` FROM media_assets WHERE status = 'uploading' AND updated_at < $1 ORDER BY updated_at LIMIT 1000`, before)
	if err != nil {
		return nil, translate("list stale uploads", err)
	}
	assets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.MediaAsset, error) { return scanAsset(row) })
	if err != nil {
		return nil, translate("list stale uploads", err)
	}
	return assets, nil
}

func (r *MediaRepository) AssetStatusByStorageKey(ctx context.Context, storageKey string) (domain.MediaAssetStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	var status string
	if err := r.pool.QueryRow(ctx, `SELECT status FROM media_assets WHERE storage_key = $1 OR original_storage_key = $1`, storageKey).Scan(&status); err != nil {
		return "", translate("asset by storage key", err)
	}
	return domain.MediaAssetStatus(status), nil
}

func (r *MediaRepository) SetProcessedMedia(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, storageKey, mimeType string, sizeBytes int64, durationSeconds *float64, at time.Time) (domain.MediaAsset, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	asset, err := scanAsset(r.pool.QueryRow(ctx, `UPDATE media_assets
SET original_storage_key = COALESCE(original_storage_key, storage_key), storage_key = $3, mime_type = $4, size_bytes = $5,
    duration_seconds = COALESCE($6, duration_seconds), processed_at = $7, updated_at = $7
WHERE workspace_id = $1 AND id = $2 AND status = 'ready'
RETURNING `+assetColumns, string(workspaceID), string(assetID), storageKey, mimeType, sizeBytes, durationSeconds, at))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := r.GetAsset(ctx, workspaceID, assetID); getErr != nil {
			return domain.MediaAsset{}, getErr
		}
		return domain.MediaAsset{}, repository.ErrConflict
	}
	if err != nil {
		return domain.MediaAsset{}, translate("set processed media", err)
	}
	return asset, nil
}

func (r *MediaRepository) MarkProcessed(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, durationSeconds *float64, at time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tag, err := r.pool.Exec(ctx, `UPDATE media_assets SET processed_at = $3, duration_seconds = COALESCE($4, duration_seconds), updated_at = $3 WHERE workspace_id = $1 AND id = $2`,
		string(workspaceID), string(assetID), at, durationSeconds)
	if err != nil {
		return translate("mark processed", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *MediaRepository) ListUnprocessedReady(ctx context.Context, limit int) ([]domain.MediaAsset, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := r.pool.Query(ctx, `SELECT `+assetColumns+` FROM media_assets WHERE status = 'ready' AND processed_at IS NULL ORDER BY created_at LIMIT $1`, limit)
	if err != nil {
		return nil, translate("list unprocessed media", err)
	}
	assets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.MediaAsset, error) { return scanAsset(row) })
	if err != nil {
		return nil, translate("list unprocessed media", err)
	}
	return assets, nil
}

// RecordingSession reads a session (tests and future APIs).
func (r *MediaRepository) RecordingSession(ctx context.Context, workspaceID domain.WorkspaceID, id domain.RecordingSessionID) (domain.RecordingSession, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	var session domain.RecordingSession
	var sid, ws, meeting, status string
	err := r.pool.QueryRow(ctx, `SELECT id, workspace_id, meeting_id, status, started_at, ended_at, created_at, updated_at FROM recording_sessions WHERE workspace_id = $1 AND id = $2`,
		string(workspaceID), string(id)).Scan(&sid, &ws, &meeting, &status, &session.StartedAt, &session.EndedAt, &session.CreatedAt, &session.UpdatedAt)
	if err != nil {
		return domain.RecordingSession{}, translate("get recording session", err)
	}
	session.ID, session.WorkspaceID, session.MeetingID, session.Status = domain.RecordingSessionID(sid), domain.WorkspaceID(ws), domain.MeetingID(meeting), domain.RecordingSessionStatus(status)
	return session, nil
}

var _ repository.MediaRepository = (*MediaRepository)(nil)
