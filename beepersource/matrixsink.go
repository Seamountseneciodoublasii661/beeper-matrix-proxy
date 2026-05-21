package beepersource

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type MatrixClientSink struct {
	cfg         Config
	store       *Store
	client      *mautrix.Client
	accessToken string
}

type MatrixRateLimitError struct {
	RetryAfter time.Duration
	StatusCode int
	ErrCode    string
	Message    string
}

func (e *MatrixRateLimitError) Error() string {
	if e == nil {
		return ""
	}
	detail := strings.TrimSpace(e.Message)
	if detail == "" {
		detail = "Matrix rate limit exceeded"
	}
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%s (retry after %s)", detail, e.RetryAfter)
	}
	return detail
}

func NewMatrixClientSink(cfg Config, store *Store, accessToken string) (*MatrixClientSink, error) {
	cli, err := mautrix.NewClient(cfg.Matrix.HomeserverURL, id.UserID(cfg.Matrix.UserID), accessToken)
	if err != nil {
		return nil, err
	}
	cli.DefaultHTTPRetries = 2
	cli.DefaultHTTPBackoff = 500 * time.Millisecond
	if cfg.Matrix.InsecureSkipTLS {
		cli.Client = &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // Local VCVM self-signed cert opt-in.
		}
	} else {
		cli.Client = &http.Client{Timeout: 30 * time.Second}
	}
	return &MatrixClientSink{cfg: cfg, store: store, client: cli, accessToken: accessToken}, nil
}

func (m *MatrixClientSink) EnsurePortal(ctx context.Context, chat Chat, avatar *MatrixMedia) (string, error) {
	if roomID, ok, err := m.store.PortalRoomID(ctx, chat.ID); err != nil {
		return "", err
	} else if ok {
		if err := m.updateRoomMetadata(ctx, roomID, chat); err != nil {
			return "", err
		}
		if err := m.updateRoomAvatar(ctx, roomID, avatar); err != nil {
			return "", err
		}
		return roomID, nil
	}

	name, topic, profileValue := portalProfileSyncValue(m.cfg, chat)
	req := &mautrix.ReqCreateRoom{
		Name:     name,
		Topic:    topic,
		Preset:   "private_chat",
		IsDirect: !chat.IsGroup,
	}
	if m.cfg.Matrix.InviteUserID != "" {
		req.Invite = []id.UserID{id.UserID(m.cfg.Matrix.InviteUserID)}
	}
	if avatar != nil {
		avatarURL, info, err := m.uploadAvatar(ctx, avatar)
		if err != nil {
			return "", err
		}
		if avatarURL != "" {
			req.InitialState = append(req.InitialState, &event.Event{
				Type: event.StateRoomAvatar,
				Content: event.Content{Parsed: &event.RoomAvatarEventContent{
					URL:  avatarURL,
					Info: info,
				}},
			})
		}
	} else if avatarURL, info, err := m.uploadLocalAvatar(ctx, chat.AvatarURL); err == nil && avatarURL != "" {
		req.InitialState = append(req.InitialState, &event.Event{
			Type: event.StateRoomAvatar,
			Content: event.Content{Parsed: &event.RoomAvatarEventContent{
				URL:  avatarURL,
				Info: info,
			}},
		})
	}
	resp, err := m.createRoom(ctx, req)
	if err != nil {
		return "", err
	}
	if err := m.store.SetValue(ctx, portalProfileSyncKey(chat.ID), profileValue); err != nil {
		return "", err
	}
	return resp.RoomID.String(), nil
}

func (m *MatrixClientSink) createRoom(ctx context.Context, req *mautrix.ReqCreateRoom) (*mautrix.RespCreateRoom, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(m.cfg.Matrix.HomeserverURL, "/") + "/_matrix/client/v3/createRoom"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+m.accessToken)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if rateErr := parseMatrixRateLimit(resp.StatusCode, resp.Header, body); rateErr != nil {
			return nil, rateErr
		}
		return nil, fmt.Errorf("create room failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out struct {
		RoomID string `json:"room_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.RoomID == "" {
		return nil, fmt.Errorf("create room failed: missing room_id")
	}
	return &mautrix.RespCreateRoom{RoomID: id.RoomID(out.RoomID)}, nil
}

func (m *MatrixClientSink) PortalAccessible(ctx context.Context, roomID string) (bool, error) {
	url := strings.TrimRight(m.cfg.Matrix.HomeserverURL, "/") +
		"/_matrix/client/v3/rooms/" + url.PathEscape(roomID) + "/state/m.room.create/"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+m.accessToken)
	resp, err := m.client.Client.Do(httpReq)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusForbidden, http.StatusNotFound:
		return false, nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if rateErr := parseMatrixRateLimit(resp.StatusCode, resp.Header, body); rateErr != nil {
		return false, rateErr
	}
	return false, fmt.Errorf("check room state failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
}

func parseMatrixRateLimit(statusCode int, headers http.Header, body []byte) *MatrixRateLimitError {
	var matrixErr struct {
		ErrCode      string `json:"errcode"`
		Message      string `json:"error"`
		RetryAfterMS int64  `json:"retry_after_ms"`
	}
	_ = json.Unmarshal(body, &matrixErr)
	if statusCode != http.StatusTooManyRequests && matrixErr.ErrCode != "M_LIMIT_EXCEEDED" {
		return nil
	}
	retryAfter := time.Duration(matrixErr.RetryAfterMS) * time.Millisecond
	if retryAfter <= 0 {
		retryAfter = retryAfterHeader(headers.Get("Retry-After"))
	}
	return &MatrixRateLimitError{
		RetryAfter: retryAfter,
		StatusCode: statusCode,
		ErrCode:    matrixErr.ErrCode,
		Message:    matrixErr.Message,
	}
}

func retryAfterHeader(raw string) time.Duration {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		if delay := time.Until(retryAt); delay > 0 {
			return delay
		}
	}
	return 0
}

func (m *MatrixClientSink) updateRoomMetadata(ctx context.Context, roomID string, chat Chat) error {
	key := portalProfileSyncKey(chat.ID)
	name, topic, value := portalProfileSyncValue(m.cfg, chat)
	last, err := m.store.GetValue(ctx, key)
	if err != nil || last == value {
		return err
	}
	if _, err = m.client.SendStateEvent(ctx, id.RoomID(roomID), event.StateRoomName, "", &event.RoomNameEventContent{Name: name}); err != nil {
		return err
	}
	if _, err = m.client.SendStateEvent(ctx, id.RoomID(roomID), event.StateTopic, "", &event.TopicEventContent{Topic: topic}); err != nil {
		return err
	}
	return m.store.SetValue(ctx, key, value)
}

func portalProfileSyncKey(chatID string) string {
	return "portal_profile:" + chatID
}

func portalProfileSyncValue(cfg Config, chat Chat) (name string, topic string, value string) {
	name = strings.TrimSpace(cfg.Matrix.RoomNamePrefix + roomDisplayName(chat))
	topic = fmt.Sprintf("Beeper source chat %s from %s (%s)", chat.ID, PlatformDisplayName(chat), chat.AccountID)
	return name, topic, name + "\x00" + topic
}

func (m *MatrixClientSink) updateRoomAvatar(ctx context.Context, roomID string, avatar *MatrixMedia) error {
	avatarURL, info, err := m.uploadAvatar(ctx, avatar)
	if err != nil || avatarURL == "" {
		return err
	}
	_, err = m.client.SendStateEvent(ctx, id.RoomID(roomID), event.StateRoomAvatar, "", &event.RoomAvatarEventContent{
		URL:  avatarURL,
		Info: info,
	})
	return err
}

func (m *MatrixClientSink) uploadAvatar(ctx context.Context, avatar *MatrixMedia) (id.ContentURIString, *event.FileInfo, error) {
	if avatar == nil || avatar.Content == nil {
		return "", nil, nil
	}
	if avatar.AssetID != "" {
		cached, ok, err := m.store.MediaByAssetID(ctx, avatar.AssetID)
		if err != nil {
			return "", nil, err
		}
		if ok {
			return id.ContentURIString(cached.CachedMXC), &event.FileInfo{
				MimeType: cached.MimeType,
				Size:     int(cached.SizeBytes),
			}, nil
		}
	}
	upload, err := m.client.UploadMedia(ctx, mautrix.ReqUploadMedia{
		Content:       avatar.Content,
		ContentLength: avatar.SizeBytes,
		ContentType:   avatar.MimeType,
		FileName:      avatar.FileName,
	})
	if err != nil {
		return "", nil, err
	}
	if err := m.store.UpsertMediaCache(ctx, *avatar, string(upload.ContentURI.CUString())); err != nil {
		return "", nil, err
	}
	return upload.ContentURI.CUString(), &event.FileInfo{
		MimeType: avatar.MimeType,
		Size:     int(avatar.SizeBytes),
	}, nil
}

func (m *MatrixClientSink) EnsurePuppet(ctx context.Context, sender Sender) (string, error) {
	if sender.ID == "" {
		return "", nil
	}
	matrixUserID := "@" + MatrixGhostLocalpart(sender.ID) + ":beeper-source.local"
	if err := m.store.UpsertPuppet(ctx, sender, matrixUserID); err != nil {
		return "", err
	}
	return matrixUserID, nil
}

func (m *MatrixClientSink) SendMessage(ctx context.Context, outbound MatrixOutbound) (string, error) {
	content := event.MessageEventContent{
		MsgType: event.MessageType(outbound.MsgType),
		Body:    outbound.Body,
		BeeperPerMessageProfile: &event.BeeperPerMessageProfile{
			ID:          outbound.SenderID,
			Displayname: outbound.SenderName,
			HasFallback: true,
		},
	}
	if outbound.Media != nil {
		upload, err := m.client.UploadMedia(ctx, mautrix.ReqUploadMedia{
			Content:       outbound.Media.Content,
			ContentLength: outbound.Media.SizeBytes,
			ContentType:   outbound.Media.MimeType,
			FileName:      outbound.Media.FileName,
		})
		if err != nil {
			return "", err
		}
		content.URL = upload.ContentURI.CUString()
		content.FileName = outbound.Media.FileName
		content.Info = &event.FileInfo{
			MimeType:   outbound.Media.MimeType,
			Width:      outbound.Media.Width,
			Height:     outbound.Media.Height,
			Duration:   outbound.Media.DurationMS,
			Size:       int(outbound.Media.SizeBytes),
			MauGIF:     outbound.Media.IsGIF,
			IsAnimated: outbound.Media.IsGIF,
		}
		if outbound.Media.IsVoiceNote {
			content.MSC3245Voice = &event.MSC3245Voice{}
			content.MSC1767Audio = &event.MSC1767Audio{Duration: outbound.Media.DurationMS}
		}
	}
	if outbound.ReplyToEvent != "" {
		content.GetRelatesTo().SetReplyTo(id.EventID(outbound.ReplyToEvent))
	}
	if m.cfg.Matrix.PrefixSender && outbound.SenderName != "" {
		content.Body = outbound.SenderName + ": " + content.Body
	}
	if outbound.HTML != "" {
		content.Format = event.FormatHTML
		content.FormattedBody = outbound.HTML
		if m.cfg.Matrix.PrefixSender && outbound.SenderName != "" {
			content.FormattedBody = "<strong>" + html.EscapeString(outbound.SenderName) + "</strong>: " + outbound.HTML
		}
	}
	req := mautrix.ReqSendEvent{TransactionID: outbound.TransactionID}
	if !outbound.Timestamp.IsZero() {
		req.Timestamp = outbound.Timestamp.UnixMilli()
	}
	resp, err := m.client.SendMessageEvent(ctx, id.RoomID(outbound.RoomID), event.EventMessage, &content, req)
	if err != nil {
		return "", err
	}
	return resp.EventID.String(), nil
}

func roomDisplayName(chat Chat) string {
	account := PlatformDisplayName(chat)
	name := strings.TrimSpace(chat.Name)
	if account == "" {
		return name
	}
	if name == "" {
		return "[" + account + "]"
	}
	return "[" + account + "] " + name
}

func (m *MatrixClientSink) uploadLocalAvatar(ctx context.Context, rawURL string) (id.ContentURIString, *event.FileInfo, error) {
	path := strings.TrimSpace(rawURL)
	if path == "" {
		return "", nil, nil
	}
	if strings.HasPrefix(path, "file://") {
		parsed, err := url.Parse(path)
		if err != nil {
			return "", nil, err
		}
		path = parsed.Path
	}
	if strings.Contains(path, "://") {
		return "", nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return "", nil, err
	}
	mimeType := mime.TypeByExtension(filepath.Ext(path))
	if mimeType == "" {
		head := make([]byte, 512)
		n, _ := file.Read(head)
		mimeType = http.DetectContentType(head[:n])
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			return "", nil, err
		}
	}
	upload, err := m.client.UploadMedia(ctx, mautrix.ReqUploadMedia{
		Content:       file,
		ContentLength: stat.Size(),
		ContentType:   mimeType,
		FileName:      filepath.Base(path),
	})
	if err != nil {
		return "", nil, err
	}
	return upload.ContentURI.CUString(), &event.FileInfo{
		MimeType: mimeType,
		Size:     int(stat.Size()),
	}, nil
}
