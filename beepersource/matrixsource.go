package beepersource

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const matrixSyncSinceKey = "matrix_sync_since"

type MatrixClientSource struct {
	cfg    Config
	store  *Store
	token  string
	client *http.Client
}

func NewMatrixClientSource(cfg Config, store *Store, accessToken string) *MatrixClientSource {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.Matrix.InsecureSkipTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // Local VCVM self-signed cert opt-in.
	}
	return &MatrixClientSource{
		cfg:    cfg,
		store:  store,
		token:  accessToken,
		client: &http.Client{Timeout: 35 * time.Second, Transport: transport},
	}
}

func (m *MatrixClientSource) SyncOnce(ctx context.Context, service *Service) error {
	if m.cfg.Safety.DisableMatrixToBeeper || m.cfg.Sync.Mode == SyncModeReadOnly {
		return nil
	}
	since, err := m.store.GetValue(ctx, matrixSyncSinceKey)
	if err != nil {
		return err
	}
	resp, err := m.sync(ctx, since)
	if err != nil {
		return err
	}
	for roomID, room := range resp.Rooms.Join {
		chatID, ok, err := m.store.PortalChatIDByRoomID(ctx, roomID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		for _, ev := range room.Timeline.Events {
			if ev.Type != "m.room.message" || ev.Sender == m.cfg.Matrix.UserID || ev.EventID == "" {
				continue
			}
			if ev.Content.MsgType != "m.text" && ev.Content.MsgType != "m.notice" {
				continue
			}
			body := strings.TrimSpace(ev.Content.Body)
			if body == "" {
				continue
			}
			if err := service.HandleMatrixMessage(ctx, MatrixInbound{
				ChatID:        chatID,
				MatrixEventID: ev.EventID,
				Body:          body,
				HTML:          ev.Content.FormattedBody,
			}); err != nil {
				return err
			}
		}
	}
	if resp.NextBatch != "" {
		return m.store.SetValue(ctx, matrixSyncSinceKey, resp.NextBatch)
	}
	return nil
}

func (m *MatrixClientSource) sync(ctx context.Context, since string) (*matrixSyncResponse, error) {
	base := strings.TrimRight(m.cfg.Matrix.HomeserverURL, "/")
	endpoint, err := url.Parse(base + "/_matrix/client/v3/sync")
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("timeout", "0")
	if since != "" {
		query.Set("since", since)
	}
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+m.token)
	httpResp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("Matrix sync returned HTTP %d", httpResp.StatusCode)
	}
	var resp matrixSyncResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

type matrixSyncResponse struct {
	NextBatch string `json:"next_batch"`
	Rooms     struct {
		Join map[string]struct {
			Timeline struct {
				Events []matrixSyncEvent `json:"events"`
			} `json:"timeline"`
		} `json:"join"`
	} `json:"rooms"`
}

type matrixSyncEvent struct {
	Type    string `json:"type"`
	EventID string `json:"event_id"`
	Sender  string `json:"sender"`
	Content struct {
		MsgType       string `json:"msgtype"`
		Body          string `json:"body"`
		Format        string `json:"format"`
		FormattedBody string `json:"formatted_body"`
	} `json:"content"`
}
