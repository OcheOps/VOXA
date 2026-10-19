package repository

import (
	"context"
	"errors"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")

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

type CredentialRepository interface {
	List(context.Context, domain.WorkspaceID) ([]domain.CredentialStatus, error)
	IsConfigured(context.Context, domain.WorkspaceID, string) (bool, error)
	Set(context.Context, domain.WorkspaceID, string, string) error
	Delete(context.Context, domain.WorkspaceID, string) error
}
