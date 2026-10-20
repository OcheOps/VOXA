package processing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OcheOps/voxa/server/internal/adapters/memory"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
	"github.com/OcheOps/voxa/server/internal/summary"
)

const summaryKey = "sk-summary-test-KEY-0123456789"

// llmServer is a fake OpenAI-compatible provider.
type llmServer struct {
	*httptest.Server
	mu       sync.Mutex
	statuses []int // consumed per request; 0 or exhausted = 200
	block    chan struct{}
	requests atomic.Int32
	auth     []string
	gotStop  atomic.Bool
}

func newLLM(t *testing.T) *llmServer {
	l := &llmServer{}
	l.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.requests.Add(1)
		_, _ = io.ReadAll(r.Body)
		l.mu.Lock()
		l.auth = append(l.auth, r.Header.Get("Authorization"))
		status := 0
		if len(l.statuses) > 0 {
			status, l.statuses = l.statuses[0], l.statuses[1:]
		}
		block := l.block
		l.mu.Unlock()
		if block != nil {
			select {
			case <-block:
			case <-r.Context().Done():
				l.gotStop.Store(true)
				return
			}
		}
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"provider said no to ` + r.Header.Get("Authorization") + `"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": "# Weekly sync\n\n**Summary**\n\nThe team agreed to ship on Friday."}}},
			"usage":   map[string]any{"prompt_tokens": 120, "completion_tokens": 30},
		})
	}))
	t.Cleanup(l.Close)
	return l
}

type summaryFixture struct {
	ctx         context.Context
	meetings    *memory.MeetingRepository
	media       *memory.MediaRepository
	jobs        *memory.JobRepository
	transcripts *memory.TranscriptRepository
	summaries   *memory.SummaryRepository
	configs     *memory.ConfigurationRepository
	credentials *memory.CredentialRepository
	service     *application.SummaryService
	handler     *SummaryHandler
	llm         *llmServer
	meeting     domain.MeetingID
	asset       domain.MediaAsset
}

func newSummaryFixture(t *testing.T, autoGenerate bool) *summaryFixture {
	t.Helper()
	ctx := context.Background()
	f := &summaryFixture{ctx: ctx, meetings: memory.NewMeetingRepository(), media: memory.NewMediaRepository(), jobs: memory.NewJobRepository(),
		transcripts: memory.NewTranscriptRepository(), credentials: memory.NewCredentialRepository(), llm: newLLM(t)}
	f.summaries = memory.NewSummaryRepository(f.jobs)
	defaults := application.DefaultConfiguration()
	defaults.Summary = domain.SummaryDefaults{Provider: "openai", Model: "gpt-test", OutputLanguage: "auto", AutoGenerate: autoGenerate}
	f.configs = memory.NewConfigurationRepository(defaults)
	if err := f.credentials.Set(ctx, "ws", "openai", domain.NewCredentialSecret([]byte(summaryKey))); err != nil {
		t.Fatal(err)
	}
	meeting, err := application.NewMeetingService(f.meetings).Create(ctx, "ws", "Weekly")
	if err != nil {
		t.Fatal(err)
	}
	f.meeting = meeting.ID
	now := time.Now().UTC()
	sessionID := domain.RecordingSessionID("rec_1")
	session := domain.RecordingSession{ID: sessionID, WorkspaceID: "ws", MeetingID: meeting.ID, Status: domain.RecordingSessionRecording, StartedAt: now, CreatedAt: now, UpdatedAt: now}
	asset := domain.MediaAsset{ID: "med_1", WorkspaceID: "ws", MeetingID: meeting.ID, RecordingSessionID: &sessionID, Kind: domain.MediaAssetAudio,
		MimeType: "audio/webm", Status: domain.MediaAssetUploading, StorageKey: "obj_1", CreatedAt: now, UpdatedAt: now}
	if err := f.media.CreateRecording(ctx, session, asset); err != nil {
		t.Fatal(err)
	}
	if f.asset, err = f.media.CompleteUpload(ctx, "ws", asset.ID, 10, 1, nil, now); err != nil {
		t.Fatal(err)
	}
	registry := summary.NewRegistry([]summary.Spec{{Name: "openai", BaseURL: f.llm.URL + "/v1", CredentialRequired: true}}, summary.NewHTTPClient(), f.credentials)
	f.service = application.NewSummaryService(f.meetings, f.media, f.jobs, f.transcripts, f.summaries, f.configs, f.credentials, registry)
	f.handler = &SummaryHandler{Jobs: f.jobs, Transcripts: f.transcripts, Summaries: f.summaries, Providers: registry, Now: time.Now}
	return f
}

// transcribe stores a transcript produced by a completed transcription job.
func (f *summaryFixture) transcribe(t *testing.T, text string) (domain.ProcessingJob, domain.Transcript) {
	t.Helper()
	job, err := NewQueue(f.jobs, true).Enqueue(f.ctx, domain.JobTranscription, f.asset)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := f.jobs.Claim(f.ctx, []domain.ProcessingJobType{domain.JobTranscription}, 1, time.Minute, time.Now().Add(time.Second))
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim transcription: %v %v", claimed, err)
	}
	id, _ := newID("trn_")
	language := "en"
	transcript := domain.Transcript{ID: domain.TranscriptID(id), WorkspaceID: "ws", MeetingID: f.meeting, MediaAssetID: f.asset.ID, ProcessingJobID: &job.ID,
		Language: &language, ASREngine: "whisper.cpp", ASRModel: "ggml-base.en", CreatedAt: time.Now(), UpdatedAt: time.Now(),
		Segments: []domain.TranscriptSegment{{Sequence: 0, StartSeconds: 1, EndSeconds: 3, Text: text}, {Sequence: 1, StartSeconds: 65.4, EndSeconds: 67, Text: "Ship Friday."}}}
	if err := f.transcripts.Replace(f.ctx, transcript); err != nil {
		t.Fatal(err)
	}
	if err := f.jobs.Complete(f.ctx, job.ID, claimed[0].Attempts, time.Now()); err != nil {
		t.Fatal(err)
	}
	completed, _ := f.jobs.GetJob(f.ctx, "ws", job.ID)
	return completed, transcript
}

// run claims the next summary job and runs the handler once.
func (f *summaryFixture) run(t *testing.T, at time.Time) (domain.ProcessingJob, error) {
	t.Helper()
	claimed, err := f.jobs.Claim(f.ctx, []domain.ProcessingJobType{domain.JobSummary}, 1, time.Minute, at)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim summary: %v %v", claimed, err)
	}
	return claimed[0], f.handler.Handle(f.ctx, claimed[0], func(float64) {})
}

func TestFormatTranscriptMatchesDesktop(t *testing.T) {
	got := FormatTranscript([]domain.TranscriptSegment{{StartSeconds: 5.9, Text: "Hello."}, {StartSeconds: 3725, Text: "Late."}})
	if got != "[00:05] Hello.\n[62:05] Late." {
		t.Fatalf("formatted = %q", got)
	}
}

func TestSummaryScheduledAfterTranscriptionOnlyWhenEnabled(t *testing.T) {
	f := newSummaryFixture(t, true)
	completed, transcript := f.transcribe(t, "We discussed the launch.")
	queued, err := f.service.AfterTranscription(f.ctx, completed)
	if err != nil || !queued {
		t.Fatalf("queued=%v err=%v", queued, err)
	}
	jobs, _ := f.jobs.ListMeetingJobs(f.ctx, "ws", f.meeting)
	summaryJob := jobs[len(jobs)-1]
	var params application.SummaryJobParameters
	_ = json.Unmarshal(summaryJob.Parameters, &params)
	if summaryJob.Type != domain.JobSummary || params.TranscriptID != transcript.ID || params.Provider != "openai" || params.Model != "gpt-test" ||
		params.TemplateID != summary.DefaultTemplateID || params.OutputLanguage != "" {
		t.Fatalf("job=%#v params=%#v", summaryJob, params)
	}
	// The snapshot is immutable: later configuration changes do not affect it.
	_, _ = f.configs.Update(f.ctx, "ws", func(c domain.Configuration) (domain.Configuration, error) { c.Summary.Model = "other"; return c, nil })
	job, err := f.run(t, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.jobs.Complete(f.ctx, job.ID, job.Attempts, time.Now())
	stored, err := f.summaries.GetByMeeting(f.ctx, "ws", f.meeting)
	if err != nil || stored.Model != "gpt-test" || stored.TranscriptID != transcript.ID || stored.Provider != "openai" || stored.TemplateID != "standard_meeting" {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	var content summary.Content
	_ = json.Unmarshal(stored.Content, &content)
	if content.Title != "Weekly sync" || content.Markdown != "**Summary**\n\nThe team agreed to ship on Friday." || content.Format != "markdown" {
		t.Fatalf("content = %#v", content)
	}
	if stored.Usage == nil || *stored.Usage.InputTokens != 120 || stored.Usage.Requests != 1 {
		t.Fatalf("usage = %#v", stored.Usage)
	}
	if f.llm.auth[0] != "Bearer "+summaryKey {
		t.Fatal("provider did not receive the workspace credential")
	}

	off := newSummaryFixture(t, false)
	completed, _ = off.transcribe(t, "No auto summary.")
	if queued, err := off.service.AfterTranscription(off.ctx, completed); err != nil || queued {
		t.Fatalf("auto-summary disabled queued=%v err=%v", queued, err)
	}
	unavailable := application.NewSummaryService(off.meetings, off.media, off.jobs, off.transcripts, off.summaries, off.configs, off.credentials, nil)
	if queued, err := unavailable.AfterTranscription(off.ctx, completed); err != nil || queued {
		t.Fatalf("unavailable queued=%v err=%v", queued, err)
	}
	if jobs, _ := off.jobs.ListMeetingJobs(off.ctx, "ws", off.meeting); len(jobs) != 1 {
		t.Fatalf("summary jobs created without auto-generation: %d jobs", len(jobs))
	}
}

func TestFailedRegenerationKeepsPreviousSummary(t *testing.T) {
	f := newSummaryFixture(t, false)
	f.transcribe(t, "First.")
	if _, err := f.service.Request(f.ctx, "ws", f.meeting, application.SummaryRequest{}); err != nil {
		t.Fatal(err)
	}
	job, err := f.run(t, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.jobs.Complete(f.ctx, job.ID, job.Attempts, time.Now())
	first, _ := f.summaries.GetByMeeting(f.ctx, "ws", f.meeting)

	// Regenerate with credentials the provider now rejects.
	f.llm.statuses = []int{http.StatusUnauthorized}
	if _, err := f.service.Request(f.ctx, "ws", f.meeting, application.SummaryRequest{}); err != nil {
		t.Fatal(err)
	}
	view, _ := f.service.Get(f.ctx, "ws", f.meeting)
	if view.State != application.SummaryQueued || view.Summary == nil || view.Summary.ID != first.ID {
		t.Fatalf("during regeneration = %#v", view)
	}
	job, err = f.run(t, time.Now().Add(2*time.Second))
	var jobErr *JobError
	if !errors.As(err, &jobErr) || jobErr.Code != "provider_auth_failed" || jobErr.Retryable {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(jobErr.Error(), summaryKey) || strings.Contains(jobErr.Message, "provider said") {
		t.Fatalf("job error leaks provider details: %v / %q", jobErr, jobErr.Message)
	}
	_ = f.jobs.Fail(f.ctx, job.ID, job.Attempts, jobErr.Code, jobErr.Message, jobErr.Error(), nil, time.Now().Add(3*time.Second))
	view, _ = f.service.Get(f.ctx, "ws", f.meeting)
	if view.State != application.SummaryFailed || view.Summary == nil || view.Summary.ID != first.ID {
		t.Fatalf("after failed regeneration = %#v", view)
	}
}

func TestSummaryRetryHonoursRetryAfter(t *testing.T) {
	f := newSummaryFixture(t, false)
	f.transcribe(t, "Retry me.")
	f.llm.statuses = []int{http.StatusTooManyRequests}
	f.llm.Config.Handler = rateLimitHeader(f.llm.Config.Handler)
	if _, err := f.service.Request(f.ctx, "ws", f.meeting, application.SummaryRequest{}); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{}
	clock.Set(time.Now().Add(time.Second))
	config := WorkerConfig{Concurrency: 1, Lease: time.Minute, Heartbeat: 20 * time.Millisecond, Poll: 10 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	worker := NewWorker(f.jobs, map[domain.ProcessingJobType]Handler{domain.JobSummary: f.handler}, config, nil)
	worker.now = clock.Now
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	jobs, _ := f.jobs.ListMeetingJobs(f.ctx, "ws", f.meeting)
	id := jobs[len(jobs)-1].ID
	retried := waitStatus(t, f.jobs, id, domain.JobQueued)
	for i := 0; i < 100 && retried.Attempts == 0; i++ {
		time.Sleep(10 * time.Millisecond)
		retried, _ = f.jobs.GetJob(f.ctx, "ws", id)
	}
	// Retry-After (1 h) wins over the 1 min backoff.
	if retried.ErrorCode == nil || *retried.ErrorCode != "provider_rate_limited" || retried.RunAfter.Sub(clock.Now()) < 59*time.Minute {
		t.Fatalf("retried = %#v", retried)
	}
	clock.Set(clock.Now().Add(61 * time.Minute))
	waitStatus(t, f.jobs, id, domain.JobCompleted)
	if _, err := f.summaries.GetByMeeting(f.ctx, "ws", f.meeting); err != nil {
		t.Fatal(err)
	}
}

func rateLimitHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		next.ServeHTTP(w, r)
	})
}

func TestSummaryCancellationStopsTheProviderCall(t *testing.T) {
	f := newSummaryFixture(t, false)
	f.transcribe(t, "Cancel me.")
	f.llm.block = make(chan struct{})
	defer close(f.llm.block)
	if _, err := f.service.Request(f.ctx, "ws", f.meeting, application.SummaryRequest{}); err != nil {
		t.Fatal(err)
	}
	config := WorkerConfig{Concurrency: 1, Lease: time.Minute, Heartbeat: 20 * time.Millisecond, Poll: 10 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	worker := NewWorker(f.jobs, map[domain.ProcessingJobType]Handler{domain.JobSummary: f.handler}, config, nil)
	clock := &fakeClock{}
	clock.Set(time.Now().Add(time.Second))
	worker.now = clock.Now
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	for i := 0; i < 300 && f.llm.requests.Load() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := f.service.Cancel(f.ctx, "ws", f.meeting); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300 && !f.llm.gotStop.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !f.llm.gotStop.Load() {
		t.Fatal("in-flight provider request was not cancelled")
	}
	if _, err := f.summaries.GetByMeeting(f.ctx, "ws", f.meeting); !errors.Is(err, repository.ErrNotFound) {
		t.Fatal("a cancelled job stored a summary")
	}
	view, _ := f.service.Get(f.ctx, "ws", f.meeting)
	if view.State != application.SummaryCancelled {
		t.Fatalf("state = %s", view.State)
	}
	if _, err := f.service.Cancel(f.ctx, "ws", f.meeting); err == nil {
		t.Fatal("second cancel succeeded")
	}
}

func TestStaleAttemptCannotOverwriteNewerSummary(t *testing.T) {
	f := newSummaryFixture(t, false)
	f.transcribe(t, "Race.")
	if _, err := f.service.Request(f.ctx, "ws", f.meeting, application.SummaryRequest{}); err != nil {
		t.Fatal(err)
	}
	claimed, _ := f.jobs.Claim(f.ctx, []domain.ProcessingJobType{domain.JobSummary}, 1, time.Second, time.Now().Add(time.Second))
	stale := claimed[0]
	// The lease expires (worker presumed dead) and another worker reclaims it.
	reclaimed, _ := f.jobs.Claim(f.ctx, []domain.ProcessingJobType{domain.JobSummary}, 1, time.Minute, time.Now().Add(5*time.Second))
	if len(reclaimed) != 1 || reclaimed[0].Attempts != stale.Attempts+1 {
		t.Fatalf("reclaimed = %#v", reclaimed)
	}
	err := f.handler.Handle(f.ctx, stale, func(float64) {})
	var jobErr *JobError
	if !errors.As(err, &jobErr) || jobErr.Code != "superseded" {
		t.Fatalf("stale attempt err = %v", err)
	}
	if _, err := f.summaries.GetByMeeting(f.ctx, "ws", f.meeting); !errors.Is(err, repository.ErrNotFound) {
		t.Fatal("stale attempt stored a summary")
	}
	if err := f.handler.Handle(f.ctx, reclaimed[0], func(float64) {}); err != nil {
		t.Fatalf("current attempt: %v", err)
	}
}

func TestRetranscriptionMarksSummaryStaleAndReschedules(t *testing.T) {
	f := newSummaryFixture(t, true)
	completed, first := f.transcribe(t, "Version one.")
	if _, err := f.service.AfterTranscription(f.ctx, completed); err != nil {
		t.Fatal(err)
	}
	job, err := f.run(t, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.jobs.Complete(f.ctx, job.ID, job.Attempts, time.Now())

	// Retranscribe: the summary now describes an older transcript.
	completed, second := f.transcribe(t, "Version two.")
	view, _ := f.service.Get(f.ctx, "ws", f.meeting)
	if !view.Stale || view.Summary.TranscriptID != first.ID || view.CurrentTranscriptID == nil || *view.CurrentTranscriptID != second.ID {
		t.Fatalf("after retranscription = %#v", view)
	}
	// A summary job queued for the old transcript is obsolete.
	if queued, err := f.service.AfterTranscription(f.ctx, completed); err != nil || !queued {
		t.Fatalf("rescheduled=%v err=%v", queued, err)
	}
	job, err = f.run(t, time.Now().Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.jobs.Complete(f.ctx, job.ID, job.Attempts, time.Now())
	view, _ = f.service.Get(f.ctx, "ws", f.meeting)
	if view.Stale || view.Summary.TranscriptID != second.ID || view.State != application.SummaryReady {
		t.Fatalf("after regeneration = %#v", view)
	}

	// A job whose snapshot no longer matches the transcript fails permanently.
	if _, err := f.service.Request(f.ctx, "ws", f.meeting, application.SummaryRequest{}); err != nil {
		t.Fatal(err)
	}
	f.transcribe(t, "Version three.")
	_, err = f.run(t, time.Now().Add(3*time.Second))
	var jobErr *JobError
	if !errors.As(err, &jobErr) || jobErr.Code != "transcript_changed" || jobErr.Retryable {
		t.Fatalf("obsolete job err = %v", err)
	}
}

func TestManualRequestRules(t *testing.T) {
	f := newSummaryFixture(t, false)
	var appErr *application.Error
	if _, err := f.service.Request(f.ctx, "ws", f.meeting, application.SummaryRequest{}); !errors.As(err, &appErr) || appErr.Details["reason"] != "transcript_not_ready" {
		t.Fatalf("without transcript err = %v", err)
	}
	f.transcribe(t, "Ready.")
	unknown := "nope"
	if _, err := f.service.Request(f.ctx, "ws", f.meeting, application.SummaryRequest{TemplateID: &unknown}); !errors.As(err, &appErr) || appErr.Code != application.Validation {
		t.Fatalf("unknown template err = %v", err)
	}
	long := strings.Repeat("x", application.MaxCustomPromptChars+1)
	if _, err := f.service.Request(f.ctx, "ws", f.meeting, application.SummaryRequest{CustomPrompt: &long}); !errors.As(err, &appErr) || appErr.Code != application.Validation {
		t.Fatalf("long prompt err = %v", err)
	}
	standup, context := "daily_standup", "Engineering standup"
	job, err := f.service.Request(f.ctx, "ws", f.meeting, application.SummaryRequest{TemplateID: &standup, CustomPrompt: &context})
	if err != nil {
		t.Fatal(err)
	}
	var params application.SummaryJobParameters
	_ = json.Unmarshal(job.Parameters, &params)
	if params.TemplateID != "daily_standup" || params.CustomPrompt != "Engineering standup" {
		t.Fatalf("params = %#v", params)
	}
	if _, err := f.service.Request(f.ctx, "ws", f.meeting, application.SummaryRequest{}); !errors.As(err, &appErr) || appErr.Details["reason"] != "already_processing" {
		t.Fatalf("duplicate err = %v", err)
	}
	// Removing the credential makes the workspace not ready.
	_ = f.credentials.Delete(f.ctx, "ws", "openai")
	view, _ := f.service.Get(f.ctx, "ws", f.meeting)
	if view.Reason != summary.ReasonCredentialMissing {
		t.Fatalf("reason = %q", view.Reason)
	}
	if _, err := f.service.Get(f.ctx, "other-ws", f.meeting); !errors.As(err, &appErr) || appErr.Code != application.NotFound {
		t.Fatalf("cross-workspace get err = %v", err)
	}
}
