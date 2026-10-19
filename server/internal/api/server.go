package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/domain"
)

const maxBodyBytes = 1 << 20
const requestIDHeader = "X-Request-ID"

type contextKey string

const (
	requestIDKey contextKey = "request-id"
	workspaceKey contextKey = "workspace-id"
)

type Server struct {
	meetings      *application.MeetingService
	configuration *application.ConfigurationService
	credentials   *application.CredentialService
	logger        *slog.Logger
	workspaceID   domain.WorkspaceID
	version       string
}

func NewHandler(meetings *application.MeetingService, configuration *application.ConfigurationService, credentials *application.CredentialService, workspaceID domain.WorkspaceID, version string, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Server{meetings: meetings, configuration: configuration, credentials: credentials, logger: logger, workspaceID: workspaceID, version: version}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/capabilities", s.capabilities)
	mux.HandleFunc("GET /api/v1/meetings", s.listMeetings)
	mux.HandleFunc("POST /api/v1/meetings", s.createMeeting)
	mux.HandleFunc("GET /api/v1/meetings/{meetingId}", s.getMeeting)
	mux.HandleFunc("PATCH /api/v1/meetings/{meetingId}", s.updateMeeting)
	mux.HandleFunc("DELETE /api/v1/meetings/{meetingId}", s.deleteMeeting)
	mux.HandleFunc("GET /api/v1/configuration", s.getConfiguration)
	mux.HandleFunc("PATCH /api/v1/configuration", s.updateConfiguration)
	mux.HandleFunc("GET /api/v1/credentials", s.listCredentials)
	mux.HandleFunc("PUT /api/v1/credentials/{provider}", s.updateCredential)
	mux.HandleFunc("DELETE /api/v1/credentials/{provider}", s.deleteCredential)
	return s.requestID(s.workspace(s.accessLog(s.recover(mux))))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", APIVersion: "v1", ServerVersion: s.version})
}
func (s *Server) capabilities(w http.ResponseWriter, _ *http.Request) {
	response := capabilitiesResponse{APIVersion: "v1", MediaStorage: availability{Available: false}, Transcription: providerAvailability{Available: false, Providers: []string{}}, Summary: providerAvailability{Available: false, Providers: []string{}}}
	response.Configuration.ServerManaged = true
	response.Configuration.CredentialManagement = true
	writeJSON(w, http.StatusOK, response)
}
func workspaceID(r *http.Request) domain.WorkspaceID {
	return r.Context().Value(workspaceKey).(domain.WorkspaceID)
}

func (s *Server) listMeetings(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, r, &application.Error{Code: application.Validation, Details: map[string]any{"field": "limit", "reason": "must be an integer"}})
			return
		}
		limit = parsed
	}
	page, err := s.meetings.List(r.Context(), workspaceID(r), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]meetingDTO, 0, len(page.Items))
	for _, meeting := range page.Items {
		items = append(items, meetingFromDomain(meeting))
	}
	writeJSON(w, http.StatusOK, meetingListResponse{Items: items, Page: cursorPage{NextCursor: page.NextCursor, HasMore: page.HasMore}})
}
func (s *Server) createMeeting(w http.ResponseWriter, r *http.Request) {
	var input createMeetingRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	meeting, err := s.meetings.Create(r.Context(), workspaceID(r), input.Title)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, meetingResponse{Meeting: meetingFromDomain(meeting)})
}
func (s *Server) getMeeting(w http.ResponseWriter, r *http.Request) {
	meeting, err := s.meetings.Get(r.Context(), workspaceID(r), domain.MeetingID(r.PathValue("meetingId")))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, meetingResponse{Meeting: meetingFromDomain(meeting)})
}
func (s *Server) updateMeeting(w http.ResponseWriter, r *http.Request) {
	var input updateMeetingRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	var title *string
	if input.Title.set {
		title = &input.Title.value
	}
	meeting, err := s.meetings.Update(r.Context(), workspaceID(r), domain.MeetingID(r.PathValue("meetingId")), title)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, meetingResponse{Meeting: meetingFromDomain(meeting)})
}
func (s *Server) deleteMeeting(w http.ResponseWriter, r *http.Request) {
	if err := s.meetings.Delete(r.Context(), workspaceID(r), domain.MeetingID(r.PathValue("meetingId"))); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) getConfiguration(w http.ResponseWriter, r *http.Request) {
	config, statuses, err := s.configuration.Get(r.Context(), workspaceID(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, configurationFromDomain(workspaceID(r), config, statuses))
}
func (s *Server) updateConfiguration(w http.ResponseWriter, r *http.Request) {
	var input updateConfigurationRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	patch := application.ConfigurationPatch{}
	if input.Transcription.set {
		value := input.Transcription.value
		patch.Transcription = &application.TranscriptionPatch{Provider: optionalPointer(value.Provider), Model: optionalPointer(value.Model), Language: optionalPointer(value.Language), TranslateToEnglish: optionalPointer(value.TranslateToEnglish)}
	}
	if input.Summary.set {
		value := input.Summary.value
		var templateID **string
		if value.TemplateID.set {
			templateID = &value.TemplateID.value
		}
		patch.Summary = &application.SummaryPatch{Provider: optionalPointer(value.Provider), Model: optionalPointer(value.Model), OutputLanguage: optionalPointer(value.OutputLanguage), TemplateID: templateID, AutoGenerate: optionalPointer(value.AutoGenerate)}
	}
	if input.Recording.set {
		patch.RetainMedia = optionalPointer(input.Recording.value.RetainMedia)
	}
	if input.Privacy.set {
		patch.AnalyticsEnabled = optionalPointer(input.Privacy.value.AnalyticsEnabled)
	}
	if input.Notifications.set {
		patch.ProcessingComplete = optionalPointer(input.Notifications.value.ProcessingComplete)
	}
	config, statuses, err := s.configuration.Update(r.Context(), workspaceID(r), patch)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updateConfigurationResponse{Configuration: configurationFromDomain(workspaceID(r), config, statuses)})
}

func optionalPointer[T any](value optional[T]) *T {
	if !value.set {
		return nil
	}
	return &value.value
}
func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request) {
	statuses, err := s.credentials.List(r.Context(), workspaceID(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	result := credentialListResponse{Credentials: make([]credentialStatusDTO, 0, len(statuses))}
	for _, status := range statuses {
		result.Credentials = append(result.Credentials, credentialFromDomain(status))
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) updateCredential(w http.ResponseWriter, r *http.Request) {
	var input credentialWriteRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	status, err := s.credentials.Set(r.Context(), workspaceID(r), r.PathValue("provider"), input.Value)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, credentialUpdateResponse{Credential: credentialFromDomain(status)})
}
func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	if err := s.credentials.Delete(r.Context(), workspaceID(r), r.PathValue("provider")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return &application.Error{Code: application.Validation, Details: map[string]any{"field": "contentType", "reason": "must be application/json"}}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return &application.Error{Code: application.Validation, Details: map[string]any{"field": "body", "reason": "must be valid protocol JSON"}}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &application.Error{Code: application.Validation, Details: map[string]any{"field": "body", "reason": "must contain one JSON object"}}
	}
	valueDecoder := json.NewDecoder(bytes.NewReader(raw))
	valueDecoder.DisallowUnknownFields()
	if err := valueDecoder.Decode(destination); err != nil {
		return &application.Error{Code: application.Validation, Details: map[string]any{"field": "body", "reason": "must be valid protocol JSON"}}
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

var messages = map[application.ErrorCode]string{application.Validation: "Request validation failed", application.Unauthorized: "Authentication required", application.Forbidden: "Access forbidden", application.NotFound: "Resource not found", application.Conflict: "Resource conflict", application.Unavailable: "Service unavailable", application.Internal: "Internal server error"}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	appErr := &application.Error{Code: application.Internal}
	if !errors.As(err, &appErr) {
		appErr = &application.Error{Code: application.Internal}
	}
	status := map[application.ErrorCode]int{application.Validation: 400, application.Unauthorized: 401, application.Forbidden: 403, application.NotFound: 404, application.Conflict: 409, application.Unavailable: 503, application.Internal: 500}[appErr.Code]
	writeJSON(w, status, apiErrorResponse{Error: apiErrorBody{Code: string(appErr.Code), Message: messages[appErr.Code], Details: appErr.Details, RequestID: requestID(r.Context())}})
}

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(raw[:])
}
func requestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !safeRequestID.MatchString(id) {
			id = newRequestID()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}
func (s *Server) workspace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), workspaceKey, s.workspaceID)))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		wrapped := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(wrapped, r)
		status := wrapped.status
		if status == 0 {
			status = http.StatusOK
		}
		s.logger.Info("http request", "method", r.Method, "route", r.URL.Path, "status", status, "duration", time.Since(started), "request_id", requestID(r.Context()))
	})
}
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				s.logger.Error("request panic", "request_id", requestID(r.Context()))
				writeError(w, r, &application.Error{Code: application.Internal})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
