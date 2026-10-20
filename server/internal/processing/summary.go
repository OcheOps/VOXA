package processing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
	"github.com/OcheOps/voxa/server/internal/summary"
)

const (
	msgSummaryFailed       = "Summary generation failed."
	msgSummaryNotConfigure = "Summaries are not configured for this workspace."
)

// SummaryProviderSource opens a provider with the job workspace's credential
// and reports the chunking threshold (summary.Registry).
type SummaryProviderSource interface {
	Open(ctx context.Context, workspaceID domain.WorkspaceID, name string) (summary.Provider, func(), error)
	ChunkThreshold(ctx context.Context, name, model string) int
}

// SummaryHandler summarizes the transcript snapshotted on the job and
// promotes the result only while the job is still owned by this attempt.
type SummaryHandler struct {
	Jobs        repository.JobRepository
	Transcripts repository.TranscriptRepository
	Summaries   repository.SummaryRepository
	Providers   SummaryProviderSource
	Now         func() time.Time
}

// FormatTranscript renders segments as the desktop does for summaries:
// one "[MM:SS] text" line per segment, recording-relative.
func FormatTranscript(segments []domain.TranscriptSegment) string {
	var b strings.Builder
	for i, segment := range segments {
		if i > 0 {
			b.WriteByte('\n')
		}
		total := int(segment.StartSeconds)
		fmt.Fprintf(&b, "[%02d:%02d] %s", total/60, total%60, segment.Text)
	}
	return b.String()
}

// summaryFailure maps a provider failure to a user-safe job error. Detail is
// already sanitized by the summary package and goes to internal_error only.
func summaryFailure(err error) error {
	var providerErr *summary.Error
	switch {
	case errors.As(err, &providerErr):
		switch providerErr.Kind {
		case summary.KindAuth:
			return permanent("provider_auth_failed", "The summary provider rejected the configured credentials.", err)
		case summary.KindModel:
			return permanent("provider_model_unavailable", "The summary provider could not find the configured model.", err)
		case summary.KindRejected:
			return permanent("provider_rejected", "The summary provider rejected the request.", err)
		case summary.KindRedirect:
			return permanent("provider_redirect", "The summary provider endpoint redirected; check the server configuration.", err)
		case summary.KindTooLarge:
			return permanent("provider_response_too_large", "The summary provider returned a response that was too large.", err)
		case summary.KindConfig:
			return permanent("summary_not_configured", msgSummaryNotConfigure, err)
		case summary.KindRateLimited:
			jobErr := transient("provider_rate_limited", "The summary provider is rate limiting requests; retrying later.", err)
			jobErr.RetryAfter = providerErr.RetryAfter
			return jobErr
		case summary.KindTimeout:
			return transient("timeout", "The summary provider took too long.", err)
		case summary.KindInvalidOutput:
			return transient("invalid_output", "The summary provider returned an unusable response.", err)
		default:
			jobErr := transient("provider_unavailable", "The summary provider is unavailable; retrying later.", err)
			jobErr.RetryAfter = providerErr.RetryAfter
			return jobErr
		}
	case errors.Is(err, summary.ErrTranscriptTooLong):
		return permanent("transcript_too_long", "The transcript is too long to summarize.", err)
	case errors.Is(err, summary.ErrEmptyTranscript):
		return permanent("empty_transcript", "The transcript has no speech to summarize.", err)
	}
	return transient("summary_failed", msgSummaryFailed, err)
}

func (h *SummaryHandler) Handle(ctx context.Context, job domain.ProcessingJob, progress func(float64)) error {
	var params application.SummaryJobParameters
	if err := json.Unmarshal(job.Parameters, &params); err != nil || params.TranscriptID == "" || params.Provider == "" || params.Model == "" {
		return permanent("invalid_job", msgSummaryFailed, errors.New("summary job parameters missing or invalid"))
	}
	template, ok := summary.GetTemplate(params.TemplateID)
	if !ok {
		return permanent("invalid_template", msgSummaryFailed, fmt.Errorf("unknown template %q", params.TemplateID))
	}
	if job.MediaAssetID == nil {
		return permanent("invalid_job", msgSummaryFailed, errors.New("summary job has no recording"))
	}
	// The transcript must be the one snapshotted at enqueue, in the job's
	// workspace and meeting. A retranscription in between makes this job
	// obsolete (a newer job is scheduled or the user regenerates).
	transcript, err := h.Transcripts.GetByAsset(ctx, job.WorkspaceID, *job.MediaAssetID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && (transcript.ID != params.TranscriptID || transcript.MeetingID != job.MeetingID)) {
		return permanent("transcript_changed", "The transcript changed; generate the summary again.", err)
	}
	if err != nil {
		return transient("storage_unavailable", "Processing is waiting for storage.", err)
	}

	provider, release, err := h.Providers.Open(ctx, job.WorkspaceID, params.Provider)
	if err != nil {
		return summaryFailure(err)
	}
	defer release()

	language := ""
	if transcript.Language != nil {
		language = *transcript.Language
	}
	request := summary.Request{
		Transcript: FormatTranscript(transcript.Segments), TranscriptLanguage: language, Template: template,
		CustomPrompt: params.CustomPrompt, OutputLanguage: params.OutputLanguage, Model: params.Model,
		ChunkTokens: h.Providers.ChunkThreshold(ctx, params.Provider, params.Model), Checkpoint: job.Checkpoint,
		SaveCheckpoint: func(data []byte) error {
			owned, err := h.Jobs.SaveCheckpoint(context.WithoutCancel(ctx), job.ID, job.Attempts, data, h.Now().UTC())
			if err == nil && !owned {
				return errLeaseLost
			}
			return err
		},
		Progress: progress,
	}
	result, err := summary.Generate(ctx, provider, request)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, errLeaseLost) {
		return permanent("superseded", msgSummaryFailed, err)
	}
	if err != nil {
		return summaryFailure(err)
	}

	content, err := json.Marshal(result.Content())
	if err != nil {
		return permanent("invalid_output", msgSummaryFailed, err)
	}
	id, err := newID("sum_")
	if err != nil {
		return transient("summary_failed", msgSummaryFailed, err)
	}
	now := h.Now().UTC().Truncate(time.Microsecond)
	jobID, assetID := job.ID, transcript.MediaAssetID
	var outputLanguage *string
	if params.OutputLanguage != "" {
		outputLanguage = &params.OutputLanguage
	}
	usage := result.Usage
	stored := domain.Summary{ID: domain.SummaryID(id), WorkspaceID: job.WorkspaceID, MeetingID: job.MeetingID, TranscriptID: transcript.ID,
		MediaAssetID: &assetID, ProcessingJobID: &jobID, Provider: params.Provider, Model: params.Model, TemplateID: template.ID,
		OutputLanguage: outputLanguage, Content: content, Usage: &usage, CreatedAt: now, UpdatedAt: now}
	if err := h.Summaries.Promote(ctx, stored, job.ID, job.Attempts); errors.Is(err, repository.ErrConflict) {
		// Cancelled or reclaimed while generating: a newer job owns the outcome.
		return permanent("superseded", msgSummaryFailed, err)
	} else if err != nil {
		return transient("storage_unavailable", "Processing is waiting for storage.", err)
	}
	return nil
}
