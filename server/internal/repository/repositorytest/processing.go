package repositorytest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

// ProcessingFixture provisions what processing contracts need: a repository
// plus ready media assets in WorkspaceA/MeetingA (and B) to attach jobs to.
type ProcessingFixture struct {
	Jobs        repository.JobRepository
	Transcripts repository.TranscriptRepository
	Summaries   repository.SummaryRepository
	// AssetA* belong to WorkspaceA/MeetingA; AssetB to WorkspaceB/MeetingB.
	AssetA1, AssetA2, AssetB domain.MediaAssetID
}

func job(id string, workspace domain.WorkspaceID, meeting domain.MeetingID, asset domain.MediaAssetID, jobType domain.ProcessingJobType, at time.Time) domain.ProcessingJob {
	return domain.ProcessingJob{ID: domain.ProcessingJobID(id), WorkspaceID: workspace, MeetingID: meeting, MediaAssetID: &asset, Type: jobType,
		Status: domain.JobQueued, MaxAttempts: 3, RunAfter: at, CreatedAt: at, UpdatedAt: at}
}

var allTypes = []domain.ProcessingJobType{domain.JobRemux, domain.JobTranscription, domain.JobSummary}

// JobRepositoryContract runs the shared queue semantics.
func JobRepositoryContract(t *testing.T, newFixture func(t *testing.T) ProcessingFixture) {
	ctx := context.Background()
	lease := time.Minute

	t.Run("enqueue rejects a second active job of the same type", func(t *testing.T) {
		f := newFixture(t)
		if err := f.Jobs.Enqueue(ctx, job("job_1", WorkspaceA, MeetingA, f.AssetA1, domain.JobRemux, baseTime)); err != nil {
			t.Fatal(err)
		}
		if err := f.Jobs.Enqueue(ctx, job("job_2", WorkspaceA, MeetingA, f.AssetA1, domain.JobRemux, baseTime)); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("duplicate active job err = %v", err)
		}
		if err := f.Jobs.Enqueue(ctx, job("job_3", WorkspaceA, MeetingA, f.AssetA1, domain.JobTranscription, baseTime)); err != nil {
			t.Fatalf("other type: %v", err)
		}
	})

	t.Run("claim, heartbeat and complete with fencing", func(t *testing.T) {
		f := newFixture(t)
		if err := f.Jobs.Enqueue(ctx, job("job_1", WorkspaceA, MeetingA, f.AssetA1, domain.JobRemux, baseTime)); err != nil {
			t.Fatal(err)
		}
		if claimed, _ := f.Jobs.Claim(ctx, []domain.ProcessingJobType{domain.JobTranscription}, 5, lease, baseTime); len(claimed) != 0 {
			t.Fatalf("claimed a job of another type: %v", claimed)
		}
		claimed, err := f.Jobs.Claim(ctx, allTypes, 5, lease, baseTime)
		if err != nil || len(claimed) != 1 || claimed[0].Status != domain.JobRunning || claimed[0].Attempts != 1 || claimed[0].StartedAt == nil {
			t.Fatalf("claimed=%#v err=%v", claimed, err)
		}
		if again, _ := f.Jobs.Claim(ctx, allTypes, 5, lease, baseTime); len(again) != 0 {
			t.Fatal("a running job was claimed twice")
		}
		progress := 0.5
		if owned, err := f.Jobs.Heartbeat(ctx, "job_1", 1, lease, &progress, baseTime.Add(time.Second)); err != nil || !owned {
			t.Fatalf("heartbeat owned=%v err=%v", owned, err)
		}
		if owned, _ := f.Jobs.Heartbeat(ctx, "job_1", 2, lease, nil, baseTime); owned {
			t.Fatal("heartbeat with a stale attempt succeeded")
		}
		if err := f.Jobs.Complete(ctx, "job_1", 2, baseTime); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("stale complete err = %v", err)
		}
		if err := f.Jobs.Complete(ctx, "job_1", 1, baseTime.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		done, _ := f.Jobs.GetJob(ctx, WorkspaceA, "job_1")
		if done.Status != domain.JobCompleted || done.CompletedAt == nil || done.Progress == nil || *done.Progress != 1 {
			t.Fatalf("completed job = %#v", done)
		}
		// Completed jobs are never claimed again, even long after.
		if again, _ := f.Jobs.Claim(ctx, allTypes, 5, lease, baseTime.Add(24*time.Hour)); len(again) != 0 {
			t.Fatal("completed job was claimed")
		}
		// A new active job of the same type is allowed once the old one finished.
		if err := f.Jobs.Enqueue(ctx, job("job_2", WorkspaceA, MeetingA, f.AssetA1, domain.JobRemux, baseTime)); err != nil {
			t.Fatalf("re-enqueue after completion: %v", err)
		}
	})

	t.Run("concurrent workers never claim the same job", func(t *testing.T) {
		f := newFixture(t)
		const total = 12
		for i := range total {
			asset := []domain.MediaAssetID{f.AssetA1, f.AssetA2}[i%2]
			jobType := []domain.ProcessingJobType{domain.JobRemux, domain.JobTranscription}[i/2%2]
			// Unique (asset, type) pairs limit actives; finished jobs make room.
			j := job(fmt.Sprintf("job_%02d", i), WorkspaceA, MeetingA, asset, jobType, baseTime.Add(time.Duration(i)*time.Millisecond))
			if i >= 4 {
				j.MediaAssetID = nil // jobs without an asset have no uniqueness constraint
			}
			if err := f.Jobs.Enqueue(ctx, j); err != nil {
				t.Fatalf("enqueue %d: %v", i, err)
			}
		}
		var mu sync.Mutex
		seen := map[domain.ProcessingJobID]int{}
		var wait sync.WaitGroup
		for range 8 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				for {
					claimed, err := f.Jobs.Claim(ctx, allTypes, 2, lease, baseTime.Add(time.Second))
					if err != nil || len(claimed) == 0 {
						return
					}
					mu.Lock()
					for _, c := range claimed {
						seen[c.ID]++
					}
					mu.Unlock()
				}
			}()
		}
		wait.Wait()
		if len(seen) != total {
			t.Fatalf("claimed %d distinct jobs, want %d", len(seen), total)
		}
		for id, n := range seen {
			if n != 1 {
				t.Fatalf("job %s claimed %d times", id, n)
			}
		}
	})

	t.Run("retryable failure backs off and attempts are bounded", func(t *testing.T) {
		f := newFixture(t)
		if err := f.Jobs.Enqueue(ctx, job("job_1", WorkspaceA, MeetingA, f.AssetA1, domain.JobTranscription, baseTime)); err != nil {
			t.Fatal(err)
		}
		now := baseTime
		for attempt := 1; attempt <= 3; attempt++ {
			claimed, err := f.Jobs.Claim(ctx, allTypes, 1, lease, now)
			if err != nil || len(claimed) != 1 || claimed[0].Attempts != attempt {
				t.Fatalf("attempt %d claimed=%#v err=%v", attempt, claimed, err)
			}
			retryAt := now.Add(time.Minute)
			if err := f.Jobs.Fail(ctx, "job_1", attempt, "transcription_failed", "Transcription failed.", "exit 1: /secret/path", &retryAt, now); err != nil {
				t.Fatal(err)
			}
			if early, _ := f.Jobs.Claim(ctx, allTypes, 1, lease, now.Add(30*time.Second)); len(early) != 0 {
				t.Fatal("job claimed before its retry time")
			}
			now = retryAt.Add(time.Second)
		}
		final, _ := f.Jobs.GetJob(ctx, WorkspaceA, "job_1")
		if final.Status != domain.JobFailed || *final.ErrorCode != "transcription_failed" || *final.ErrorMessage != "Transcription failed." || final.InternalError == nil {
			t.Fatalf("final = %#v", final)
		}
		if again, _ := f.Jobs.Claim(ctx, allTypes, 1, lease, now.Add(time.Hour)); len(again) != 0 {
			t.Fatal("exhausted job was claimed")
		}
	})

	t.Run("permanent failure does not retry", func(t *testing.T) {
		f := newFixture(t)
		_ = f.Jobs.Enqueue(ctx, job("job_1", WorkspaceA, MeetingA, f.AssetA1, domain.JobRemux, baseTime))
		claimed, _ := f.Jobs.Claim(ctx, allTypes, 1, lease, baseTime)
		if err := f.Jobs.Fail(ctx, claimed[0].ID, 1, "invalid_media", "bad", "", nil, baseTime); err != nil {
			t.Fatal(err)
		}
		if got, _ := f.Jobs.GetJob(ctx, WorkspaceA, "job_1"); got.Status != domain.JobFailed {
			t.Fatalf("status = %s", got.Status)
		}
	})

	t.Run("an abandoned lease is reclaimed, then failed when exhausted", func(t *testing.T) {
		f := newFixture(t)
		j := job("job_1", WorkspaceA, MeetingA, f.AssetA1, domain.JobRemux, baseTime)
		j.MaxAttempts = 2
		_ = f.Jobs.Enqueue(ctx, j)
		first, _ := f.Jobs.Claim(ctx, allTypes, 1, lease, baseTime)
		if len(first) != 1 {
			t.Fatal("not claimed")
		}
		// Worker crashed: no heartbeat. Before expiry nothing happens.
		if again, _ := f.Jobs.Claim(ctx, allTypes, 1, lease, baseTime.Add(30*time.Second)); len(again) != 0 {
			t.Fatal("reclaimed before the lease expired")
		}
		second, _ := f.Jobs.Claim(ctx, allTypes, 1, lease, baseTime.Add(2*time.Minute))
		if len(second) != 1 || second[0].Attempts != 2 {
			t.Fatalf("reclaim = %#v", second)
		}
		// The crashed worker's late completion is fenced off.
		if err := f.Jobs.Complete(ctx, "job_1", 1, baseTime.Add(2*time.Minute)); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("stale completion err = %v", err)
		}
		// Second worker also crashes: attempts exhausted, the sweep fails it.
		later := baseTime.Add(10 * time.Minute)
		if again, _ := f.Jobs.Claim(ctx, allTypes, 1, lease, later); len(again) != 0 {
			t.Fatal("exhausted job reclaimed")
		}
		if n, err := f.Jobs.FailExhausted(ctx, later); err != nil || n != 1 {
			t.Fatalf("FailExhausted n=%d err=%v", n, err)
		}
		if got, _ := f.Jobs.GetJob(ctx, WorkspaceA, "job_1"); got.Status != domain.JobFailed || *got.ErrorCode != "abandoned" {
			t.Fatalf("abandoned job = %#v", got)
		}
	})

	t.Run("release returns the attempt; cancel stops the owner", func(t *testing.T) {
		f := newFixture(t)
		_ = f.Jobs.Enqueue(ctx, job("job_1", WorkspaceA, MeetingA, f.AssetA1, domain.JobRemux, baseTime))
		claimed, _ := f.Jobs.Claim(ctx, allTypes, 1, lease, baseTime)
		if err := f.Jobs.Release(ctx, "job_1", claimed[0].Attempts, baseTime); err != nil {
			t.Fatal(err)
		}
		if got, _ := f.Jobs.GetJob(ctx, WorkspaceA, "job_1"); got.Status != domain.JobQueued || got.Attempts != 0 {
			t.Fatalf("released = %#v", got)
		}
		claimed, _ = f.Jobs.Claim(ctx, allTypes, 1, lease, baseTime)
		if _, err := f.Jobs.Cancel(ctx, WorkspaceB, "job_1", baseTime); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("cross-workspace cancel err = %v", err)
		}
		cancelled, err := f.Jobs.Cancel(ctx, WorkspaceA, "job_1", baseTime)
		if err != nil || cancelled.Status != domain.JobCancelled {
			t.Fatalf("cancel = %#v err=%v", cancelled, err)
		}
		if owned, _ := f.Jobs.Heartbeat(ctx, "job_1", claimed[0].Attempts, lease, nil, baseTime); owned {
			t.Fatal("cancelled job still owned")
		}
		if err := f.Jobs.Complete(ctx, "job_1", claimed[0].Attempts, baseTime); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("complete after cancel err = %v", err)
		}
		if _, err := f.Jobs.Cancel(ctx, WorkspaceA, "job_1", baseTime); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("second cancel err = %v", err)
		}
		if again, _ := f.Jobs.Claim(ctx, allTypes, 1, lease, baseTime.Add(time.Hour)); len(again) != 0 {
			t.Fatal("cancelled job claimed")
		}
	})

	t.Run("one active summary job per meeting; parameters and checkpoints round-trip with fencing", func(t *testing.T) {
		f := newFixture(t)
		first := job("job_s1", WorkspaceA, MeetingA, f.AssetA1, domain.JobSummary, baseTime)
		first.Parameters = []byte(`{"provider":"openai","transcriptId":"trn_1"}`)
		if err := f.Jobs.Enqueue(ctx, first); err != nil {
			t.Fatal(err)
		}
		// Another recording of the same meeting still conflicts: summaries are per meeting.
		if err := f.Jobs.Enqueue(ctx, job("job_s2", WorkspaceA, MeetingA, f.AssetA2, domain.JobSummary, baseTime)); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("second active summary err = %v", err)
		}
		if err := f.Jobs.Enqueue(ctx, job("job_s3", WorkspaceB, MeetingB, f.AssetB, domain.JobSummary, baseTime.Add(time.Hour))); err != nil {
			t.Fatalf("other meeting: %v", err)
		}
		claimed, err := f.Jobs.Claim(ctx, []domain.ProcessingJobType{domain.JobSummary}, 1, lease, baseTime)
		if err != nil || len(claimed) != 1 || claimed[0].ID != "job_s1" || !jsonEqual(claimed[0].Parameters, first.Parameters) || claimed[0].Checkpoint != nil {
			t.Fatalf("claimed=%#v err=%v", claimed, err)
		}
		if ok, err := f.Jobs.SaveCheckpoint(ctx, "job_s1", 1, []byte(`{"chunks":["a"]}`), baseTime); err != nil || !ok {
			t.Fatalf("checkpoint ok=%v err=%v", ok, err)
		}
		if ok, _ := f.Jobs.SaveCheckpoint(ctx, "job_s1", 2, []byte(`{"chunks":["stale"]}`), baseTime); ok {
			t.Fatal("stale attempt saved a checkpoint")
		}
		// The checkpoint survives a retry so the next attempt can resume.
		retry := baseTime.Add(time.Second)
		if err := f.Jobs.Fail(ctx, "job_s1", 1, "provider_unavailable", "The summary provider is unavailable.", "", &retry, baseTime); err != nil {
			t.Fatal(err)
		}
		again, _ := f.Jobs.Claim(ctx, []domain.ProcessingJobType{domain.JobSummary}, 1, lease, retry)
		if len(again) != 1 || !jsonEqual(again[0].Checkpoint, []byte(`{"chunks":["a"]}`)) {
			t.Fatalf("reclaimed = %#v", again)
		}
		if _, err := f.Jobs.Cancel(ctx, WorkspaceA, "job_s1", retry); err != nil {
			t.Fatal(err)
		}
		if err := f.Jobs.Enqueue(ctx, job("job_s4", WorkspaceA, MeetingA, f.AssetA2, domain.JobSummary, retry)); err != nil {
			t.Fatalf("after cancel: %v", err)
		}
	})

	t.Run("listing and reads are workspace scoped", func(t *testing.T) {
		f := newFixture(t)
		_ = f.Jobs.Enqueue(ctx, job("job_a", WorkspaceA, MeetingA, f.AssetA1, domain.JobRemux, baseTime))
		_ = f.Jobs.Enqueue(ctx, job("job_b", WorkspaceB, MeetingB, f.AssetB, domain.JobRemux, baseTime))
		if _, err := f.Jobs.GetJob(ctx, WorkspaceB, "job_a"); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("cross-workspace get err = %v", err)
		}
		if jobs, _ := f.Jobs.ListMeetingJobs(ctx, WorkspaceA, MeetingA); len(jobs) != 1 || jobs[0].ID != "job_a" {
			t.Fatalf("meeting jobs = %v", jobs)
		}
		if jobs, _ := f.Jobs.ListAssetJobs(ctx, WorkspaceB, f.AssetA1); len(jobs) != 0 {
			t.Fatalf("cross-workspace asset jobs = %v", jobs)
		}
	})
}

// TranscriptRepositoryContract runs the shared transcript semantics.
func TranscriptRepositoryContract(t *testing.T, newFixture func(t *testing.T) ProcessingFixture) {
	ctx := context.Background()
	transcript := func(id string, asset domain.MediaAssetID, texts ...string) domain.Transcript {
		language := "en"
		tr := domain.Transcript{ID: domain.TranscriptID(id), WorkspaceID: WorkspaceA, MeetingID: MeetingA, MediaAssetID: asset, Language: &language,
			ASREngine: "whisper.cpp", ASRModel: "ggml-tiny.en", CreatedAt: baseTime, UpdatedAt: baseTime}
		for i, text := range texts {
			tr.Segments = append(tr.Segments, domain.TranscriptSegment{Sequence: i, StartSeconds: float64(i), EndSeconds: float64(i) + 0.75, Text: text})
		}
		return tr
	}

	t.Run("replace and read back", func(t *testing.T) {
		f := newFixture(t)
		if _, err := f.Transcripts.GetByAsset(ctx, WorkspaceA, f.AssetA1); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("missing transcript err = %v", err)
		}
		if err := f.Transcripts.Replace(ctx, transcript("trn_1", f.AssetA1, "hello", "world")); err != nil {
			t.Fatal(err)
		}
		got, err := f.Transcripts.GetByAsset(ctx, WorkspaceA, f.AssetA1)
		if err != nil || got.ID != "trn_1" || len(got.Segments) != 2 || got.Segments[1].Text != "world" || got.Segments[1].EndSeconds != 1.75 ||
			got.Segments[0].Confidence != nil || *got.Language != "en" || got.ASRModel != "ggml-tiny.en" {
			t.Fatalf("got=%#v err=%v", got, err)
		}
		// Retranscription replaces the whole transcript.
		if err := f.Transcripts.Replace(ctx, transcript("trn_2", f.AssetA1, "only")); err != nil {
			t.Fatal(err)
		}
		got, _ = f.Transcripts.GetByAsset(ctx, WorkspaceA, f.AssetA1)
		if got.ID != "trn_2" || len(got.Segments) != 1 {
			t.Fatalf("after replace = %#v", got)
		}
		if _, err := f.Transcripts.GetByAsset(ctx, WorkspaceB, f.AssetA1); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("cross-workspace read err = %v", err)
		}
	})

	t.Run("an empty transcript is valid (no speech)", func(t *testing.T) {
		f := newFixture(t)
		if err := f.Transcripts.Replace(ctx, transcript("trn_1", f.AssetA2)); err != nil {
			t.Fatal(err)
		}
		if got, err := f.Transcripts.GetByAsset(ctx, WorkspaceA, f.AssetA2); err != nil || len(got.Segments) != 0 {
			t.Fatalf("got=%#v err=%v", got, err)
		}
	})
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func summaryFor(id string, workspace domain.WorkspaceID, meeting domain.MeetingID, transcript string, at time.Time) domain.Summary {
	language := "fr"
	input, output := 120, 40
	return domain.Summary{ID: domain.SummaryID(id), WorkspaceID: workspace, MeetingID: meeting, TranscriptID: domain.TranscriptID(transcript),
		Provider: "openai", Model: "gpt-test", TemplateID: "standard_meeting", OutputLanguage: &language,
		Content: []byte(`{"format":"markdown","markdown":"**Summary**\n\nShipped. \u00e9t\u00e9","title":"Weekly","chunkCount":2,"reasoningStripped":false}`),
		Usage:   &domain.SummaryUsage{InputTokens: &input, OutputTokens: &output, Requests: 3, DurationMs: 1500}, CreatedAt: at, UpdatedAt: at}
}

// SummaryRepositoryContract runs the shared summary semantics.
func SummaryRepositoryContract(t *testing.T, newFixture func(t *testing.T) ProcessingFixture) {
	ctx := context.Background()
	claim := func(t *testing.T, f ProcessingFixture, id string, workspace domain.WorkspaceID, meeting domain.MeetingID, asset domain.MediaAssetID, at time.Time) int {
		t.Helper()
		if err := f.Jobs.Enqueue(ctx, job(id, workspace, meeting, asset, domain.JobSummary, at)); err != nil {
			t.Fatal(err)
		}
		claimed, err := f.Jobs.Claim(ctx, []domain.ProcessingJobType{domain.JobSummary}, 1, time.Minute, at)
		if err != nil || len(claimed) != 1 || string(claimed[0].ID) != id {
			t.Fatalf("claim %s: %#v %v", id, claimed, err)
		}
		return claimed[0].Attempts
	}

	t.Run("promote stores structured content with provenance; reads are workspace scoped", func(t *testing.T) {
		f := newFixture(t)
		attempt := claim(t, f, "job_1", WorkspaceA, MeetingA, f.AssetA1, baseTime)
		summary := summaryFor("sum_1", WorkspaceA, MeetingA, "trn_1", baseTime)
		jobID, asset := domain.ProcessingJobID("job_1"), f.AssetA1
		summary.ProcessingJobID, summary.MediaAssetID = &jobID, &asset
		if err := f.Summaries.Promote(ctx, summary, "job_1", attempt); err != nil {
			t.Fatal(err)
		}
		got, err := f.Summaries.GetByMeeting(ctx, WorkspaceA, MeetingA)
		if err != nil || got.ID != "sum_1" || got.TranscriptID != "trn_1" || got.Provider != "openai" || got.Model != "gpt-test" || got.TemplateID != "standard_meeting" ||
			got.OutputLanguage == nil || *got.OutputLanguage != "fr" || got.ProcessingJobID == nil || *got.ProcessingJobID != "job_1" ||
			got.MediaAssetID == nil || *got.MediaAssetID != f.AssetA1 || !jsonEqual(got.Content, summary.Content) || !got.CreatedAt.Equal(baseTime) {
			t.Fatalf("summary = %#v err=%v", got, err)
		}
		if got.Usage == nil || *got.Usage.InputTokens != 120 || *got.Usage.OutputTokens != 40 || got.Usage.Requests != 3 {
			t.Fatalf("usage = %#v", got.Usage)
		}
		if _, err := f.Summaries.GetByMeeting(ctx, WorkspaceB, MeetingA); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("cross-workspace read err = %v", err)
		}
		if _, err := f.Summaries.GetByMeeting(ctx, WorkspaceB, MeetingB); !errors.Is(err, repository.ErrNotFound) {
			t.Fatalf("missing summary err = %v", err)
		}
	})

	t.Run("only the owning attempt can promote; replacement keeps created_at", func(t *testing.T) {
		f := newFixture(t)
		attempt := claim(t, f, "job_1", WorkspaceA, MeetingA, f.AssetA1, baseTime)
		if err := f.Summaries.Promote(ctx, summaryFor("sum_1", WorkspaceA, MeetingA, "trn_1", baseTime), "job_1", attempt); err != nil {
			t.Fatal(err)
		}
		if err := f.Jobs.Complete(ctx, "job_1", attempt, baseTime); err != nil {
			t.Fatal(err)
		}
		// A finished job can no longer write.
		if err := f.Summaries.Promote(ctx, summaryFor("sum_x", WorkspaceA, MeetingA, "trn_x", baseTime), "job_1", attempt); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("promote after completion err = %v", err)
		}
		later := baseTime.Add(time.Hour)
		second := claim(t, f, "job_2", WorkspaceA, MeetingA, f.AssetA1, later)
		// A stale attempt number, or a job of another meeting, cannot write.
		if err := f.Summaries.Promote(ctx, summaryFor("sum_y", WorkspaceA, MeetingA, "trn_y", later), "job_2", second+1); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("stale attempt err = %v", err)
		}
		if err := f.Summaries.Promote(ctx, summaryFor("sum_z", WorkspaceB, MeetingB, "trn_z", later), "job_2", second); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("foreign meeting err = %v", err)
		}
		if got, _ := f.Summaries.GetByMeeting(ctx, WorkspaceA, MeetingA); got.ID != "sum_1" {
			t.Fatalf("previous summary replaced by a rejected promote: %#v", got)
		}
		if err := f.Summaries.Promote(ctx, summaryFor("sum_2", WorkspaceA, MeetingA, "trn_2", later), "job_2", second); err != nil {
			t.Fatal(err)
		}
		got, _ := f.Summaries.GetByMeeting(ctx, WorkspaceA, MeetingA)
		if got.ID != "sum_2" || got.TranscriptID != "trn_2" || !got.CreatedAt.Equal(baseTime) || !got.UpdatedAt.Equal(later) {
			t.Fatalf("replaced summary = %#v", got)
		}
	})

	t.Run("a cancelled job cannot promote", func(t *testing.T) {
		f := newFixture(t)
		attempt := claim(t, f, "job_1", WorkspaceA, MeetingA, f.AssetA1, baseTime)
		if _, err := f.Jobs.Cancel(ctx, WorkspaceA, "job_1", baseTime); err != nil {
			t.Fatal(err)
		}
		if err := f.Summaries.Promote(ctx, summaryFor("sum_1", WorkspaceA, MeetingA, "trn_1", baseTime), "job_1", attempt); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("cancelled promote err = %v", err)
		}
	})
}
