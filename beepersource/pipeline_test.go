package beepersource

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestReconcileMirrorsTextMessagesAndStoresMapping(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	api := &fakeBeeperAPI{
		chats: []Chat{{
			ID:        "!chat:beeper",
			AccountID: "local-whatsapp",
			Name:      "Test Chat",
		}},
		messages: map[string][]Message{
			"!chat:beeper": {{
				ID:        "$m1",
				ChatID:    "!chat:beeper",
				SenderID:  "@alice:local-whatsapp.localhost",
				Type:      MessageTypeText,
				Text:      "hello",
				Timestamp: time.Unix(100, 0).UTC(),
			}},
		},
	}
	matrix := &fakeMatrixSink{}
	svc := NewService(DefaultConfig(), store, api, matrix)

	if err := svc.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
	if len(matrix.events) != 1 {
		t.Fatalf("expected one Matrix event, got %d", len(matrix.events))
	}
	if matrix.events[0].Body != "hello" {
		t.Fatalf("unexpected event body %q", matrix.events[0].Body)
	}

	got, ok, err := store.MessageByBeeperID(ctx, "$m1")
	if err != nil || !ok {
		t.Fatalf("expected message mapping, ok=%v err=%v", ok, err)
	}
	if got.MatrixEventID == "" {
		t.Fatal("expected Matrix event ID to be stored")
	}
}

func TestReconcileIsIdempotentWhenMessageMappingExists(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	api := &fakeBeeperAPI{
		chats: []Chat{{ID: "!chat:beeper", AccountID: "local-signal", Name: "Idempotent"}},
		messages: map[string][]Message{
			"!chat:beeper": {{
				ID:        "$m1",
				ChatID:    "!chat:beeper",
				SenderID:  "@bob:local-signal.localhost",
				Type:      MessageTypeText,
				Text:      "once",
				Timestamp: time.Unix(100, 0).UTC(),
			}},
		},
	}
	matrix := &fakeMatrixSink{}
	svc := NewService(DefaultConfig(), store, api, matrix)

	if err := svc.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(matrix.events) != 1 {
		t.Fatalf("expected idempotent reconcile to send one event, got %d", len(matrix.events))
	}
}

func TestReconcileMirrorsImageAttachmentAsMatrixMedia(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	api := &fakeBeeperAPI{
		chats: []Chat{{ID: "!chat:beeper", AccountID: "whatsapp", Name: "Media Test"}},
		messages: map[string][]Message{
			"!chat:beeper": {{
				ID:        "$img1",
				ChatID:    "!chat:beeper",
				SenderID:  "@alice:local-whatsapp.localhost",
				Type:      MessageTypeImage,
				Text:      "image caption",
				Timestamp: time.Unix(100, 0).UTC(),
				Attachments: []Attachment{{
					URL:       "localmxc://image",
					FileName:  "image.png",
					MimeType:  "image/png",
					SizeBytes: 7,
					Width:     2,
					Height:    3,
				}},
			}},
		},
		assets: map[string]string{"localmxc://image": "pngdata"},
	}
	matrix := &fakeMatrixSink{}
	svc := NewService(DefaultConfig(), store, api, matrix)

	if err := svc.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
	if len(matrix.events) != 1 {
		t.Fatalf("expected one Matrix event, got %d", len(matrix.events))
	}
	if matrix.events[0].Media == nil {
		t.Fatal("expected Matrix media to be attached")
	}
	if matrix.events[0].Media.FileName != "image.png" || matrix.events[0].Media.MimeType != "image/png" {
		t.Fatalf("unexpected media metadata: %#v", matrix.events[0].Media)
	}
	body, err := io.ReadAll(matrix.events[0].Media.Content)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "pngdata" {
		t.Fatalf("unexpected media body %q", string(body))
	}
}

func TestReconcileFallsBackToNoticeWhenMatrixMediaUploadFails(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	api := &fakeBeeperAPI{
		chats: []Chat{{ID: "!chat:beeper", AccountID: "whatsapp", Name: "Media Test"}},
		messages: map[string][]Message{
			"!chat:beeper": {{
				ID:        "$img1",
				ChatID:    "!chat:beeper",
				SenderID:  "@alice:local-whatsapp.localhost",
				Type:      MessageTypeImage,
				Timestamp: time.Unix(100, 0).UTC(),
				Attachments: []Attachment{{
					URL:       "localmxc://image",
					FileName:  "image.png",
					MimeType:  "image/png",
					SizeBytes: 7,
				}},
			}},
		},
		assets: map[string]string{"localmxc://image": "pngdata"},
	}
	matrix := &fakeMatrixSink{failMedia: true}
	svc := NewService(DefaultConfig(), store, api, matrix)

	if err := svc.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
	if len(matrix.events) != 2 {
		t.Fatalf("expected media attempt and fallback notice, got %d events", len(matrix.events))
	}
	if matrix.events[1].MsgType != "m.notice" || !strings.Contains(matrix.events[1].Body, "could not be mirrored") {
		t.Fatalf("unexpected fallback event: %#v", matrix.events[1])
	}
}

func TestMatrixToBeeperHonorsKillSwitch(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	cfg := DefaultConfig()
	cfg.Safety.DisableMatrixToBeeper = true
	api := &fakeBeeperAPI{}
	svc := NewService(cfg, store, api, &fakeMatrixSink{})

	err := svc.HandleMatrixMessage(ctx, MatrixInbound{
		ChatID: "!chat:beeper",
		Body:   "blocked",
	})

	if err != ErrMatrixToBeeperDisabled {
		t.Fatalf("expected kill switch error, got %v", err)
	}
	if len(api.sent) != 0 {
		t.Fatalf("expected no Beeper sends, got %#v", api.sent)
	}
}

func TestMatrixToBeeperUsesDeterministicEchoKey(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	api := &fakeBeeperAPI{}
	svc := NewService(DefaultConfig(), store, api, &fakeMatrixSink{})

	err := svc.HandleMatrixMessage(ctx, MatrixInbound{
		ChatID:        "!chat:beeper",
		MatrixEventID: "$matrix-event:local",
		Body:          "hi from matrix",
	})
	if err != nil {
		t.Fatalf("HandleMatrixMessage returned error: %v", err)
	}
	if len(api.sent) != 1 {
		t.Fatalf("expected one Beeper send, got %d", len(api.sent))
	}
	if api.sent[0].ClientTxnID == "" {
		t.Fatal("expected deterministic client transaction ID")
	}
}

func TestReconcileSuppressesEchoAfterMatrixToBeeperSend(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	api := &fakeBeeperAPI{
		chats: []Chat{{ID: "!chat:beeper", AccountID: "signal", Name: "Echo"}},
		messages: map[string][]Message{
			"!chat:beeper": {{
				ID:        "$beeper-echo",
				ChatID:    "!chat:beeper",
				SenderID:  "@self:signal",
				Type:      MessageTypeText,
				Text:      "echo me once",
				Timestamp: time.Now().UTC(),
			}},
		},
	}
	matrix := &fakeMatrixSink{}
	svc := NewService(DefaultConfig(), store, api, matrix)
	if err := svc.HandleMatrixMessage(ctx, MatrixInbound{
		ChatID:        "!chat:beeper",
		MatrixEventID: "$matrix-origin:local",
		Body:          "echo me once",
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(matrix.events) != 0 {
		t.Fatalf("expected echoed Beeper message to be suppressed, got %#v", matrix.events)
	}
	mapping, ok, err := store.MessageByBeeperID(ctx, "$beeper-echo")
	if err != nil || !ok {
		t.Fatalf("expected echo mapping, ok=%v err=%v", ok, err)
	}
	if mapping.MatrixEventID != "$matrix-origin:local" {
		t.Fatalf("expected echo to map to original event, got %q", mapping.MatrixEventID)
	}
}

type fakeBeeperAPI struct {
	chats    []Chat
	messages map[string][]Message
	assets   map[string]string
	sent     []BeeperOutbound
}

func (f *fakeBeeperAPI) Health(context.Context) error { return nil }

func (f *fakeBeeperAPI) ListChats(context.Context) ([]Chat, error) {
	return f.chats, nil
}

func (f *fakeBeeperAPI) ListMessages(ctx context.Context, chatID string, afterCursor string, limit int) ([]Message, string, error) {
	return f.messages[chatID], "cursor-" + chatID, nil
}

func (f *fakeBeeperAPI) DownloadAsset(ctx context.Context, assetURL string) (*AssetStream, error) {
	return &AssetStream{
		Content:   io.NopCloser(strings.NewReader(f.assets[assetURL])),
		MimeType:  "application/octet-stream",
		SizeBytes: int64(len(f.assets[assetURL])),
	}, nil
}

func (f *fakeBeeperAPI) SendMessage(ctx context.Context, outbound BeeperOutbound) (string, error) {
	f.sent = append(f.sent, outbound)
	return "$beeper-sent", nil
}

type fakeMatrixSink struct {
	events    []MatrixOutbound
	failMedia bool
}

func (f *fakeMatrixSink) EnsurePortal(ctx context.Context, chat Chat) (string, error) {
	return "!matrix-" + chat.ID, nil
}

func (f *fakeMatrixSink) EnsurePuppet(ctx context.Context, sender Sender) (string, error) {
	return "@" + MatrixGhostLocalpart(sender.ID) + ":local", nil
}

func (f *fakeMatrixSink) SendMessage(ctx context.Context, outbound MatrixOutbound) (string, error) {
	f.events = append(f.events, outbound)
	if f.failMedia && outbound.Media != nil {
		return "", errors.New("HTTP 413")
	}
	return "$event-" + outbound.MessageID + ":local", nil
}
