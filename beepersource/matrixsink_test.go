package beepersource

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMatrixClientSinkCreatesRoomAndSendsMessage(t *testing.T) {
	var createdRoom bool
	var sentBody string
	var sentURL string
	var sentFileName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/createRoom"):
			createdRoom = true
			_ = json.NewEncoder(w).Encode(map[string]string{"room_id": "!beeper_test:local"})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/upload"):
			if ct := r.Header.Get("Content-Type"); ct != "image/png" {
				t.Fatalf("unexpected upload content-type %q", ct)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"content_uri": "mxc://local/uploaded"})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/send/m.room.message/"):
			var body struct {
				Body     string `json:"body"`
				URL      string `json:"url"`
				FileName string `json:"filename"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode send body: %v", err)
			}
			sentBody = body.Body
			sentURL = body.URL
			sentFileName = body.FileName
			_ = json.NewEncoder(w).Encode(map[string]string{"event_id": "$event:local"})
		default:
			t.Fatalf("unexpected Matrix request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	cfg := DefaultConfig()
	cfg.Matrix.HomeserverURL = server.URL
	cfg.Matrix.UserID = "@proxy:local"
	sink, err := NewMatrixClientSink(cfg, store, "token")
	if err != nil {
		t.Fatal(err)
	}

	roomID, err := sink.EnsurePortal(ctx, Chat{ID: "!chat:beeper", AccountID: "whatsapp", Name: "Family", IsGroup: true})
	if err != nil {
		t.Fatal(err)
	}
	if roomID != "!beeper_test:local" || !createdRoom {
		t.Fatalf("unexpected room result roomID=%q created=%v", roomID, createdRoom)
	}
	eventID, err := sink.SendMessage(ctx, MatrixOutbound{
		RoomID:        roomID,
		MessageID:     "$m1",
		SenderID:      "@alice:whatsapp",
		SenderName:    "Alice",
		Body:          "hello",
		MsgType:       "m.text",
		Timestamp:     time.Unix(100, 0).UTC(),
		TransactionID: "txn1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if eventID != "$event:local" {
		t.Fatalf("unexpected event ID %q", eventID)
	}
	if sentBody != "Alice: hello" {
		t.Fatalf("unexpected Matrix body %q", sentBody)
	}

	eventID, err = sink.SendMessage(ctx, MatrixOutbound{
		RoomID:        roomID,
		MessageID:     "$m2",
		SenderID:      "@alice:whatsapp",
		SenderName:    "Alice",
		Body:          "image",
		MsgType:       "m.image",
		TransactionID: "txn2",
		Media: &MatrixMedia{
			Content:   bytes.NewReader([]byte("png")),
			FileName:  "image.png",
			MimeType:  "image/png",
			SizeBytes: 3,
			Width:     2,
			Height:    3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if eventID != "$event:local" {
		t.Fatalf("unexpected media event ID %q", eventID)
	}
	if sentURL != "mxc://local/uploaded" || sentFileName != "image.png" {
		t.Fatalf("unexpected media payload url=%q filename=%q", sentURL, sentFileName)
	}
}
