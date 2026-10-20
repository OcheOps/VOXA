package postgres_test

import (
	"context"
	"fmt"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OcheOps/voxa/server/internal/adapters/postgres"
	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
	"github.com/OcheOps/voxa/server/internal/repository/repositorytest"
	"github.com/OcheOps/voxa/server/migrations"
)

func provisionMediaMeetings(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	meetings := postgres.NewMeetingRepository(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, m := range []struct {
		ws domain.WorkspaceID
		id domain.MeetingID
	}{{repositorytest.WorkspaceA, repositorytest.MeetingA}, {repositorytest.WorkspaceB, repositorytest.MeetingB}} {
		if err := meetings.Create(context.Background(), domain.Meeting{ID: m.id, WorkspaceID: m.ws, Title: "Media", Status: domain.MeetingCreated, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMediaRepositoryContract(t *testing.T) {
	repositorytest.MediaRepositoryContract(t, func(t *testing.T) repository.MediaRepository {
		pool := migratedPool(t)
		provisionMediaMeetings(t, pool)
		return postgres.NewMediaRepository(pool)
	})
}

func TestMediaRowsFollowMeetingOwnershipAndDeletion(t *testing.T) {
	pool := migratedPool(t)
	provisionMediaMeetings(t, pool)
	ctx := context.Background()
	repo := postgres.NewMediaRepository(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	sessionID := domain.RecordingSessionID("rec_x")
	session := domain.RecordingSession{ID: sessionID, WorkspaceID: repositorytest.WorkspaceA, MeetingID: repositorytest.MeetingA, Status: domain.RecordingSessionRecording, StartedAt: now, CreatedAt: now, UpdatedAt: now}
	asset := domain.MediaAsset{ID: "med_x", WorkspaceID: repositorytest.WorkspaceA, MeetingID: repositorytest.MeetingA, RecordingSessionID: &sessionID,
		Kind: domain.MediaAssetAudio, MimeType: "audio/webm", Status: domain.MediaAssetUploading, StorageKey: "obj_x", CreatedAt: now, UpdatedAt: now}

	// An asset cannot reference another workspace's meeting.
	foreign := asset
	foreign.ID, foreign.StorageKey, foreign.WorkspaceID = "med_foreign", "obj_foreign", repositorytest.WorkspaceB
	foreignSession := session
	foreignSession.ID, foreignSession.WorkspaceID = "rec_foreign", repositorytest.WorkspaceB
	foreignSessionID := foreignSession.ID
	foreign.RecordingSessionID = &foreignSessionID
	if err := repo.CreateRecording(ctx, foreignSession, foreign); err == nil {
		t.Fatal("asset attached to another workspace's meeting")
	}
	if err := repo.CreateRecording(ctx, session, asset); err != nil {
		t.Fatal(err)
	}
	ready, err := repo.CompleteUpload(ctx, repositorytest.WorkspaceA, asset.ID, 10, 1, nil, now)
	if err != nil || ready.Status != domain.MediaAssetReady {
		t.Fatalf("ready=%#v err=%v", ready, err)
	}
	stored, err := repo.RecordingSession(ctx, repositorytest.WorkspaceA, sessionID)
	if err != nil || stored.Status != domain.RecordingSessionCompleted || stored.EndedAt == nil {
		t.Fatalf("session=%#v err=%v", stored, err)
	}
	// Deleting the meeting removes its media rows (bytes become orphans for cleanup).
	if err := postgres.NewMeetingRepository(pool).Delete(ctx, repositorytest.WorkspaceA, repositorytest.MeetingA); err != nil {
		t.Fatal(err)
	}
	if count(t, pool, "media_assets") != 0 || count(t, pool, "recording_sessions") != 0 {
		t.Fatal("media rows survived meeting deletion")
	}
	if _, err := repo.AssetStatusByStorageKey(ctx, "obj_x"); err == nil {
		t.Fatal("orphaned storage key still resolves")
	}
}

func TestDatabaseAtIdentityVersionUpgradesToMedia(t *testing.T) {
	pool := isolatedPool(t)
	ctx := context.Background()
	previous := fstest.MapFS{}
	for _, name := range []string{"0001_initial.sql", "0002_workspace_credentials.sql", "0003_identity.sql"} {
		data, err := fs.ReadFile(migrations.Files, name)
		if err != nil {
			t.Fatal(err)
		}
		previous[name] = &fstest.MapFile{Data: data}
	}
	if _, err := postgres.Migrate(ctx, pool, previous); err != nil {
		t.Fatal(err)
	}
	if _, err := postgres.EnsureWorkspace(ctx, pool, repositorytest.WorkspaceA); err != nil {
		t.Fatal(err)
	}
	provisionMediaMeetingsSingle(t, pool)
	applied, err := postgres.Migrate(ctx, pool, migrations.Files)
	if err != nil || len(applied) != len(mustLoad(t))-3 || applied[0].Version != 4 {
		t.Fatalf("applied=%#v err=%v", applied, err)
	}
	if count(t, pool, "meetings") != 1 || !tableExists(t, pool, "media_assets") {
		t.Fatal("upgrade lost data or tables")
	}
}

func provisionMediaMeetingsSingle(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := postgres.NewMeetingRepository(pool).Create(context.Background(), domain.Meeting{ID: repositorytest.MeetingA, WorkspaceID: repositorytest.WorkspaceA, Title: "Before media", Status: domain.MeetingCreated, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func postgresProcessingFixture(t *testing.T) repositorytest.ProcessingFixture {
	t.Helper()
	pool := migratedPool(t)
	provisionMediaMeetings(t, pool)
	media := postgres.NewMediaRepository(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i, a := range []struct {
		ws      domain.WorkspaceID
		meeting domain.MeetingID
		id      string
	}{{repositorytest.WorkspaceA, repositorytest.MeetingA, "med_a1"}, {repositorytest.WorkspaceA, repositorytest.MeetingA, "med_a2"}, {repositorytest.WorkspaceB, repositorytest.MeetingB, "med_b"}} {
		sessionID := domain.RecordingSessionID(fmt.Sprintf("rec_%d", i))
		session := domain.RecordingSession{ID: sessionID, WorkspaceID: a.ws, MeetingID: a.meeting, Status: domain.RecordingSessionRecording, StartedAt: now, CreatedAt: now, UpdatedAt: now}
		asset := domain.MediaAsset{ID: domain.MediaAssetID(a.id), WorkspaceID: a.ws, MeetingID: a.meeting, RecordingSessionID: &sessionID, Kind: domain.MediaAssetAudio,
			MimeType: "audio/webm", Status: domain.MediaAssetUploading, StorageKey: fmt.Sprintf("obj_%032d", i), CreatedAt: now, UpdatedAt: now}
		if err := media.CreateRecording(context.Background(), session, asset); err != nil {
			t.Fatal(err)
		}
		if _, err := media.CompleteUpload(context.Background(), a.ws, asset.ID, 10, 1, nil, now); err != nil {
			t.Fatal(err)
		}
	}
	return repositorytest.ProcessingFixture{Jobs: postgres.NewJobRepository(pool), Transcripts: postgres.NewTranscriptRepository(pool),
		Summaries: postgres.NewSummaryRepository(pool), AssetA1: "med_a1", AssetA2: "med_a2", AssetB: "med_b"}
}

func TestSummaryRepositoryContract(t *testing.T) {
	repositorytest.SummaryRepositoryContract(t, postgresProcessingFixture)
}

func TestJobRepositoryContract(t *testing.T) {
	repositorytest.JobRepositoryContract(t, postgresProcessingFixture)
}

func TestTranscriptRepositoryContract(t *testing.T) {
	repositorytest.TranscriptRepositoryContract(t, postgresProcessingFixture)
}

func TestDatabaseAtMediaVersionUpgradesToProcessing(t *testing.T) {
	pool := isolatedPool(t)
	ctx := context.Background()
	previous := fstest.MapFS{}
	for _, name := range []string{"0001_initial.sql", "0002_workspace_credentials.sql", "0003_identity.sql", "0004_media.sql"} {
		data, err := fs.ReadFile(migrations.Files, name)
		if err != nil {
			t.Fatal(err)
		}
		previous[name] = &fstest.MapFile{Data: data}
	}
	if _, err := postgres.Migrate(ctx, pool, previous); err != nil {
		t.Fatal(err)
	}
	for _, ws := range []domain.WorkspaceID{repositorytest.WorkspaceA, repositorytest.WorkspaceB} {
		if _, err := postgres.EnsureWorkspace(ctx, pool, ws); err != nil {
			t.Fatal(err)
		}
	}
	provisionMediaMeetings(t, pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	// A ready recording made before processing existed (inserted with 0004's columns).
	if _, err := pool.Exec(ctx, `INSERT INTO recording_sessions (id, workspace_id, meeting_id, status, started_at, created_at, updated_at) VALUES ('rec_old', $1, $2, 'completed', $3, $3, $3)`,
		string(repositorytest.WorkspaceA), string(repositorytest.MeetingA), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_assets (id, workspace_id, meeting_id, recording_session_id, kind, mime_type, status, size_bytes, chunk_count, storage_key, created_at, updated_at)
VALUES ('med_old', $1, $2, 'rec_old', 'audio', 'audio/webm', 'ready', 10, 1, 'obj_old', $3, $3)`, string(repositorytest.WorkspaceA), string(repositorytest.MeetingA), now); err != nil {
		t.Fatal(err)
	}
	applied, err := postgres.Migrate(ctx, pool, migrations.Files)
	if err != nil || len(applied) != 3 || applied[0].Version != 5 || applied[2].Version != 7 {
		t.Fatalf("applied=%#v err=%v", applied, err)
	}
	// Sessions recorded before capture provenance existed read as unknown.
	if session, err := postgres.NewMediaRepository(pool).RecordingSession(ctx, repositorytest.WorkspaceA, "rec_old"); err != nil ||
		session.Capture.Mode != domain.CaptureModeUnknown || session.Capture.Client != domain.CaptureClientUnknown || session.Capture.SharedSurface != nil {
		t.Fatalf("legacy session = %#v err=%v", session, err)
	}
	// Pre-existing recordings are picked up by reconciliation.
	unprocessed, err := postgres.NewMediaRepository(pool).ListUnprocessedReady(ctx, 10)
	if err != nil || len(unprocessed) != 1 || unprocessed[0].ID != "med_old" || unprocessed[0].OriginalStorageKey != nil {
		t.Fatalf("unprocessed=%#v err=%v", unprocessed, err)
	}
}
