//go:build synapse_e2e

package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	count := envInt("LOCAL_SYNAPSE_E2E_BURST", 40)
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
	counts, msgTypes := client.syncUntilEventTypes(ctx, t, filterID, nextBatch, roomID, map[string]int{
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

func (c synapseE2EClient) sendTopic(ctx context.Context, t *testing.T, roomID id.RoomID) {
	t.Helper()
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/state/m.room.topic/", url.PathEscape(string(roomID)))
	c.doJSON(ctx, t, http.MethodPut, path, map[string]any{"topic": "modality-topic"}, nil)
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

func (c synapseE2EClient) syncUntilEventTypes(ctx context.Context, t *testing.T, filterID, since string, roomID id.RoomID, want map[string]int, wantMsgTypes map[string]int) (map[string]int, map[string]int) {
	t.Helper()
	counts := make(map[string]int, len(want))
	msgTypes := make(map[string]int, len(wantMsgTypes))
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp := c.syncOnce(ctx, t, filterID, since, 5*time.Second)
		if resp.NextBatch != "" {
			since = resp.NextBatch
		}
		if room := resp.Rooms.Join[roomID]; room != nil {
			for _, evt := range room.Timeline.Events {
				counts[evt.Type]++
				if evt.Type == "m.room.message" && evt.Content.MsgType != "" {
					msgTypes[evt.Content.MsgType]++
				}
			}
		}
		if hasEventTypeCounts(counts, want) && hasEventTypeCounts(msgTypes, wantMsgTypes) {
			return counts, msgTypes
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
	return counts, msgTypes
}

func hasEventTypeCounts(counts, want map[string]int) bool {
	for eventType, count := range want {
		if counts[eventType] < count {
			return false
		}
	}
	return true
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
			Timeline struct {
				Events []struct {
					Type    string `json:"type"`
					Content struct {
						Body    string `json:"body"`
						MsgType string `json:"msgtype"`
					} `json:"content"`
				} `json:"events"`
			} `json:"timeline"`
		} `json:"join"`
	} `json:"rooms"`
}
