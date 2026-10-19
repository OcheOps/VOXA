package memory

import (
	"bytes"
	"context"
	"sort"
	"sync"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

type MeetingRepository struct {
	mu       sync.RWMutex
	meetings map[domain.WorkspaceID]map[domain.MeetingID]domain.Meeting
}

func NewMeetingRepository() *MeetingRepository {
	return &MeetingRepository{meetings: make(map[domain.WorkspaceID]map[domain.MeetingID]domain.Meeting)}
}

func (r *MeetingRepository) List(_ context.Context, workspaceID domain.WorkspaceID, after *repository.MeetingPosition, limit int) ([]domain.Meeting, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]domain.Meeting, 0, len(r.meetings[workspaceID]))
	for _, meeting := range r.meetings[workspaceID] {
		if after != nil && (meeting.CreatedAt.Before(after.CreatedAt) || meeting.CreatedAt.Equal(after.CreatedAt) && meeting.ID <= after.ID) {
			continue
		}
		items = append(items, meeting)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func (r *MeetingRepository) Create(_ context.Context, meeting domain.Meeting) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.meetings[meeting.WorkspaceID] == nil {
		r.meetings[meeting.WorkspaceID] = make(map[domain.MeetingID]domain.Meeting)
	}
	if _, exists := r.meetings[meeting.WorkspaceID][meeting.ID]; exists {
		return repository.ErrConflict
	}
	r.meetings[meeting.WorkspaceID][meeting.ID] = meeting
	return nil
}

func (r *MeetingRepository) Get(_ context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) (domain.Meeting, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	meeting, ok := r.meetings[workspaceID][meetingID]
	if !ok {
		return domain.Meeting{}, repository.ErrNotFound
	}
	return meeting, nil
}

func (r *MeetingRepository) Update(_ context.Context, meeting domain.Meeting) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.meetings[meeting.WorkspaceID][meeting.ID]; !ok {
		return repository.ErrNotFound
	}
	r.meetings[meeting.WorkspaceID][meeting.ID] = meeting
	return nil
}

func (r *MeetingRepository) Delete(_ context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.meetings[workspaceID][meetingID]; !ok {
		return repository.ErrNotFound
	}
	delete(r.meetings[workspaceID], meetingID)
	return nil
}

type ConfigurationRepository struct {
	mu            sync.RWMutex
	configs       map[domain.WorkspaceID]domain.Configuration
	defaultConfig domain.Configuration
}

func NewConfigurationRepository(defaultConfig domain.Configuration) *ConfigurationRepository {
	return &ConfigurationRepository{configs: make(map[domain.WorkspaceID]domain.Configuration), defaultConfig: defaultConfig}
}

func (r *ConfigurationRepository) Get(_ context.Context, workspaceID domain.WorkspaceID) (domain.Configuration, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if config, ok := r.configs[workspaceID]; ok {
		return config, nil
	}
	return r.defaultConfig, nil
}

func (r *ConfigurationRepository) Update(_ context.Context, workspaceID domain.WorkspaceID, update func(domain.Configuration) (domain.Configuration, error)) (domain.Configuration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	config, ok := r.configs[workspaceID]
	if !ok {
		config = r.defaultConfig
	}
	config, err := update(config)
	if err != nil {
		return domain.Configuration{}, err
	}
	r.configs[workspaceID] = config
	return config, nil
}

// CredentialRepository keeps plaintext secrets in process memory. It is a
// development/test adapter only and is never used when PostgreSQL is enabled.
type CredentialRepository struct {
	mu      sync.RWMutex
	secrets map[domain.WorkspaceID]map[string][]byte
}

func NewCredentialRepository() *CredentialRepository {
	return &CredentialRepository{secrets: make(map[domain.WorkspaceID]map[string][]byte)}
}

func (r *CredentialRepository) List(_ context.Context, workspaceID domain.WorkspaceID) ([]domain.CredentialStatus, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	statuses := make([]domain.CredentialStatus, 0, len(r.secrets[workspaceID]))
	for provider := range r.secrets[workspaceID] {
		statuses = append(statuses, domain.CredentialStatus{Provider: provider, Configured: true})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Provider < statuses[j].Provider })
	return statuses, nil
}

func (r *CredentialRepository) IsConfigured(_ context.Context, workspaceID domain.WorkspaceID, provider string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.secrets[workspaceID][provider]
	return ok, nil
}

func (r *CredentialRepository) Set(_ context.Context, workspaceID domain.WorkspaceID, provider string, secret domain.CredentialSecret) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.secrets[workspaceID] == nil {
		r.secrets[workspaceID] = make(map[string][]byte)
	}
	// Copy so the caller may Wipe its secret after the call.
	r.secrets[workspaceID][provider] = bytes.Clone(secret.Expose())
	return nil
}

func (r *CredentialRepository) Delete(_ context.Context, workspaceID domain.WorkspaceID, provider string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.secrets[workspaceID], provider)
	return nil
}

// RevealCredential implements repository.CredentialSecretReader.
func (r *CredentialRepository) RevealCredential(_ context.Context, workspaceID domain.WorkspaceID, provider string) (domain.CredentialSecret, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	secret, ok := r.secrets[workspaceID][provider]
	if !ok {
		return domain.CredentialSecret{}, repository.ErrNotFound
	}
	return domain.NewCredentialSecret(bytes.Clone(secret)), nil
}
