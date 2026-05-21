package beepersource

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMatrixSyncForwardsCinnyMessageToBeeper(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	chat := Chat{ID: "!chat:beeper", AccountID: "signal", Name: "Test"}
	if err := store.UpsertPortal(ctx, chat, "!room:local", "beeper-cursor"); err != nil {
		t.Fatal(err)
	}
	api := &fakeBeeperAPI{}
	svc := NewService(DefaultConfig(), store, api, &fakeMatrixSink{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_matrix/client/v3/sync" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"next_batch": "sync-2",
			"rooms": {
				"join": {
					"!room:local": {
						"timeline": {
							"events": [{
								"type": "m.room.message",
								"event_id": "$cinny1:local",
								"sender": "@cinny:local",
								"content": {
									"msgtype": "m.text",
									"body": "hello from cinny"
								}
							}]
						}
					}
				}
			}
		}`))
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.Matrix.HomeserverURL = server.URL
	cfg.Matrix.UserID = "@bridge:local"
	cfg.Matrix.InsecureSkipTLS = true
	source := NewMatrixClientSource(cfg, store, "matrix-token")

	if err := source.SyncOnce(ctx, svc); err != nil {
		t.Fatalf("SyncOnce returned error: %v", err)
	}
	if len(api.sent) != 1 {
		t.Fatalf("expected one Beeper send, got %d", len(api.sent))
	}
	if api.sent[0].ChatID != chat.ID || api.sent[0].Text != "hello from cinny" {
		t.Fatalf("unexpected outbound: %#v", api.sent[0])
	}
	token, err := store.GetValue(ctx, "matrix_sync_since")
	if err != nil {
		t.Fatal(err)
	}
	if token != "sync-2" {
		t.Fatalf("expected sync token to be stored, got %q", token)
	}
}

func TestMatrixSyncIgnoresBridgeEchoes(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	if err := store.UpsertPortal(ctx, Chat{ID: "!chat:beeper"}, "!room:local", ""); err != nil {
		t.Fatal(err)
	}
	api := &fakeBeeperAPI{}
	svc := NewService(DefaultConfig(), store, api, &fakeMatrixSink{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"next_batch": "sync-3",
			"rooms": {
				"join": {
					"!room:local": {
						"timeline": {
							"events": [{
								"type": "m.room.message",
								"event_id": "$echo:local",
								"sender": "@bridge:local",
								"content": {"msgtype": "m.text", "body": "mirrored"}
							}]
						}
					}
				}
			}
		}`))
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.Matrix.HomeserverURL = server.URL
	cfg.Matrix.UserID = "@bridge:local"
	cfg.Matrix.InsecureSkipTLS = true
	source := NewMatrixClientSource(cfg, store, "matrix-token")

	if err := source.SyncOnce(ctx, svc); err != nil {
		t.Fatalf("SyncOnce returned error: %v", err)
	}
	if len(api.sent) != 0 {
		t.Fatalf("expected bridge echo to be ignored, got %#v", api.sent)
	}
}
