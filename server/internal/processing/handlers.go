package processing

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/mediastore"
	"github.com/OcheOps/voxa/server/internal/repository"
)

const (
	probeTimeout  = 2 * time.Minute
	remuxTimeout  = 20 * time.Minute
	decodeTimeout = 60 * time.Minute
)

// loadAsset fetches the job's media asset within the job's workspace and
// checks it belongs to the job's meeting — jobs never cross workspaces.
func loadAsset(ctx context.Context, media repository.MediaRepository, job domain.ProcessingJob) (domain.MediaAsset, error) {
	if job.MediaAssetID == nil {
		return domain.MediaAsset{}, permanent("invalid_job", msgInvalidMedia, errors.New("job has no media asset"))
	}
	asset, err := media.GetAsset(ctx, job.WorkspaceID, *job.MediaAssetID)
	if errors.Is(err, repository.ErrNotFound) {
		return domain.MediaAsset{}, permanent("media_missing", "The recording no longer exists.", err)
	}
	if err != nil {
		return domain.MediaAsset{}, transient("storage_unavailable", "Processing is waiting for storage.", err)
	}
	if asset.MeetingID != job.MeetingID || asset.Status != domain.MediaAssetReady {
		return domain.MediaAsset{}, permanent("invalid_job", msgInvalidMedia, fmt.Errorf("asset not ready or not owned by job meeting"))
	}
	return asset, nil
}

func cancelled(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled)
}

func toolFailure(ctx context.Context, code, message string, err error) error {
	if cancelled(ctx, err) {
		return ctx.Err()
	}
	var toolErr *ToolError
	if errors.As(err, &toolErr) && toolErr.TimedOut {
		return transient("timeout", msgTimeout, err)
	}
	if errors.Is(err, ErrInvalidMedia) {
		return permanent("invalid_media", msgInvalidMedia, err)
	}
	return transient(code, message, err)
}

// RemuxHandler rewrites a finalized recording's container (stream copy) so
// it carries duration and a seek index, then queues transcription. The same
// MediaAsset switches to the processed object; the upload is kept as the
// asset's original. Failures never touch the original.
type RemuxHandler struct {
	Media repository.MediaRepository
	Store mediastore.Store
	Tools Tools
	Queue *Queue
	Now   func() time.Time
}

func (h *RemuxHandler) now() time.Time { return h.Now().UTC().Truncate(time.Microsecond) }

func (h *RemuxHandler) Handle(ctx context.Context, job domain.ProcessingJob, progress func(float64)) error {
	asset, err := loadAsset(ctx, h.Media, job)
	if err != nil {
		return err
	}
	if asset.ProcessedAt != nil {
		// Already processed (e.g. crash after the swap): only the follow-up is pending.
		return h.followUp(ctx, asset)
	}
	source, release, err := h.Store.OpenLocal(ctx, asset.StorageKey)
	if err != nil {
		return transient("storage_unavailable", "Processing is waiting for storage.", err)
	}
	defer release()

	probe, err := h.Tools.Probe(ctx, source, probeTimeout)
	if err != nil {
		if !errors.Is(err, ErrInvalidMedia) {
			return toolFailure(ctx, "processing_failed", msgRemuxFailed, err)
		}
		// Unreadable recording: keep it, record that processing gave up, no transcription.
		_ = h.Media.MarkProcessed(ctx, asset.WorkspaceID, asset.ID, nil, h.now())
		return permanent("invalid_media", msgInvalidMedia, err)
	}
	progress(0.2)

	result, remuxErr := h.remux(ctx, asset, source)
	if remuxErr != nil {
		if cancelled(ctx, remuxErr) {
			return ctx.Err()
		}
		failure := toolFailure(ctx, "remux_failed", msgRemuxFailed, remuxErr)
		var jobErr *JobError
		if errors.As(failure, &jobErr) && jobErr.Retryable && job.Attempts < job.MaxAttempts {
			return failure
		}
		// Final failure: playback keeps the original upload; transcription
		// still runs on it.
		if err := h.Media.MarkProcessed(ctx, asset.WorkspaceID, asset.ID, probe.DurationSeconds, h.now()); err != nil {
			return transient("storage_unavailable", "Processing is waiting for storage.", err)
		}
		if err := h.followUp(ctx, asset); err != nil {
			return err
		}
		return permanent("remux_failed", msgRemuxFailed, remuxErr)
	}
	progress(0.9)
	updated, err := h.Media.SetProcessedMedia(ctx, asset.WorkspaceID, asset.ID, result.key, asset.MimeType, result.size, result.duration, h.now())
	if err != nil {
		_ = h.Store.DeleteObject(context.WithoutCancel(ctx), result.key)
		if errors.Is(err, repository.ErrConflict) || errors.Is(err, repository.ErrNotFound) {
			return permanent("media_missing", "The recording no longer exists.", err)
		}
		return transient("storage_unavailable", "Processing is waiting for storage.", err)
	}
	return h.followUp(ctx, updated)
}

type remuxed struct {
	key      string
	size     int64
	duration *float64
}

func (h *RemuxHandler) remux(ctx context.Context, asset domain.MediaAsset, source string) (remuxed, error) {
	dir, cleanup, err := h.Store.WorkDir(ctx)
	if err != nil {
		return remuxed{}, err
	}
	defer cleanup()
	output := filepath.Join(dir, "remuxed")
	if err := h.Tools.Remux(ctx, source, output, asset.MimeType, remuxTimeout); err != nil {
		return remuxed{}, err
	}
	probe, err := h.Tools.Probe(ctx, output, probeTimeout)
	if err != nil {
		return remuxed{}, fmt.Errorf("remuxed output unreadable: %w", err)
	}
	if probe.DurationSeconds == nil {
		return remuxed{}, errors.New("remuxed output has no duration")
	}
	key, err := newID("obj_")
	if err != nil {
		return remuxed{}, err
	}
	info, err := h.Store.PutObjectFile(ctx, key, output)
	if err != nil {
		return remuxed{}, err
	}
	return remuxed{key: key, size: info.Size, duration: probe.DurationSeconds}, nil
}

func (h *RemuxHandler) followUp(ctx context.Context, asset domain.MediaAsset) error {
	if err := h.Queue.enqueueTranscription(ctx, asset); err != nil {
		return transient("storage_unavailable", "Processing is waiting for storage.", err)
	}
	return nil
}

// TranscriptionHandler decodes the asset's current (processed) audio to
// 16 kHz mono PCM and transcribes it, replacing the asset's transcript.
type TranscriptionHandler struct {
	Media          repository.MediaRepository
	Transcripts    repository.TranscriptRepository
	Configurations repository.ConfigurationRepository
	Store          mediastore.Store
	Tools          Tools
	Engine         Engine
	Now            func() time.Time
}

// transcriptionTimeout allows for CPU inference slower than real time.
func transcriptionTimeout(duration *float64) time.Duration {
	timeout := 30 * time.Minute
	if duration != nil {
		timeout += time.Duration(*duration*3) * time.Second
	}
	return min(timeout, 12*time.Hour)
}

func (h *TranscriptionHandler) Handle(ctx context.Context, job domain.ProcessingJob, progress func(float64)) error {
	if h.Engine == nil {
		return permanent("engine_unavailable", msgUnavailable, errors.New("no transcription engine configured"))
	}
	asset, err := loadAsset(ctx, h.Media, job)
	if err != nil {
		return err
	}
	source, release, err := h.Store.OpenLocal(ctx, asset.StorageKey)
	if err != nil {
		return transient("storage_unavailable", "Processing is waiting for storage.", err)
	}
	defer release()
	dir, cleanup, err := h.Store.WorkDir(ctx)
	if err != nil {
		return transient("storage_unavailable", "Processing is waiting for storage.", err)
	}
	defer cleanup()

	probe, err := h.Tools.Probe(ctx, source, probeTimeout)
	if err != nil {
		return toolFailure(ctx, "transcription_failed", msgTranscription, err)
	}
	duration := asset.DurationSeconds
	if duration == nil {
		duration = probe.DurationSeconds
	}
	wav := filepath.Join(dir, "audio.wav")
	if err := h.Tools.DecodeForASR(ctx, source, wav, decodeTimeout); err != nil {
		return toolFailure(ctx, "transcription_failed", msgTranscription, err)
	}
	progress(0.05)

	language, translate := "auto", false
	if config, err := h.Configurations.Get(ctx, asset.WorkspaceID); err == nil {
		language, translate = config.Transcription.Language, config.Transcription.TranslateToEnglish
	}
	result, err := h.Engine.Transcribe(ctx, wav, dir, TranscribeOptions{
		Language: language, Translate: translate, Timeout: transcriptionTimeout(duration),
		OnProgress: func(value float64) { progress(0.05 + 0.9*value) },
	})
	if err != nil {
		return toolFailure(ctx, "transcription_failed", msgTranscription, err)
	}

	id, err := newID("trn_")
	if err != nil {
		return transient("transcription_failed", msgTranscription, err)
	}
	now := h.Now().UTC().Truncate(time.Microsecond)
	jobID := job.ID
	transcript := domain.Transcript{ID: domain.TranscriptID(id), WorkspaceID: asset.WorkspaceID, MeetingID: asset.MeetingID, MediaAssetID: asset.ID,
		ProcessingJobID: &jobID, Language: result.Language, ASREngine: h.Engine.Name(), ASRModel: h.Engine.Model(), CreatedAt: now, UpdatedAt: now}
	transcript.Segments = toSegments(result.Segments, duration)
	if err := h.Transcripts.Replace(ctx, transcript); err != nil {
		return transient("storage_unavailable", "Processing is waiting for storage.", err)
	}
	return nil
}

// toSegments assigns sequence numbers and clamps times to the recording.
// Confidence stays nil: whisper.cpp token probabilities are not calibrated
// segment confidence.
func toSegments(segments []EngineSegment, duration *float64) []domain.TranscriptSegment {
	out := make([]domain.TranscriptSegment, 0, len(segments))
	for _, segment := range segments {
		start, end := segment.StartSeconds, segment.EndSeconds
		if duration != nil {
			start, end = min(start, *duration), min(end, *duration)
		}
		if end < start {
			end = start
		}
		text := segment.Text
		if len(text) > 10000 {
			text = text[:10000]
		}
		out = append(out, domain.TranscriptSegment{Sequence: len(out), StartSeconds: start, EndSeconds: end, Text: text})
	}
	return out
}
