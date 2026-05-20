package connector

import (
	"context"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/id"
)

type loginSyncStore struct {
	metadata *loginMetadataStore
}

type loginMetadataStore struct {
	login *bridgev2.UserLogin
	mu    sync.Mutex
}

func newLoginMetadataStore(login *bridgev2.UserLogin) *loginMetadataStore {
	return &loginMetadataStore{login: login}
}

func newLoginSyncStore(metadata *loginMetadataStore) *loginSyncStore {
	return &loginSyncStore{metadata: metadata}
}

func (s *loginSyncStore) SaveFilterID(ctx context.Context, _ id.UserID, filterID string) error {
	return s.metadata.update(ctx, func(meta *LoginMetadata) bool {
		if meta.SyncFilterID == filterID {
			return false
		}
		meta.SyncFilterID = filterID
		return true
	})
}

func (s *loginSyncStore) LoadFilterID(context.Context, id.UserID) (string, error) {
	meta := s.metadata.snapshot()
	return meta.SyncFilterID, nil
}

func (s *loginSyncStore) SaveNextBatch(ctx context.Context, _ id.UserID, nextBatchToken string) error {
	return s.metadata.update(ctx, func(meta *LoginMetadata) bool {
		if meta.SyncNextBatch == nextBatchToken {
			return false
		}
		now := time.Now()
		meta.SyncNextBatch = nextBatchToken
		meta.LastSyncAt = &now
		return true
	})
}

func (s *loginSyncStore) LoadNextBatch(context.Context, id.UserID) (string, error) {
	meta := s.metadata.snapshot()
	return meta.SyncNextBatch, nil
}

func (s *loginMetadataStore) update(ctx context.Context, update func(*LoginMetadata) bool) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	meta := s.metadataLocked()
	if !update(meta) {
		return nil
	}
	if s.login == nil || s.login.Bridge == nil || s.login.Bridge.DB == nil || s.login.Bridge.DB.UserLogin == nil || s.login.UserLogin == nil {
		return nil
	}
	return s.login.Bridge.DB.UserLogin.Update(ctx, s.login.UserLogin)
}

func (s *loginMetadataStore) snapshot() *LoginMetadata {
	if s == nil {
		return &LoginMetadata{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	meta := s.metadataLocked()
	clone := *meta
	if meta.RemoteReactions != nil {
		clone.RemoteReactions = make(map[string]StoredRemoteReaction, len(meta.RemoteReactions))
		for key, value := range meta.RemoteReactions {
			clone.RemoteReactions[key] = value
		}
	}
	return &clone
}

func (s *loginMetadataStore) metadataLocked() *LoginMetadata {
	if s.login == nil {
		return &LoginMetadata{}
	}
	meta, ok := s.login.Metadata.(*LoginMetadata)
	if !ok || meta == nil {
		meta = &LoginMetadata{}
		s.login.Metadata = meta
	}
	return meta
}
