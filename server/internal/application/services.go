package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

type ErrorCode string

const (
	Validation   ErrorCode = "validation"
	Unauthorized ErrorCode = "unauthorized"
	Forbidden    ErrorCode = "forbidden"
	NotFound     ErrorCode = "not_found"
	Conflict     ErrorCode = "conflict"
	Unavailable  ErrorCode = "unavailable"
	Internal     ErrorCode = "internal"
)

type Error struct {
	Code    ErrorCode
	Details map[string]any
	Err     error
}

func (e *Error) Error() string { return string(e.Code) }
func (e *Error) Unwrap() error { return e.Err }

func validation(field, reason string) error {
	return &Error{Code: Validation, Details: map[string]any{"field": field, "reason": reason}}
}

type MeetingPage struct {
	Items      []domain.Meeting
	NextCursor *string
	HasMore    bool
}

type MeetingService struct {
	repository repository.MeetingRepository
	now        func() time.Time
	newID      func() (domain.MeetingID, error)
}

func NewMeetingService(repo repository.MeetingRepository) *MeetingService {
	return &MeetingService{repository: repo, now: time.Now, newID: randomMeetingID}
}

func randomMeetingID() (domain.MeetingID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return domain.MeetingID("mtg_" + hex.EncodeToString(raw[:])), nil
}

func (s *MeetingService) Create(ctx context.Context, workspaceID domain.WorkspaceID, title string) (domain.Meeting, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return domain.Meeting{}, validation("title", "must not be blank")
	}
	if len(title) > 200 {
		return domain.Meeting{}, validation("title", "must be at most 200 characters")
	}
	id, err := s.newID()
	if err != nil {
		return domain.Meeting{}, &Error{Code: Internal, Err: err}
	}
	now := s.now().UTC()
	meeting := domain.Meeting{ID: id, WorkspaceID: workspaceID, Title: title, Status: domain.MeetingCreated, CreatedAt: now, UpdatedAt: now}
	if err := s.repository.Create(ctx, meeting); errors.Is(err, repository.ErrConflict) {
		return domain.Meeting{}, &Error{Code: Conflict, Err: err}
	} else if err != nil {
		return domain.Meeting{}, &Error{Code: Internal, Err: err}
	}
	return meeting, nil
}

func (s *MeetingService) Get(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) (domain.Meeting, error) {
	if strings.TrimSpace(string(meetingID)) == "" {
		return domain.Meeting{}, validation("meetingId", "must not be blank")
	}
	meeting, err := s.repository.Get(ctx, workspaceID, meetingID)
	if errors.Is(err, repository.ErrNotFound) {
		return domain.Meeting{}, &Error{Code: NotFound, Err: err}
	}
	if err != nil {
		return domain.Meeting{}, &Error{Code: Internal, Err: err}
	}
	return meeting, nil
}

func (s *MeetingService) Update(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID, title *string) (domain.Meeting, error) {
	meeting, err := s.Get(ctx, workspaceID, meetingID)
	if err != nil {
		return domain.Meeting{}, err
	}
	if title != nil {
		clean := strings.TrimSpace(*title)
		if clean == "" {
			return domain.Meeting{}, validation("title", "must not be blank")
		}
		if len(clean) > 200 {
			return domain.Meeting{}, validation("title", "must be at most 200 characters")
		}
		meeting.Title = clean
	}
	meeting.UpdatedAt = s.now().UTC()
	if err := s.repository.Update(ctx, meeting); err != nil {
		return domain.Meeting{}, &Error{Code: Internal, Err: err}
	}
	return meeting, nil
}

func (s *MeetingService) Delete(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) error {
	if strings.TrimSpace(string(meetingID)) == "" {
		return validation("meetingId", "must not be blank")
	}
	err := s.repository.Delete(ctx, workspaceID, meetingID)
	if errors.Is(err, repository.ErrNotFound) {
		return &Error{Code: NotFound, Err: err}
	}
	if err != nil {
		return &Error{Code: Internal, Err: err}
	}
	return nil
}

type cursorPayload struct {
	CreatedAt string `json:"createdAt"`
	ID        string `json:"id"`
}

func (s *MeetingService) List(ctx context.Context, workspaceID domain.WorkspaceID, cursor string, limit int) (MeetingPage, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return MeetingPage{}, validation("limit", "must be between 1 and 100")
	}
	position, err := decodeCursor(cursor)
	if err != nil {
		return MeetingPage{}, validation("cursor", "is malformed")
	}
	items, hasMore, err := s.repository.List(ctx, workspaceID, position, limit)
	if err != nil {
		return MeetingPage{}, &Error{Code: Internal, Err: err}
	}
	page := MeetingPage{Items: items, HasMore: hasMore}
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		next, err := encodeCursor(repository.MeetingPosition{CreatedAt: last.CreatedAt, ID: last.ID})
		if err != nil {
			return MeetingPage{}, &Error{Code: Internal, Err: err}
		}
		page.NextCursor = &next
	}
	return page, nil
}

func encodeCursor(position repository.MeetingPosition) (string, error) {
	raw, err := json.Marshal(cursorPayload{CreatedAt: position.CreatedAt.UTC().Format(time.RFC3339Nano), ID: string(position.ID)})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCursor(cursor string) (*repository.MeetingPosition, error) {
	if cursor == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, err
	}
	var payload cursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	createdAt, err := time.Parse(time.RFC3339Nano, payload.CreatedAt)
	if err != nil || payload.ID == "" {
		return nil, fmt.Errorf("invalid cursor")
	}
	return &repository.MeetingPosition{CreatedAt: createdAt, ID: domain.MeetingID(payload.ID)}, nil
}

type ConfigurationPatch struct {
	Transcription      *TranscriptionPatch
	Summary            *SummaryPatch
	RetainMedia        *bool
	AnalyticsEnabled   *bool
	ProcessingComplete *bool
}
type TranscriptionPatch struct {
	Provider, Model, Language *string
	TranslateToEnglish        *bool
}
type SummaryPatch struct {
	Provider, Model, OutputLanguage *string
	TemplateID                      **string
	AutoGenerate                    *bool
}

type ConfigurationService struct {
	configurations repository.ConfigurationRepository
	credentials    repository.CredentialRepository
}

func NewConfigurationService(configurations repository.ConfigurationRepository, credentials repository.CredentialRepository) *ConfigurationService {
	return &ConfigurationService{configurations: configurations, credentials: credentials}
}

func (s *ConfigurationService) Get(ctx context.Context, workspaceID domain.WorkspaceID) (domain.Configuration, []domain.CredentialStatus, error) {
	config, err := s.configurations.Get(ctx, workspaceID)
	if err != nil {
		return domain.Configuration{}, nil, &Error{Code: Internal, Err: err}
	}
	credentials, err := s.credentials.List(ctx, workspaceID)
	if err != nil {
		return domain.Configuration{}, nil, &Error{Code: Internal, Err: err}
	}
	return config, credentials, nil
}

func (s *ConfigurationService) Update(ctx context.Context, workspaceID domain.WorkspaceID, patch ConfigurationPatch) (domain.Configuration, []domain.CredentialStatus, error) {
	config, err := s.configurations.Update(ctx, workspaceID, func(config domain.Configuration) (domain.Configuration, error) {
		if patch.Transcription != nil {
			p := patch.Transcription
			if p.Provider != nil {
				if err := validateProvider(*p.Provider); err != nil {
					return domain.Configuration{}, validation("transcription.provider", "has an invalid format")
				}
				config.Transcription.Provider = *p.Provider
			}
			if p.Model != nil {
				config.Transcription.Model = strings.TrimSpace(*p.Model)
			}
			if p.Language != nil {
				if strings.TrimSpace(*p.Language) == "" {
					return domain.Configuration{}, validation("transcription.language", "must not be blank")
				}
				config.Transcription.Language = *p.Language
			}
			if p.TranslateToEnglish != nil {
				config.Transcription.TranslateToEnglish = *p.TranslateToEnglish
			}
		}
		if patch.Summary != nil {
			p := patch.Summary
			if p.Provider != nil {
				if err := validateProvider(*p.Provider); err != nil {
					return domain.Configuration{}, validation("summary.provider", "has an invalid format")
				}
				config.Summary.Provider = *p.Provider
			}
			if p.Model != nil {
				config.Summary.Model = strings.TrimSpace(*p.Model)
			}
			if p.OutputLanguage != nil {
				if strings.TrimSpace(*p.OutputLanguage) == "" {
					return domain.Configuration{}, validation("summary.outputLanguage", "must not be blank")
				}
				config.Summary.OutputLanguage = *p.OutputLanguage
			}
			if p.TemplateID != nil {
				config.Summary.TemplateID = *p.TemplateID
			}
			if p.AutoGenerate != nil {
				config.Summary.AutoGenerate = *p.AutoGenerate
			}
		}
		if patch.RetainMedia != nil {
			config.RetainMedia = *patch.RetainMedia
		}
		if patch.AnalyticsEnabled != nil {
			config.AnalyticsEnabled = *patch.AnalyticsEnabled
		}
		if patch.ProcessingComplete != nil {
			config.ProcessingComplete = *patch.ProcessingComplete
		}
		return config, nil
	})
	if err != nil {
		var appError *Error
		if errors.As(err, &appError) {
			return domain.Configuration{}, nil, appError
		}
		return domain.Configuration{}, nil, &Error{Code: Internal, Err: err}
	}
	_, credentials, err := s.Get(ctx, workspaceID)
	return config, credentials, err
}

type CredentialService struct {
	repository repository.CredentialRepository
}

func NewCredentialService(repo repository.CredentialRepository) *CredentialService {
	return &CredentialService{repository: repo}
}

var providerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func validateProvider(provider string) error {
	if !providerPattern.MatchString(provider) {
		return validation("provider", "has an invalid format")
	}
	return nil
}
func (s *CredentialService) List(ctx context.Context, workspaceID domain.WorkspaceID) ([]domain.CredentialStatus, error) {
	statuses, err := s.repository.List(ctx, workspaceID)
	if err != nil {
		return nil, &Error{Code: Internal, Err: err}
	}
	return statuses, nil
}
func (s *CredentialService) IsConfigured(ctx context.Context, workspaceID domain.WorkspaceID, provider string) (bool, error) {
	if err := validateProvider(provider); err != nil {
		return false, err
	}
	configured, err := s.repository.IsConfigured(ctx, workspaceID, provider)
	if err != nil {
		return false, &Error{Code: Internal, Err: err}
	}
	return configured, nil
}
func (s *CredentialService) Set(ctx context.Context, workspaceID domain.WorkspaceID, provider, secret string) (domain.CredentialStatus, error) {
	if err := validateProvider(provider); err != nil {
		return domain.CredentialStatus{}, err
	}
	if strings.TrimSpace(secret) == "" {
		return domain.CredentialStatus{}, validation("value", "must not be blank")
	}
	if err := s.repository.Set(ctx, workspaceID, provider, secret); err != nil {
		return domain.CredentialStatus{}, &Error{Code: Internal, Err: err}
	}
	return domain.CredentialStatus{Provider: provider, Configured: true}, nil
}
func (s *CredentialService) Delete(ctx context.Context, workspaceID domain.WorkspaceID, provider string) error {
	if err := validateProvider(provider); err != nil {
		return err
	}
	if err := s.repository.Delete(ctx, workspaceID, provider); err != nil {
		return &Error{Code: Internal, Err: err}
	}
	return nil
}

func DefaultConfiguration() domain.Configuration {
	return domain.Configuration{
		Transcription: domain.TranscriptionDefaults{Provider: "none", Model: "", Language: "auto"},
		Summary:       domain.SummaryDefaults{Provider: "none", Model: "", OutputLanguage: "auto"},
		RetainMedia:   true,
	}
}
