// Package mediastore stores opaque media bytes: chunked uploads staged per
// upload, then assembled into immutable objects. Callers address everything
// by opaque keys; physical locations never leave this package, so a
// filesystem implementation can later be replaced by S3-compatible storage
// (chunks as staged objects, assembly via multipart compose, reads via
// ranged GETs) behind the same interface.
package mediastore

import (
	"context"
	"errors"
	"io"
	"regexp"
	"time"
)

var (
	ErrNotFound      = errors.New("media not found")
	ErrChunkConflict = errors.New("chunk already stored with different content")
	ErrTooLarge      = errors.New("media exceeds size limit")
	ErrInvalidKey    = errors.New("invalid media key")
	ErrMissingChunk  = errors.New("upload is missing a chunk")
)

// keyPattern restricts keys to server-generated identifiers, so a key can
// never express a path.
var keyPattern = regexp.MustCompile(`^[a-z]{2,8}_[0-9a-f]{32}$`)

func ValidKey(key string) bool { return keyPattern.MatchString(key) }

type ChunkInfo struct {
	Index  int
	Size   int64
	SHA256 [32]byte
}

type ObjectInfo struct {
	Size    int64
	ModTime time.Time
}

// Object is an open, seekable stored object.
type Object interface {
	io.ReadSeekCloser
	Info() ObjectInfo
}

type UploadInfo struct {
	Key     string
	ModTime time.Time
}

type Store interface {
	// PutChunk stores chunk index of an upload, reading at most maxBytes.
	// Re-sending identical bytes is a no-op; different bytes for an existing
	// index return ErrChunkConflict.
	PutChunk(ctx context.Context, uploadKey string, index int, data io.Reader, maxBytes int64) (ChunkInfo, error)
	// ListChunks returns the stored chunks of an upload ordered by index.
	ListChunks(ctx context.Context, uploadKey string) ([]ChunkInfo, error)
	// Assemble concatenates chunks 0..count-1 into object objectKey
	// atomically. Existing objects are replaced (finalization is retryable).
	Assemble(ctx context.Context, uploadKey string, count int, objectKey string) (ObjectInfo, error)
	Open(ctx context.Context, objectKey string) (Object, error)
	DeleteObject(ctx context.Context, objectKey string) error
	DeleteUpload(ctx context.Context, uploadKey string) error
	ListUploads(ctx context.Context) ([]UploadInfo, error)
	ListObjects(ctx context.Context) ([]UploadInfo, error)
}
