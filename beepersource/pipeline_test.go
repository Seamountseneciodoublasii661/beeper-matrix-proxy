package beepersource

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
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

func TestReconcileMapsBeeperLinkedMessageToMatrixReply(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	if err := store.UpsertMessageMapping(ctx, MessageMapping{
		BeeperMessageID: "$beeper-parent",
		MatrixEventID:   "$matrix-parent:local",
		ChatID:          "!chat:beeper",
		Version:         "parent",
	}); err != nil {
		t.Fatal(err)
	}
	api := &fakeBeeperAPI{
		chats: []Chat{{ID: "!chat:beeper", AccountID: "signal", Name: "Reply Test"}},
		messages: map[string][]Message{
			"!chat:beeper": {{
				ID:              "$beeper-reply",
				ChatID:          "!chat:beeper",
				SenderID:        "@alice:signal",
				Type:            MessageTypeText,
				Text:            "reply from beeper",
				LinkedMessageID: "$beeper-parent",
				Timestamp:       time.Unix(100, 0).UTC(),
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
	if matrix.events[0].ReplyToEvent != "$matrix-parent:local" {
		t.Fatalf("expected Matrix reply target to be remapped, got %q", matrix.events[0].ReplyToEvent)
	}
}

func TestReconcileDownloadsChatAvatarForNewPortal(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	api := &fakeBeeperAPI{
		chats: []Chat{{
			ID:        "!chat:beeper",
			AccountID: "whatsapp",
			Name:      "Avatar Test",
			AvatarURL: "localmxc://avatar",
		}},
		messages: map[string][]Message{"!chat:beeper": nil},
		assets:   map[string]string{"localmxc://avatar": "avatar-bytes"},
	}
	matrix := &fakeMatrixSink{}
	svc := NewService(DefaultConfig(), store, api, matrix)

	if err := svc.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
	if len(matrix.avatars) != 1 {
		t.Fatalf("expected one portal avatar, got %d", len(matrix.avatars))
	}
	body, err := io.ReadAll(matrix.avatars[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "avatar-bytes" {
		t.Fatalf("unexpected avatar body %q", string(body))
	}
}

func TestReconcilePortalsOnlyCreatesRoomsWithoutMessages(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	api := &fakeBeeperAPI{
		chats: []Chat{{
			ID:        "!chat:beeper",
			AccountID: "whatsapp",
			Network:   "WhatsApp",
			Name:      "Family",
		}},
		messages: map[string][]Message{
			"!chat:beeper": {{
				ID:     "$m1",
				ChatID: "!chat:beeper",
				Type:   MessageTypeText,
				Text:   "should not import",
			}},
		},
	}
	matrix := &fakeMatrixSink{}
	svc := NewService(DefaultConfig(), store, api, matrix)

	if err := svc.ReconcilePortalsOnly(ctx); err != nil {
		t.Fatalf("ReconcilePortalsOnly returned error: %v", err)
	}
	if len(matrix.avatars) != 1 {
		t.Fatalf("expected platform avatar fallback, got %d", len(matrix.avatars))
	}
	if len(matrix.events) != 0 {
		t.Fatalf("expected no message imports, got %#v", matrix.events)
	}
	roomID, ok, err := store.PortalRoomID(ctx, "!chat:beeper")
	if err != nil || !ok || roomID == "" {
		t.Fatalf("expected portal mapping, roomID=%q ok=%v err=%v", roomID, ok, err)
	}
}

func TestReconcileCanPreferPlatformAvatarsOverChatAvatars(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	api := &fakeBeeperAPI{
		chats: []Chat{{
			ID:        "!chat:beeper",
			AccountID: "whatsapp",
			Network:   "WhatsApp",
			Name:      "Has Real Avatar",
			AvatarURL: "localmxc://real-avatar",
		}},
		messages: map[string][]Message{"!chat:beeper": nil},
		assets:   map[string]string{"localmxc://real-avatar": "real-avatar"},
	}
	matrix := &fakeMatrixSink{}
	cfg := DefaultConfig()
	cfg.Matrix.PlatformAvatars = true
	svc := NewService(cfg, store, api, matrix)

	if err := svc.ReconcilePortalsOnly(ctx); err != nil {
		t.Fatalf("ReconcilePortalsOnly returned error: %v", err)
	}
	if len(matrix.avatars) != 1 {
		t.Fatalf("expected one platform avatar, got %d", len(matrix.avatars))
	}
	if matrix.avatars[0].MimeType != "image/png" {
		t.Fatalf("expected platform PNG avatar, got %#v", matrix.avatars[0])
	}
}

func TestReconcilePortalsOnlySkipsArchivedChatsByDefault(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	api := &fakeBeeperAPI{
		chats: []Chat{{
			ID:         "!archived:beeper",
			AccountID:  "whatsapp",
			Network:    "WhatsApp",
			Name:       "Archived",
			IsArchived: true,
		}},
	}
	matrix := &fakeMatrixSink{}
	svc := NewService(DefaultConfig(), store, api, matrix)

	if err := svc.ReconcilePortalsOnly(ctx); err != nil {
		t.Fatalf("ReconcilePortalsOnly returned error: %v", err)
	}
	if len(matrix.avatars) != 0 {
		t.Fatalf("expected archived chat to be skipped, got %d avatars", len(matrix.avatars))
	}
	if roomID, ok, err := store.PortalRoomID(ctx, "!archived:beeper"); err != nil || ok || roomID != "" {
		t.Fatalf("expected no archived portal mapping, roomID=%q ok=%v err=%v", roomID, ok, err)
	}
}

func TestReconcilePortalsOnlyContinuesAfterPortalError(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	api := &fakeBeeperAPI{
		chats: []Chat{
			{ID: "!bad:beeper", AccountID: "signal", Name: "Bad"},
			{ID: "!good:beeper", AccountID: "signal", Name: "Good"},
		},
	}
	matrix := &fakeMatrixSink{failPortals: map[string]error{"!bad:beeper": errors.New("boom")}}
	svc := NewService(DefaultConfig(), store, api, matrix)

	err := svc.ReconcilePortalsOnly(ctx)
	if err == nil {
		t.Fatal("expected portal error to be reported")
	}
	if _, ok, lookupErr := store.PortalRoomID(ctx, "!good:beeper"); lookupErr != nil || !ok {
		t.Fatalf("expected good portal to still be stored, ok=%v err=%v", ok, lookupErr)
	}
}

func TestReconcilePortalsOnlyRetriesRateLimitedPortalWithoutFailingImport(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	api := &fakeBeeperAPI{
		chats: []Chat{
			{ID: "!limited:beeper", AccountID: "signal", Name: "Limited"},
			{ID: "!good:beeper", AccountID: "signal", Name: "Good"},
		},
	}
	matrix := &fakeMatrixSink{
		failPortalSequences: map[string][]error{
			"!limited:beeper": {&MatrixRateLimitError{RetryAfter: time.Millisecond, StatusCode: http.StatusTooManyRequests, ErrCode: "M_LIMIT_EXCEEDED"}},
		},
	}
	cfg := DefaultConfig()
	cfg.Sync.PortalWorkers = 2
	cfg.Sync.PortalTimeoutSeconds = 2
	svc := NewService(cfg, store, api, matrix)

	if err := svc.ReconcilePortalsOnly(ctx); err != nil {
		t.Fatalf("ReconcilePortalsOnly returned error after temporary 429: %v", err)
	}
	if got := matrix.portalAttempts["!limited:beeper"]; got != 2 {
		t.Fatalf("expected rate-limited portal to be retried once, got %d attempts", got)
	}
	for _, chatID := range []string{"!limited:beeper", "!good:beeper"} {
		if _, ok, err := store.PortalRoomID(ctx, chatID); err != nil || !ok {
			t.Fatalf("expected portal %s to be stored after import, ok=%v err=%v", chatID, ok, err)
		}
	}
}

func TestReconcilePortalsOnlyRecreatesInaccessibleExistingPortal(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	chat := Chat{ID: "!stale:beeper", AccountID: "telegram", Network: "Telegram", Name: "Stale"}
	if err := store.UpsertPortal(ctx, chat, "!old:local", ""); err != nil {
		t.Fatal(err)
	}
	api := &fakeBeeperAPI{chats: []Chat{chat}}
	matrix := &fakeMatrixSink{inaccessibleRooms: map[string]bool{"!old:local": true}}
	svc := NewService(DefaultConfig(), store, api, matrix)

	if err := svc.ReconcilePortalsOnly(ctx); err != nil {
		t.Fatalf("ReconcilePortalsOnly returned error: %v", err)
	}
	roomID, ok, err := store.PortalRoomID(ctx, chat.ID)
	if err != nil || !ok {
		t.Fatalf("expected recreated portal mapping, ok=%v err=%v", ok, err)
	}
	if roomID == "!old:local" {
		t.Fatalf("expected stale room to be replaced, still got %q", roomID)
	}
	if got := matrix.portalAttempts[chat.ID]; got != 1 {
		t.Fatalf("expected inaccessible portal to be recreated once, got %d attempts", got)
	}
}

func TestReconcilePortalsOnlyOrganizesExistingPortalsIntoSpaces(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	chats := []Chat{
		{ID: "!wa:beeper", AccountID: "whatsapp", Network: "WhatsApp", Name: "WA"},
		{ID: "!sig:beeper", AccountID: "signal", Network: "Signal", Name: "Signal"},
	}
	for _, chat := range chats {
		if err := store.UpsertPortal(ctx, chat, "!matrix-"+chat.ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	api := &fakeBeeperAPI{chats: chats}
	matrix := &fakeMatrixSink{}
	cfg := DefaultConfig()
	cfg.Matrix.Spaces = true
	svc := NewService(cfg, store, api, matrix)

	if err := svc.ReconcilePortalsOnly(ctx); err != nil {
		t.Fatalf("ReconcilePortalsOnly returned error: %v", err)
	}
	if got := len(matrix.spaceChats); got != 2 {
		t.Fatalf("expected existing portals to be organized into spaces, got %d chats", got)
	}
}

func TestReconcileRefreshesExistingPortalAvatarWhenChanged(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	if err := store.UpsertPortal(ctx, Chat{ID: "!chat:beeper"}, "!matrix-!chat:beeper", ""); err != nil {
		t.Fatal(err)
	}
	api := &fakeBeeperAPI{
		chats: []Chat{{
			ID:        "!chat:beeper",
			AccountID: "signal",
			Name:      "Existing Avatar Test",
			AvatarURL: "localmxc://avatar-v2",
		}},
		messages: map[string][]Message{"!chat:beeper": nil},
		assets:   map[string]string{"localmxc://avatar-v2": "avatar-v2"},
	}
	matrix := &fakeMatrixSink{}
	svc := NewService(DefaultConfig(), store, api, matrix)

	if err := svc.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce returned error: %v", err)
	}
	if len(matrix.avatars) != 1 {
		t.Fatalf("expected existing portal avatar refresh, got %d avatars", len(matrix.avatars))
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

func TestMatrixToBeeperRemapsReplyTargetToBeeperMessageID(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	if err := store.UpsertMessageMapping(ctx, MessageMapping{
		BeeperMessageID: "$beeper-parent",
		MatrixEventID:   "$matrix-parent:local",
		ChatID:          "!chat:beeper",
		Version:         "parent",
	}); err != nil {
		t.Fatal(err)
	}
	api := &fakeBeeperAPI{}
	svc := NewService(DefaultConfig(), store, api, &fakeMatrixSink{})

	err := svc.HandleMatrixMessage(ctx, MatrixInbound{
		ChatID:        "!chat:beeper",
		MatrixEventID: "$matrix-reply:local",
		ReplyToEvent:  "$matrix-parent:local",
		Body:          "reply from matrix",
	})
	if err != nil {
		t.Fatalf("HandleMatrixMessage returned error: %v", err)
	}
	if len(api.sent) != 1 {
		t.Fatalf("expected one Beeper send, got %d", len(api.sent))
	}
	if api.sent[0].ReplyToID != "$beeper-parent" {
		t.Fatalf("expected Beeper reply ID to be remapped, got %q", api.sent[0].ReplyToID)
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

func TestReconcilePreservesMatrixOriginMappingWhenEchoVersionChanges(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	defer store.Close()
	api := &fakeBeeperAPI{
		chats: []Chat{{ID: "!chat:beeper", AccountID: "signal", Name: "Edited Echo"}},
		messages: map[string][]Message{
			"!chat:beeper": {{
				ID:              "$beeper-echo",
				ChatID:          "!chat:beeper",
				SenderID:        "@self:signal",
				Type:            MessageTypeText,
				Text:            "edited in Matrix",
				Timestamp:       time.Now().UTC(),
				EditedTimestamp: ptrTime(time.Unix(200, 0).UTC()),
			}},
		},
	}
	matrix := &fakeMatrixSink{}
	svc := NewService(DefaultConfig(), store, api, matrix)
	if err := store.UpsertMessageMapping(ctx, MessageMapping{
		BeeperMessageID: "$beeper-echo",
		MatrixEventID:   "$matrix-origin:local",
		ChatID:          "!chat:beeper",
		Version:         "old",
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(matrix.events) != 0 {
		t.Fatalf("expected changed echo to preserve mapping without a duplicate Matrix event, got %#v", matrix.events)
	}
	mapping, ok, err := store.MessageByBeeperID(ctx, "$beeper-echo")
	if err != nil || !ok {
		t.Fatalf("expected echo mapping, ok=%v err=%v", ok, err)
	}
	if mapping.MatrixEventID != "$matrix-origin:local" {
		t.Fatalf("expected origin mapping to survive version update, got %q", mapping.MatrixEventID)
	}
	if mapping.Version == "old" {
		t.Fatal("expected version to be refreshed")
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

type fakeBeeperAPI struct {
	chats     []Chat
	messages  map[string][]Message
	assets    map[string]string
	sent      []BeeperOutbound
	updates   []BeeperOutbound
	deletes   []string
	reactions []string
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

func (f *fakeBeeperAPI) UpdateMessage(ctx context.Context, chatID, messageID, text string) error {
	f.updates = append(f.updates, BeeperOutbound{ChatID: chatID, Text: text, ClientTxnID: messageID})
	return nil
}

func (f *fakeBeeperAPI) DeleteMessage(ctx context.Context, chatID, messageID string, forEveryone bool) error {
	f.deletes = append(f.deletes, messageID)
	return nil
}

func (f *fakeBeeperAPI) AddReaction(ctx context.Context, chatID, messageID, reactionKey, txnID string) error {
	f.reactions = append(f.reactions, chatID+"|"+messageID+"|"+reactionKey+"|"+txnID)
	return nil
}

func (f *fakeBeeperAPI) RemoveReaction(ctx context.Context, chatID, messageID, reactionKey string) error {
	f.reactions = append(f.reactions, "delete|"+chatID+"|"+messageID+"|"+reactionKey)
	return nil
}

type fakeMatrixSink struct {
	mu                  sync.Mutex
	events              []MatrixOutbound
	failMedia           bool
	failPortals         map[string]error
	failPortalSequences map[string][]error
	inaccessibleRooms   map[string]bool
	portalAttempts      map[string]int
	avatars             []*MatrixMedia
	spaceChats          []Chat
}

func (f *fakeMatrixSink) EnsurePortal(ctx context.Context, chat Chat, avatar *MatrixMedia) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.portalAttempts == nil {
		f.portalAttempts = make(map[string]int)
	}
	attempt := f.portalAttempts[chat.ID]
	f.portalAttempts[chat.ID] = attempt + 1
	if seq := f.failPortalSequences[chat.ID]; attempt < len(seq) && seq[attempt] != nil {
		return "", seq[attempt]
	}
	if err := f.failPortals[chat.ID]; err != nil {
		return "", err
	}
	if avatar != nil {
		f.avatars = append(f.avatars, avatar)
	}
	return "!matrix-" + chat.ID, nil
}

func (f *fakeMatrixSink) PortalAccessible(ctx context.Context, roomID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.inaccessibleRooms[roomID], nil
}

func (f *fakeMatrixSink) EnsurePortalSpaces(ctx context.Context, chats []Chat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spaceChats = append([]Chat(nil), chats...)
	return nil
}

func (f *fakeMatrixSink) EnsurePuppet(ctx context.Context, sender Sender) (string, error) {
	return "@" + MatrixGhostLocalpart(sender.ID) + ":local", nil
}

func (f *fakeMatrixSink) SendMessage(ctx context.Context, outbound MatrixOutbound) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, outbound)
	if f.failMedia && outbound.Media != nil {
		return "", errors.New("HTTP 413")
	}
	return "$event-" + outbound.MessageID + ":local", nil
}
