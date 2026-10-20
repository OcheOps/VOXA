package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/domain"
)

// WithSummaries enables server-side summaries. Capabilities advertise them
// only when the operator enabled at least one provider.
func WithSummaries(summaries *application.SummaryService) Option {
	return func(s *Server) { s.summaries = summaries }
}

type summaryDTO struct {
	ID              string          `json:"id"`
	WorkspaceID     string          `json:"workspaceId"`
	MeetingID       string          `json:"meetingId"`
	TranscriptID    string          `json:"transcriptId"`
	MediaAssetID    *string         `json:"mediaAssetId"`
	ProcessingJobID *string         `json:"processingJobId"`
	Provider        string          `json:"provider"`
	Model           string          `json:"model"`
	TemplateID      string          `json:"templateId"`
	OutputLanguage  *string         `json:"outputLanguage"`
	Content         json.RawMessage `json:"content"`
	CreatedAt       string          `json:"createdAt"`
	UpdatedAt       string          `json:"updatedAt"`
}

// summaryFromDomain exposes provenance and content; usage stays internal.
func summaryFromDomain(summary domain.Summary) summaryDTO {
	dto := summaryDTO{ID: string(summary.ID), WorkspaceID: string(summary.WorkspaceID), MeetingID: string(summary.MeetingID),
		TranscriptID: string(summary.TranscriptID), Provider: summary.Provider, Model: summary.Model, TemplateID: summary.TemplateID,
		OutputLanguage: summary.OutputLanguage, Content: summary.Content,
		CreatedAt: summary.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: summary.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	if summary.MediaAssetID != nil {
		id := string(*summary.MediaAssetID)
		dto.MediaAssetID = &id
	}
	if summary.ProcessingJobID != nil {
		id := string(*summary.ProcessingJobID)
		dto.ProcessingJobID = &id
	}
	return dto
}

type meetingSummaryResponse struct {
	MeetingID           string              `json:"meetingId"`
	State               string              `json:"state"`
	Reason              *string             `json:"reason"`
	Stale               bool                `json:"stale"`
	CurrentTranscriptID *string             `json:"currentTranscriptId"`
	Error               *processingErrorDTO `json:"error"`
	Job                 *processingJobDTO   `json:"job"`
	Summary             *summaryDTO         `json:"summary"`
}

type generateSummaryRequest struct {
	TemplateID   *string `json:"templateId"`
	CustomPrompt *string `json:"customPrompt"`
}

type summaryTemplateDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type summaryTemplateListResponse struct {
	Items []summaryTemplateDTO `json:"items"`
}

func (s *Server) registerSummaries(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/meetings/{meetingId}/summary", s.authorize(s.getSummary))
	mux.HandleFunc("POST /api/v1/meetings/{meetingId}/summary", s.authorize(s.generateSummary))
	mux.HandleFunc("POST /api/v1/meetings/{meetingId}/summary/cancel", s.authorize(s.cancelSummary))
	mux.HandleFunc("GET /api/v1/summary-templates", s.authorize(s.listSummaryTemplates))
}

func (s *Server) summaryAvailability() providerAvailability {
	if s.summaries != nil && s.summaries.Available() {
		return providerAvailability{Available: true, Providers: s.summaries.Providers()}
	}
	return providerAvailability{Available: false, Providers: []string{}}
}

func meetingSummaryFromApp(meetingID domain.MeetingID, view application.MeetingSummary) meetingSummaryResponse {
	response := meetingSummaryResponse{MeetingID: string(meetingID), State: string(view.State), Stale: view.Stale}
	if view.Reason != "" {
		reason := view.Reason
		response.Reason = &reason
	}
	if view.CurrentTranscriptID != nil {
		id := string(*view.CurrentTranscriptID)
		response.CurrentTranscriptID = &id
	}
	if view.Job != nil {
		job := jobFromDomain(*view.Job)
		response.Job = &job
		if view.State == application.SummaryFailed && view.Job.ErrorCode != nil {
			message := ""
			if view.Job.ErrorMessage != nil {
				message = *view.Job.ErrorMessage
			}
			response.Error = &processingErrorDTO{Code: *view.Job.ErrorCode, Message: message}
		} else if view.State == application.SummaryCancelled {
			response.Error = &processingErrorDTO{Code: "cancelled", Message: "Summary generation was cancelled."}
		}
	}
	if view.Summary != nil {
		summary := summaryFromDomain(*view.Summary)
		response.Summary = &summary
	}
	return response
}

func (s *Server) getSummary(w http.ResponseWriter, r *http.Request) {
	meetingID := domain.MeetingID(r.PathValue("meetingId"))
	if s.summaries == nil {
		if _, err := s.meetings.Get(r.Context(), workspaceID(r), meetingID); err != nil {
			s.writeError(w, r, err)
			return
		}
		reason := "disabled"
		writeJSON(w, http.StatusOK, meetingSummaryResponse{MeetingID: string(meetingID), State: string(application.SummaryUnavailable), Reason: &reason})
		return
	}
	view, err := s.summaries.Get(r.Context(), workspaceID(r), meetingID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, meetingSummaryFromApp(meetingID, view))
}

func (s *Server) generateSummary(w http.ResponseWriter, r *http.Request) {
	if s.summaries == nil {
		s.writeError(w, r, &application.Error{Code: application.Unavailable, Op: "summaries", Category: "summaries_disabled"})
		return
	}
	var input generateSummaryRequest
	// The body is optional: an empty POST uses the workspace configuration.
	if r.ContentLength != 0 || strings.TrimSpace(r.Header.Get("Content-Type")) != "" {
		if err := decodeJSON(w, r, &input); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	job, err := s.summaries.Request(r.Context(), workspaceID(r), domain.MeetingID(r.PathValue("meetingId")),
		application.SummaryRequest{TemplateID: input.TemplateID, CustomPrompt: input.CustomPrompt})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, jobResponse{Job: jobFromDomain(job)})
}

func (s *Server) cancelSummary(w http.ResponseWriter, r *http.Request) {
	if s.summaries == nil {
		s.writeError(w, r, &application.Error{Code: application.Unavailable, Op: "summaries", Category: "summaries_disabled"})
		return
	}
	job, err := s.summaries.Cancel(r.Context(), workspaceID(r), domain.MeetingID(r.PathValue("meetingId")))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, jobResponse{Job: jobFromDomain(job)})
}

func (s *Server) listSummaryTemplates(w http.ResponseWriter, r *http.Request) {
	items := []summaryTemplateDTO{}
	if s.summaries != nil {
		for _, template := range s.summaries.Templates() {
			items = append(items, summaryTemplateDTO{ID: template.ID, Name: template.Name, Description: template.Description})
		}
	}
	writeJSON(w, http.StatusOK, summaryTemplateListResponse{Items: items})
}
