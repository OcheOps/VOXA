package processing

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OcheOps/voxa/server/internal/adapters/memory"
	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/mediastore"
	"github.com/OcheOps/voxa/server/internal/repository"
)

func requireTools(t *testing.T) Tools {
	t.Helper()
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	ffprobe, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	return Tools{FFmpeg: ffmpeg, FFprobe: ffprobe}
}

// fixture encodes a tone with ffmpeg. With streamed=true the output is
// written to a pipe, like MediaRecorder output: no duration, no seek index.
func fixture(t *testing.T, tools Tools, format, codec string, seconds string, streamed bool) []byte {
	t.Helper()
	args := []string{"-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=" + seconds, "-c:a", codec, "-f", format}
	var out bytes.Buffer
	if streamed {
		args = append(args, "pipe:1")
	} else {
		path := filepath.Join(t.TempDir(), "fixture")
		args = append(args, path)
		defer func() { data, _ := os.ReadFile(path); out.Write(data) }()
	}
	cmd := exec.Command(tools.FFmpeg, args...)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg fixture: %v", err)
	}
	if streamed {
		return out.Bytes()
	}
	return nil
}

func writeTemp(t *testing.T, data []byte) string {
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRemuxAddsDurationWithStreamCopy(t *testing.T) {
	tools := requireTools(t)
	ctx := context.Background()
	input := writeTemp(t, fixture(t, tools, "webm", "libopus", "3", true))
	before, err := tools.Probe(ctx, input, time.Minute)
	if err != nil || before.AudioCodec != "opus" {
		t.Fatalf("probe before = %#v err=%v", before, err)
	}
	if before.DurationSeconds != nil {
		t.Fatalf("streamed fixture unexpectedly has a duration: %v", *before.DurationSeconds)
	}
	output := filepath.Join(t.TempDir(), "out")
	if err := tools.Remux(ctx, input, output, "audio/webm;codecs=opus", time.Minute); err != nil {
		t.Fatal(err)
	}
	after, err := tools.Probe(ctx, output, time.Minute)
	if err != nil || after.DurationSeconds == nil || *after.DurationSeconds < 2.9 || *after.DurationSeconds > 3.2 || after.AudioCodec != "opus" {
		t.Fatalf("probe after = %#v err=%v", after, err)
	}
	// The output decodes cleanly.
	if _, err := runTool(ctx, tools.FFmpeg, []string{"-v", "error", "-i", output, "-f", "null", "-"}, runOptions{timeout: time.Minute}); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Stream copy: identical encoded audio packets, no re-encode.
	packets := func(path string) string {
		out, err := runTool(ctx, tools.FFprobe, []string{"-v", "error", "-select_streams", "a:0", "-show_entries", "packet=size", "-of", "csv=p=0", path}, runOptions{timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if packets(input) != packets(output) {
		t.Fatal("remux changed the encoded audio (not a stream copy)")
	}
}

func TestRemuxOtherContainersAndInvalidMedia(t *testing.T) {
	tools := requireTools(t)
	ctx := context.Background()
	ogg := writeTemp(t, fixture(t, tools, "ogg", "libopus", "2", true))
	if err := tools.Remux(ctx, ogg, filepath.Join(t.TempDir(), "o"), "audio/ogg;codecs=opus", time.Minute); err != nil {
		t.Fatalf("ogg: %v", err)
	}
	garbage := writeTemp(t, []byte("<html>not audio</html>"))
	if _, err := tools.Probe(ctx, garbage, time.Minute); !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("probe garbage err = %v", err)
	}
	if err := tools.Remux(ctx, garbage, filepath.Join(t.TempDir(), "g"), "video/x-unknown", time.Minute); !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("unsupported container err = %v", err)
	}
}

// --- handlers with real ffmpeg and a fake engine ---

type fakeEngine struct {
	segments []EngineSegment
	err      error
	block    bool
	calls    int
	mu       sync.Mutex
	options  TranscribeOptions
}

func (f *fakeEngine) Name() string  { return "fake" }
func (f *fakeEngine) Model() string { return "fake-model" }
func (f *fakeEngine) Transcribe(ctx context.Context, wav, _ string, options TranscribeOptions) (EngineResult, error) {
	f.mu.Lock()
	f.calls++
	f.options = options
	f.mu.Unlock()
	if info, err := os.Stat(wav); err != nil || info.Size() < 44 {
		return EngineResult{}, errors.New("no wav")
	}
	if f.block {
		<-ctx.Done()
		return EngineResult{}, ctx.Err()
	}
	if f.err != nil {
		return EngineResult{}, f.err
	}
	options.OnProgress(1)
	language := "en"
	return EngineResult{Language: &language, Segments: f.segments}, nil
}

type pipeline struct {
	media       *memory.MediaRepository
	jobs        *memory.JobRepository
	transcripts *memory.TranscriptRepository
	store       *mediastore.Filesystem
	queue       *Queue
	remux       *RemuxHandler
	transcribe  *TranscriptionHandler
	asset       domain.MediaAsset
	engine      *fakeEngine
	service     *application.MediaService
}

func newPipeline(t *testing.T, tools Tools, data []byte) *pipeline {
	t.Helper()
	ctx := context.Background()
	store, err := mediastore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	meetings := memory.NewMeetingRepository()
	media := memory.NewMediaRepository()
	meeting, err := application.NewMeetingService(meetings).Create(ctx, "ws", "Test")
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewMediaService(meetings, media, store, application.DefaultMediaLimits())
	start, err := service.CreateRecording(ctx, "ws", meeting.ID, "audio/webm;codecs=opus")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UploadChunk(ctx, "ws", start.Asset.ID, 0, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	asset, err := service.Finalize(ctx, "ws", start.Asset.ID, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := &pipeline{media: media, jobs: memory.NewJobRepository(), transcripts: memory.NewTranscriptRepository(), store: store, asset: asset, engine: &fakeEngine{}, service: service}
	p.queue = NewQueue(p.jobs, true)
	p.remux = &RemuxHandler{Media: media, Store: store, Tools: tools, Queue: p.queue, Now: time.Now}
	p.transcribe = &TranscriptionHandler{Media: media, Transcripts: p.transcripts, Configurations: memory.NewConfigurationRepository(application.DefaultConfiguration()),
		Store: store, Tools: tools, Engine: p.engine, Now: time.Now}
	return p
}

func (p *pipeline) job(t *testing.T, jobType domain.ProcessingJobType) domain.ProcessingJob {
	t.Helper()
	assetID := p.asset.ID
	return domain.ProcessingJob{ID: "job_test", WorkspaceID: p.asset.WorkspaceID, MeetingID: p.asset.MeetingID, MediaAssetID: &assetID, Type: jobType,
		Status: domain.JobRunning, Attempts: 1, MaxAttempts: 3}
}

func readObject(t *testing.T, store mediastore.Store, key string) []byte {
	t.Helper()
	object, err := store.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer object.Close()
	data, _ := io.ReadAll(object)
	return data
}

func TestRemuxHandlerSwapsPlaybackObjectAndKeepsOriginal(t *testing.T) {
	tools := requireTools(t)
	original := fixture(t, tools, "webm", "libopus", "2", true)
	p := newPipeline(t, tools, original)
	if err := p.remux.Handle(context.Background(), p.job(t, domain.JobRemux), func(float64) {}); err != nil {
		t.Fatal(err)
	}
	asset, _ := p.media.GetAsset(context.Background(), p.asset.WorkspaceID, p.asset.ID)
	if asset.ProcessedAt == nil || asset.DurationSeconds == nil || *asset.DurationSeconds < 1.9 || asset.OriginalStorageKey == nil ||
		*asset.OriginalStorageKey != p.asset.StorageKey || asset.StorageKey == p.asset.StorageKey || asset.ID != p.asset.ID {
		t.Fatalf("asset = %#v", asset)
	}
	if !bytes.Equal(readObject(t, p.store, *asset.OriginalStorageKey), original) {
		t.Fatal("original upload was modified")
	}
	if status, err := p.media.AssetStatusByStorageKey(context.Background(), *asset.OriginalStorageKey); err != nil || status != domain.MediaAssetReady {
		t.Fatalf("original not protected from cleanup: %v %v", status, err)
	}
	jobs, _ := p.jobs.ListAssetJobs(context.Background(), p.asset.WorkspaceID, p.asset.ID)
	if len(jobs) != 1 || jobs[0].Type != domain.JobTranscription {
		t.Fatalf("follow-up jobs = %#v", jobs)
	}
	// Idempotent rerun (e.g. after a crash): no second swap, no duplicate job.
	if err := p.remux.Handle(context.Background(), p.job(t, domain.JobRemux), func(float64) {}); err != nil {
		t.Fatal(err)
	}
	again, _ := p.media.GetAsset(context.Background(), p.asset.WorkspaceID, p.asset.ID)
	if again.StorageKey != asset.StorageKey {
		t.Fatal("rerun replaced the processed object")
	}
	// Deleting the recording removes both the processed object and the original.
	if err := p.service.DeleteAsset(context.Background(), p.asset.WorkspaceID, p.asset.ID); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{asset.StorageKey, *asset.OriginalStorageKey} {
		if _, err := p.store.Open(context.Background(), key); err == nil {
			t.Fatalf("object %s survived deletion", key)
		}
	}
}

func TestRemuxHandlerInvalidMediaKeepsOriginal(t *testing.T) {
	tools := requireTools(t)
	// Passes the upload magic check but is not decodable audio.
	broken := append([]byte{0x1a, 0x45, 0xdf, 0xa3}, bytes.Repeat([]byte{0x42}, 4096)...)
	p := newPipeline(t, tools, broken)
	err := p.remux.Handle(context.Background(), p.job(t, domain.JobRemux), func(float64) {})
	var jobErr *JobError
	if !errors.As(err, &jobErr) || jobErr.Retryable || jobErr.Code != "invalid_media" || strings.Contains(jobErr.Message, "/") {
		t.Fatalf("err = %#v", err)
	}
	asset, _ := p.media.GetAsset(context.Background(), p.asset.WorkspaceID, p.asset.ID)
	if asset.StorageKey != p.asset.StorageKey || !bytes.Equal(readObject(t, p.store, asset.StorageKey), broken) || asset.ProcessedAt == nil {
		t.Fatalf("original changed: %#v", asset)
	}
	if jobs, _ := p.jobs.ListAssetJobs(context.Background(), p.asset.WorkspaceID, p.asset.ID); len(jobs) != 0 {
		t.Fatalf("transcription queued for unreadable media: %#v", jobs)
	}
}

func TestRemuxHandlerRejectsJobsForAnotherMeeting(t *testing.T) {
	tools := requireTools(t)
	p := newPipeline(t, tools, fixture(t, tools, "webm", "libopus", "1", true))
	job := p.job(t, domain.JobRemux)
	job.MeetingID = "mtg_other"
	var jobErr *JobError
	if err := p.remux.Handle(context.Background(), job, func(float64) {}); !errors.As(err, &jobErr) || jobErr.Retryable {
		t.Fatalf("err = %v", err)
	}
	job = p.job(t, domain.JobRemux)
	job.WorkspaceID = "ws_other"
	if err := p.remux.Handle(context.Background(), job, func(float64) {}); !errors.As(err, &jobErr) || jobErr.Code != "media_missing" {
		t.Fatalf("cross-workspace err = %v", err)
	}
}

func TestTranscriptionHandlerPersistsSegments(t *testing.T) {
	tools := requireTools(t)
	p := newPipeline(t, tools, fixture(t, tools, "webm", "libopus", "3", true))
	if err := p.remux.Handle(context.Background(), p.job(t, domain.JobRemux), func(float64) {}); err != nil {
		t.Fatal(err)
	}
	p.engine.segments = []EngineSegment{{0, 1.2, "Hello there."}, {1.2, 9, "General Kenobi."}}
	var progress []float64
	if err := p.transcribe.Handle(context.Background(), p.job(t, domain.JobTranscription), func(v float64) { progress = append(progress, v) }); err != nil {
		t.Fatal(err)
	}
	transcript, err := p.transcripts.GetByAsset(context.Background(), p.asset.WorkspaceID, p.asset.ID)
	if err != nil || transcript.ASREngine != "fake" || transcript.ASRModel != "fake-model" || *transcript.Language != "en" || len(transcript.Segments) != 2 {
		t.Fatalf("transcript=%#v err=%v", transcript, err)
	}
	// End is clamped to the recording's processed duration (~3 s).
	if last := transcript.Segments[1]; last.EndSeconds > 3.1 || last.Sequence != 1 || last.Confidence != nil {
		t.Fatalf("segment = %#v", last)
	}
	if p.engine.options.Language != "auto" || p.engine.options.Translate || progress[len(progress)-1] < 0.9 {
		t.Fatalf("options=%#v progress=%v", p.engine.options, progress)
	}
}

func TestTranscriptionHandlerFailuresAndCancellation(t *testing.T) {
	tools := requireTools(t)
	p := newPipeline(t, tools, fixture(t, tools, "webm", "libopus", "1", true))
	p.engine.err = &ToolError{Tool: "/opt/secret/whisper-cli", ExitCode: 1, Stderr: "crash at /var/lib/voxa"}
	err := p.transcribe.Handle(context.Background(), p.job(t, domain.JobTranscription), func(float64) {})
	var jobErr *JobError
	if !errors.As(err, &jobErr) || !jobErr.Retryable || jobErr.Code != "transcription_failed" || strings.Contains(jobErr.Message, "/") {
		t.Fatalf("engine failure err = %#v", err)
	}
	p.engine.err, p.engine.block = nil, true
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	if err := p.transcribe.Handle(ctx, p.job(t, domain.JobTranscription), func(float64) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err = %v", err)
	}
	if _, err := p.transcripts.GetByAsset(context.Background(), p.asset.WorkspaceID, p.asset.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatal("a failed transcription stored a transcript")
	}
	p.transcribe.Engine = nil
	if err := p.transcribe.Handle(context.Background(), p.job(t, domain.JobTranscription), func(float64) {}); !errors.As(err, &jobErr) || jobErr.Code != "engine_unavailable" || jobErr.Retryable {
		t.Fatalf("missing engine err = %v", err)
	}
}

// --- worker ---

type scriptedHandler struct {
	mu      sync.Mutex
	results []error
	block   chan struct{}
	started chan struct{}
	seen    []int
}

func (h *scriptedHandler) Handle(ctx context.Context, job domain.ProcessingJob, progress func(float64)) error {
	h.mu.Lock()
	h.seen = append(h.seen, job.Attempts)
	var result error
	if len(h.results) > 0 {
		result, h.results = h.results[0], h.results[1:]
	}
	h.mu.Unlock()
	progress(0.5)
	if h.started != nil {
		h.started <- struct{}{}
	}
	if h.block != nil {
		select {
		case <-h.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return result
}

type fakeClock struct{ nanos atomic.Int64 }

func (c *fakeClock) Now() time.Time   { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *fakeClock) Set(at time.Time) { c.nanos.Store(at.UnixNano()) }

func testWorker(jobs repository.JobRepository, handler Handler) (*Worker, *fakeClock) {
	// Slightly ahead of real time so jobs enqueued "now" are runnable.
	clock := &fakeClock{}
	clock.Set(time.Now().UTC().Add(time.Second))
	config := WorkerConfig{Concurrency: 1, Lease: time.Minute, Heartbeat: 20 * time.Millisecond, Poll: 10 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w := NewWorker(jobs, map[domain.ProcessingJobType]Handler{domain.JobTranscription: handler}, config, nil)
	w.now = clock.Now
	return w, clock
}

func received(t *testing.T, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}
}

func enqueue(t *testing.T, jobs repository.JobRepository) domain.ProcessingJobID {
	t.Helper()
	asset := domain.MediaAsset{ID: "med_1", WorkspaceID: "ws", MeetingID: "mtg", Status: domain.MediaAssetReady}
	job, err := NewQueue(jobs, true).Enqueue(context.Background(), domain.JobTranscription, asset)
	if err != nil {
		t.Fatal(err)
	}
	return job.ID
}

func waitStatus(t *testing.T, jobs repository.JobRepository, id domain.ProcessingJobID, status domain.ProcessingJobStatus) domain.ProcessingJob {
	t.Helper()
	for i := 0; i < 200; i++ {
		job, _ := jobs.GetJob(context.Background(), "ws", id)
		if job.Status == status {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, _ := jobs.GetJob(context.Background(), "ws", id)
	t.Fatalf("job status = %s, want %s", job.Status, status)
	return job
}

func TestWorkerCompletesRetriesAndFails(t *testing.T) {
	jobs := memory.NewJobRepository()
	handler := &scriptedHandler{results: []error{transient("transcription_failed", msgTranscription, errors.New("crash")), nil}}
	worker, now := testWorker(jobs, handler)
	id := enqueue(t, jobs)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()

	var failed domain.ProcessingJob
	for i := 0; i < 300 && failed.ErrorCode == nil; i++ {
		failed, _ = jobs.GetJob(context.Background(), "ws", id)
		time.Sleep(10 * time.Millisecond)
	}
	if failed.ErrorCode == nil || failed.Status != domain.JobQueued {
		t.Fatalf("first attempt did not fail and requeue: %#v", failed)
	}
	if *failed.ErrorCode != "transcription_failed" || !failed.RunAfter.After(now.Now()) {
		t.Fatalf("retry not scheduled: %#v", failed)
	}
	now.Set(failed.RunAfter.Add(time.Second)) // advance the clock past the backoff
	completed := waitStatus(t, jobs, id, domain.JobCompleted)
	if completed.Attempts != 2 {
		t.Fatalf("attempts = %d", completed.Attempts)
	}
	cancel()
	<-done

	permanentJobs := memory.NewJobRepository()
	worker2, _ := testWorker(permanentJobs, &scriptedHandler{results: []error{permanent("invalid_media", msgInvalidMedia, errors.New("bad"))}})
	id2 := enqueue(t, permanentJobs)
	ctx2, cancel2 := context.WithCancel(context.Background())
	go worker2.Run(ctx2)
	job := waitStatus(t, permanentJobs, id2, domain.JobFailed)
	cancel2()
	if job.Attempts != 1 || *job.ErrorCode != "invalid_media" {
		t.Fatalf("permanent failure = %#v", job)
	}
}

func TestWorkerStopsOnCancellationAndReleasesOnShutdown(t *testing.T) {
	jobs := memory.NewJobRepository()
	handler := &scriptedHandler{block: make(chan struct{}), started: make(chan struct{}, 4)}
	worker, _ := testWorker(jobs, handler)
	id := enqueue(t, jobs)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	received(t, handler.started)
	if _, err := jobs.Cancel(context.Background(), "ws", id, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The heartbeat notices the cancellation and stops the handler; status stays cancelled.
	time.Sleep(150 * time.Millisecond)
	if job, _ := jobs.GetJob(context.Background(), "ws", id); job.Status != domain.JobCancelled {
		t.Fatalf("status after cancel = %s", job.Status)
	}

	id2 := enqueue(t, jobs)
	received(t, handler.started)
	cancel() // server shutdown while running
	<-done
	released, _ := jobs.GetJob(context.Background(), "ws", id2)
	if released.Status != domain.JobQueued || released.Attempts != 0 {
		t.Fatalf("not released on shutdown: %#v", released)
	}
}
