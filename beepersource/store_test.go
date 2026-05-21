package beepersource

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreMigratesSQLiteWALSchema(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	var mode string
	if err := store.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("journal_mode query failed: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("expected WAL journal mode, got %q", mode)
	}

	for _, table := range []string{"portal", "puppet", "message_mapping", "reaction_mapping", "pending_mutation", "media_cache", "queue"} {
		var name string
		err := store.db.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Fatalf("expected table %s to exist: %v", table, err)
		}
	}
}

func TestMessageMappingIsIdempotentAcrossRestarts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	mapping := MessageMapping{
		BeeperMessageID: "$beeper-message",
		MatrixEventID:   "$matrix-event:local",
		ChatID:          "!chat:beeper",
		Version:         "1",
	}
	if err := store.UpsertMessageMapping(ctx, mapping); err != nil {
		t.Fatalf("UpsertMessageMapping returned error: %v", err)
	}
	store.Close()

	reopened, err := OpenStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	got, ok, err := reopened.MessageByBeeperID(ctx, "$beeper-message")
	if err != nil {
		t.Fatalf("MessageByBeeperID returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected mapping after reopening store")
	}
	if got.MatrixEventID != "$matrix-event:local" || got.Version != "1" {
		t.Fatalf("unexpected mapping after restart: %#v", got)
	}
}

func TestPendingMutationQueuePreservesUnmappedOperations(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()

	id, err := store.EnqueuePendingMutation(ctx, PendingMutation{
		BeeperMessageID: "$unknown",
		MutationType:    MutationReaction,
		PayloadJSON:     []byte(`{"emoji":"👍"}`),
		CreatedAt:       time.Unix(100, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("EnqueuePendingMutation returned error: %v", err)
	}
	if id == 0 {
		t.Fatal("expected queue ID")
	}

	pending, err := store.PendingMutations(ctx, "$unknown")
	if err != nil {
		t.Fatalf("PendingMutations returned error: %v", err)
	}
	if len(pending) != 1 || pending[0].MutationType != MutationReaction {
		t.Fatalf("unexpected pending mutations: %#v", pending)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestStoreCloseIsIdempotent(t *testing.T) {
	store := openTestStore(t)
	if err := store.Close(); err != nil && err != sql.ErrConnDone {
		t.Fatalf("first close failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}
}
