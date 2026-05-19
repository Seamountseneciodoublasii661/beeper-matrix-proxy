package connector

import (
	"testing"

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
	replyTo := &database.Message{ID: networkid.MessageID("$remote-reply:100.120.120.120")}
	threadRoot := &database.Message{ID: networkid.MessageID("$remote-thread:100.120.120.120")}

	rewriteContentRelationsForLocalMatrix(content, replyTo, threadRoot)

	if got := content.RelatesTo.InReplyTo.EventID; got != id.EventID(replyTo.ID) {
		t.Fatalf("expected reply target %s, got %s", replyTo.ID, got)
	}
	if got := content.RelatesTo.EventID; got != id.EventID(threadRoot.ID) {
		t.Fatalf("expected thread root %s, got %s", threadRoot.ID, got)
	}
}
