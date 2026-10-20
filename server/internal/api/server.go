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
	"github.com/OcheOps/voxa/server/internal/repository"
)

const maxBodyBytes = 1 << 20
const requestIDHeader = "X-Request-ID"

type contextKey string

const (
	requestIDKey contextKey = "request-id"
	stateKey     contextKey = "request-state"
)

type Server struct {
	meetings      *application.MeetingService
	configuration *application.ConfigurationService
	credentials   *application.CredentialService
	authenticator Authenticator
	resolver      WorkspaceResolver
	logger        *slog.Logger
	version       string
	media         *application.MediaService
	signer        *PlaybackSigner
	processing    *application.ProcessingService
	summaries     *application.SummaryService
}

func NewHandler(meetings *application.MeetingService, configuration *application.ConfigurationService, credentials *application.CredentialService, authenticator Authenticator, resolver WorkspaceResolver, version string, logger *slog.Logger, options ...Option) http.Handler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Server{meetings: meetings, configuration: configuration, credentials: credentials, authenticator: authenticator, resolver: resolver, logger: logger, version: version}
	for _, option := range options {
		option(s)
	}
	mux := http.NewServeMux()
	// Public: liveness and non-sensitive capability discovery.
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/capabilities", s.capabilities)
	// Protected: authenticated identity + workspace membership required.
	mux.HandleFunc("GET /api/v1/me", s.authorize(s.me))
	mux.HandleFunc("GET /api/v1/meetings", s.authorize(s.listMeetings))
	mux.HandleFunc("POST /api/v1/meetings", s.authorize(s.createMeeting))
	mux.HandleFunc("GET /api/v1/meetings/{meetingId}", s.authorize(s.getMeeting))
	mux.HandleFunc("PATCH /api/v1/meetings/{meetingId}", s.authorize(s.updateMeeting))
	mux.HandleFunc("DELETE /api/v1/meetings/{meetingId}", s.authorize(s.deleteMeeting))
	mux.HandleFunc("GET /api/v1/configuration", s.authorize(s.getConfiguration))
	mux.HandleFunc("PATCH /api/v1/configuration", s.authorize(s.updateConfiguration))
	mux.HandleFunc("GET /api/v1/credentials", s.authorize(s.listCredentials))
	mux.HandleFunc("PUT /api/v1/credentials/{provider}", s.authorize(s.updateCredential))
	mux.HandleFunc("DELETE /api/v1/credentials/{provider}", s.authorize(s.deleteCredential))
	s.registerMedia(mux)
	s.registerProcessing(mux)
	s.registerSummaries(mux)
	return s.requestID(securityHeaders(s.accessLog(s.recover(mux))))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", APIVersion: "v1", ServerVersion: s.version})
}
func (s *Server) capabilities(w http.ResponseWriter, _ *http.Request) {
	response := capabilitiesResponse{APIVersion: "v1", MediaStorage: availability{Available: s.media != nil}, Transcription: s.transcriptionAvailability(), Summary: s.summaryAvailability()}
	response.Configuration.ServerManaged = true
	response.Configuration.CredentialManagement = true
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) listMeetings(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			s.writeError(w, r, &application.Error{Code: application.Validation, Details: map[string]any{"field": "limit", "reason": "must be an integer"}})
			return
		}
		limit = parsed
	}
	page, err := s.meetings.List(r.Context(), workspaceID(r), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		s.writeError(w, r, err)
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
		s.writeError(w, r, err)
		return
	}
	meeting, err := s.meetings.Create(r.Context(), workspaceID(r), input.Title)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, meetingResponse{Meeting: meetingFromDomain(meeting)})
}
func (s *Server) getMeeting(w http.ResponseWriter, r *http.Request) {
	meeting, err := s.meetings.Get(r.Context(), workspaceID(r), domain.MeetingID(r.PathValue("meetingId")))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, meetingResponse{Meeting: meetingFromDomain(meeting)})
}
func (s *Server) updateMeeting(w http.ResponseWriter, r *http.Request) {
	var input updateMeetingRequest
	if err := decodeJSON(w, r, &input); err != nil {
		s.writeError(w, r, err)
		return
	}
	var title *string
	if input.Title.set {
		title = &input.Title.value
	}
	meeting, err := s.meetings.Update(r.Context(), workspaceID(r), domain.MeetingID(r.PathValue("meetingId")), title)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, meetingResponse{Meeting: meetingFromDomain(meeting)})
}
func (s *Server) deleteMeeting(w http.ResponseWriter, r *http.Request) {
	if err := s.meetings.Delete(r.Context(), workspaceID(r), domain.MeetingID(r.PathValue("meetingId"))); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) getConfiguration(w http.ResponseWriter, r *http.Request) {
	config, statuses, err := s.configuration.Get(r.Context(), workspaceID(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, configurationFromDomain(workspaceID(r), config, statuses))
}
func (s *Server) updateConfiguration(w http.ResponseWriter, r *http.Request) {
	var input updateConfigurationRequest
	if err := decodeJSON(w, r, &input); err != nil {
		s.writeError(w, r, err)
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
		s.writeError(w, r, err)
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
		s.writeError(w, r, err)
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
		s.writeError(w, r, err)
		return
	}
	defer input.Value.Wipe()
	status, err := s.credentials.Set(r.Context(), workspaceID(r), r.PathValue("provider"), input.Value)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, credentialUpdateResponse{Credential: credentialFromDomain(status)})
}
func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	if err := s.credentials.Delete(r.Context(), workspaceID(r), r.PathValue("provider")); err != nil {
		s.writeError(w, r, err)
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

var statuses = map[application.ErrorCode]int{application.Validation: 400, application.Unauthorized: 401, application.Forbidden: 403, application.NotFound: 404, application.Conflict: 409, application.Unavailable: 503, application.Internal: 500}

// writeError sends a generic, safe error body. Server failures (internal,
// unavailable) and access decisions (unauthorized, forbidden, auth
// conflicts) are logged with operational context; ordinary client errors
// such as validation or not-found are not.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	appErr := &application.Error{Code: application.Internal}
	if !errors.As(err, &appErr) {
		appErr = &application.Error{Code: application.Internal, Err: err}
	}
	switch {
	case appErr.Code == application.Internal, appErr.Code == application.Unavailable,
		appErr.Code == application.Unauthorized, appErr.Code == application.Forbidden, appErr.Category != "":
		s.logFailure(r, appErr)
	}
	if appErr.Code == application.Unauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="voxa"`)
	}
	writeJSON(w, statuses[appErr.Code], apiErrorResponse{Error: apiErrorBody{Code: string(appErr.Code), Message: messages[appErr.Code], Details: appErr.Details, RequestID: requestID(r.Context())}})
}

// logFailure records server-side failures. It logs identifiers, the route
// template, and the error chain. Adapters return sanitized errors (no SQL
// parameters, server messages, keys or secrets), so the chain is safe to log.
// Request bodies, headers and query strings are never logged.
func (s *Server) logFailure(r *http.Request, appErr *application.Error) {
	level := slog.LevelError
	if appErr.Code != application.Internal {
		level = slog.LevelWarn
	}
	category := appErr.Category
	if category == "" {
		category = errorCategory(appErr.Err)
	}
	attrs := []slog.Attr{
		slog.String("request_id", requestID(r.Context())),
		slog.String("method", r.Method),
		slog.String("route", r.Pattern),
		slog.String("code", string(appErr.Code)),
		slog.String("operation", appErr.Op),
		slog.String("category", category),
	}
	attrs = append(attrs, principalAttrs(r.Context())...)
	if provider := r.PathValue("provider"); safeProvider.MatchString(provider) {
		attrs = append(attrs, slog.String("provider", provider))
	}
	if appErr.Err != nil {
		attrs = append(attrs, slog.Any("error", appErr.Err))
	}
	s.logger.LogAttrs(r.Context(), level, "request failed", attrs...)
}

// principalAttrs returns the safe identifiers of the resolved caller. Email,
// display name, issuer and subject are deliberately omitted.
func principalAttrs(ctx context.Context) []slog.Attr {
	principal, ok := principalFrom(ctx)
	if !ok {
		return nil
	}
	return []slog.Attr{
		slog.String("user_id", string(principal.User.ID)),
		slog.String("workspace_id", string(principal.Workspace.ID)),
		slog.String("role", string(principal.Role)),
	}
}

var safeProvider = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func errorCategory(err error) string {
	var unreadable *repository.CredentialUnreadableError
	switch {
	case err == nil:
		return "unspecified"
	case errors.Is(err, repository.ErrUnavailable):
		return "storage_unavailable"
	case errors.As(err, &unreadable):
		return "credential_unreadable"
	case errors.Is(err, context.Canceled):
		return "client_cancelled"
	case errors.Is(err, errPanic):
		return "panic"
	}
	return "internal"
}

var errPanic = errors.New("handler panic recovered")

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

// securityHeaders applies baseline API response headers. Responses are
// per-user and may describe credentials, so nothing is cacheable. No CORS
// headers are emitted: cross-origin browser access stays denied until an
// explicit origin allow-list exists.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Cache-Control", "no-store")
		header.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
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
		ctx := context.WithValue(r.Context(), stateKey, &requestState{})
		next.ServeHTTP(wrapped, r.WithContext(ctx))
		status := wrapped.status
		if status == 0 {
			status = http.StatusOK
		}
		// Path only: query strings and headers (Authorization) are never logged.
		attrs := []slog.Attr{slog.String("method", r.Method), slog.String("route", r.URL.Path), slog.Int("status", status),
			slog.Duration("duration", time.Since(started)), slog.String("request_id", requestID(ctx))}
		s.logger.LogAttrs(ctx, slog.LevelInfo, "http request", append(attrs, principalAttrs(ctx)...)...)
	})
}
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			// The panic value is not logged: it could carry request data.
			if recover() != nil {
				s.writeError(w, r, &application.Error{Code: application.Internal, Op: "http.handler", Err: errPanic})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) transcriptionAvailability() providerAvailability {
	if s.processing != nil && s.processing.TranscriptionAvailable() {
		return providerAvailability{Available: true, Providers: []string{"whisper.cpp"}}
	}
	return providerAvailability{Available: false, Providers: []string{}}
}
