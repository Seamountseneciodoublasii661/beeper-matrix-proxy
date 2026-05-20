package connector

import (
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

func TestUpdateRemoteTypingDiffsAndSkipsOwnUser(t *testing.T) {
	nc := &MyNetworkClient{
		mx: &mautrix.Client{UserID: id.UserID("@me:matrix.example.com")},
	}
	roomID := id.RoomID("!room:matrix.example.com")

	started, stopped := nc.updateRemoteTyping(roomID, []id.UserID{
		id.UserID("@me:matrix.example.com"),
		id.UserID("@alice:matrix.example.com"),
	})
	if len(started) != 1 || started[0] != id.UserID("@alice:matrix.example.com") {
		t.Fatalf("expected alice to start typing, got started=%v stopped=%v", started, stopped)
	}
	if len(stopped) != 0 {
		t.Fatalf("expected nobody to stop typing, got %v", stopped)
	}

	started, stopped = nc.updateRemoteTyping(roomID, []id.UserID{id.UserID("@bob:matrix.example.com")})
	if len(started) != 1 || started[0] != id.UserID("@bob:matrix.example.com") {
		t.Fatalf("expected bob to start typing, got started=%v stopped=%v", started, stopped)
	}
	if len(stopped) != 1 || stopped[0] != id.UserID("@alice:matrix.example.com") {
		t.Fatalf("expected alice to stop typing, got started=%v stopped=%v", started, stopped)
	}

	started, stopped = nc.updateRemoteTyping(roomID, nil)
	if len(started) != 0 {
		t.Fatalf("expected nobody to start typing, got %v", started)
	}
	if len(stopped) != 1 || stopped[0] != id.UserID("@bob:matrix.example.com") {
		t.Fatalf("expected bob to stop typing, got %v", stopped)
	}
	if _, ok := nc.remoteTyping[roomID]; ok {
		t.Fatal("expected empty room typing state to be removed")
	}
}
