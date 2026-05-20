package connector

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestLocalMatrixSyncFilterKeepsBursts(t *testing.T) {
	filter := localMatrixSyncFilter()
	if filter == nil || filter.Room == nil || filter.Room.Timeline == nil {
		t.Fatal("expected room timeline filter")
	}
	if got := filter.Room.Timeline.Limit; got < 50 {
		t.Fatalf("expected timeline limit to preserve bursts, got %d", got)
	}
	if !containsEventType(filter.Room.Timeline.Types, event.EventSticker) {
		t.Fatal("expected live sync filter to include stickers")
	}
	if filter.Room.Ephemeral == nil || !containsEventType(filter.Room.Ephemeral.Types, event.EphemeralEventTyping) || !containsEventType(filter.Room.Ephemeral.Types, event.EphemeralEventReceipt) {
		t.Fatalf("expected sync filter to include typing and receipt ephemeral events, got %#v", filter.Room.Ephemeral)
	}
}

func TestSyncExitClearsCancelForReconnect(t *testing.T) {
	nc := &MyNetworkClient{
		loggedIn:       true,
		syncGeneration: 7,
	}
	nc.cancel = func() {}

	nc.handleSyncExit(7, errors.New("temporary sync failure"))

	if nc.cancel != nil {
		t.Fatal("expected sync exit to clear cancel so Connect can reconnect")
	}
	if nc.loggedIn {
		t.Fatal("expected transient sync failure to mark client logged out")
	}
}

func TestOldSyncExitDoesNotClearNewConnection(t *testing.T) {
	nc := &MyNetworkClient{
		loggedIn:       true,
		syncGeneration: 8,
	}
	nc.cancel = func() {}

	nc.handleSyncExit(7, errors.New("old sync failure"))

	if nc.cancel == nil {
		t.Fatal("expected old sync generation not to clear current cancel")
	}
	if !nc.loggedIn {
		t.Fatal("expected old sync generation not to alter login state")
	}
}

func TestCanceledSyncExitClearsCancelWithoutTransientLogout(t *testing.T) {
	nc := &MyNetworkClient{
		loggedIn:       true,
		syncGeneration: 3,
		cancel:         func() {},
	}

	nc.handleSyncExit(3, context.Canceled)

	if nc.cancel != nil {
		t.Fatal("expected canceled sync exit to clear cancel")
	}
	if !nc.loggedIn {
		t.Fatal("expected normal cancellation not to mark logged out")
	}
}

func TestUnknownTokenSyncExitDoesNotScheduleReconnect(t *testing.T) {
	nc := &MyNetworkClient{
		loggedIn:       true,
		syncGeneration: 4,
		cancel:         func() {},
	}

	nc.handleSyncExit(4, mautrix.MUnknownToken)

	if nc.cancel != nil {
		t.Fatal("expected unknown-token sync exit to clear cancel")
	}
	if nc.reconnectScheduled {
		t.Fatal("expected unknown-token sync exit not to schedule reconnect")
	}
	if nc.loggedIn {
		t.Fatal("expected unknown-token sync exit to mark client logged out")
	}
}

func TestRemoteReconnectDelayBacksOffWithCap(t *testing.T) {
	if got := remoteReconnectDelay(0); got != remoteReconnectBaseDelay {
		t.Fatalf("expected first reconnect delay %s, got %s", remoteReconnectBaseDelay, got)
	}
	if got := remoteReconnectDelay(2); got != 2*time.Minute {
		t.Fatalf("expected third reconnect delay 2m, got %s", got)
	}
	if got := remoteReconnectDelay(20); got != remoteReconnectMaxDelay {
		t.Fatalf("expected reconnect delay cap %s, got %s", remoteReconnectMaxDelay, got)
	}
}

func TestRemoteMatrixPreflightFailsOnBadGateway(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer server.Close()

	client, err := mautrix.NewClient(server.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	nc := &MyNetworkClient{mx: client}

	err = nc.remoteMatrixPreflight(context.Background())
	if err == nil {
		t.Fatal("expected preflight to fail on HTTP 502")
	}
	if !strings.Contains(err.Error(), "/versions") {
		t.Fatalf("expected error to explain /versions preflight, got %v", err)
	}
}

func TestRemoteMatrixPreflightAcceptsHealthyVersionsEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_matrix/client/versions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"versions":["v1.11"]}`))
	}))
	defer server.Close()

	client, err := mautrix.NewClient(server.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	nc := &MyNetworkClient{mx: client}

	if err := nc.remoteMatrixPreflight(context.Background()); err != nil {
		t.Fatalf("expected healthy /versions endpoint, got %v", err)
	}
}

func TestConfigureLocalMatrixSyncerIsIdempotent(t *testing.T) {
	syncer := mautrix.NewDefaultSyncer()
	nc := &MyNetworkClient{mx: &mautrix.Client{Syncer: syncer}}

	nc.configureLocalMatrixSyncer()
	nc.configureLocalMatrixSyncer()

	syncerValue := reflect.ValueOf(syncer).Elem()
	if got := syncerValue.FieldByName("syncListeners").Len(); got != 1 {
		t.Fatalf("expected one sync listener after repeated configure, got %d", got)
	}
	listeners := syncerValue.FieldByName("listeners")
	if got := listeners.MapIndex(reflect.ValueOf(event.EventMessage)).Len(); got != 1 {
		t.Fatalf("expected one message listener after repeated configure, got %d", got)
	}
}

func TestLoginSyncStorePersistsNextBatchAndFilterInMetadata(t *testing.T) {
	dbLogin := &database.UserLogin{Metadata: &LoginMetadata{}}
	login := &bridgev2.UserLogin{UserLogin: dbLogin}
	store := newLoginSyncStore(newLoginMetadataStore(login))

	if err := store.SaveFilterID(context.Background(), "@user:example", "filter-1"); err != nil {
		t.Fatalf("SaveFilterID returned error: %v", err)
	}
	if err := store.SaveNextBatch(context.Background(), "@user:example", "batch-1"); err != nil {
		t.Fatalf("SaveNextBatch returned error: %v", err)
	}

	filterID, err := store.LoadFilterID(context.Background(), "@user:example")
	if err != nil {
		t.Fatalf("LoadFilterID returned error: %v", err)
	}
	nextBatch, err := store.LoadNextBatch(context.Background(), "@user:example")
	if err != nil {
		t.Fatalf("LoadNextBatch returned error: %v", err)
	}
	if filterID != "filter-1" || nextBatch != "batch-1" {
		t.Fatalf("unexpected persisted sync state: filter=%q next_batch=%q", filterID, nextBatch)
	}
	meta := dbLogin.Metadata.(*LoginMetadata)
	if meta.LastSyncAt == nil {
		t.Fatal("expected LastSyncAt to be updated with next_batch")
	}
}

func TestPersistentRemoteReactionSurvivesMemoryMapMiss(t *testing.T) {
	dbLogin := &database.UserLogin{Metadata: &LoginMetadata{}}
	login := &bridgev2.UserLogin{UserLogin: dbLogin}
	nc := &MyNetworkClient{
		metadata: newLoginMetadataStore(login),
	}
	reactionEventID := id.EventID("$reaction:matrix.example")
	reaction := remoteReaction{
		RoomID:        "!room:matrix.example",
		TargetMessage: "$target:matrix.example",
		Sender:        "@alice:matrix.example",
		EmojiID:       "👍",
		Emoji:         "👍",
		Timestamp:     time.Unix(123, 0),
	}

	nc.persistRemoteReaction(context.Background(), reactionEventID, reaction)

	loaded, ok := nc.popRemoteReaction(context.Background(), reactionEventID)
	if !ok {
		t.Fatal("expected persisted remote reaction to be found without memory map")
	}
	if loaded.TargetMessage != reaction.TargetMessage || loaded.Sender != reaction.Sender || loaded.Emoji != reaction.Emoji {
		t.Fatalf("unexpected loaded reaction: %#v", loaded)
	}
	meta := dbLogin.Metadata.(*LoginMetadata)
	if len(meta.RemoteReactions) != 0 {
		t.Fatalf("expected reaction metadata to be removed after pop, got %#v", meta.RemoteReactions)
	}
}

func TestRoomCapabilitiesExposeBoundedInteractiveFeatures(t *testing.T) {
	features := (&MyNetworkClient{}).GetCapabilities(context.Background(), nil)

	if features.Edit != event.CapLevelFullySupported || features.EditMaxAge == nil || features.EditMaxCount == 0 {
		t.Fatalf("expected bounded edit capability, got %#v", features)
	}
	if features.Delete != event.CapLevelFullySupported || features.DeleteMaxAge == nil {
		t.Fatalf("expected bounded delete capability, got %#v", features)
	}
	if features.Reaction != event.CapLevelFullySupported || features.ReactionCount != 1 {
		t.Fatalf("expected single-reaction capability, got reaction=%d count=%d", features.Reaction, features.ReactionCount)
	}
	if features.File[event.CapMsgVoice] == nil || features.File[event.CapMsgVoice].MaxDuration == nil {
		t.Fatal("expected voice message max duration to be advertised")
	}
}

func TestCleanEditContentUsesNewContentWithoutLegacyPrefix(t *testing.T) {
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    "* edited fallback",
		NewContent: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    "* edited clean",
		},
	}

	cleaned := cleanEditContentForBeeper(content)

	if cleaned == content {
		t.Fatal("expected edit content to be cloned")
	}
	if cleaned.Body != "edited clean" {
		t.Fatalf("expected clean body, got %q", cleaned.Body)
	}
	if cleaned.NewContent != nil {
		t.Fatalf("expected nested new content to be removed, got %#v", cleaned.NewContent)
	}
	if cleaned.RelatesTo != nil {
		t.Fatalf("expected relates_to to be removed, got %#v", cleaned.RelatesTo)
	}
}

func TestNormalizePollStartRawPreservesMSC1767Text(t *testing.T) {
	raw := map[string]any{
		"org.matrix.msc3381.poll.start": map[string]any{
			"kind":           "org.matrix.msc3381.poll.undisclosed",
			"max_selections": float64(1),
			"question": map[string]any{
				"body": "Frage?",
			},
			"answers": []any{
				map[string]any{"id": "a", "body": "Antwort A"},
				map[string]any{"id": "b", "org.matrix.msc1767.text": "Antwort B"},
			},
		},
	}

	normalizePollStartRaw(raw)

	start := raw["org.matrix.msc3381.poll.start"].(map[string]any)
	question := start["question"].(map[string]any)
	if got := question["org.matrix.msc1767.text"]; got != "Frage?" {
		t.Fatalf("expected normalized question text, got %#v", got)
	}
	answers := start["answers"].([]any)
	first := answers[0].(map[string]any)
	if got := first["org.matrix.msc1767.text"]; got != "Antwort A" {
		t.Fatalf("expected normalized first answer text, got %#v", got)
	}
	if body := rawEventFallbackBody(event.EventUnstablePollStart, raw); body != "[Poll] Frage?" {
		t.Fatalf("expected poll fallback body, got %q", body)
	}
}

func TestRewriteContentRelationsForLocalMatrixUsesRemoteIDs(t *testing.T) {
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    "reply",
		RelatesTo: &event.RelatesTo{
			InReplyTo: &event.InReplyTo{EventID: id.EventID("$beeper-reply:beeper.local")},
			Type:      event.RelThread,
			EventID:   id.EventID("$beeper-thread:beeper.local"),
		},
	}
	replyTo := &database.Message{ID: networkid.MessageID("$remote-reply:matrix.example.com")}
	threadRoot := &database.Message{ID: networkid.MessageID("$remote-thread:matrix.example.com")}

	rewriteContentRelationsForLocalMatrix(content, replyTo, threadRoot)

	if got := content.RelatesTo.InReplyTo.EventID; got != id.EventID(replyTo.ID) {
		t.Fatalf("expected reply target %s, got %s", replyTo.ID, got)
	}
	if got := content.RelatesTo.EventID; got != id.EventID(threadRoot.ID) {
		t.Fatalf("expected thread root %s, got %s", threadRoot.ID, got)
	}
}

func TestInsecureLocalTLSRequiresExplicitOptIn(t *testing.T) {
	t.Setenv("LOCAL_MATRIX_INSECURE_TLS", "")
	if insecureLocalTLS() {
		t.Fatal("expected TLS verification to be enabled by default")
	}
	t.Setenv("LOCAL_MATRIX_INSECURE_TLS", "1")
	if !insecureLocalTLS() {
		t.Fatal("expected explicit opt-in to allow insecure TLS")
	}
}

func TestRemoteRelationTargetsForBeeper(t *testing.T) {
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    "reply",
		RelatesTo: &event.RelatesTo{
			InReplyTo: &event.InReplyTo{EventID: id.EventID("$remote-reply:matrix.example.com")},
			Type:      event.RelThread,
			EventID:   id.EventID("$remote-thread:matrix.example.com"),
		},
	}

	replyTo, threadRoot := remoteRelationTargets(content)
	removeRemoteRelationTargets(content)

	if replyTo == nil || replyTo.MessageID != networkid.MessageID("$remote-reply:matrix.example.com") {
		t.Fatalf("expected remote reply target, got %#v", replyTo)
	}
	if threadRoot == nil || *threadRoot != networkid.MessageID("$remote-thread:matrix.example.com") {
		t.Fatalf("expected remote thread root, got %#v", threadRoot)
	}
	if content.RelatesTo != nil {
		t.Fatalf("expected raw remote relates_to to be removed, got %#v", content.RelatesTo)
	}
}

func containsEventType(types []event.Type, needle event.Type) bool {
	for _, item := range types {
		if item == needle {
			return true
		}
	}
	return false
}
