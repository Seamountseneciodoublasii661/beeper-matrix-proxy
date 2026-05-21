package beepersource

import (
	"context"
	"crypto/tls"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type MatrixClientSink struct {
	cfg    Config
	store  *Store
	client *mautrix.Client
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
	return &MatrixClientSink{cfg: cfg, store: store, client: cli}, nil
}

func (m *MatrixClientSink) EnsurePortal(ctx context.Context, chat Chat, avatar *MatrixMedia) (string, error) {
	if roomID, ok, err := m.store.PortalRoomID(ctx, chat.ID); err != nil {
		return "", err
	} else if ok {
		if err := m.updateRoomAvatar(ctx, roomID, avatar); err != nil {
			return "", err
		}
		return roomID, nil
	}

	name := strings.TrimSpace(m.cfg.Matrix.RoomNamePrefix + roomDisplayName(chat))
	if chat.Name == "" {
		name = strings.TrimSpace(m.cfg.Matrix.RoomNamePrefix + chat.ID)
	}
	req := &mautrix.ReqCreateRoom{
		Name:     name,
		Topic:    fmt.Sprintf("Beeper source chat %s from account %s", chat.ID, chat.AccountID),
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
	resp, err := m.client.CreateRoom(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.RoomID.String(), nil
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
	upload, err := m.client.UploadMedia(ctx, mautrix.ReqUploadMedia{
		Content:       avatar.Content,
		ContentLength: avatar.SizeBytes,
		ContentType:   avatar.MimeType,
		FileName:      avatar.FileName,
	})
	if err != nil {
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
	account := strings.TrimSpace(chat.AccountID)
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
