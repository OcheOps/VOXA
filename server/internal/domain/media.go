package domain

import "time"

type RecordingSessionID string
type MediaAssetID string

// RecordingSessionStatus is the logical state of a capture. Pausing is a
// client concern and is not modelled on the server.
type RecordingSessionStatus string

const (
	RecordingSessionRecording RecordingSessionStatus = "recording"
	RecordingSessionCompleted RecordingSessionStatus = "completed"
	RecordingSessionFailed    RecordingSessionStatus = "failed"
)

// RecordingSession represents one logical capture activity for a meeting,
// not a browser MediaRecorder or device.
type RecordingSession struct {
	ID          RecordingSessionID
	WorkspaceID WorkspaceID
	MeetingID   MeetingID
	Status      RecordingSessionStatus
	StartedAt   time.Time
	EndedAt     *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type MediaAssetKind string

const MediaAssetAudio MediaAssetKind = "audio"

// MediaAssetStatus: uploading (chunks may still arrive), ready (assembled
// and playable), failed (abandoned or unrecoverable).
type MediaAssetStatus string

const (
	MediaAssetUploading MediaAssetStatus = "uploading"
	MediaAssetReady     MediaAssetStatus = "ready"
	MediaAssetFailed    MediaAssetStatus = "failed"
)

type MediaAsset struct {
	ID                 MediaAssetID
	WorkspaceID        WorkspaceID
	MeetingID          MeetingID
	RecordingSessionID *RecordingSessionID
	Kind               MediaAssetKind
	MimeType           string
	Status             MediaAssetStatus
	SizeBytes          int64
	DurationSeconds    *float64
	ChunkCount         *int
	// StorageKey is an internal, opaque media-store locator. It is never
	// serialized to API clients.
	StorageKey string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
