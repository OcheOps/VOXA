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

// CaptureMode says which sources a recording captured when it started.
type CaptureMode string

const (
	CaptureMicrophone               CaptureMode = "microphone"
	CaptureSharedAudio              CaptureMode = "shared_audio"
	CaptureMicrophoneAndSharedAudio CaptureMode = "microphone_and_shared_audio"
	// CaptureModeUnknown: legacy recordings and clients that do not report it.
	CaptureModeUnknown CaptureMode = "unknown"
)

// CaptureClient is the kind of client that captured the media.
type CaptureClient string

const (
	CaptureClientWeb       CaptureClient = "web"
	CaptureClientDesktop   CaptureClient = "desktop"
	CaptureClientExtension CaptureClient = "extension"
	CaptureClientAndroid   CaptureClient = "android"
	CaptureClientUnknown   CaptureClient = "unknown"
)

// CaptureProvenance is operational provenance for diagnostics (how a
// recording was captured), never tracking: no user agent, URLs, tab titles
// or meeting-provider data. SharedSurface is the kind of source the user
// shared (browser tab, window, whole screen) when shared audio was captured.
type CaptureProvenance struct {
	Mode          CaptureMode
	Client        CaptureClient
	SharedSurface *string
}

func UnknownCapture() CaptureProvenance {
	return CaptureProvenance{Mode: CaptureModeUnknown, Client: CaptureClientUnknown}
}

// RecordingSession represents one logical capture activity for a meeting,
// not a browser MediaRecorder or device.
type RecordingSession struct {
	ID          RecordingSessionID
	WorkspaceID WorkspaceID
	MeetingID   MeetingID
	Status      RecordingSessionStatus
	// Capture records how the session was captured when it started.
	Capture   CaptureProvenance
	StartedAt time.Time
	EndedAt   *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
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
	// StorageKey is the internal, opaque media-store locator of the current
	// playback object. OriginalStorageKey keeps the uploaded bytes after
	// processing replaces the playback object. Neither is ever serialized.
	StorageKey         string
	OriginalStorageKey *string
	// ProcessedAt is set once remux/probe has produced the playback object.
	ProcessedAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
