package domain

import (
	"encoding/json"
	"time"
)

type SummaryID string

// SummaryUsage is provider-reported usage, kept for operators. Token counts
// are nil when the provider does not report them.
type SummaryUsage struct {
	InputTokens  *int  `json:"inputTokens,omitempty"`
	OutputTokens *int  `json:"outputTokens,omitempty"`
	Requests     int   `json:"requests"`
	DurationMs   int64 `json:"durationMs"`
}

// Summary is the current server-generated summary of a meeting, with the
// provenance needed to tell which transcript, provider, model and template
// produced it. Content is a structured JSON object (see the summary package).
type Summary struct {
	ID              SummaryID
	WorkspaceID     WorkspaceID
	MeetingID       MeetingID
	TranscriptID    TranscriptID
	MediaAssetID    *MediaAssetID
	ProcessingJobID *ProcessingJobID
	Provider        string
	Model           string
	TemplateID      string
	OutputLanguage  *string
	Content         json.RawMessage
	Usage           *SummaryUsage
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
