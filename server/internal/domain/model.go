package domain

import "time"

type WorkspaceID string
type MeetingID string

type MeetingStatus string

const MeetingCreated MeetingStatus = "created"

type Meeting struct {
	ID          MeetingID
	WorkspaceID WorkspaceID
	Title       string
	Status      MeetingStatus
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type TranscriptionDefaults struct {
	Provider           string
	Model              string
	Language           string
	TranslateToEnglish bool
}

type SummaryDefaults struct {
	Provider       string
	Model          string
	OutputLanguage string
	TemplateID     *string
	AutoGenerate   bool
}

type Configuration struct {
	Transcription      TranscriptionDefaults
	Summary            SummaryDefaults
	RetainMedia        bool
	AnalyticsEnabled   bool
	ProcessingComplete bool
}

type CredentialStatus struct {
	Provider   string
	Configured bool
}
