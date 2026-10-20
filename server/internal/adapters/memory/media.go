package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/OcheOps/voxa/server/internal/domain"
	"github.com/OcheOps/voxa/server/internal/repository"
)

// MediaRepository is an in-memory media metadata store for tests and
// development. Metadata is lost on restart while media files may remain.
type MediaRepository struct {
	mu       sync.Mutex
	sessions map[domain.RecordingSessionID]domain.RecordingSession
	assets   map[domain.MediaAssetID]domain.MediaAsset
}

func NewMediaRepository() *MediaRepository {
	return &MediaRepository{sessions: map[domain.RecordingSessionID]domain.RecordingSession{}, assets: map[domain.MediaAssetID]domain.MediaAsset{}}
}

func (r *MediaRepository) CreateRecording(_ context.Context, session domain.RecordingSession, asset domain.MediaAsset) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.assets[asset.ID]; exists {
		return repository.ErrConflict
	}
	if _, exists := r.sessions[session.ID]; exists {
		return repository.ErrConflict
	}
	for _, existing := range r.assets {
		if existing.StorageKey == asset.StorageKey {
			return repository.ErrConflict
		}
	}
	r.sessions[session.ID] = session
	r.assets[asset.ID] = asset
	return nil
}

func (r *MediaRepository) find(workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (domain.MediaAsset, bool) {
	asset, ok := r.assets[assetID]
	return asset, ok && asset.WorkspaceID == workspaceID
}

func (r *MediaRepository) GetAsset(_ context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (domain.MediaAsset, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	asset, ok := r.find(workspaceID, assetID)
	if !ok {
		return domain.MediaAsset{}, repository.ErrNotFound
	}
	return asset, nil
}

func (r *MediaRepository) ListMeetingAssets(_ context.Context, workspaceID domain.WorkspaceID, meetingID domain.MeetingID) ([]domain.MediaAsset, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := []domain.MediaAsset{}
	for _, asset := range r.assets {
		if asset.WorkspaceID == workspaceID && asset.MeetingID == meetingID {
			items = append(items, asset)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items, nil
}

func (r *MediaRepository) TouchUpload(_ context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	asset, ok := r.find(workspaceID, assetID)
	if !ok {
		return repository.ErrNotFound
	}
	if asset.Status == domain.MediaAssetUploading {
		asset.UpdatedAt = at
		r.assets[assetID] = asset
	}
	return nil
}

func (r *MediaRepository) CompleteUpload(_ context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, sizeBytes int64, chunkCount int, durationSeconds *float64, at time.Time) (domain.MediaAsset, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	asset, ok := r.find(workspaceID, assetID)
	if !ok {
		return domain.MediaAsset{}, repository.ErrNotFound
	}
	if asset.Status != domain.MediaAssetUploading {
		return domain.MediaAsset{}, repository.ErrConflict
	}
	asset.Status, asset.SizeBytes, asset.ChunkCount, asset.DurationSeconds, asset.UpdatedAt = domain.MediaAssetReady, sizeBytes, &chunkCount, durationSeconds, at
	r.assets[assetID] = asset
	r.endSession(asset.RecordingSessionID, domain.RecordingSessionCompleted, at)
	return asset, nil
}

func (r *MediaRepository) endSession(id *domain.RecordingSessionID, status domain.RecordingSessionStatus, at time.Time) {
	if id == nil {
		return
	}
	if session, ok := r.sessions[*id]; ok && session.Status == domain.RecordingSessionRecording {
		session.Status, session.EndedAt, session.UpdatedAt = status, &at, at
		r.sessions[*id] = session
	}
}

func (r *MediaRepository) FailUpload(_ context.Context, assetID domain.MediaAssetID, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	asset, ok := r.assets[assetID]
	if !ok || asset.Status != domain.MediaAssetUploading {
		return nil
	}
	asset.Status, asset.UpdatedAt = domain.MediaAssetFailed, at
	r.assets[assetID] = asset
	r.endSession(asset.RecordingSessionID, domain.RecordingSessionFailed, at)
	return nil
}

func (r *MediaRepository) DeleteAsset(_ context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID) (domain.MediaAsset, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	asset, ok := r.find(workspaceID, assetID)
	if !ok {
		return domain.MediaAsset{}, repository.ErrNotFound
	}
	delete(r.assets, assetID)
	return asset, nil
}

func (r *MediaRepository) ListStaleUploads(_ context.Context, before time.Time) ([]domain.MediaAsset, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var items []domain.MediaAsset
	for _, asset := range r.assets {
		if asset.Status == domain.MediaAssetUploading && asset.UpdatedAt.Before(before) {
			items = append(items, asset)
		}
	}
	return items, nil
}

func (r *MediaRepository) AssetStatusByStorageKey(_ context.Context, storageKey string) (domain.MediaAssetStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, asset := range r.assets {
		if asset.StorageKey == storageKey || asset.OriginalStorageKey != nil && *asset.OriginalStorageKey == storageKey {
			return asset.Status, nil
		}
	}
	return "", repository.ErrNotFound
}

func (r *MediaRepository) SetProcessedMedia(_ context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, storageKey, mimeType string, sizeBytes int64, durationSeconds *float64, at time.Time) (domain.MediaAsset, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	asset, ok := r.find(workspaceID, assetID)
	if !ok {
		return domain.MediaAsset{}, repository.ErrNotFound
	}
	if asset.Status != domain.MediaAssetReady {
		return domain.MediaAsset{}, repository.ErrConflict
	}
	if asset.OriginalStorageKey == nil {
		original := asset.StorageKey
		asset.OriginalStorageKey = &original
	}
	asset.StorageKey, asset.MimeType, asset.SizeBytes, asset.UpdatedAt, asset.ProcessedAt = storageKey, mimeType, sizeBytes, at, &at
	if durationSeconds != nil {
		asset.DurationSeconds = durationSeconds
	}
	r.assets[assetID] = asset
	return asset, nil
}

func (r *MediaRepository) MarkProcessed(_ context.Context, workspaceID domain.WorkspaceID, assetID domain.MediaAssetID, durationSeconds *float64, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	asset, ok := r.find(workspaceID, assetID)
	if !ok {
		return repository.ErrNotFound
	}
	asset.ProcessedAt, asset.UpdatedAt = &at, at
	if durationSeconds != nil {
		asset.DurationSeconds = durationSeconds
	}
	r.assets[assetID] = asset
	return nil
}

func (r *MediaRepository) ListUnprocessedReady(_ context.Context, limit int) ([]domain.MediaAsset, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var items []domain.MediaAsset
	for _, asset := range r.assets {
		if asset.Status == domain.MediaAssetReady && asset.ProcessedAt == nil && len(items) < limit {
			items = append(items, asset)
		}
	}
	return items, nil
}

// Session returns a stored session (test helper).
func (r *MediaRepository) Session(id domain.RecordingSessionID) (domain.RecordingSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[id]
	return session, ok
}
