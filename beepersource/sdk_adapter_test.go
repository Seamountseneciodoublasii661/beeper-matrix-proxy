package beepersource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDesktopAPIAdapterHealthUsesInfoEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/info" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"app":       map[string]any{"bundle_id": "com.beeper", "name": "Beeper", "version": "4.2.830"},
			"endpoints": map[string]any{"mcp": "/v0/mcp", "oauth": map[string]string{}, "spec": "/openapi.json", "ws_events": "/v1/ws"},
			"platform":  map[string]any{"arch": "arm64", "os": "darwin"},
			"server":    map[string]any{"base_url": serverURL(r), "hostname": "localhost", "mcp_enabled": true, "port": 23373, "remote_access": false, "status": "ok"},
		})
	}))
	defer server.Close()

	adapter := NewDesktopAPIAdapter(Config{Beeper: BeeperConfig{BaseURL: server.URL}}, "test-token")

	if err := adapter.Health(context.Background()); err != nil {
		t.Fatalf("Health returned error: %v", err)
	}
}

func TestDesktopAPIAdapterListChatsUsesConfiguredChatIDs(t *testing.T) {
	var gotPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":        strings.TrimPrefix(r.URL.Path, "/v1/chats/"),
			"accountID": "signal",
			"title":     "Test",
			"type":      "group",
		})
	}))
	defer server.Close()

	adapter := NewDesktopAPIAdapter(Config{Beeper: BeeperConfig{
		BaseURL: server.URL,
		ChatIDs: []string{"!signal:beeper", "!whatsapp:beeper"},
	}}, "test-token")
	chats, err := adapter.ListChats(context.Background())

	if err != nil {
		t.Fatalf("ListChats returned error: %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("expected 2 chats, got %d", len(chats))
	}
	if strings.Join(gotPaths, ",") != "/v1/chats/!signal:beeper,/v1/chats/!whatsapp:beeper" {
		t.Fatalf("unexpected paths %v", gotPaths)
	}
}

func TestDesktopAPIAdapterSendMessageUsesBearerTokenAndReplyID(t *testing.T) {
	var gotAuth string
	var gotPath string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"chatID":           "!chat:beeper",
			"pendingMessageID": "$pending",
		})
	}))
	defer server.Close()

	adapter := NewDesktopAPIAdapter(Config{Beeper: BeeperConfig{BaseURL: server.URL}}, "test-token")
	pendingID, err := adapter.SendMessage(context.Background(), BeeperOutbound{
		ChatID:    "!chat:beeper",
		Text:      "hello",
		ReplyToID: "$reply",
	})

	if err != nil {
		t.Fatalf("SendMessage returned error: %v", err)
	}
	if pendingID != "$pending" {
		t.Fatalf("unexpected pending ID %q", pendingID)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("expected bearer token, got %q", gotAuth)
	}
	if gotPath != "/v1/chats/!chat:beeper/messages" {
		t.Fatalf("unexpected path %s", gotPath)
	}
	if body["text"] != "hello" || body["replyToMessageID"] != "$reply" {
		t.Fatalf("unexpected request body %#v", body)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
