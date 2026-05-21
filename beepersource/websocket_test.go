package beepersource

import (
	"encoding/json"
	"testing"
)

func TestWebSocketSubscribeAllCommandMatchesBeeperContract(t *testing.T) {
	raw, err := json.Marshal(SubscribeAllChatsCommand("r1"))
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["type"] != "subscriptions.set" {
		t.Fatalf("unexpected websocket command type %#v", decoded["type"])
	}
	chatIDs := decoded["chatIDs"].([]any)
	if len(chatIDs) != 1 || chatIDs[0] != "*" {
		t.Fatalf("expected all-chat subscription, got %#v", chatIDs)
	}
}
