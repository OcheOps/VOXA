package application

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/mediastore"
	"github.com/OcheOps/voxa/server/internal/repository"
)

// MediaLimits bound uploads. Defaults are documented in server/README.md.
type MediaLimits struct {
	MaxChunkBytes     int64
	MaxRecordingBytes int64
	StaleUploadAge    time.Duration
}

func DefaultMediaLimits() MediaLimits {
	return MediaLimits{MaxChunkBytes: 8 << 20, MaxRecordingBytes: 1 << 30, StaleUploadAge: 48 * time.Hour}
}

// MaxRecordingDurationSeconds bounds client-reported durations (7 days).
const MaxRecordingDurationSeconds = 7 * 24 * 3600

// orphanGrace protects media written moments ago from orphan cleanup.
const orphanGrace = 10 * time.Minute

// allowedMimeTypes lists accepted recording formats (normalized). Each must
// be produced by a single MediaRecorder as a continuous stream whose chunks
// concatenate into one playable file.
var allowedMimeTypes = map[string]string{
	"audio/webm":                 "webm",
	"audio/webm;codecs=opus":     "webm",
	"audio/ogg":                  "ogg",
	"audio/ogg;codecs=opus":      "ogg",
	"audio/mp4":                  "mp4",
	"audio/mp4;codecs=mp4a.40.2": "mp4",
	"audio/mp4;codecs=opus":      "mp4",
}

// NormalizeMimeType lower-cases and strips whitespace and quotes; it returns
// "" for formats outside the allowlist.
func NormalizeMimeType(value string) string {
	normalized := strings.ToLower(strings.NewReplacer(" ", "", "\t", "", `"`, "").Replace(value))
	if _, ok := allowedMimeTypes[normalized]; !ok {
		return ""
	}
	return normalized
}

// matchesContainer checks the leading bytes of chunk 0 against the declared
// container so arbitrary files cannot be stored under an audio type.
func matchesContainer(container string, head []byte) bool {
	switch container {
	case "webm":
		return bytes.HasPrefix(head, []byte{0x1a, 0x45, 0xdf, 0xa3})
	case "ogg":
		return bytes.HasPrefix(head, []byte("OggS"))
	case "mp4":
		return len(head) >= 8 && string(head[4:8]) == "ftyp"
	}
	return false
}

type UploadStatus struct {
	Asset          domain.MediaAsset
	ReceivedChunks []int
	ReceivedBytes  int64
	Limits         MediaLimits
}

type RecordingStart struct {
	Session domain.RecordingSession
	Asset   domain.MediaAsset
	Upload  UploadStatus
}

type CleanupReport struct {
	FailedUploads  int
	RemovedStaging int
	RemovedOrphans int
}

// MediaService owns recording uploads and media access. Every operation is
// scoped by workspace; the storage layer only ever sees opaque keys.
type MediaService struct {
	meetings repository.MeetingRepository
	media    repository.MediaRepository
	store    mediastore.Store
	limits   MediaLimits
	now      func() time.Time
	newID    func(prefix string) (string, error)
}

func NewMediaService(meetings repository.MeetingRepository, media repository.MediaRepository, store mediastore.Store, limits MediaLimits) *MediaService {
	return &MediaService{meetings: meetings, media: media, store: store, limits: limits, now: time.Now, newID: randomID}
}

func (s *MediaService) Limits() MediaLimits { return s.limits }

func (s *MediaService) timestamp() time.Time { return s.now().UTC().Truncate(time.Microsecond) }

func mediaError(code ErrorCode, op, reason string, err error) error {
	return &Error{Code: code, Op: op, Err: err, Details: map[string]any{"reason": reason}}
}

func (s *MediaService) CreateRecording(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID, mimeType string) (RecordingStart, error) {
	normalized := NormalizeMimeType(mimeType)
	if normalized == "" {
		return RecordingStart{}, validation("mimeType", "is not a supported recording format")
	}
	if _, err := s.meetings.Get(ctx, workspaceID, meetingID); errors.Is(err, repository.ErrNotFound) {
		return RecordingStart{}, &Error{Code: NotFound, Op: "media.create_recording", Err: err}
	} else if err != nil {
		return RecordingStart{}, storageError("media.create_recording", err)
	}
	sessionID, err := s.newID("rec_")
	if err != nil {
		return RecordingStart{}, &Error{Code: Internal, Op: "media.create_recording", Err: err}
	}
	assetID, err := s.newID("med_")
	if err != nil {
		return RecordingStart{}, &Error{Code: Internal, Op: "media.create_recording", Err: err}
	}
	storageKey, err := s.newID("obj_")
	if err != nil {
		return RecordingStart{}, &Error{Code: Internal, Op: "media.create_recording", Err: err}
	}
	now := s.timestamp()
	session := domain.RecordingSession{ID: domain.RecordingSessionID(sessionID), WorkspaceID: workspaceID, MeetingID: meetingID,
		Status: domain.RecordingSessionRecording, StartedAt: now, CreatedAt: now, UpdatedAt: now}
	sessionRef := session.ID
	asset := domain.MediaAsset{ID: domain.MediaAssetID(assetID), WorkspaceID: workspaceID, MeetingID: meetingID, RecordingSessionID: &sessionRef,
		Kind: domain.MediaAssetAudio, MimeType: normalized, Status: domain.MediaAssetUploading, StorageKey: storageKey, CreatedAt: now, UpdatedAt: now}
	if err := s.media.CreateRecording(ctx, session, asset); err != nil {
		return RecordingStart{}, storageError("media.create_recording", err)
	}
	return RecordingStart{Session: session, Asset: asset, Upload: UploadStatus{Asset: asset, ReceivedChunks: []int{}, Limits: s.limits}}, nil
}

func (s *MediaService) getAsset(ctx context.Context, op string, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (domain.MediaAsset, error) {
	asset, err := s.media.GetAsset(ctx, workspaceID, assetID)
	if errors.Is(err, repository.ErrNotFound) {
		return domain.MediaAsset{}, &Error{Code: NotFound, Op: op, Err: err}
	}
	if err != nil {
		return domain.MediaAsset{}, storageError(op, err)
	}
	return asset, nil
}

func (s *MediaService) status(ctx context.Context, op string, asset domain.MediaAsset) (UploadStatus, error) {
	status := UploadStatus{Asset: asset, ReceivedChunks: []int{}, Limits: s.limits}
	if asset.Status != domain.MediaAssetUploading {
		if asset.ChunkCount != nil {
			for i := 0; i < *asset.ChunkCount; i++ {
				status.ReceivedChunks = append(status.ReceivedChunks, i)
			}
		}
		status.ReceivedBytes = asset.SizeBytes
		return status, nil
	}
	chunks, err := s.store.ListChunks(ctx, asset.StorageKey)
	if err != nil {
		return UploadStatus{}, &Error{Code: Internal, Op: op, Err: err}
	}
	for _, chunk := range chunks {
		status.ReceivedChunks = append(status.ReceivedChunks, chunk.Index)
		status.ReceivedBytes += chunk.Size
	}
	return status, nil
}

func (s *MediaService) UploadStatus(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (UploadStatus, error) {
	asset, err := s.getAsset(ctx, "media.upload_status", workspaceID, assetID)
	if err != nil {
		return UploadStatus{}, err
	}
	return s.status(ctx, "media.upload_status", asset)
}

// UploadChunk stores chunk index. Retrying an identical chunk is safe;
// different bytes for a stored index are a conflict.
func (s *MediaService) UploadChunk(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, index int, body io.Reader) (UploadStatus, error) {
	const op = "media.upload_chunk"
	if index < 0 || index > mediastore.MaxChunkIndex {
		return UploadStatus{}, validation("index", "is out of range")
	}
	asset, err := s.getAsset(ctx, op, workspaceID, assetID)
	if err != nil {
		return UploadStatus{}, err
	}
	if asset.Status != domain.MediaAssetUploading {
		return UploadStatus{}, mediaError(Conflict, op, "upload_closed", nil)
	}
	existing, err := s.store.ListChunks(ctx, asset.StorageKey)
	if err != nil {
		return UploadStatus{}, &Error{Code: Internal, Op: op, Err: err}
	}
	var stored int64
	for _, chunk := range existing {
		if chunk.Index != index {
			stored += chunk.Size
		}
	}
	remaining := s.limits.MaxRecordingBytes - stored
	maxBytes := min(s.limits.MaxChunkBytes, remaining)
	reader := bufio.NewReaderSize(body, 64)
	if index == 0 {
		head, _ := reader.Peek(12)
		if !matchesContainer(allowedMimeTypes[asset.MimeType], head) {
			return UploadStatus{}, validation("chunk", "does not match the recording format")
		}
	}
	if _, err := s.store.PutChunk(ctx, asset.StorageKey, index, reader, maxBytes); err != nil {
		switch {
		case errors.Is(err, mediastore.ErrTooLarge) && remaining < s.limits.MaxChunkBytes:
			return UploadStatus{}, validation("recording", "exceeds the maximum recording size")
		case errors.Is(err, mediastore.ErrTooLarge):
			return UploadStatus{}, validation("chunk", "exceeds the maximum chunk size")
		case errors.Is(err, mediastore.ErrChunkConflict):
			return UploadStatus{}, mediaError(Conflict, op, "chunk_mismatch", err)
		case errors.Is(err, context.Canceled):
			return UploadStatus{}, &Error{Code: Internal, Op: op, Err: err}
		}
		return UploadStatus{}, &Error{Code: Internal, Op: op, Err: err}
	}
	if err := s.media.TouchUpload(ctx, workspaceID, assetID, s.timestamp()); err != nil {
		return UploadStatus{}, storageError(op, err)
	}
	return s.status(ctx, op, asset)
}

// Finalize assembles chunks 0..chunkCount-1 into the playable object. It is
// idempotent: finalizing a ready asset with the same count returns it.
func (s *MediaService) Finalize(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, chunkCount int, durationSeconds *float64) (domain.MediaAsset, error) {
	const op = "media.finalize"
	if chunkCount < 1 || chunkCount > mediastore.MaxChunkIndex+1 {
		return domain.MediaAsset{}, validation("chunkCount", "must be between 1 and 100000")
	}
	if durationSeconds != nil && (*durationSeconds < 0 || *durationSeconds > MaxRecordingDurationSeconds) {
		return domain.MediaAsset{}, validation("durationSeconds", "is out of range")
	}
	asset, err := s.getAsset(ctx, op, workspaceID, assetID)
	if err != nil {
		return domain.MediaAsset{}, err
	}
	switch asset.Status {
	case domain.MediaAssetReady:
		if asset.ChunkCount != nil && *asset.ChunkCount == chunkCount {
			return asset, nil
		}
		return domain.MediaAsset{}, mediaError(Conflict, op, "already_finalized", nil)
	case domain.MediaAssetFailed:
		return domain.MediaAsset{}, mediaError(Conflict, op, "upload_failed", nil)
	}
	chunks, err := s.store.ListChunks(ctx, asset.StorageKey)
	if err != nil {
		return domain.MediaAsset{}, &Error{Code: Internal, Op: op, Err: err}
	}
	received := make(map[int]bool, len(chunks))
	for _, chunk := range chunks {
		received[chunk.Index] = true
	}
	var missing []int
	for i := 0; i < chunkCount && len(missing) < 20; i++ {
		if !received[i] {
			missing = append(missing, i)
		}
	}
	if len(missing) > 0 {
		return domain.MediaAsset{}, &Error{Code: Conflict, Op: op, Details: map[string]any{"reason": "missing_chunks", "missing": missing}}
	}
	if len(chunks) != chunkCount {
		return domain.MediaAsset{}, mediaError(Conflict, op, "unexpected_chunks", nil)
	}
	info, err := s.store.Assemble(ctx, asset.StorageKey, chunkCount, asset.StorageKey)
	if err != nil {
		return domain.MediaAsset{}, &Error{Code: Internal, Op: op, Err: err}
	}
	ready, err := s.media.CompleteUpload(ctx, workspaceID, assetID, info.Size, chunkCount, durationSeconds, s.timestamp())
	if errors.Is(err, repository.ErrConflict) {
		// A concurrent finalize won; report its result.
		current, getErr := s.getAsset(ctx, op, workspaceID, assetID)
		if getErr == nil && current.Status == domain.MediaAssetReady && current.ChunkCount != nil && *current.ChunkCount == chunkCount {
			return current, nil
		}
		return domain.MediaAsset{}, mediaError(Conflict, op, "already_finalized", err)
	}
	if err != nil {
		return domain.MediaAsset{}, storageError(op, err)
	}
	// Staged chunks are no longer needed; leftovers are removed by cleanup.
	_ = s.store.DeleteUpload(ctx, asset.StorageKey)
	return ready, nil
}

func (s *MediaService) ListMeetingMedia(ctx context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) ([]domain.MediaAsset, error) {
	const op = "media.list"
	if _, err := s.meetings.Get(ctx, workspaceID, meetingID); errors.Is(err, repository.ErrNotFound) {
		return nil, &Error{Code: NotFound, Op: op, Err: err}
	} else if err != nil {
		return nil, storageError(op, err)
	}
	assets, err := s.media.ListMeetingAssets(ctx, workspaceID, meetingID)
	if err != nil {
		return nil, storageError(op, err)
	}
	return assets, nil
}

func (s *MediaService) GetAsset(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (domain.MediaAsset, error) {
	return s.getAsset(ctx, "media.get", workspaceID, assetID)
}

// OpenContent opens a ready asset for streaming. The caller must close it.
func (s *MediaService) OpenContent(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (domain.MediaAsset, mediastore.Object, error) {
	const op = "media.content"
	asset, err := s.getAsset(ctx, op, workspaceID, assetID)
	if err != nil {
		return domain.MediaAsset{}, nil, err
	}
	if asset.Status != domain.MediaAssetReady {
		return domain.MediaAsset{}, nil, mediaError(Conflict, op, "not_ready", nil)
	}
	object, err := s.store.Open(ctx, asset.StorageKey)
	if err != nil {
		return domain.MediaAsset{}, nil, &Error{Code: Internal, Op: op, Err: err}
	}
	return asset, object, nil
}

// DeleteAsset removes metadata first, then bytes; leftover bytes from a
// failed delete are reclaimed by cleanup as orphans.
func (s *MediaService) DeleteAsset(ctx context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) error {
	const op = "media.delete"
	asset, err := s.media.DeleteAsset(ctx, workspaceID, assetID)
	if errors.Is(err, repository.ErrNotFound) {
		return &Error{Code: NotFound, Op: op, Err: err}
	}
	if err != nil {
		return storageError(op, err)
	}
	_ = s.store.DeleteObject(ctx, asset.StorageKey)
	_ = s.store.DeleteUpload(ctx, asset.StorageKey)
	return nil
}

// Cleanup fails uploads idle longer than StaleUploadAge and removes staged
// chunks and objects that no live upload or asset references. Objects of
// ready assets are never removed.
func (s *MediaService) Cleanup(ctx context.Context) (CleanupReport, error) {
	var report CleanupReport
	now := s.timestamp()
	stale, err := s.media.ListStaleUploads(ctx, now.Add(-s.limits.StaleUploadAge))
	if err != nil {
		return report, err
	}
	for _, asset := range stale {
		if err := s.media.FailUpload(ctx, asset.ID, now); err != nil {
			return report, err
		}
		_ = s.store.DeleteUpload(ctx, asset.StorageKey)
		report.FailedUploads++
	}

	uploads, err := s.store.ListUploads(ctx)
	if err != nil {
		return report, err
	}
	for _, upload := range uploads {
		status, err := s.media.AssetStatusByStorageKey(ctx, upload.Key)
		orphan := errors.Is(err, repository.ErrNotFound) && now.Sub(upload.ModTime) > orphanGrace
		if orphan || (err == nil && status != domain.MediaAssetUploading) {
			if s.store.DeleteUpload(ctx, upload.Key) == nil {
				report.RemovedStaging++
			}
		} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
			return report, err
		}
	}

	objects, err := s.store.ListObjects(ctx)
	if err != nil {
		return report, err
	}
	for _, object := range objects {
		status, err := s.media.AssetStatusByStorageKey(ctx, object.Key)
		switch {
		case errors.Is(err, repository.ErrNotFound) && now.Sub(object.ModTime) > orphanGrace,
			err == nil && status == domain.MediaAssetFailed:
			if s.store.DeleteObject(ctx, object.Key) == nil {
				report.RemovedOrphans++
			}
		case err != nil && !errors.Is(err, repository.ErrNotFound):
			return report, err
		}
	}
	return report, nil
}
