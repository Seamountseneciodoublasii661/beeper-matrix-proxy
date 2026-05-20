//go:build synapse_e2e

package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/id"
)

type synapseE2EClient struct {
	baseURL     string
	userID      string
	accessToken string
	httpClient  *http.Client
}

func TestSynapseBurstSyncE2E(t *testing.T) {
	client := newSynapseE2EClient(t)
	for _, count := range envIntList("LOCAL_SYNAPSE_E2E_BURSTS", envInt("LOCAL_SYNAPSE_E2E_BURST", 40)) {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			runSynapseBurstSyncE2E(t, client, count)
		})
	}
}

func runSynapseBurstSyncE2E(t *testing.T, client synapseE2EClient, count int) {
	t.Helper()
	if count <= 0 {
		count = 40
	}
	if limit := localMatrixSyncTimelineLimit(); count > limit {
		t.Fatalf("burst count %d is larger than localMatrixSyncFilter timeline limit %d", count, limit)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	filterID := client.uploadFilter(ctx, t)
	roomID := client.createRoom(ctx, t)
	nextBatch := client.syncOnce(ctx, t, filterID, "", 0).NextBatch
	if nextBatch == "" {
		t.Fatal("initial sync did not return next_batch")
	}

	start := time.Now()
	for i := 0; i < count; i++ {
		body := fmt.Sprintf("perf-burst-%03d", i)
		client.sendText(ctx, t, roomID, body, i)
	}
	sendDuration := time.Since(start)

	syncStart := time.Now()
	got := client.syncUntilBurstMessages(ctx, t, filterID, nextBatch, roomID, count)
	syncDuration := time.Since(syncStart)
	if got != count {
		t.Fatalf("expected %d burst messages in one sync, got %d", count, got)
	}
	t.Logf("synapse burst sync delivered %d/%d messages; send_duration=%s sync_duration=%s", got, count, sendDuration, syncDuration)
}

func TestSynapseMixedModalitySyncE2E(t *testing.T) {
	client := newSynapseE2EClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	filterID := client.uploadFilter(ctx, t)
	roomID := client.createRoom(ctx, t)
	nextBatch := client.syncOnce(ctx, t, filterID, "", 0).NextBatch
	if nextBatch == "" {
		t.Fatal("initial sync did not return next_batch")
	}

	start := time.Now()
	textID := client.sendText(ctx, t, roomID, "modality-text", 0)
	client.sendMessageModality(ctx, t, roomID, "m.image", "modality-image.png", map[string]any{
		"url": "mxc://localhost/test-image",
		"info": map[string]any{
			"mimetype": "image/png",
			"w":        64,
			"h":        64,
		},
	})
	client.sendMessageModality(ctx, t, roomID, "m.file", "modality-file.txt", map[string]any{
		"url":      "mxc://localhost/test-file",
		"filename": "modality-file.txt",
		"info": map[string]any{
			"mimetype": "text/plain",
			"size":     12,
		},
	})
	client.sendMessageModality(ctx, t, roomID, "m.audio", "modality-audio.ogg", map[string]any{
		"url": "mxc://localhost/test-audio",
		"info": map[string]any{
			"mimetype": "audio/ogg",
			"duration": 1200,
		},
	})
	client.sendMessageModality(ctx, t, roomID, "m.video", "modality-video.mp4", map[string]any{
		"url": "mxc://localhost/test-video",
		"info": map[string]any{
			"mimetype": "video/mp4",
			"duration": 1200,
			"w":        64,
			"h":        64,
		},
	})
	client.sendMessageModality(ctx, t, roomID, "m.location", "modality-location", map[string]any{
		"geo_uri": "geo:48.2082,16.3738",
	})
	client.sendMessageModality(ctx, t, roomID, "m.emote", "waves", nil)
	client.sendMessageModality(ctx, t, roomID, "m.notice", "modality-notice", nil)
	stickerID := client.sendSticker(ctx, t, roomID)
	client.sendReaction(ctx, t, roomID, textID)
	client.sendEdit(ctx, t, roomID, textID)
	client.sendPollStart(ctx, t, roomID)
	client.sendCallInvite(ctx, t, roomID)
	client.sendTopic(ctx, t, roomID)
	client.redactEvent(ctx, t, roomID, stickerID)
	sendDuration := time.Since(start)

	syncStart := time.Now()
	counts, msgTypes, _ := client.syncUntilEventTypes(ctx, t, filterID, nextBatch, roomID, map[string]int{
		"m.room.message":                9,
		"m.sticker":                     1,
		"m.reaction":                    1,
		"org.matrix.msc3381.poll.start": 1,
		"m.call.invite":                 1,
		"m.room.topic":                  1,
		"m.room.redaction":              1,
	}, map[string]int{
		"m.text":     2,
		"m.image":    1,
		"m.file":     1,
		"m.audio":    1,
		"m.video":    1,
		"m.location": 1,
		"m.emote":    1,
		"m.notice":   1,
	})
	syncDuration := time.Since(syncStart)
	t.Logf("synapse mixed modality sync counts=%v msgtypes=%v send_duration=%s sync_duration=%s", counts, msgTypes, sendDuration, syncDuration)
}

func TestSynapseMultiRoomBurstE2E(t *testing.T) {
	client := newSynapseE2EClient(t)
	roomCount := envInt("LOCAL_SYNAPSE_E2E_ROOM_COUNT", 4)
	perRoom := envInt("LOCAL_SYNAPSE_E2E_ROOM_BURST", 12)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	filterID := client.uploadFilter(ctx, t)
	nextBatch := client.syncOnce(ctx, t, filterID, "", 0).NextBatch
	if nextBatch == "" {
		t.Fatal("initial sync did not return next_batch")
	}

	rooms := make([]id.RoomID, 0, roomCount)
	start := time.Now()
	for roomIndex := 0; roomIndex < roomCount; roomIndex++ {
		roomID := client.createRoom(ctx, t)
		rooms = append(rooms, roomID)
		for msgIndex := 0; msgIndex < perRoom; msgIndex++ {
			client.sendText(ctx, t, roomID, fmt.Sprintf("multi-room-%02d-%02d", roomIndex, msgIndex), msgIndex)
		}
	}
	sendDuration := time.Since(start)

	syncStart := time.Now()
	got := client.syncUntilRoomBursts(ctx, t, filterID, nextBatch, rooms, perRoom)
	syncDuration := time.Since(syncStart)
	for roomID, count := range got {
		if count != perRoom {
			t.Fatalf("expected %d messages in %s, got %d across counts=%v", perRoom, roomID, count, got)
		}
	}
	t.Logf("synapse multi-room burst sync rooms=%d per_room=%d total=%d send_duration=%s sync_duration=%s", roomCount, perRoom, roomCount*perRoom, sendDuration, syncDuration)
}

func TestSynapseDualUserRoomE2E(t *testing.T) {
	primary := newSynapseE2EClient(t)
	peer := newSynapseE2EPeerClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	filterID := primary.uploadFilter(ctx, t)
	roomID := primary.createRoom(ctx, t)
	nextBatch := primary.syncOnce(ctx, t, filterID, "", 0).NextBatch
	if nextBatch == "" {
		t.Fatal("initial sync did not return next_batch")
	}

	primary.inviteUser(ctx, t, roomID, peer.userID)
	peer.joinRoom(ctx, t, roomID)
	peer.sendText(ctx, t, roomID, "peer-message", 0)
	primary.sendText(ctx, t, roomID, "primary-message", 1)

	counts, msgTypes, senders := primary.syncUntilEventTypes(ctx, t, filterID, nextBatch, roomID, map[string]int{
		"m.room.message": 2,
	}, map[string]int{
		"m.text": 2,
	})
	if senders[primary.userID] < 1 || senders[peer.userID] < 1 {
		t.Fatalf("expected messages from primary and peer users, got senders=%v", senders)
	}
	t.Logf("synapse dual-user sync counts=%v msgtypes=%v senders=%v", counts, msgTypes, senders)
}

func TestSynapseMediaUploadDownloadE2E(t *testing.T) {
	client := newSynapseE2EClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	filterID := client.uploadFilter(ctx, t)
	roomID := client.createRoom(ctx, t)
	nextBatch := client.syncOnce(ctx, t, filterID, "", 0).NextBatch
	if nextBatch == "" {
		t.Fatal("initial sync did not return next_batch")
	}

	uploaded := client.uploadMedia(ctx, t, "e2e.txt", "text/plain", []byte("beeper-matrix-proxy-media-e2e"))
	client.sendMessageModality(ctx, t, roomID, "m.file", "e2e.txt", map[string]any{
		"url":      string(uploaded),
		"filename": "e2e.txt",
		"info": map[string]any{
			"mimetype": "text/plain",
			"size":     29,
		},
	})
	_, msgTypes, _ := client.syncUntilEventTypes(ctx, t, filterID, nextBatch, roomID, map[string]int{
		"m.room.message": 1,
	}, map[string]int{
		"m.file": 1,
	})
	downloaded := client.downloadMedia(ctx, t, uploaded)
	if string(downloaded) != "beeper-matrix-proxy-media-e2e" {
		t.Fatalf("downloaded media mismatch: %q", downloaded)
	}
	t.Logf("synapse media upload/download msgtypes=%v bytes=%d", msgTypes, len(downloaded))
}

func TestSynapseUploadLimitE2E(t *testing.T) {
	client := newSynapseE2EClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	small := bytes.Repeat([]byte("s"), 1024)
	large := bytes.Repeat([]byte("l"), 2*1024*1024)
	if status := client.uploadMediaStatus(ctx, t, "small.bin", "application/octet-stream", small); status < 200 || status >= 300 {
		t.Fatalf("expected small upload to pass, got HTTP %d", status)
	}
	status := client.uploadMediaStatus(ctx, t, "too-large.bin", "application/octet-stream", large)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected oversized upload to return HTTP 413, got HTTP %d", status)
	}
	t.Logf("synapse upload limit small_bytes=%d large_bytes=%d oversized_status=%d", len(small), len(large), status)
}

func TestSynapseRoomStateProfileE2E(t *testing.T) {
	client := newSynapseE2EClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	filterID := client.uploadFilter(ctx, t)
	roomID := client.createRoom(ctx, t)
	nextBatch := client.syncOnce(ctx, t, filterID, "", 0).NextBatch
	if nextBatch == "" {
		t.Fatal("initial sync did not return next_batch")
	}

	avatar := client.uploadMedia(ctx, t, "avatar.png", "image/png", tinyPNG())
	client.sendName(ctx, t, roomID, "state-profile-name")
	client.sendTopic(ctx, t, roomID)
	client.sendAvatar(ctx, t, roomID, avatar)
	counts, _, _ := client.syncUntilEventTypes(ctx, t, filterID, nextBatch, roomID, map[string]int{
		"m.room.name":   1,
		"m.room.topic":  1,
		"m.room.avatar": 1,
	}, nil)
	t.Logf("synapse room state profile counts=%v", counts)
}

func TestSynapseRelationsE2E(t *testing.T) {
	client := newSynapseE2EClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	filterID := client.uploadFilter(ctx, t)
	roomID := client.createRoom(ctx, t)
	nextBatch := client.syncOnce(ctx, t, filterID, "", 0).NextBatch
	if nextBatch == "" {
		t.Fatal("initial sync did not return next_batch")
	}

	root := client.sendText(ctx, t, roomID, "relation-root", 0)
	client.sendReply(ctx, t, roomID, root)
	client.sendThreadReply(ctx, t, roomID, root)
	replySeen, threadSeen := client.syncUntilRelations(ctx, t, filterID, nextBatch, roomID, root)
	if !replySeen || !threadSeen {
		t.Fatalf("expected reply and thread relations, got reply=%v thread=%v", replySeen, threadSeen)
	}
	t.Logf("synapse relations reply=%v thread=%v", replySeen, threadSeen)
}

func TestSynapsePollLifecycleE2E(t *testing.T) {
	client := newSynapseE2EClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	filterID := client.uploadFilter(ctx, t)
	roomID := client.createRoom(ctx, t)
	nextBatch := client.syncOnce(ctx, t, filterID, "", 0).NextBatch
	if nextBatch == "" {
		t.Fatal("initial sync did not return next_batch")
	}

	pollID := client.sendPollStart(ctx, t, roomID)
	client.sendPollResponse(ctx, t, roomID, pollID)
	client.sendPollEnd(ctx, t, roomID, pollID)
	counts, _, _ := client.syncUntilEventTypes(ctx, t, filterID, nextBatch, roomID, map[string]int{
		"org.matrix.msc3381.poll.start":    1,
		"org.matrix.msc3381.poll.response": 1,
		"org.matrix.msc3381.poll.end":      1,
	}, nil)
	t.Logf("synapse poll lifecycle counts=%v", counts)
}

func TestSynapseTypingReceiptE2E(t *testing.T) {
	primary := newSynapseE2EClient(t)
	peer := newSynapseE2EPeerClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	filterID := primary.uploadFilter(ctx, t)
	roomID := primary.createRoom(ctx, t)
	nextBatch := primary.syncOnce(ctx, t, filterID, "", 0).NextBatch
	if nextBatch == "" {
		t.Fatal("initial sync did not return next_batch")
	}
	primary.inviteUser(ctx, t, roomID, peer.userID)
	peer.joinRoom(ctx, t, roomID)
	eventID := peer.sendText(ctx, t, roomID, "receipt-target", 0)
	peer.sendTyping(ctx, t, roomID, true, 5000)
	peer.sendReceipt(ctx, t, roomID, eventID)

	gotTyping, gotReceipt := primary.syncUntilEphemeral(ctx, t, filterID, nextBatch, roomID)
	if !gotTyping || !gotReceipt {
		t.Fatalf("expected typing and receipt ephemeral events, got typing=%v receipt=%v", gotTyping, gotReceipt)
	}
	t.Logf("synapse ephemeral sync typing=%v receipt=%v", gotTyping, gotReceipt)
}

func TestSynapseThirtyPointE2EMatrix(t *testing.T) {
	primary := newSynapseE2EClient(t)
	peer := newSynapseE2EPeerClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	checks := newSynapseE2EChecklist(30)
	start := time.Now()
	filterID := primary.uploadFilter(ctx, t)
	checks.pass("filter upload")
	roomID := primary.createRoom(ctx, t)
	checks.pass("room create")
	nextBatch := primary.syncOnce(ctx, t, filterID, "", 0).NextBatch
	if nextBatch == "" {
		t.Fatal("initial sync did not return next_batch")
	}
	checks.pass("initial sync checkpoint")

	textID := primary.sendText(ctx, t, roomID, "thirty-text", 0)
	primary.sendFormattedText(ctx, t, roomID)
	primary.sendMessageModality(ctx, t, roomID, "m.notice", "thirty-notice", nil)
	primary.sendMessageModality(ctx, t, roomID, "m.emote", "thirty-emote", nil)
	primary.sendMessageModality(ctx, t, roomID, "m.image", "thirty-image.png", map[string]any{
		"url": "mxc://localhost/thirty-image",
		"info": map[string]any{
			"mimetype": "image/png",
			"w":        32,
			"h":        32,
		},
	})
	primary.sendMessageModality(ctx, t, roomID, "m.file", "thirty-file.txt", map[string]any{
		"url":      "mxc://localhost/thirty-file",
		"filename": "thirty-file.txt",
		"info": map[string]any{
			"mimetype": "text/plain",
			"size":     11,
		},
	})
	primary.sendVoiceMessage(ctx, t, roomID)
	primary.sendGIFVideo(ctx, t, roomID)
	primary.sendMessageModality(ctx, t, roomID, "m.location", "thirty-location", map[string]any{
		"geo_uri": "geo:48.2082,16.3738",
	})
	stickerID := primary.sendSticker(ctx, t, roomID)
	primary.sendReaction(ctx, t, roomID, textID)
	primary.sendEdit(ctx, t, roomID, textID)
	primary.redactEvent(ctx, t, roomID, stickerID)
	primary.sendCallInvite(ctx, t, roomID)
	pollID := primary.sendPollStart(ctx, t, roomID)
	primary.sendPollResponse(ctx, t, roomID, pollID)
	primary.sendPollEnd(ctx, t, roomID, pollID)
	avatar := primary.uploadMedia(ctx, t, "thirty-avatar.png", "image/png", tinyPNG())
	primary.sendName(ctx, t, roomID, "thirty-room-name")
	primary.sendTopic(ctx, t, roomID)
	primary.sendAvatar(ctx, t, roomID, avatar)
	primary.sendReply(ctx, t, roomID, textID)
	primary.sendThreadReply(ctx, t, roomID, textID)

	primary.inviteUser(ctx, t, roomID, peer.userID)
	peer.joinRoom(ctx, t, roomID)
	peerEventID := peer.sendText(ctx, t, roomID, "thirty-peer-message", 0)
	peer.sendTyping(ctx, t, roomID, true, 5000)
	peer.sendReceipt(ctx, t, roomID, peerEventID)

	uploaded := primary.uploadMedia(ctx, t, "thirty-media.txt", "text/plain", []byte("thirty-media"))
	if string(primary.downloadMedia(ctx, t, uploaded)) != "thirty-media" {
		t.Fatal("downloaded media mismatch in thirty-point matrix")
	}
	checks.pass("media upload/download")
	large := bytes.Repeat([]byte("x"), 2*1024*1024)
	if status := primary.uploadMediaStatus(ctx, t, "thirty-too-large.bin", "application/octet-stream", large); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected thirty-point oversized upload to return HTTP 413, got HTTP %d", status)
	}
	checks.pass("upload limit 413")

	counts, msgTypes, senders := primary.syncUntilEventTypes(ctx, t, filterID, nextBatch, roomID, map[string]int{
		"m.room.message":                   12,
		"m.sticker":                        1,
		"m.reaction":                       1,
		"m.room.redaction":                 1,
		"m.call.invite":                    1,
		"org.matrix.msc3381.poll.start":    1,
		"org.matrix.msc3381.poll.response": 1,
		"org.matrix.msc3381.poll.end":      1,
		"m.room.name":                      1,
		"m.room.topic":                     1,
		"m.room.avatar":                    1,
	}, map[string]int{
		"m.text":     4,
		"m.notice":   1,
		"m.emote":    1,
		"m.image":    1,
		"m.file":     1,
		"m.audio":    1,
		"m.video":    1,
		"m.location": 1,
	})
	checks.pass("text message")
	checks.pass("formatted text")
	checks.pass("notice message")
	checks.pass("emote message")
	checks.pass("image metadata")
	checks.pass("file metadata")
	checks.pass("voice payload")
	checks.pass("gif video payload")
	checks.pass("location payload")
	checks.pass("sticker event")
	checks.pass("reaction event")
	checks.pass("edit replacement")
	checks.pass("redaction event")
	checks.pass("call invite event")
	checks.pass("poll start")
	checks.pass("poll response")
	checks.pass("poll end")
	checks.pass("room name state")
	checks.pass("room topic state")
	checks.pass("room avatar state")
	if senders[peer.userID] < 1 {
		t.Fatalf("expected peer sender in thirty-point matrix, got senders=%v", senders)
	}
	checks.pass("dual-user sender")

	replySeen, threadSeen := primary.syncUntilRelations(ctx, t, filterID, nextBatch, roomID, textID)
	if !replySeen || !threadSeen {
		t.Fatalf("expected reply and thread relations in thirty-point matrix, got reply=%v thread=%v", replySeen, threadSeen)
	}
	checks.pass("reply relation")
	checks.pass("thread relation")
	gotTyping, gotReceipt := primary.syncUntilEphemeral(ctx, t, filterID, nextBatch, roomID)
	if !gotTyping || !gotReceipt {
		t.Fatalf("expected typing and receipt in thirty-point matrix, got typing=%v receipt=%v", gotTyping, gotReceipt)
	}
	checks.pass("typing event")
	checks.pass("read receipt")

	checks.assertComplete(t)
	t.Logf("synapse 30-point matrix passed=%d total=%d duration=%s counts=%v msgtypes=%v", checks.passed(), checks.total, time.Since(start), counts, msgTypes)
}

func newSynapseE2EClient(t *testing.T) synapseE2EClient {
	t.Helper()
	hs := os.Getenv("LOCAL_SYNAPSE_E2E_HS")
	userID := os.Getenv("LOCAL_SYNAPSE_E2E_USER_ID")
	token := os.Getenv("LOCAL_SYNAPSE_E2E_ACCESS_TOKEN")
	if hs == "" || userID == "" || token == "" {
		t.Skip("set LOCAL_SYNAPSE_E2E_HS, LOCAL_SYNAPSE_E2E_USER_ID and LOCAL_SYNAPSE_E2E_ACCESS_TOKEN to run the Synapse E2E test")
	}
	return synapseE2EClient{
		baseURL:     hs,
		userID:      userID,
		accessToken: token,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

func newSynapseE2EPeerClient(t *testing.T) synapseE2EClient {
	t.Helper()
	hs := os.Getenv("LOCAL_SYNAPSE_E2E_HS")
	userID := os.Getenv("LOCAL_SYNAPSE_E2E_PEER_USER_ID")
	token := os.Getenv("LOCAL_SYNAPSE_E2E_PEER_ACCESS_TOKEN")
	if hs == "" || userID == "" || token == "" {
		t.Skip("set LOCAL_SYNAPSE_E2E_PEER_USER_ID and LOCAL_SYNAPSE_E2E_PEER_ACCESS_TOKEN to run multi-user Synapse E2E tests")
	}
	return synapseE2EClient{
		baseURL:     hs,
		userID:      userID,
		accessToken: token,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (c synapseE2EClient) uploadFilter(ctx context.Context, t *testing.T) string {
	t.Helper()
	var resp struct {
		FilterID string `json:"filter_id"`
	}
	c.doJSON(ctx, t, http.MethodPost, "/_matrix/client/v3/user/"+url.PathEscape(c.userID)+"/filter", localMatrixSyncFilter(), &resp)
	if resp.FilterID == "" {
		t.Fatal("filter upload did not return filter_id")
	}
	return resp.FilterID
}

func (c synapseE2EClient) createRoom(ctx context.Context, t *testing.T) id.RoomID {
	t.Helper()
	var resp struct {
		RoomID id.RoomID `json:"room_id"`
	}
	c.doJSON(ctx, t, http.MethodPost, "/_matrix/client/v3/createRoom", map[string]any{
		"preset":     "private_chat",
		"name":       "beeper-matrix-proxy perf " + strconv.FormatInt(time.Now().UnixNano(), 10),
		"visibility": "private",
	}, &resp)
	if resp.RoomID == "" {
		t.Fatal("createRoom did not return room_id")
	}
	return resp.RoomID
}

func (c synapseE2EClient) sendText(ctx context.Context, t *testing.T, roomID id.RoomID, body string, index int) id.EventID {
	t.Helper()
	return c.sendRoomEvent(ctx, t, roomID, "m.room.message", fmt.Sprintf("perf-%d", index), map[string]any{
		"msgtype": "m.text",
		"body":    body,
	})
}

func (c synapseE2EClient) sendFormattedText(ctx context.Context, t *testing.T, roomID id.RoomID) id.EventID {
	t.Helper()
	return c.sendRoomEvent(ctx, t, roomID, "m.room.message", "perf-formatted", map[string]any{
		"msgtype":        "m.text",
		"body":           "thirty formatted text",
		"format":         "org.matrix.custom.html",
		"formatted_body": "<strong>thirty</strong> formatted text",
	})
}

func (c synapseE2EClient) sendReply(ctx context.Context, t *testing.T, roomID id.RoomID, target id.EventID) id.EventID {
	t.Helper()
	return c.sendRoomEvent(ctx, t, roomID, "m.room.message", "perf-reply", map[string]any{
		"msgtype": "m.text",
		"body":    "relation-reply",
		"m.relates_to": map[string]any{
			"m.in_reply_to": map[string]any{
				"event_id": string(target),
			},
		},
	})
}

func (c synapseE2EClient) sendThreadReply(ctx context.Context, t *testing.T, roomID id.RoomID, target id.EventID) id.EventID {
	t.Helper()
	return c.sendRoomEvent(ctx, t, roomID, "m.room.message", "perf-thread", map[string]any{
		"msgtype": "m.text",
		"body":    "relation-thread",
		"m.relates_to": map[string]any{
			"rel_type": "m.thread",
			"event_id": string(target),
			"m.in_reply_to": map[string]any{
				"event_id": string(target),
			},
		},
	})
}

func (c synapseE2EClient) sendVoiceMessage(ctx context.Context, t *testing.T, roomID id.RoomID) id.EventID {
	t.Helper()
	return c.sendMessageModality(ctx, t, roomID, "m.audio", "thirty-voice.ogg", map[string]any{
		"url": "mxc://localhost/thirty-voice",
		"info": map[string]any{
			"mimetype": "audio/ogg",
			"duration": 1500,
		},
		"org.matrix.msc3245.voice": map[string]any{},
		"org.matrix.msc1767.audio": map[string]any{
			"duration": 1500,
			"waveform": []int{0, 12, 24, 48, 24, 12, 0},
		},
	})
}

func (c synapseE2EClient) sendGIFVideo(ctx context.Context, t *testing.T, roomID id.RoomID) id.EventID {
	t.Helper()
	return c.sendMessageModality(ctx, t, roomID, "m.video", "thirty-animation.mp4", map[string]any{
		"url": "mxc://localhost/thirty-gif",
		"info": map[string]any{
			"mimetype":             "video/mp4",
			"duration":             900,
			"w":                    64,
			"h":                    64,
			"fi.mau.gif":           true,
			"fi.mau.loop":          true,
			"fi.mau.autoplay":      true,
			"fi.mau.hide_controls": true,
			"fi.mau.no_audio":      true,
		},
	})
}

func (c synapseE2EClient) sendMessageModality(ctx context.Context, t *testing.T, roomID id.RoomID, msgType, body string, extra map[string]any) id.EventID {
	t.Helper()
	content := map[string]any{
		"msgtype": msgType,
		"body":    body,
	}
	for key, value := range extra {
		content[key] = value
	}
	return c.sendRoomEvent(ctx, t, roomID, "m.room.message", "perf-"+strings.TrimPrefix(msgType, "m."), content)
}

func (c synapseE2EClient) sendSticker(ctx context.Context, t *testing.T, roomID id.RoomID) id.EventID {
	t.Helper()
	return c.sendRoomEvent(ctx, t, roomID, "m.sticker", "perf-sticker", map[string]any{
		"body": "modality-sticker",
		"url":  "mxc://localhost/test-sticker",
		"info": map[string]any{
			"mimetype": "image/png",
			"w":        64,
			"h":        64,
		},
	})
}

func (c synapseE2EClient) sendReaction(ctx context.Context, t *testing.T, roomID id.RoomID, target id.EventID) id.EventID {
	t.Helper()
	return c.sendRoomEvent(ctx, t, roomID, "m.reaction", "perf-reaction", map[string]any{
		"m.relates_to": map[string]any{
			"rel_type": "m.annotation",
			"event_id": string(target),
			"key":      "+1",
		},
	})
}

func (c synapseE2EClient) sendEdit(ctx context.Context, t *testing.T, roomID id.RoomID, target id.EventID) id.EventID {
	t.Helper()
	return c.sendRoomEvent(ctx, t, roomID, "m.room.message", "perf-edit", map[string]any{
		"msgtype": "m.text",
		"body":    "* modality-text-edited",
		"m.new_content": map[string]any{
			"msgtype": "m.text",
			"body":    "modality-text-edited",
		},
		"m.relates_to": map[string]any{
			"rel_type": "m.replace",
			"event_id": string(target),
		},
	})
}

func (c synapseE2EClient) sendPollStart(ctx context.Context, t *testing.T, roomID id.RoomID) id.EventID {
	t.Helper()
	return c.sendRoomEvent(ctx, t, roomID, "org.matrix.msc3381.poll.start", "perf-poll", map[string]any{
		"org.matrix.msc3381.poll.start": map[string]any{
			"kind":           "org.matrix.msc3381.poll.undisclosed",
			"max_selections": 1,
			"question":       map[string]any{"org.matrix.msc1767.text": "modality poll?"},
			"answers": []map[string]any{
				{"id": "yes", "org.matrix.msc1767.text": "yes"},
				{"id": "no", "org.matrix.msc1767.text": "no"},
			},
		},
	})
}

func (c synapseE2EClient) sendPollResponse(ctx context.Context, t *testing.T, roomID id.RoomID, target id.EventID) id.EventID {
	t.Helper()
	return c.sendRoomEvent(ctx, t, roomID, "org.matrix.msc3381.poll.response", "perf-poll-response", map[string]any{
		"org.matrix.msc3381.poll.response": map[string]any{
			"answers": []string{"yes"},
		},
		"m.relates_to": map[string]any{
			"rel_type": "m.reference",
			"event_id": string(target),
		},
	})
}

func (c synapseE2EClient) sendPollEnd(ctx context.Context, t *testing.T, roomID id.RoomID, target id.EventID) id.EventID {
	t.Helper()
	return c.sendRoomEvent(ctx, t, roomID, "org.matrix.msc3381.poll.end", "perf-poll-end", map[string]any{
		"org.matrix.msc3381.poll.end": map[string]any{},
		"m.relates_to": map[string]any{
			"rel_type": "m.reference",
			"event_id": string(target),
		},
	})
}

func (c synapseE2EClient) sendCallInvite(ctx context.Context, t *testing.T, roomID id.RoomID) id.EventID {
	t.Helper()
	return c.sendRoomEvent(ctx, t, roomID, "m.call.invite", "perf-call", map[string]any{
		"call_id":  "perf-call",
		"lifetime": 60000,
		"version":  1,
		"offer": map[string]any{
			"type": "offer",
			"sdp":  "v=0\r\n",
		},
	})
}

func (c synapseE2EClient) inviteUser(ctx context.Context, t *testing.T, roomID id.RoomID, userID string) {
	t.Helper()
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/invite", url.PathEscape(string(roomID)))
	c.doJSON(ctx, t, http.MethodPost, path, map[string]any{"user_id": userID}, nil)
}

func (c synapseE2EClient) joinRoom(ctx context.Context, t *testing.T, roomID id.RoomID) {
	t.Helper()
	path := fmt.Sprintf("/_matrix/client/v3/join/%s", url.PathEscape(string(roomID)))
	c.doJSON(ctx, t, http.MethodPost, path, map[string]any{}, nil)
}

func (c synapseE2EClient) sendTyping(ctx context.Context, t *testing.T, roomID id.RoomID, typing bool, timeoutMS int) {
	t.Helper()
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/typing/%s", url.PathEscape(string(roomID)), url.PathEscape(c.userID))
	c.doJSON(ctx, t, http.MethodPut, path, map[string]any{"typing": typing, "timeout": timeoutMS}, nil)
}

func (c synapseE2EClient) sendReceipt(ctx context.Context, t *testing.T, roomID id.RoomID, eventID id.EventID) {
	t.Helper()
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/receipt/m.read/%s", url.PathEscape(string(roomID)), url.PathEscape(string(eventID)))
	c.doJSON(ctx, t, http.MethodPost, path, map[string]any{}, nil)
}

func (c synapseE2EClient) uploadMedia(ctx context.Context, t *testing.T, filename, mimeType string, data []byte) id.ContentURIString {
	t.Helper()
	resp := c.uploadMediaResponse(ctx, t, filename, mimeType, data)
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("media upload returned HTTP %d", resp.StatusCode)
	}
	var out struct {
		ContentURI id.ContentURIString `json:"content_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.ContentURI == "" {
		t.Fatal("media upload did not return content_uri")
	}
	return out.ContentURI
}

func (c synapseE2EClient) uploadMediaStatus(ctx context.Context, t *testing.T, filename, mimeType string, data []byte) int {
	t.Helper()
	resp := c.uploadMediaResponse(ctx, t, filename, mimeType, data)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func (c synapseE2EClient) uploadMediaResponse(ctx context.Context, t *testing.T, filename, mimeType string, data []byte) *http.Response {
	t.Helper()
	values := url.Values{}
	values.Set("filename", filename)
	path := "/_matrix/media/v3/upload?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("Content-Type", mimeType)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (c synapseE2EClient) downloadMedia(ctx context.Context, t *testing.T, contentURI id.ContentURIString) []byte {
	t.Helper()
	parsed, err := contentURI.Parse()
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{
		fmt.Sprintf("/_matrix/client/v1/media/download/%s/%s", url.PathEscape(parsed.Homeserver), url.PathEscape(parsed.FileID)),
		fmt.Sprintf("/_matrix/media/v3/download/%s/%s", url.PathEscape(parsed.Homeserver), url.PathEscape(parsed.FileID)),
		fmt.Sprintf("/_matrix/media/r0/download/%s/%s", url.PathEscape(parsed.Homeserver), url.PathEscape(parsed.FileID)),
	}
	var lastStatus int
	for _, path := range paths {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
		resp, err := c.httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if readErr != nil {
				t.Fatal(readErr)
			}
			return data
		}
		lastStatus = resp.StatusCode
	}
	t.Fatalf("media download returned HTTP %d for all media endpoints", lastStatus)
	return nil
}

func (c synapseE2EClient) sendTopic(ctx context.Context, t *testing.T, roomID id.RoomID) {
	t.Helper()
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/state/m.room.topic/", url.PathEscape(string(roomID)))
	c.doJSON(ctx, t, http.MethodPut, path, map[string]any{"topic": "modality-topic"}, nil)
}

func (c synapseE2EClient) sendName(ctx context.Context, t *testing.T, roomID id.RoomID, name string) {
	t.Helper()
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/state/m.room.name/", url.PathEscape(string(roomID)))
	c.doJSON(ctx, t, http.MethodPut, path, map[string]any{"name": name}, nil)
}

func (c synapseE2EClient) sendAvatar(ctx context.Context, t *testing.T, roomID id.RoomID, avatar id.ContentURIString) {
	t.Helper()
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/state/m.room.avatar/", url.PathEscape(string(roomID)))
	c.doJSON(ctx, t, http.MethodPut, path, map[string]any{"url": string(avatar)}, nil)
}

func (c synapseE2EClient) redactEvent(ctx context.Context, t *testing.T, roomID id.RoomID, target id.EventID) id.EventID {
	t.Helper()
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/redact/%s/perf-redact-%d", url.PathEscape(string(roomID)), url.PathEscape(string(target)), time.Now().UnixNano())
	return c.sendEvent(ctx, t, path, map[string]any{"reason": "modality-redaction"})
}

func (c synapseE2EClient) sendRoomEvent(ctx context.Context, t *testing.T, roomID id.RoomID, eventType, txnPrefix string, content map[string]any) id.EventID {
	t.Helper()
	path := fmt.Sprintf(
		"/_matrix/client/v3/rooms/%s/send/%s/%s-%d",
		url.PathEscape(string(roomID)),
		url.PathEscape(eventType),
		txnPrefix,
		time.Now().UnixNano(),
	)
	return c.sendEvent(ctx, t, path, content)
}

func (c synapseE2EClient) sendEvent(ctx context.Context, t *testing.T, path string, content map[string]any) id.EventID {
	t.Helper()
	var resp struct {
		EventID id.EventID `json:"event_id"`
	}
	c.doJSON(ctx, t, http.MethodPut, path, content, &resp)
	if resp.EventID == "" {
		t.Fatalf("%s did not return event_id", path)
	}
	return resp.EventID
}

func (c synapseE2EClient) syncOnce(ctx context.Context, t *testing.T, filterID, since string, timeout time.Duration) synapseSyncResponse {
	t.Helper()
	values := url.Values{}
	values.Set("filter", filterID)
	values.Set("timeout", strconv.FormatInt(timeout.Milliseconds(), 10))
	if since != "" {
		values.Set("since", since)
	}
	var resp synapseSyncResponse
	c.doJSON(ctx, t, http.MethodGet, "/_matrix/client/v3/sync?"+values.Encode(), nil, &resp)
	return resp
}

func (c synapseE2EClient) syncUntilBurstMessages(ctx context.Context, t *testing.T, filterID, since string, roomID id.RoomID, want int) int {
	t.Helper()
	seen := make(map[string]struct{}, want)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp := c.syncOnce(ctx, t, filterID, since, 5*time.Second)
		if resp.NextBatch != "" {
			since = resp.NextBatch
		}
		if room := resp.Rooms.Join[roomID]; room != nil {
			for _, evt := range room.Timeline.Events {
				if evt.Type == "m.room.message" && strings.HasPrefix(evt.Content.Body, "perf-burst-") {
					seen[evt.Content.Body] = struct{}{}
				}
			}
		}
		if len(seen) >= want {
			return len(seen)
		}
	}
	return len(seen)
}

func (c synapseE2EClient) syncUntilRoomBursts(ctx context.Context, t *testing.T, filterID, since string, rooms []id.RoomID, wantPerRoom int) map[id.RoomID]int {
	t.Helper()
	seen := make(map[id.RoomID]map[string]struct{}, len(rooms))
	for _, roomID := range rooms {
		seen[roomID] = make(map[string]struct{}, wantPerRoom)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp := c.syncOnce(ctx, t, filterID, since, 5*time.Second)
		if resp.NextBatch != "" {
			since = resp.NextBatch
		}
		for _, roomID := range rooms {
			if room := resp.Rooms.Join[roomID]; room != nil {
				for _, evt := range room.Timeline.Events {
					if evt.Type == "m.room.message" && strings.HasPrefix(evt.Content.Body, "multi-room-") {
						seen[roomID][evt.Content.Body] = struct{}{}
					}
				}
			}
		}
		allSeen := true
		for _, roomID := range rooms {
			if len(seen[roomID]) < wantPerRoom {
				allSeen = false
				break
			}
		}
		if allSeen {
			break
		}
	}
	counts := make(map[id.RoomID]int, len(rooms))
	for _, roomID := range rooms {
		counts[roomID] = len(seen[roomID])
	}
	return counts
}

func (c synapseE2EClient) syncUntilEventTypes(ctx context.Context, t *testing.T, filterID, since string, roomID id.RoomID, want map[string]int, wantMsgTypes map[string]int) (map[string]int, map[string]int, map[string]int) {
	t.Helper()
	counts := make(map[string]int, len(want))
	msgTypes := make(map[string]int, len(wantMsgTypes))
	senders := make(map[string]int)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp := c.syncOnce(ctx, t, filterID, since, 5*time.Second)
		if resp.NextBatch != "" {
			since = resp.NextBatch
		}
		if room := resp.Rooms.Join[roomID]; room != nil {
			for _, evt := range room.Timeline.Events {
				counts[evt.Type]++
				if evt.Sender != "" {
					senders[evt.Sender]++
				}
				if evt.Type == "m.room.message" && evt.Content.MsgType != "" {
					msgTypes[evt.Content.MsgType]++
				}
			}
		}
		if hasEventTypeCounts(counts, want) && hasEventTypeCounts(msgTypes, wantMsgTypes) {
			return counts, msgTypes, senders
		}
	}
	for eventType, count := range want {
		if counts[eventType] < count {
			t.Fatalf("expected at least %d %s events, got %d in counts=%v", count, eventType, counts[eventType], counts)
		}
	}
	for msgType, count := range wantMsgTypes {
		if msgTypes[msgType] < count {
			t.Fatalf("expected at least %d %s msgtypes, got %d in msgtypes=%v", count, msgType, msgTypes[msgType], msgTypes)
		}
	}
	return counts, msgTypes, senders
}

func (c synapseE2EClient) syncUntilEphemeral(ctx context.Context, t *testing.T, filterID, since string, roomID id.RoomID) (bool, bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var gotTyping, gotReceipt bool
	for time.Now().Before(deadline) {
		resp := c.syncOnce(ctx, t, filterID, since, 5*time.Second)
		if resp.NextBatch != "" {
			since = resp.NextBatch
		}
		if room := resp.Rooms.Join[roomID]; room != nil {
			for _, evt := range room.Ephemeral.Events {
				switch evt.Type {
				case "m.typing":
					gotTyping = true
				case "m.receipt":
					gotReceipt = true
				}
			}
		}
		if gotTyping && gotReceipt {
			return true, true
		}
	}
	return gotTyping, gotReceipt
}

func (c synapseE2EClient) syncUntilRelations(ctx context.Context, t *testing.T, filterID, since string, roomID id.RoomID, target id.EventID) (bool, bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var replySeen, threadSeen bool
	for time.Now().Before(deadline) {
		resp := c.syncOnce(ctx, t, filterID, since, 5*time.Second)
		if resp.NextBatch != "" {
			since = resp.NextBatch
		}
		if room := resp.Rooms.Join[roomID]; room != nil {
			for _, evt := range room.Timeline.Events {
				if evt.Content.RelatesTo.InReplyTo.EventID == string(target) {
					replySeen = true
				}
				if evt.Content.RelatesTo.RelType == "m.thread" && evt.Content.RelatesTo.EventID == string(target) {
					threadSeen = true
				}
			}
		}
		if replySeen && threadSeen {
			return true, true
		}
	}
	return replySeen, threadSeen
}

func hasEventTypeCounts(counts, want map[string]int) bool {
	for eventType, count := range want {
		if counts[eventType] < count {
			return false
		}
	}
	return true
}

type synapseE2EChecklist struct {
	total int
	seen  map[string]struct{}
}

func newSynapseE2EChecklist(total int) *synapseE2EChecklist {
	return &synapseE2EChecklist{
		total: total,
		seen:  make(map[string]struct{}, total),
	}
}

func (c *synapseE2EChecklist) pass(name string) {
	c.seen[name] = struct{}{}
}

func (c *synapseE2EChecklist) passed() int {
	return len(c.seen)
}

func (c *synapseE2EChecklist) assertComplete(t *testing.T) {
	t.Helper()
	if c.passed() != c.total {
		t.Fatalf("expected %d completed E2E checks, got %d: %v", c.total, c.passed(), c.seen)
	}
}

func (c synapseE2EClient) doJSON(ctx context.Context, t *testing.T, method, path string, reqBody any, out any) {
	t.Helper()
	var body *bytes.Reader
	if reqBody == nil {
		body = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(reqBody)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("%s %s returned HTTP %d", method, path, resp.StatusCode)
	}
	if out != nil {
		if err = json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
}

type synapseSyncResponse struct {
	NextBatch string `json:"next_batch"`
	Rooms     struct {
		Join map[id.RoomID]*struct {
			Ephemeral struct {
				Events []struct {
					Type string `json:"type"`
				} `json:"events"`
			} `json:"ephemeral"`
			Timeline struct {
				Events []struct {
					Type    string `json:"type"`
					Sender  string `json:"sender"`
					Content struct {
						Body      string `json:"body"`
						MsgType   string `json:"msgtype"`
						RelatesTo struct {
							RelType   string `json:"rel_type"`
							EventID   string `json:"event_id"`
							InReplyTo struct {
								EventID string `json:"event_id"`
							} `json:"m.in_reply_to"`
						} `json:"m.relates_to"`
					} `json:"content"`
				} `json:"events"`
			} `json:"timeline"`
		} `json:"join"`
	} `json:"rooms"`
}

func envIntList(name string, fallback int) []int {
	raw := os.Getenv(name)
	if raw == "" {
		return []int{fallback}
	}
	var out []int
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value, err := strconv.Atoi(part)
		if err == nil {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return []int{fallback}
	}
	return out
}

func tinyPNG() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
}
