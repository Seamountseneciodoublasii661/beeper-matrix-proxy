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
	hs := os.Getenv("LOCAL_SYNAPSE_E2E_HS")
	userID := os.Getenv("LOCAL_SYNAPSE_E2E_USER_ID")
	token := os.Getenv("LOCAL_SYNAPSE_E2E_ACCESS_TOKEN")
	if hs == "" || userID == "" || token == "" {
		t.Skip("set LOCAL_SYNAPSE_E2E_HS, LOCAL_SYNAPSE_E2E_USER_ID and LOCAL_SYNAPSE_E2E_ACCESS_TOKEN to run the Synapse E2E test")
	}
	count := envInt("LOCAL_SYNAPSE_E2E_BURST", 40)
	if count <= 0 {
		count = 40
	}
	if limit := localMatrixSyncTimelineLimit(); count > limit {
		t.Fatalf("burst count %d is larger than localMatrixSyncFilter timeline limit %d", count, limit)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client := synapseE2EClient{
		baseURL:     hs,
		userID:      userID,
		accessToken: token,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
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

func (c synapseE2EClient) sendText(ctx context.Context, t *testing.T, roomID id.RoomID, body string, index int) {
	t.Helper()
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/send/m.room.message/perf-%d-%d", url.PathEscape(string(roomID)), time.Now().UnixNano(), index)
	c.doJSON(ctx, t, http.MethodPut, path, map[string]any{
		"msgtype": "m.text",
		"body":    body,
	}, nil)
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
						Body string `json:"body"`
					} `json:"content"`
				} `json:"events"`
			} `json:"timeline"`
		} `json:"join"`
	} `json:"rooms"`
}
