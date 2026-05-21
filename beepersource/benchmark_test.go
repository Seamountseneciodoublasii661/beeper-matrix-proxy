package beepersource

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkReconcileFiveHundredTextMessages(b *testing.B) {
	ctx := context.Background()
	messages := make([]Message, 500)
	for i := range messages {
		messages[i] = Message{
			ID:        fmt.Sprintf("$bench-%d", i),
			ChatID:    "!bench:beeper",
			SenderID:  "@bench:signal",
			Type:      MessageTypeText,
			Text:      fmt.Sprintf("benchmark message %d", i),
			Timestamp: time.Unix(int64(i), 0).UTC(),
		}
	}
	for b.Loop() {
		store, err := OpenStore(ctx, filepath.Join(b.TempDir(), "state.db"))
		if err != nil {
			b.Fatal(err)
		}
		api := &fakeBeeperAPI{
			chats:    []Chat{{ID: "!bench:beeper", AccountID: "signal", Name: "Bench"}},
			messages: map[string][]Message{"!bench:beeper": messages},
		}
		svc := NewService(DefaultConfig(), store, api, &fakeMatrixSink{})
		if err := svc.ReconcileOnce(ctx); err != nil {
			b.Fatal(err)
		}
		if err := store.Close(); err != nil {
			b.Fatal(err)
		}
	}
}
