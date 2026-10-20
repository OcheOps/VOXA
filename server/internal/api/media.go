package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OcheOps/voxa/server/internal/application"
	"github.com/OcheOps/voxa/server/internal/domain"
)

// Option configures optional handler features.
type Option func(*Server)

// WithMedia enables the media API. Without it every media route answers
// 503 unavailable and capabilities report media storage as unavailable.
func WithMedia(media *application.MediaService, signer *PlaybackSigner) Option {
	return func(s *Server) {
		s.media = media
		s.signer = signer
	}
}

// PlaybackSigner issues short-lived HMAC-signed playback URLs. <audio>
// elements cannot send Authorization headers, so playback uses a URL bound
// to one workspace, one asset and an expiry. The key is random per process:
// URLs stop working after a restart and clients simply request a new one.
type PlaybackSigner struct {
	key []byte
	ttl time.Duration
	now func() time.Time
}

const DefaultPlaybackTTL = 2 * time.Hour

func NewPlaybackSigner(ttl time.Duration) (*PlaybackSigner, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return &PlaybackSigner{key: key, ttl: ttl, now: time.Now}, nil
}

func (p *PlaybackSigner) mac(workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, expires int64) []byte {
	mac := hmac.New(sha256.New, p.key)
	mac.Write([]byte("voxa.media.playback.v1\n" + string(workspaceID) + "\n" + string(assetID) + "\n" + strconv.FormatInt(expires, 10)))
	return mac.Sum(nil)
}

// Sign returns the relative stream path (with query) and its expiry.
func (p *PlaybackSigner) Sign(workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (string, time.Time) {
	expires := p.now().Add(p.ttl).Unix()
	query := url.Values{
		"workspace": {string(workspaceID)},
		"expires":   {strconv.FormatInt(expires, 10)},
		"signature": {base64.RawURLEncoding.EncodeToString(p.mac(workspaceID, assetID, expires))},
	}
	return "/api/v1/media/" + url.PathEscape(string(assetID)) + "/stream?" + query.Encode(), time.Unix(expires, 0).UTC()
}

func (p *PlaybackSigner) Verify(workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, expiresValue, signature string) bool {
	expires, err := strconv.ParseInt(expiresValue, 10, 64)
	if err != nil || p.now().Unix() > expires {
		return false
	}
	given, err := base64.RawURLEncoding.DecodeString(signature)
	return err == nil && hmac.Equal(given, p.mac(workspaceID, assetID, expires))
}

func (s *Server) registerMedia(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/meetings/{meetingId}/recordings", s.authorize(s.withMedia(s.createRecording)))
	mux.HandleFunc("GET /api/v1/meetings/{meetingId}/media", s.authorize(s.withMedia(s.listMeetingMedia)))
	mux.HandleFunc("GET /api/v1/media/{mediaAssetId}", s.authorize(s.withMedia(s.getMediaAsset)))
	mux.HandleFunc("DELETE /api/v1/media/{mediaAssetId}", s.authorize(s.withMedia(s.deleteMediaAsset)))
	mux.HandleFunc("GET /api/v1/media/{mediaAssetId}/upload", s.authorize(s.withMedia(s.getUpload)))
	mux.HandleFunc("PUT /api/v1/media/{mediaAssetId}/chunks/{index}", s.authorize(s.withMedia(s.putChunk)))
	mux.HandleFunc("POST /api/v1/media/{mediaAssetId}/finalize", s.authorize(s.withMedia(s.finalizeMedia)))
	mux.HandleFunc("GET /api/v1/media/{mediaAssetId}/playback", s.authorize(s.withMedia(s.playbackURL)))
	mux.HandleFunc("GET /api/v1/media/{mediaAssetId}/content", s.authorize(s.withMedia(s.mediaContent)))
	// Public route: authorized by the signed URL, not a bearer token.
	mux.HandleFunc("GET /api/v1/media/{mediaAssetId}/stream", s.withMedia(s.mediaStream))
}

func (s *Server) withMedia(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.media == nil {
			s.writeError(w, r, &application.Error{Code: application.Unavailable, Op: "media", Category: "media_disabled"})
			return
		}
		next(w, r)
	}
}

func mediaAssetID(r *http.Request) domain.MediaAssetID {
	return domain.MediaAssetID(r.PathValue("mediaAssetId"))
}

func (s *Server) uploadDTO(status application.UploadStatus) mediaUploadDTO {
	return mediaUploadDTO{
		MediaAssetID: string(status.Asset.ID), Status: string(status.Asset.Status), ReceivedChunks: status.ReceivedChunks,
		ReceivedBytes: status.ReceivedBytes, MaxChunkBytes: status.Limits.MaxChunkBytes, MaxTotalBytes: status.Limits.MaxRecordingBytes,
	}
}

func (s *Server) createRecording(w http.ResponseWriter, r *http.Request) {
	var input createRecordingRequest
	if err := decodeJSON(w, r, &input); err != nil {
		s.writeError(w, r, err)
		return
	}
	start, err := s.media.CreateRecordingWithCapture(r.Context(), workspaceID(r), domain.MeetingID(r.PathValue("meetingId")), input.MimeType, input.Capture.toDomain())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	asset := withCapture(mediaAssetFromDomain(start.Asset), start.Asset, map[domain.RecordingSessionID]domain.CaptureProvenance{start.Session.ID: start.Session.Capture})
	writeJSON(w, http.StatusCreated, createRecordingResponse{
		RecordingSession: recordingSessionFromDomain(start.Session), MediaAsset: asset, Upload: s.uploadDTO(start.Upload),
	})
}

func (s *Server) listMeetingMedia(w http.ResponseWriter, r *http.Request) {
	assets, err := s.media.ListMeetingMedia(r.Context(), workspaceID(r), domain.MeetingID(r.PathValue("meetingId")))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	captures, err := s.media.MeetingCaptures(r.Context(), workspaceID(r), domain.MeetingID(r.PathValue("meetingId")))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items := make([]mediaAssetDTO, 0, len(assets))
	for _, asset := range assets {
		items = append(items, withCapture(mediaAssetFromDomain(asset), asset, captures))
	}
	writeJSON(w, http.StatusOK, meetingMediaResponse{Items: items})
}

func (s *Server) getMediaAsset(w http.ResponseWriter, r *http.Request) {
	asset, err := s.media.GetAsset(r.Context(), workspaceID(r), mediaAssetID(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mediaAssetResponse{MediaAsset: s.assetWithCapture(r, asset)})
}

func (s *Server) deleteMediaAsset(w http.ResponseWriter, r *http.Request) {
	if err := s.media.DeleteAsset(r.Context(), workspaceID(r), mediaAssetID(r)); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getUpload(w http.ResponseWriter, r *http.Request) {
	status, err := s.media.UploadStatus(r.Context(), workspaceID(r), mediaAssetID(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, uploadResponse{Upload: s.uploadDTO(status)})
}

func (s *Server) putChunk(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || r.PathValue("index") != strconv.Itoa(index) {
		s.writeError(w, r, &application.Error{Code: application.Validation, Details: map[string]any{"field": "index", "reason": "must be a non-negative integer"}})
		return
	}
	if mediaType := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]); mediaType != "application/octet-stream" {
		s.writeError(w, r, &application.Error{Code: application.Validation, Details: map[string]any{"field": "contentType", "reason": "must be application/octet-stream"}})
		return
	}
	limits := s.media.Limits()
	if r.ContentLength > limits.MaxChunkBytes {
		s.writeError(w, r, &application.Error{Code: application.Validation, Details: map[string]any{"field": "chunk", "reason": "exceeds the maximum chunk size"}})
		return
	}
	// Chunks may be large on slow links: allow more than the default read timeout.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(2 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, limits.MaxChunkBytes+1)
	status, err := s.media.UploadChunk(r.Context(), workspaceID(r), mediaAssetID(r), index, r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			err = &application.Error{Code: application.Validation, Details: map[string]any{"field": "chunk", "reason": "exceeds the maximum chunk size"}}
		}
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, uploadResponse{Upload: s.uploadDTO(status)})
}

func (s *Server) finalizeMedia(w http.ResponseWriter, r *http.Request) {
	var input finalizeMediaRequest
	if err := decodeJSON(w, r, &input); err != nil {
		s.writeError(w, r, err)
		return
	}
	asset, err := s.media.Finalize(r.Context(), workspaceID(r), mediaAssetID(r), input.ChunkCount, input.DurationSeconds)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mediaAssetResponse{MediaAsset: s.assetWithCapture(r, asset)})
}

// assetWithCapture adds provenance for single-asset responses; a lookup
// failure only omits it.
func (s *Server) assetWithCapture(r *http.Request, asset domain.MediaAsset) mediaAssetDTO {
	captures, err := s.media.MeetingCaptures(r.Context(), asset.WorkspaceID, asset.MeetingID)
	if err != nil {
		return mediaAssetFromDomain(asset)
	}
	return withCapture(mediaAssetFromDomain(asset), asset, captures)
}

func (s *Server) playbackURL(w http.ResponseWriter, r *http.Request) {
	asset, err := s.media.GetAsset(r.Context(), workspaceID(r), mediaAssetID(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if asset.Status != domain.MediaAssetReady {
		s.writeError(w, r, &application.Error{Code: application.Conflict, Op: "media.playback", Details: map[string]any{"reason": "not_ready"}})
		return
	}
	path, expires := s.signer.Sign(asset.WorkspaceID, asset.ID)
	writeJSON(w, http.StatusOK, mediaPlaybackResponse{URL: path, ExpiresAt: expires.Format(time.RFC3339)})
}

func (s *Server) mediaContent(w http.ResponseWriter, r *http.Request) {
	s.serveMedia(w, r, workspaceID(r), mediaAssetID(r))
}

func (s *Server) mediaStream(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	workspace := domain.WorkspaceID(query.Get("workspace"))
	if workspace == "" || !s.signer.Verify(workspace, mediaAssetID(r), query.Get("expires"), query.Get("signature")) {
		// Expired or tampered URLs look like missing media.
		s.writeError(w, r, &application.Error{Code: application.Forbidden, Op: "media.stream", Category: "playback_url_invalid"})
		return
	}
	s.serveMedia(w, r, workspace, mediaAssetID(r))
}

// serveMedia streams a ready asset with HTTP range support (206, Content-
// Range, If-Range) from net/http.ServeContent.
func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request, workspace domain.WorkspaceID, assetID domain.MediaAssetID) {
	asset, object, err := s.media.OpenContent(r.Context(), workspace, assetID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer object.Close()
	// Long recordings stream for longer than the default write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Hour))
	header := w.Header()
	header.Set("Content-Type", asset.MimeType)
	header.Set("Content-Disposition", "inline")
	header.Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "", object.Info().ModTime, object)
}
