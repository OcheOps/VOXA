package repositorytest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

// Meetings the media contract uses; factories must provision them.
const (
	MeetingA domain.MeetingID = "mtg_media_a"
	MeetingB domain.MeetingID = "mtg_media_b"
)

func recording(workspaceID domain.WorkspaceID, meetingID domain.MeetingID, n int, at time.Time) (domain.RecordingSession, domain.MediaAsset) {
	session := domain.RecordingSession{ID: domain.RecordingSessionID(fmt.Sprintf("rec_%d", n)), WorkspaceID: workspaceID, MeetingID: meetingID,
		Status: domain.RecordingSessionRecording, StartedAt: at, CreatedAt: at, UpdatedAt: at}
	sessionID := session.ID
	asset := domain.MediaAsset{ID: domain.MediaAssetID(fmt.Sprintf("med_%d", n)), WorkspaceID: workspaceID, MeetingID: meetingID, RecordingSessionID: &sessionID,
		Kind: domain.MediaAssetAudio, MimeType: "audio/webm;codecs=opus", Status: domain.MediaAssetUploading,
		StorageKey: fmt.Sprintf("obj_%032d", n), CreatedAt: at, UpdatedAt: at}
	return session, asset
}

// MediaRepositoryContract runs the shared media metadata semantics.
func MediaRepositoryContract(t *testing.T, newRepository func(t *testing.T) repository.MediaRepository) {
	ctx := context.Background()

	t.Run("create, get and list scoped by workspace", func(t *testing.T) {
		repo := newRepository(t)
		first, a1 := recording(WorkspaceA, MeetingA, 1, baseTime)
		second, a2 := recording(WorkspaceA, MeetingA, 2, baseTime.Add(time.Second))
		other, b1 := recording(WorkspaceB, MeetingB, 3, baseTime)
		for _, pair := range []struct {
			s domain.RecordingSession
			a domain.MediaAsset
		}{{second, a2}, {first, a1}, {other, b1}} {
			if err := repo.CreateRecording(ctx, pair.s, pair.a); err != nil {
				t.Fatal(err)
			}
		}
		got, err := repo.GetAsset(ctx, WorkspaceA, a1.ID)
		if err != nil || got.StorageKey != a1.StorageKey || got.Status != domain.MediaAssetUploading || *got.RecordingSessionID != first.ID {
			t.Fatalf("got=%#v err=%v", got, err)
		}
		if _, err := repo.GetAsset(ctx, WorkspaceB, a1.ID); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("cross-workspace get err = %v", err)
		}
		listed, err := repo.ListMeetingAssets(ctx, WorkspaceA, MeetingA)
		if err != nil || len(listed) != 2 || listed[0].ID != a1.ID || listed[1].ID != a2.ID {
			t.Fatalf("listed=%v err=%v", listed, err)
		}
		if listed, _ := repo.ListMeetingAssets(ctx, WorkspaceB, MeetingA); len(listed) != 0 {
			t.Fatalf("cross-workspace list = %v", listed)
		}
		if err := repo.CreateRecording(ctx, first, a1); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("duplicate create err = %v", err)
		}
	})

	t.Run("complete upload once", func(t *testing.T) {
		repo := newRepository(t)
		session, asset := recording(WorkspaceA, MeetingA, 1, baseTime)
		if err := repo.CreateRecording(ctx, session, asset); err != nil {
			t.Fatal(err)
		}
		duration := 12.5
		ready, err := repo.CompleteUpload(ctx, WorkspaceA, asset.ID, 4096, 3, &duration, baseTime.Add(time.Minute))
		if err != nil || ready.Status != domain.MediaAssetReady || ready.SizeBytes != 4096 || *ready.ChunkCount != 3 || *ready.DurationSeconds != 12.5 {
			t.Fatalf("ready=%#v err=%v", ready, err)
		}
		if _, err := repo.CompleteUpload(ctx, WorkspaceA, asset.ID, 1, 1, nil, baseTime); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("second complete err = %v", err)
		}
		if _, err := repo.CompleteUpload(ctx, WorkspaceB, asset.ID, 1, 1, nil, baseTime); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("cross-workspace complete err = %v", err)
		}
		if status, err := repo.AssetStatusByStorageKey(ctx, asset.StorageKey); err != nil || status != domain.MediaAssetReady {
			t.Fatalf("status=%v err=%v", status, err)
		}
	})

	t.Run("concurrent completion has one winner", func(t *testing.T) {
		repo := newRepository(t)
		session, asset := recording(WorkspaceA, MeetingA, 1, baseTime)
		if err := repo.CreateRecording(ctx, session, asset); err != nil {
			t.Fatal(err)
		}
		var wait sync.WaitGroup
		var mu sync.Mutex
		winners := 0
		for range 8 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				if _, err := repo.CompleteUpload(ctx, WorkspaceA, asset.ID, 10, 1, nil, baseTime); err == nil {
					mu.Lock()
					winners++
					mu.Unlock()
				}
			}()
		}
		wait.Wait()
		if winners != 1 {
			t.Fatalf("%d completions succeeded", winners)
		}
	})

	t.Run("stale uploads fail, ready ones never", func(t *testing.T) {
		repo := newRepository(t)
		oldSession, old := recording(WorkspaceA, MeetingA, 1, baseTime)
		readySession, ready := recording(WorkspaceA, MeetingA, 2, baseTime)
		freshSession, fresh := recording(WorkspaceA, MeetingA, 3, baseTime.Add(2*time.Hour))
		for _, pair := range []struct {
			s domain.RecordingSession
			a domain.MediaAsset
		}{{oldSession, old}, {readySession, ready}, {freshSession, fresh}} {
			if err := repo.CreateRecording(ctx, pair.s, pair.a); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := repo.CompleteUpload(ctx, WorkspaceA, ready.ID, 1, 1, nil, baseTime); err != nil {
			t.Fatal(err)
		}
		if err := repo.TouchUpload(ctx, WorkspaceA, fresh.ID, baseTime.Add(3*time.Hour)); err != nil {
			t.Fatal(err)
		}
		stale, err := repo.ListStaleUploads(ctx, baseTime.Add(time.Hour))
		if err != nil || len(stale) != 1 || stale[0].ID != old.ID {
			t.Fatalf("stale=%v err=%v", stale, err)
		}
		if err := repo.FailUpload(ctx, old.ID, baseTime.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := repo.FailUpload(ctx, ready.ID, baseTime.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if got, _ := repo.GetAsset(ctx, WorkspaceA, old.ID); got.Status != domain.MediaAssetFailed {
			t.Fatalf("stale asset status = %s", got.Status)
		}
		if got, _ := repo.GetAsset(ctx, WorkspaceA, ready.ID); got.Status != domain.MediaAssetReady {
			t.Fatalf("ready asset changed to %s", got.Status)
		}
		if err := repo.TouchUpload(ctx, WorkspaceB, fresh.ID, baseTime); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("cross-workspace touch err = %v", err)
		}
	})

	t.Run("delete is scoped and reports the deleted asset", func(t *testing.T) {
		repo := newRepository(t)
		session, asset := recording(WorkspaceA, MeetingA, 1, baseTime)
		if err := repo.CreateRecording(ctx, session, asset); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.DeleteAsset(ctx, WorkspaceB, asset.ID); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("cross-workspace delete err = %v", err)
		}
		deleted, err := repo.DeleteAsset(ctx, WorkspaceA, asset.ID)
		if err != nil || deleted.StorageKey != asset.StorageKey {
			t.Fatalf("deleted=%#v err=%v", deleted, err)
		}
		if _, err := repo.AssetStatusByStorageKey(ctx, asset.StorageKey); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("status after delete err = %v", err)
		}
	})
}
