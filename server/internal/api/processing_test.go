package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OcheOps/voxa/server/internal/adapters/memory"
	"github.com/OcheOps/voxa/server/internal/api"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/auth"
	"github.com/OcheOps/voxa/server/internal/domain"
)

type queueStub struct{ jobs *memory.JobRepository }

func (q queueStub) Enqueue(ctx context.Context, jobType domain.ProcessingJobType, asset domain.MediaAsset) (domain.ProcessingJob, error) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	id := asset.ID
	job := domain.ProcessingJob{ID: domain.ProcessingJobID("job_" + string(jobType) + "_" + now.Format("150405.000000")), WorkspaceID: asset.WorkspaceID,
		MeetingID: asset.MeetingID, MediaAssetID: &id, Type: jobType, Status: domain.JobQueued, MaxAttempts: 3, RunAfter: now, CreatedAt: now, UpdatedAt: now}
	return job, q.jobs.Enqueue(ctx, job)
}

type processingFixture struct {
	*mediaFixture
	jobs        *memory.JobRepository
	transcripts *memory.TranscriptRepository
}

func newProcessingFixture(t *testing.T) *processingFixture {
	f := &processingFixture{mediaFixture: newMediaFixture(t, application.DefaultMediaLimits()), jobs: memory.NewJobRepository(), transcripts: memory.NewTranscriptRepository()}
	return f
}

func (f *processingFixture) handler(workspace string, transcription bool) http.Handler {
	credentials := memory.NewCredentialRepository()
	processing := application.NewProcessingService(f.meetings, f.media, f.jobs, f.transcripts, queueStub{f.jobs}, transcription)
	return api.NewHandler(application.NewMeetingService(f.meetings),
		application.NewConfigurationService(memory.NewConfigurationRepository(application.DefaultConfiguration()), credentials),
		application.NewCredentialService(credentials), auth.DevelopmentAuthenticator{}, application.NewDevelopmentResolver(domain.WorkspaceID(workspace)), "test", nil,
		api.WithMedia(f.service, f.signer), api.WithProcessing(processing))
}

func (f *processingFixture) readyRecording(t *testing.T, handler http.Handler) (string, string) {
	meetingID := createMeeting(t, handler)
	assetID := startRecording(t, handler, meetingID)
	putChunk(t, handler, assetID, 0, webmChunk(true, "audio"))
	finalize(t, handler, assetID, 1)
	return meetingID, assetID
}

func state(t *testing.T, handler http.Handler, assetID string) map[string]any {
	return jsonBody(t, send(t, handler, http.MethodGet, "/api/v1/media/"+assetID+"/transcript", nil, nil))
}

func TestTranscriptStatesAndRetrieval(t *testing.T) {
	f := newProcessingFixture(t)
	handler := f.handler("workspace-a", true)
	meetingID, assetID := f.readyRecording(t, handler)
	ctx := context.Background()
	now := time.Now().UTC()

	// Not processed yet and no job: preparing (reconciliation will queue it).
	if got := state(t, handler, assetID); got["state"] != "preparing" || got["transcript"] != nil {
		t.Fatalf("initial = %v", got)
	}
	asset, _ := f.media.GetAsset(ctx, "workspace-a", domain.MediaAssetID(assetID))
	remux, _ := queueStub{f.jobs}.Enqueue(ctx, domain.JobRemux, asset)
	if got := state(t, handler, assetID); got["state"] != "preparing" {
		t.Fatalf("with remux job = %v", got)
	}
	claimed, _ := f.jobs.Claim(ctx, []domain.ProcessingJobType{domain.JobRemux}, 1, time.Minute, now.Add(time.Second))
	_ = f.jobs.Complete(ctx, remux.ID, claimed[0].Attempts, now)
	_ = f.media.MarkProcessed(ctx, "workspace-a", asset.ID, nil, now)
	transcription, _ := queueStub{f.jobs}.Enqueue(ctx, domain.JobTranscription, asset)
	if got := state(t, handler, assetID); got["state"] != "transcribing" {
		t.Fatalf("with transcription job = %v", got)
	}
	claimed, _ = f.jobs.Claim(ctx, []domain.ProcessingJobType{domain.JobTranscription}, 1, time.Minute, now.Add(time.Second))
	_ = f.jobs.Fail(ctx, transcription.ID, claimed[0].Attempts, "transcription_failed", "Transcription failed.", "exit 1 at /var/lib/voxa/tmp", nil, now)
	failed := state(t, handler, assetID)
	if failed["state"] != "failed" || failed["error"].(map[string]any)["code"] != "transcription_failed" {
		t.Fatalf("failed = %v", failed)
	}

	language := "en"
	jobID := transcription.ID
	_ = f.transcripts.Replace(ctx, domain.Transcript{ID: "trn_1", WorkspaceID: "workspace-a", MeetingID: domain.MeetingID(meetingID), MediaAssetID: asset.ID,
		ProcessingJobID: &jobID, Language: &language, ASREngine: "whisper.cpp", ASRModel: "ggml-base.en", CreatedAt: now, UpdatedAt: now,
		Segments: []domain.TranscriptSegment{{Sequence: 0, StartSeconds: 0.5, EndSeconds: 2.25, Text: "Hello world."}}})
	// A newer successful run: ready with the transcript.
	retry, _ := queueStub{f.jobs}.Enqueue(ctx, domain.JobTranscription, asset)
	claimed, _ = f.jobs.Claim(ctx, []domain.ProcessingJobType{domain.JobTranscription}, 1, time.Minute, now.Add(2*time.Second))
	_ = f.jobs.Complete(ctx, retry.ID, claimed[0].Attempts, now)
	ready := state(t, handler, assetID)
	if ready["state"] != "ready" || ready["error"] != nil {
		t.Fatalf("ready = %v", ready)
	}
	transcript := ready["transcript"].(map[string]any)
	assertKeys(t, transcript, "id", "workspaceId", "meetingId", "mediaAssetId", "processingJobId", "language", "asrEngine", "asrModel", "segments", "createdAt", "updatedAt")
	segment := transcript["segments"].([]any)[0].(map[string]any)
	assertKeys(t, segment, "id", "text", "startTimeSeconds", "endTimeSeconds", "durationSeconds", "sequence", "isFinal", "confidence", "source", "timingProvenance")
	if segment["startTimeSeconds"] != 0.5 || segment["durationSeconds"] != 1.75 || segment["isFinal"] != true || segment["confidence"] != nil || segment["timingProvenance"] != "asr" {
		t.Fatalf("segment = %v", segment)
	}
	if meeting := jsonBody(t, send(t, handler, http.MethodGet, "/api/v1/meetings/"+meetingID+"/transcript", nil, nil)); meeting["state"] != "ready" || meeting["mediaAssetId"] != assetID {
		t.Fatalf("meeting transcript = %v", meeting)
	}
	jobs := jsonBody(t, send(t, handler, http.MethodGet, "/api/v1/meetings/"+meetingID+"/jobs", nil, nil))["items"].([]any)
	if len(jobs) != 3 {
		t.Fatalf("jobs = %v", jobs)
	}
	assertKeys(t, jobs[0].(map[string]any), "id", "workspaceId", "meetingId", "mediaAssetId", "kind", "status", "progress", "errorCode", "errorMessage", "createdAt", "startedAt", "completedAt")
	body := send(t, handler, http.MethodGet, "/api/v1/meetings/"+meetingID+"/jobs", nil, nil).Body.String()
	for _, leak := range []string{"/var/lib", "internal", "attempts", "lease"} {
		if strings.Contains(body, leak) {
			t.Fatalf("jobs response leaked %q: %s", leak, body)
		}
	}
}

func TestRetranscribeCancelAndIsolation(t *testing.T) {
	f := newProcessingFixture(t)
	a := f.handler("workspace-a", true)
	b := f.handler("workspace-b", true)
	meetingID, assetID := f.readyRecording(t, a)

	requested := send(t, a, http.MethodPost, "/api/v1/media/"+assetID+"/transcription", nil, nil)
	if requested.Code != http.StatusAccepted {
		t.Fatalf("request status=%d body=%s", requested.Code, requested.Body.String())
	}
	jobID := jsonBody(t, requested)["job"].(map[string]any)["id"].(string)
	if again := send(t, a, http.MethodPost, "/api/v1/media/"+assetID+"/transcription", nil, nil); again.Code != http.StatusConflict || reason(t, again) != "already_processing" {
		t.Fatalf("duplicate request status=%d", again.Code)
	}
	for _, response := range []*httptest.ResponseRecorder{
		send(t, b, http.MethodGet, "/api/v1/media/"+assetID+"/transcript", nil, nil),
		send(t, b, http.MethodGet, "/api/v1/meetings/"+meetingID+"/jobs", nil, nil),
		send(t, b, http.MethodPost, "/api/v1/media/"+assetID+"/transcription", nil, nil),
		send(t, b, http.MethodPost, "/api/v1/jobs/"+jobID+"/cancel", nil, nil),
	} {
		if response.Code != http.StatusNotFound {
			t.Fatalf("cross-workspace status=%d body=%s", response.Code, response.Body.String())
		}
	}
	cancelled := send(t, a, http.MethodPost, "/api/v1/jobs/"+jobID+"/cancel", nil, nil)
	if cancelled.Code != http.StatusOK || jsonBody(t, cancelled)["job"].(map[string]any)["status"] != "cancelled" {
		t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
	if again := send(t, a, http.MethodPost, "/api/v1/jobs/"+jobID+"/cancel", nil, nil); again.Code != http.StatusConflict {
		t.Fatalf("second cancel status=%d", again.Code)
	}
	if got := state(t, a, assetID); got["state"] != "cancelled" {
		t.Fatalf("after cancel = %v", got)
	}

	// Without a usable engine: unavailable, and requests are refused.
	off := f.handler("workspace-a", false)
	if got := send(t, off, http.MethodPost, "/api/v1/media/"+assetID+"/transcription", nil, nil); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("request without engine status=%d", got.Code)
	}
	capabilities := jsonBody(t, send(t, off, http.MethodGet, "/api/v1/capabilities", nil, nil))
	if capabilities["transcription"].(map[string]any)["available"] != false || capabilities["mediaStorage"].(map[string]any)["available"] != true || capabilities["summary"].(map[string]any)["available"] != false {
		t.Fatalf("capabilities without engine = %v", capabilities)
	}
	on := jsonBody(t, send(t, a, http.MethodGet, "/api/v1/capabilities", nil, nil))["transcription"].(map[string]any)
	if on["available"] != true || on["providers"].([]any)[0] != "whisper.cpp" {
		t.Fatalf("capabilities with engine = %v", on)
	}
}

func TestTranscriptWithoutProcessingIsUnavailable(t *testing.T) {
	f := newMediaFixture(t, application.DefaultMediaLimits())
	handler := f.handler("workspace-a", true)
	meetingID := createMeeting(t, handler)
	assetID := startRecording(t, handler, meetingID)
	if got := jsonBody(t, send(t, handler, http.MethodGet, "/api/v1/media/"+assetID+"/transcript", nil, nil)); got["state"] != "unavailable" {
		t.Fatalf("media transcript = %v", got)
	}
	if got := jsonBody(t, send(t, handler, http.MethodGet, "/api/v1/meetings/"+meetingID+"/transcript", nil, nil)); got["state"] != "unavailable" {
		t.Fatalf("meeting transcript = %v", got)
	}
	if got := send(t, handler, http.MethodGet, "/api/v1/media/med_missing/transcript", nil, nil); got.Code != http.StatusNotFound {
		t.Fatalf("missing asset status=%d", got.Code)
	}
}
