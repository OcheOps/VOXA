package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

const meetingColumns = `id, workspace_id, title, status, created_at, updated_at`

type MeetingRepository struct {
	pool *pgxpool.Pool
}

func NewMeetingRepository(pool *pgxpool.Pool) *MeetingRepository {
	return &MeetingRepository{pool: pool}
}

// List returns up to limit meetings ordered by (created_at, id), strictly
// after the given keyset position, fetching one extra row to report hasMore.
func (r *MeetingRepository) List(ctx context.Context, workspaceID domain.WorkspaceID, after *repository.MeetingPosition, limit int) ([]domain.Meeting, bool, error) {
	if limit < 1 {
		return nil, false, errors.New("postgres list meetings: limit must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	var rows pgx.Rows
	var err error
	if after == nil {
		rows, err = r.pool.Query(ctx, `SELECT `+meetingColumns+` FROM meetings
WHERE workspace_id = $1
ORDER BY created_at, id
LIMIT $2`, string(workspaceID), limit+1)
	} else {
		rows, err = r.pool.Query(ctx, `SELECT `+meetingColumns+` FROM meetings
WHERE workspace_id = $1 AND (created_at, id) > ($2, $3)
ORDER BY created_at, id
LIMIT $4`, string(workspaceID), after.CreatedAt, string(after.ID), limit+1)
	}
	if err != nil {
		return nil, false, translate("list meetings", err)
	}
	items, err := pgx.CollectRows(rows, scanMeeting)
	if err != nil {
		return nil, false, translate("list meetings", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func (r *MeetingRepository) Create(ctx context.Context, meeting domain.Meeting) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	_, err := r.pool.Exec(ctx, `INSERT INTO meetings (`+meetingColumns+`) VALUES ($1, $2, $3, $4, $5, $6)`,
		string(meeting.ID), string(meeting.WorkspaceID), meeting.Title, string(meeting.Status), meeting.CreatedAt, meeting.UpdatedAt)
	if err != nil {
		return translate("create meeting", err)
	}
	return nil
}

func (r *MeetingRepository) Get(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) (domain.Meeting, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := r.pool.Query(ctx, `SELECT `+meetingColumns+` FROM meetings WHERE workspace_id = $1 AND id = $2`, string(workspaceID), string(meetingID))
	if err != nil {
		return domain.Meeting{}, translate("get meeting", err)
	}
	meeting, err := pgx.CollectExactlyOneRow(rows, scanMeeting)
	if err != nil {
		return domain.Meeting{}, translate("get meeting", err)
	}
	return meeting, nil
}

// Update replaces the mutable fields of an existing meeting. created_at is
// immutable once stored.
func (r *MeetingRepository) Update(ctx context.Context, meeting domain.Meeting) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tag, err := r.pool.Exec(ctx, `UPDATE meetings SET title = $3, status = $4, updated_at = $5 WHERE workspace_id = $1 AND id = $2`,
		string(meeting.WorkspaceID), string(meeting.ID), meeting.Title, string(meeting.Status), meeting.UpdatedAt)
	if err != nil {
		return translate("update meeting", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func (r *MeetingRepository) Delete(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) error {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tag, err := r.pool.Exec(ctx, `DELETE FROM meetings WHERE workspace_id = $1 AND id = $2`, string(workspaceID), string(meetingID))
	if err != nil {
		return translate("delete meeting", err)
	}
	if tag.RowsAffected() == 0 {
		return repository.ErrNotFound
	}
	return nil
}

func scanMeeting(row pgx.CollectableRow) (domain.Meeting, error) {
	var id, workspaceID, status string
	var meeting domain.Meeting
	if err := row.Scan(&id, &workspaceID, &meeting.Title, &status, &meeting.CreatedAt, &meeting.UpdatedAt); err != nil {
		return domain.Meeting{}, err
	}
	meeting.ID = domain.MeetingID(id)
	meeting.WorkspaceID = domain.WorkspaceID(workspaceID)
	meeting.Status = domain.MeetingStatus(status)
	meeting.CreatedAt = meeting.CreatedAt.UTC()
	meeting.UpdatedAt = meeting.UpdatedAt.UTC()
	return meeting, nil
}
