package repository

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")

// ErrUnavailable means the storage backend could not be reached or refused
// service (connection failure, timeout, shutdown). Callers may retry.
var ErrUnavailable = errors.New("storage unavailable")

type MeetingPosition struct {
	CreatedAt time.Time
	ID        domain.MeetingID
}

type MeetingRepository interface {
	List(context.Context, domain.WorkspaceID, *MeetingPosition, int) ([]domain.Meeting, bool, error)
	Create(context.Context, domain.Meeting) error
	Get(context.Context, domain.WorkspaceID, domain.MeetingID) (domain.Meeting, error)
	Update(context.Context, domain.Meeting) error
	Delete(context.Context, domain.WorkspaceID, domain.MeetingID) error
}

type ConfigurationRepository interface {
	Get(context.Context, domain.WorkspaceID) (domain.Configuration, error)
	Update(context.Context, domain.WorkspaceID, func(domain.Configuration) (domain.Configuration, error)) (domain.Configuration, error)
}

// CredentialRepository manages credential metadata and write-only secret
// storage. Nothing in it returns secret material; HTTP-facing services
// depend only on this interface.
type CredentialRepository interface {
	List(context.Context, domain.WorkspaceID) ([]domain.CredentialStatus, error)
	IsConfigured(context.Context, domain.WorkspaceID, string) (bool, error)
	Set(context.Context, domain.WorkspaceID, string, domain.CredentialSecret) error
	Delete(context.Context, domain.WorkspaceID, string) error
}

// CredentialSecretReader returns decrypted credential material for internal
// provider execution. It is deliberately separate from CredentialRepository
// so metadata/HTTP code paths cannot reach it. Callers should Wipe the secret
// once the provider call is done.
type CredentialSecretReader interface {
	RevealCredential(context.Context, domain.WorkspaceID, string) (domain.CredentialSecret, error)
}

// CredentialUnreadableError reports a stored credential that exists but
// cannot be decrypted (unknown key version, tampering, corruption). It
// carries only non-secret metadata for logs.
type CredentialUnreadableError struct {
	WorkspaceID domain.WorkspaceID
	Provider    string
	KeyVersion  string
	Reason      error
}

func (e *CredentialUnreadableError) Error() string {
	return "stored credential cannot be decrypted: " + e.Reason.Error()
}
func (e *CredentialUnreadableError) Unwrap() error { return e.Reason }
func (e *CredentialUnreadableError) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("workspace_id", string(e.WorkspaceID)),
		slog.String("provider", e.Provider),
		slog.String("key_version", e.KeyVersion),
		slog.String("reason", e.Reason.Error()),
	)
}
