package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
)

type healthResponse struct {
	Status        string `json:"status"`
	APIVersion    string `json:"apiVersion"`
	ServerVersion string `json:"serverVersion"`
}
type availability struct {
	Available bool `json:"available"`
}
type providerAvailability struct {
	Available bool     `json:"available"`
	Providers []string `json:"providers"`
}
type capabilitiesResponse struct {
	APIVersion    string               `json:"apiVersion"`
	MediaStorage  availability         `json:"mediaStorage"`
	Transcription providerAvailability `json:"transcription"`
	Summary       providerAvailability `json:"summary"`
	Configuration struct {
		ServerManaged        bool `json:"serverManaged"`
		CredentialManagement bool `json:"credentialManagement"`
	} `json:"configuration"`
}
type meetingDTO struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

func meetingFromDomain(m domain.Meeting) meetingDTO {
	return meetingDTO{ID: string(m.ID), WorkspaceID: string(m.WorkspaceID), Title: m.Title, Status: string(m.Status), CreatedAt: m.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: m.UpdatedAt.UTC().Format(time.RFC3339Nano)}
}

type currentUserDTO struct {
	ID          string  `json:"id"`
	Email       *string `json:"email"`
	DisplayName *string `json:"displayName"`
}
type currentWorkspaceDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type meResponse struct {
	User      currentUserDTO      `json:"user"`
	Workspace currentWorkspaceDTO `json:"workspace"`
	Role      string              `json:"role"`
}

type mediaAssetDTO struct {
	ID                 string   `json:"id"`
	WorkspaceID        string   `json:"workspaceId"`
	MeetingID          string   `json:"meetingId"`
	RecordingSessionID *string  `json:"recordingSessionId"`
	Kind               string   `json:"kind"`
	MimeType           string   `json:"mimeType"`
	Status             string   `json:"status"`
	DurationSeconds    *float64 `json:"durationSeconds"`
	SizeBytes          int64    `json:"sizeBytes"`
	CreatedAt          string   `json:"createdAt"`
	UpdatedAt          string   `json:"updatedAt"`
}

// mediaAssetFromDomain never includes the internal storage key.
func mediaAssetFromDomain(a domain.MediaAsset) mediaAssetDTO {
	dto := mediaAssetDTO{ID: string(a.ID), WorkspaceID: string(a.WorkspaceID), MeetingID: string(a.MeetingID), Kind: string(a.Kind),
		MimeType: a.MimeType, Status: string(a.Status), DurationSeconds: a.DurationSeconds, SizeBytes: a.SizeBytes,
		CreatedAt: a.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: a.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	if a.RecordingSessionID != nil {
		id := string(*a.RecordingSessionID)
		dto.RecordingSessionID = &id
	}
	return dto
}

type recordingSessionDTO struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspaceId"`
	MeetingID   string  `json:"meetingId"`
	Status      string  `json:"status"`
	StartedAt   string  `json:"startedAt"`
	EndedAt     *string `json:"endedAt"`
	CreatedAt   string  `json:"createdAt"`
}

func recordingSessionFromDomain(s domain.RecordingSession) recordingSessionDTO {
	dto := recordingSessionDTO{ID: string(s.ID), WorkspaceID: string(s.WorkspaceID), MeetingID: string(s.MeetingID), Status: string(s.Status),
		StartedAt: s.StartedAt.UTC().Format(time.RFC3339Nano), CreatedAt: s.CreatedAt.UTC().Format(time.RFC3339Nano)}
	if s.EndedAt != nil {
		ended := s.EndedAt.UTC().Format(time.RFC3339Nano)
		dto.EndedAt = &ended
	}
	return dto
}

type mediaUploadDTO struct {
	MediaAssetID   string `json:"mediaAssetId"`
	Status         string `json:"status"`
	ReceivedChunks []int  `json:"receivedChunks"`
	ReceivedBytes  int64  `json:"receivedBytes"`
	MaxChunkBytes  int64  `json:"maxChunkBytes"`
	MaxTotalBytes  int64  `json:"maxTotalBytes"`
}
type createRecordingRequest struct {
	MimeType string `json:"mimeType"`
}
type createRecordingResponse struct {
	RecordingSession recordingSessionDTO `json:"recordingSession"`
	MediaAsset       mediaAssetDTO       `json:"mediaAsset"`
	Upload           mediaUploadDTO      `json:"upload"`
}
type uploadResponse struct {
	Upload mediaUploadDTO `json:"upload"`
}
type finalizeMediaRequest struct {
	ChunkCount      int      `json:"chunkCount"`
	DurationSeconds *float64 `json:"durationSeconds"`
}
type mediaAssetResponse struct {
	MediaAsset mediaAssetDTO `json:"mediaAsset"`
}
type meetingMediaResponse struct {
	Items []mediaAssetDTO `json:"items"`
}
type mediaPlaybackResponse struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
}

type createMeetingRequest struct {
	Title string `json:"title"`
}
type updateMeetingRequest struct {
	Title optional[string] `json:"title,omitempty"`
}
type meetingResponse struct {
	Meeting meetingDTO `json:"meeting"`
}
type meetingListResponse struct {
	Items []meetingDTO `json:"items"`
	Page  cursorPage   `json:"page"`
}
type cursorPage struct {
	NextCursor *string `json:"nextCursor"`
	HasMore    bool    `json:"hasMore"`
}

type transcriptionDefaultsDTO struct {
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	Language           string `json:"language"`
	TranslateToEnglish bool   `json:"translateToEnglish"`
}
type summaryDefaultsDTO struct {
	Provider       string  `json:"provider"`
	Model          string  `json:"model"`
	OutputLanguage string  `json:"outputLanguage"`
	TemplateID     *string `json:"templateId"`
	AutoGenerate   bool    `json:"autoGenerate"`
}
type credentialStatusDTO struct {
	Provider    string  `json:"provider"`
	Configured  bool    `json:"configured"`
	MaskedValue *string `json:"maskedValue,omitempty"`
}
type configurationResponse struct {
	WorkspaceID   string                   `json:"workspaceId"`
	Transcription transcriptionDefaultsDTO `json:"transcription"`
	Summary       summaryDefaultsDTO       `json:"summary"`
	Recording     struct {
		RetainMedia bool `json:"retainMedia"`
	} `json:"recording"`
	Privacy struct {
		AnalyticsEnabled bool `json:"analyticsEnabled"`
	} `json:"privacy"`
	Notifications struct {
		ProcessingComplete bool `json:"processingComplete"`
	} `json:"notifications"`
	Credentials []credentialStatusDTO `json:"credentials"`
}

func credentialFromDomain(status domain.CredentialStatus) credentialStatusDTO {
	dto := credentialStatusDTO{Provider: status.Provider, Configured: status.Configured}
	if status.Configured {
		mask := "••••"
		dto.MaskedValue = &mask
	}
	return dto
}
func configurationFromDomain(workspaceID domain.WorkspaceID, config domain.Configuration, statuses []domain.CredentialStatus) configurationResponse {
	result := configurationResponse{WorkspaceID: string(workspaceID), Transcription: transcriptionDefaultsDTO{Provider: config.Transcription.Provider, Model: config.Transcription.Model, Language: config.Transcription.Language, TranslateToEnglish: config.Transcription.TranslateToEnglish}, Summary: summaryDefaultsDTO{Provider: config.Summary.Provider, Model: config.Summary.Model, OutputLanguage: config.Summary.OutputLanguage, TemplateID: config.Summary.TemplateID, AutoGenerate: config.Summary.AutoGenerate}, Credentials: make([]credentialStatusDTO, 0, len(statuses))}
	result.Recording.RetainMedia = config.RetainMedia
	result.Privacy.AnalyticsEnabled = config.AnalyticsEnabled
	result.Notifications.ProcessingComplete = config.ProcessingComplete
	for _, status := range statuses {
		result.Credentials = append(result.Credentials, credentialFromDomain(status))
	}
	return result
}

type updateConfigurationRequest struct {
	Transcription optionalObject[transcriptionPatchDTO] `json:"transcription,omitempty"`
	Summary       optionalObject[summaryPatchDTO]       `json:"summary,omitempty"`
	Recording     optionalObject[recordingPatchDTO]     `json:"recording,omitempty"`
	Privacy       optionalObject[privacyPatchDTO]       `json:"privacy,omitempty"`
	Notifications optionalObject[notificationPatchDTO]  `json:"notifications,omitempty"`
}

type transcriptionPatchDTO struct {
	Provider           optional[string] `json:"provider,omitempty"`
	Model              optional[string] `json:"model,omitempty"`
	Language           optional[string] `json:"language,omitempty"`
	TranslateToEnglish optional[bool]   `json:"translateToEnglish,omitempty"`
}
type summaryPatchDTO struct {
	Provider       optional[string] `json:"provider,omitempty"`
	Model          optional[string] `json:"model,omitempty"`
	OutputLanguage optional[string] `json:"outputLanguage,omitempty"`
	TemplateID     nullableString   `json:"templateId,omitempty"`
	AutoGenerate   optional[bool]   `json:"autoGenerate,omitempty"`
}
type recordingPatchDTO struct {
	RetainMedia optional[bool] `json:"retainMedia,omitempty"`
}
type privacyPatchDTO struct {
	AnalyticsEnabled optional[bool] `json:"analyticsEnabled,omitempty"`
}
type notificationPatchDTO struct {
	ProcessingComplete optional[bool] `json:"processingComplete,omitempty"`
}

type optionalObject[T any] struct {
	set   bool
	value T
}

func (value *optionalObject[T]) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		return fmt.Errorf("must not be null")
	}
	value.set = true
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(&value.value)
}

type optional[T any] struct {
	set   bool
	value T
}

func (value *optional[T]) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		return fmt.Errorf("must not be null")
	}
	value.set = true
	return json.Unmarshal(data, &value.value)
}

type nullableString struct {
	set   bool
	value *string
}

func (value *nullableString) UnmarshalJSON(data []byte) error {
	value.set = true
	if bytes.Equal(data, []byte("null")) {
		value.value = nil
		return nil
	}
	var decoded string
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value.value = &decoded
	return nil
}

type updateConfigurationResponse struct {
	Configuration configurationResponse `json:"configuration"`
}
type credentialListResponse struct {
	Credentials []credentialStatusDTO `json:"credentials"`
}

// credentialWriteRequest decodes the submitted value directly into a
// redacting secret type; it is never held in a plain string field.
type credentialWriteRequest struct {
	Value domain.CredentialSecret `json:"value"`
}
type credentialUpdateResponse struct {
	Credential credentialStatusDTO `json:"credential"`
}
type apiErrorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	RequestID string         `json:"requestId,omitempty"`
}
type apiErrorResponse struct {
	Error apiErrorBody `json:"error"`
}
