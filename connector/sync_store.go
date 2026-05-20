package connector

import (
	"context"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/id"
)

type loginSyncStore struct {
	login *bridgev2.UserLogin
	mu    sync.Mutex
}

func newLoginSyncStore(login *bridgev2.UserLogin) *loginSyncStore {
	return &loginSyncStore{login: login}
}

func (s *loginSyncStore) SaveFilterID(ctx context.Context, _ id.UserID, filterID string) error {
	return s.updateMetadata(ctx, func(meta *LoginMetadata) bool {
		if meta.SyncFilterID == filterID {
			return false
		}
		meta.SyncFilterID = filterID
		return true
	})
}

func (s *loginSyncStore) LoadFilterID(context.Context, id.UserID) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta := s.metadataLocked()
	return meta.SyncFilterID, nil
}

func (s *loginSyncStore) SaveNextBatch(ctx context.Context, _ id.UserID, nextBatchToken string) error {
	return s.updateMetadata(ctx, func(meta *LoginMetadata) bool {
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
	s.mu.Lock()
	defer s.mu.Unlock()
	meta := s.metadataLocked()
	return meta.SyncNextBatch, nil
}

func (s *loginSyncStore) updateMetadata(ctx context.Context, update func(*LoginMetadata) bool) error {
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

func (s *loginSyncStore) metadataLocked() *LoginMetadata {
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
