package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
	"github.com/OcheOps/voxa/server/internal/summary"
)

// SummaryJobParameters is the immutable snapshot stored on a summary job at
// enqueue. Execution never reads mutable workspace configuration.
type SummaryJobParameters struct {
	TranscriptID   domain.TranscriptID `json:"transcriptId"`
	Provider       string              `json:"provider"`
	Model          string              `json:"model"`
	TemplateID     string              `json:"templateId"`
	OutputLanguage string              `json:"outputLanguage,omitempty"`
	CustomPrompt   string              `json:"customPrompt,omitempty"`
}

// SummaryState is the user-facing state of a meeting's summary.
type SummaryState string

const (
	SummaryNone        SummaryState = "none"
	SummaryQueued      SummaryState = "queued"
	SummaryGenerating  SummaryState = "generating"
	SummaryReady       SummaryState = "ready"
	SummaryFailed      SummaryState = "failed"
	SummaryCancelled   SummaryState = "cancelled"
	SummaryUnavailable SummaryState = "unavailable"
)

// SummaryJobMaxAttempts bounds summary retries (see processing policies).
const SummaryJobMaxAttempts = 3

// MaxCustomPromptChars bounds user-provided context.
const MaxCustomPromptChars = 4000

type MeetingSummary struct {
	State SummaryState
	// Reason explains unavailable/none states: disabled, not_configured,
	// provider_not_enabled, model_missing, credential_missing, no_transcript.
	Reason string
	// Summary is the current summary; it stays set while a regeneration runs
	// or after one failed.
	Summary *domain.Summary
	// Stale: the summary was produced from a transcript that is no longer
	// the meeting's current transcript.
	Stale bool
	// Job is the latest summary job, if any.
	Job                 *domain.ProcessingJob
	CurrentTranscriptID *domain.TranscriptID
}

type SummaryRequest struct {
	TemplateID   *string
	CustomPrompt *string
}

// SummaryProviders is the operator-enabled provider set (summary.Registry).
type SummaryProviders interface {
	Enabled() []string
	Readiness(config domain.SummaryDefaults, credentialConfigured func(provider string) (bool, error)) (string, error)
}

// SummaryService exposes summary state and controls and schedules summary
// jobs. A nil providers set means summaries are disabled on this server.
type SummaryService struct {
	meetings       repository.MeetingRepository
	media          repository.MediaRepository
	jobs           repository.JobRepository
	transcripts    repository.TranscriptRepository
	summaries      repository.SummaryRepository
	configurations repository.ConfigurationRepository
	credentials    repository.CredentialRepository
	providers      SummaryProviders
	now            func() time.Time
}

func NewSummaryService(meetings repository.MeetingRepository, media repository.MediaRepository, jobs repository.JobRepository,
	transcripts repository.TranscriptRepository, summaries repository.SummaryRepository, configurations repository.ConfigurationRepository,
	credentials repository.CredentialRepository, providers SummaryProviders) *SummaryService {
	return &SummaryService{meetings: meetings, media: media, jobs: jobs, transcripts: transcripts, summaries: summaries,
		configurations: configurations, credentials: credentials, providers: providers, now: time.Now}
}

// Available reports whether this server can run summaries at all (at least
// one provider enabled by the operator). Workspace readiness is separate.
func (s *SummaryService) Available() bool {
	return s.providers != nil && len(s.providers.Enabled()) > 0
}

func (s *SummaryService) Providers() []string {
	if s.providers == nil {
		return []string{}
	}
	return s.providers.Enabled()
}

func (s *SummaryService) Templates() []summary.Template { return summary.Templates() }

// currentTranscript returns the transcript of the meeting's most recent
// ready recording (the same recording GET /meetings/{id}/transcript shows),
// and whether it is final (no transcription job still running for it).
func (s *SummaryService) currentTranscript(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) (*domain.Transcript, bool, error) {
	assets, err := s.media.ListMeetingAssets(ctx, workspaceID, meetingID)
	if err != nil {
		return nil, false, err
	}
	for i := len(assets) - 1; i >= 0; i-- {
		if assets[i].Status != domain.MediaAssetReady {
			continue
		}
		transcript, err := s.transcripts.GetByAsset(ctx, workspaceID, assets[i].ID)
		if errors.Is(err, repository.ErrNotFound) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		jobs, err := s.jobs.ListAssetJobs(ctx, workspaceID, assets[i].ID)
		if err != nil {
			return nil, false, err
		}
		for _, job := range jobs {
			if job.Type == domain.JobTranscription && job.Active() {
				return &transcript, false, nil
			}
		}
		return &transcript, true, nil
	}
	return nil, false, nil
}

func (s *SummaryService) readiness(ctx context.Context, workspaceID domain.WorkspaceID) (domain.Configuration, string, error) {
	if !s.Available() {
		return domain.Configuration{}, "disabled", nil
	}
	config, err := s.configurations.Get(ctx, workspaceID)
	if err != nil {
		return domain.Configuration{}, "", err
	}
	reason, err := s.providers.Readiness(config.Summary, func(provider string) (bool, error) {
		return s.credentials.IsConfigured(ctx, workspaceID, provider)
	})
	return config, reason, err
}

func (s *SummaryService) requireMeeting(ctx context.Context, op string, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) error {
	if _, err := s.meetings.Get(ctx, workspaceID, meetingID); errors.Is(err, repository.ErrNotFound) {
		return &Error{Code: NotFound, Op: op, Err: err}
	} else if err != nil {
		return storageError(op, err)
	}
	return nil
}

func latestSummaryJob(jobs []domain.ProcessingJob) *domain.ProcessingJob {
	var found *domain.ProcessingJob
	for i := range jobs {
		if jobs[i].Type == domain.JobSummary {
			found = &jobs[i]
		}
	}
	return found
}

// Get describes the meeting's summary and its processing state.
func (s *SummaryService) Get(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) (MeetingSummary, error) {
	const op = "summaries.get"
	if err := s.requireMeeting(ctx, op, workspaceID, meetingID); err != nil {
		return MeetingSummary{}, err
	}
	result := MeetingSummary{State: SummaryNone}
	current, err := s.summaries.GetByMeeting(ctx, workspaceID, meetingID)
	if err == nil {
		result.Summary = &current
	} else if !errors.Is(err, repository.ErrNotFound) {
		return MeetingSummary{}, storageError(op, err)
	}
	jobs, err := s.jobs.ListMeetingJobs(ctx, workspaceID, meetingID)
	if err != nil {
		return MeetingSummary{}, storageError(op, err)
	}
	result.Job = latestSummaryJob(jobs)
	transcript, _, err := s.currentTranscript(ctx, workspaceID, meetingID)
	if err != nil {
		return MeetingSummary{}, storageError(op, err)
	}
	if transcript != nil {
		result.CurrentTranscriptID = &transcript.ID
	}
	if result.Summary != nil {
		result.Stale = transcript == nil || transcript.ID != result.Summary.TranscriptID
	}
	_, reason, err := s.readiness(ctx, workspaceID)
	if err != nil {
		return MeetingSummary{}, storageError(op, err)
	}
	job := result.Job
	newerThanSummary := job != nil && (result.Summary == nil || job.UpdatedAt.After(result.Summary.UpdatedAt))
	switch {
	case job != nil && job.Status == domain.JobQueued:
		result.State = SummaryQueued
	case job != nil && job.Status == domain.JobRunning:
		result.State = SummaryGenerating
	case newerThanSummary && job.Status == domain.JobFailed:
		result.State = SummaryFailed
	case newerThanSummary && job.Status == domain.JobCancelled:
		result.State = SummaryCancelled
	case result.Summary != nil:
		result.State = SummaryReady
	case reason != "":
		result.State, result.Reason = SummaryUnavailable, reason
	case transcript == nil:
		result.Reason = "no_transcript"
	}
	if result.State != SummaryUnavailable && reason != "" && result.Reason == "" {
		// Still report why a new summary could not be generated now.
		result.Reason = reason
	}
	return result, nil
}

// Request generates or regenerates the meeting's summary from its current
// transcript. The previous summary stays until the new one succeeds.
func (s *SummaryService) Request(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID, request SummaryRequest) (domain.ProcessingJob, error) {
	const op = "summaries.request"
	if !s.Available() {
		return domain.ProcessingJob{}, &Error{Code: Unavailable, Op: op, Category: "summaries_disabled"}
	}
	if err := s.requireMeeting(ctx, op, workspaceID, meetingID); err != nil {
		return domain.ProcessingJob{}, err
	}
	templateID := ""
	if request.TemplateID != nil {
		templateID = strings.TrimSpace(*request.TemplateID)
		if _, ok := summary.GetTemplate(templateID); !ok {
			return domain.ProcessingJob{}, validation("templateId", "is not a known template")
		}
	}
	customPrompt := ""
	if request.CustomPrompt != nil {
		customPrompt = strings.TrimSpace(*request.CustomPrompt)
		if utf8.RuneCountInString(customPrompt) > MaxCustomPromptChars {
			return domain.ProcessingJob{}, validation("customPrompt", "must be at most 4000 characters")
		}
	}
	job, err := s.schedule(ctx, workspaceID, meetingID, nil, templateID, customPrompt)
	var appErr *Error
	if errors.As(err, &appErr) {
		appErr.Op = op
		return domain.ProcessingJob{}, appErr
	}
	if err != nil {
		return domain.ProcessingJob{}, storageError(op, err)
	}
	return job, nil
}

// schedule snapshots configuration into a new summary job. expected, when
// set, requires the current transcript to be that one.
func (s *SummaryService) schedule(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID, expected *domain.TranscriptID,
	templateID, customPrompt string) (domain.ProcessingJob, error) {
	config, reason, err := s.readiness(ctx, workspaceID)
	if err != nil {
		return domain.ProcessingJob{}, err
	}
	if reason != "" {
		return domain.ProcessingJob{}, &Error{Code: Conflict, Details: map[string]any{"reason": reason}}
	}
	transcript, final, err := s.currentTranscript(ctx, workspaceID, meetingID)
	if err != nil {
		return domain.ProcessingJob{}, err
	}
	if transcript == nil || !final || (expected != nil && transcript.ID != *expected) {
		return domain.ProcessingJob{}, &Error{Code: Conflict, Details: map[string]any{"reason": "transcript_not_ready"}}
	}
	if templateID == "" && config.Summary.TemplateID != nil {
		if _, ok := summary.GetTemplate(*config.Summary.TemplateID); ok {
			templateID = *config.Summary.TemplateID
		}
	}
	if templateID == "" {
		templateID = summary.DefaultTemplateID
	}
	outputLanguage := strings.TrimSpace(config.Summary.OutputLanguage)
	if outputLanguage == "auto" {
		outputLanguage = ""
	}
	parameters, err := json.Marshal(SummaryJobParameters{TranscriptID: transcript.ID, Provider: strings.TrimSpace(config.Summary.Provider),
		Model: strings.TrimSpace(config.Summary.Model), TemplateID: templateID, OutputLanguage: outputLanguage, CustomPrompt: customPrompt})
	if err != nil {
		return domain.ProcessingJob{}, err
	}
	id, err := randomID("job_")
	if err != nil {
		return domain.ProcessingJob{}, err
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	assetID := transcript.MediaAssetID
	job := domain.ProcessingJob{ID: domain.ProcessingJobID(id), WorkspaceID: workspaceID, MeetingID: meetingID, MediaAssetID: &assetID,
		Type: domain.JobSummary, Status: domain.JobQueued, MaxAttempts: SummaryJobMaxAttempts, RunAfter: now, CreatedAt: now, UpdatedAt: now,
		Parameters: parameters}
	if err := s.jobs.Enqueue(ctx, job); errors.Is(err, repository.ErrConflict) {
		return domain.ProcessingJob{}, &Error{Code: Conflict, Details: map[string]any{"reason": "already_processing"}}
	} else if err != nil {
		return domain.ProcessingJob{}, err
	}
	return job, nil
}

// Cancel stops the meeting's active summary job. The previous summary, if
// any, stays.
func (s *SummaryService) Cancel(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) (domain.ProcessingJob, error) {
	const op = "summaries.cancel"
	if err := s.requireMeeting(ctx, op, workspaceID, meetingID); err != nil {
		return domain.ProcessingJob{}, err
	}
	jobs, err := s.jobs.ListMeetingJobs(ctx, workspaceID, meetingID)
	if err != nil {
		return domain.ProcessingJob{}, storageError(op, err)
	}
	for _, job := range jobs {
		if job.Type != domain.JobSummary || !job.Active() {
			continue
		}
		cancelled, err := s.jobs.Cancel(ctx, workspaceID, job.ID, s.now().UTC().Truncate(time.Microsecond))
		if errors.Is(err, repository.ErrConflict) {
			break
		}
		if err != nil {
			return domain.ProcessingJob{}, storageError(op, err)
		}
		return cancelled, nil
	}
	return domain.ProcessingJob{}, &Error{Code: Conflict, Op: op, Details: map[string]any{"reason": "not_running"}}
}

// AfterTranscription runs after a transcription job completed. With
// auto-generation enabled and a ready configuration, and when the job's
// transcript is the meeting's current one, it replaces any active summary
// job (which would summarize an older transcript) with one for this
// transcript. It reports whether a job was queued.
func (s *SummaryService) AfterTranscription(ctx context.Context, completed domain.ProcessingJob) (bool, error) {
	if !s.Available() || completed.Type != domain.JobTranscription || completed.MediaAssetID == nil {
		return false, nil
	}
	config, err := s.configurations.Get(ctx, completed.WorkspaceID)
	if err != nil || !config.Summary.AutoGenerate {
		return false, err
	}
	transcript, final, err := s.currentTranscript(ctx, completed.WorkspaceID, completed.MeetingID)
	if err != nil || transcript == nil || !final || transcript.MediaAssetID != *completed.MediaAssetID ||
		transcript.ProcessingJobID == nil || *transcript.ProcessingJobID != completed.ID {
		return false, err
	}
	jobs, err := s.jobs.ListMeetingJobs(ctx, completed.WorkspaceID, completed.MeetingID)
	if err != nil {
		return false, err
	}
	for _, job := range jobs {
		if job.Type == domain.JobSummary && job.Active() {
			_, _ = s.jobs.Cancel(ctx, completed.WorkspaceID, job.ID, s.now().UTC().Truncate(time.Microsecond))
		}
	}
	_, err = s.schedule(ctx, completed.WorkspaceID, completed.MeetingID, &transcript.ID, "", "")
	var appErr *Error
	if errors.As(err, &appErr) && appErr.Code == Conflict {
		// Configuration incomplete or a concurrent schedule won: nothing to do.
		return false, nil
	}
	return err == nil, err
}
