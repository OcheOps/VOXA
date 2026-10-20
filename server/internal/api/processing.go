package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/domain"
)

// WithProcessing enables processing jobs and transcripts. Transcription is
// advertised only when the service reports a usable engine.
func WithProcessing(processing *application.ProcessingService) Option {
	return func(s *Server) { s.processing = processing }
}

type processingJobDTO struct {
	ID           string   `json:"id"`
	WorkspaceID  string   `json:"workspaceId"`
	MeetingID    string   `json:"meetingId"`
	MediaAssetID *string  `json:"mediaAssetId"`
	Kind         string   `json:"kind"`
	Status       string   `json:"status"`
	Progress     *float64 `json:"progress"`
	ErrorCode    *string  `json:"errorCode"`
	ErrorMessage *string  `json:"errorMessage"`
	CreatedAt    string   `json:"createdAt"`
	StartedAt    *string  `json:"startedAt"`
	CompletedAt  *string  `json:"completedAt"`
}

func optionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

// jobFromDomain never includes attempts, leases or internal errors.
func jobFromDomain(job domain.ProcessingJob) processingJobDTO {
	dto := processingJobDTO{ID: string(job.ID), WorkspaceID: string(job.WorkspaceID), MeetingID: string(job.MeetingID), Kind: string(job.Type),
		Status: string(job.Status), Progress: job.Progress, ErrorCode: job.ErrorCode, ErrorMessage: job.ErrorMessage,
		CreatedAt: job.CreatedAt.UTC().Format(time.RFC3339Nano), StartedAt: optionalTime(job.StartedAt), CompletedAt: optionalTime(job.CompletedAt)}
	if job.MediaAssetID != nil {
		id := string(*job.MediaAssetID)
		dto.MediaAssetID = &id
	}
	return dto
}

type transcriptSegmentDTO struct {
	ID               string   `json:"id"`
	Text             string   `json:"text"`
	StartTimeSeconds float64  `json:"startTimeSeconds"`
	EndTimeSeconds   float64  `json:"endTimeSeconds"`
	DurationSeconds  float64  `json:"durationSeconds"`
	Sequence         int      `json:"sequence"`
	IsFinal          bool     `json:"isFinal"`
	Confidence       *float64 `json:"confidence"`
	Source           *string  `json:"source"`
	TimingProvenance string   `json:"timingProvenance"`
}

type transcriptDTO struct {
	ID              string                 `json:"id"`
	WorkspaceID     string                 `json:"workspaceId"`
	MeetingID       string                 `json:"meetingId"`
	MediaAssetID    string                 `json:"mediaAssetId"`
	ProcessingJobID *string                `json:"processingJobId"`
	Language        *string                `json:"language"`
	ASREngine       string                 `json:"asrEngine"`
	ASRModel        string                 `json:"asrModel"`
	Segments        []transcriptSegmentDTO `json:"segments"`
	CreatedAt       string                 `json:"createdAt"`
	UpdatedAt       string                 `json:"updatedAt"`
}

func transcriptFromDomain(t domain.Transcript) transcriptDTO {
	dto := transcriptDTO{ID: string(t.ID), WorkspaceID: string(t.WorkspaceID), MeetingID: string(t.MeetingID), MediaAssetID: string(t.MediaAssetID),
		Language: t.Language, ASREngine: t.ASREngine, ASRModel: t.ASRModel, Segments: make([]transcriptSegmentDTO, 0, len(t.Segments)),
		CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	if t.ProcessingJobID != nil {
		id := string(*t.ProcessingJobID)
		dto.ProcessingJobID = &id
	}
	for _, segment := range t.Segments {
		dto.Segments = append(dto.Segments, transcriptSegmentDTO{
			ID: string(t.ID) + ":" + strconv.Itoa(segment.Sequence), Text: segment.Text,
			StartTimeSeconds: segment.StartSeconds, EndTimeSeconds: segment.EndSeconds, DurationSeconds: segment.EndSeconds - segment.StartSeconds,
			Sequence: segment.Sequence, IsFinal: true, Confidence: segment.Confidence, TimingProvenance: "asr",
		})
	}
	return dto
}

type processingErrorDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type mediaTranscriptResponse struct {
	MediaAssetID *string             `json:"mediaAssetId"`
	State        string              `json:"state"`
	Error        *processingErrorDTO `json:"error"`
	Transcript   *transcriptDTO      `json:"transcript"`
}

func transcriptResponse(processing application.AssetProcessing) mediaTranscriptResponse {
	id := string(processing.Asset.ID)
	response := mediaTranscriptResponse{MediaAssetID: &id, State: string(processing.State)}
	if processing.LastFailure != nil && processing.LastFailure.ErrorCode != nil {
		message := ""
		if processing.LastFailure.ErrorMessage != nil {
			message = *processing.LastFailure.ErrorMessage
		}
		response.Error = &processingErrorDTO{Code: *processing.LastFailure.ErrorCode, Message: message}
	} else if processing.LastFailure != nil && processing.LastFailure.Status == domain.JobCancelled {
		response.Error = &processingErrorDTO{Code: "cancelled", Message: "Processing was cancelled."}
	}
	if processing.Transcript != nil {
		transcript := transcriptFromDomain(*processing.Transcript)
		response.Transcript = &transcript
	}
	return response
}

type jobListResponse struct {
	Items []processingJobDTO `json:"items"`
}
type jobResponse struct {
	Job processingJobDTO `json:"job"`
}

func (s *Server) registerProcessing(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/meetings/{meetingId}/jobs", s.authorize(s.listMeetingJobs))
	mux.HandleFunc("GET /api/v1/meetings/{meetingId}/transcript", s.authorize(s.meetingTranscript))
	mux.HandleFunc("GET /api/v1/media/{mediaAssetId}/transcript", s.authorize(s.mediaTranscript))
	mux.HandleFunc("POST /api/v1/media/{mediaAssetId}/transcription", s.authorize(s.requestTranscription))
	mux.HandleFunc("POST /api/v1/jobs/{jobId}/cancel", s.authorize(s.cancelJob))
}

func (s *Server) processingDisabled(w http.ResponseWriter, r *http.Request) bool {
	if s.processing != nil {
		return false
	}
	s.writeError(w, r, &application.Error{Code: application.Unavailable, Op: "processing", Category: "processing_disabled"})
	return true
}

func (s *Server) listMeetingJobs(w http.ResponseWriter, r *http.Request) {
	if s.processing == nil {
		writeJSON(w, http.StatusOK, jobListResponse{Items: []processingJobDTO{}})
		return
	}
	jobs, err := s.processing.MeetingJobs(r.Context(), workspaceID(r), domain.MeetingID(r.PathValue("meetingId")))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items := make([]processingJobDTO, 0, len(jobs))
	for _, job := range jobs {
		items = append(items, jobFromDomain(job))
	}
	writeJSON(w, http.StatusOK, jobListResponse{Items: items})
}

func (s *Server) meetingTranscript(w http.ResponseWriter, r *http.Request) {
	meetingID := domain.MeetingID(r.PathValue("meetingId"))
	if s.processing == nil {
		if _, err := s.meetings.Get(r.Context(), workspaceID(r), meetingID); err != nil {
			s.writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, mediaTranscriptResponse{State: string(application.StateUnavailable)})
		return
	}
	processing, ok, err := s.processing.MeetingTranscript(r.Context(), workspaceID(r), meetingID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, mediaTranscriptResponse{State: string(application.StateNone)})
		return
	}
	writeJSON(w, http.StatusOK, transcriptResponse(processing))
}

func (s *Server) mediaTranscript(w http.ResponseWriter, r *http.Request) {
	assetID := mediaAssetID(r)
	if s.processing == nil {
		if s.media == nil {
			s.processingDisabled(w, r)
			return
		}
		if _, err := s.media.GetAsset(r.Context(), workspaceID(r), assetID); err != nil {
			s.writeError(w, r, err)
			return
		}
		id := string(assetID)
		writeJSON(w, http.StatusOK, mediaTranscriptResponse{MediaAssetID: &id, State: string(application.StateUnavailable)})
		return
	}
	processing, err := s.processing.AssetProcessing(r.Context(), workspaceID(r), assetID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, transcriptResponse(processing))
}

func (s *Server) requestTranscription(w http.ResponseWriter, r *http.Request) {
	if s.processingDisabled(w, r) {
		return
	}
	job, err := s.processing.RequestTranscription(r.Context(), workspaceID(r), mediaAssetID(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, jobResponse{Job: jobFromDomain(job)})
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	if s.processingDisabled(w, r) {
		return
	}
	job, err := s.processing.CancelJob(r.Context(), workspaceID(r), domain.ProcessingJobID(r.PathValue("jobId")))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, jobResponse{Job: jobFromDomain(job)})
}
