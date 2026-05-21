package beepersource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrMatrixToBeeperDisabled = errors.New("matrix to beeper is disabled")

type BeeperAPI interface {
	Health(context.Context) error
	ListChats(context.Context) ([]Chat, error)
	ListMessages(ctx context.Context, chatID string, afterCursor string, limit int) ([]Message, string, error)
	DownloadAsset(ctx context.Context, assetURL string) (*AssetStream, error)
	SendMessage(ctx context.Context, outbound BeeperOutbound) (string, error)
	UpdateMessage(ctx context.Context, chatID, messageID, text string) error
	DeleteMessage(ctx context.Context, chatID, messageID string, forEveryone bool) error
	AddReaction(ctx context.Context, chatID, messageID, reactionKey, txnID string) error
	RemoveReaction(ctx context.Context, chatID, messageID, reactionKey string) error
}

type MatrixSink interface {
	EnsurePortal(context.Context, Chat, *MatrixMedia) (string, error)
	EnsurePuppet(context.Context, Sender) (string, error)
	SendMessage(context.Context, MatrixOutbound) (string, error)
}

type Service struct {
	cfg    Config
	store  *Store
	api    BeeperAPI
	matrix MatrixSink
}

func NewService(cfg Config, store *Store, api BeeperAPI, matrix MatrixSink) *Service {
	return &Service{cfg: cfg, store: store, api: api, matrix: matrix}
}

func (s *Service) ReconcileOnce(ctx context.Context) error {
	if err := s.api.Health(ctx); err != nil {
		return err
	}
	chats, err := s.api.ListChats(ctx)
	if err != nil {
		return err
	}
	for _, chat := range chats {
		if !s.cfg.AllowsBeeperChat(chat.ID) {
			continue
		}
		avatar, err := s.portalAvatar(ctx, chat)
		if err != nil {
			return fmt.Errorf("prepare Matrix portal avatar for %s: %w", chat.ID, err)
		}
		roomID, err := s.matrix.EnsurePortal(ctx, chat, avatar)
		if avatar != nil && avatar.Close != nil {
			_ = avatar.Close()
		}
		if err != nil {
			return fmt.Errorf("ensure Matrix portal for %s: %w", chat.ID, err)
		}
		if avatar != nil {
			if err := s.store.SetValue(ctx, portalAvatarSyncKey(chat.ID), chat.AvatarURL); err != nil {
				return err
			}
		}
		cursor, err := s.store.PortalCursor(ctx, chat.ID)
		if err != nil {
			return err
		}
		messages, nextCursor, err := s.api.ListMessages(ctx, chat.ID, cursor, 100)
		if err != nil {
			return fmt.Errorf("list Beeper messages for %s: %w", chat.ID, err)
		}
		for _, msg := range messages {
			if err := s.mirrorMessage(ctx, roomID, msg); err != nil {
				return err
			}
		}
		if err := s.store.UpsertPortal(ctx, chat, roomID, nextCursor); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) portalAvatar(ctx context.Context, chat Chat) (*MatrixMedia, error) {
	if chat.AvatarURL == "" {
		return nil, nil
	}
	lastAvatar, err := s.store.GetValue(ctx, portalAvatarSyncKey(chat.ID))
	if err != nil || lastAvatar == chat.AvatarURL {
		return nil, err
	}
	if avatar, ok, err := localAvatarMedia(chat.AvatarURL); ok || err != nil {
		return avatar, err
	}
	asset, err := s.api.DownloadAsset(ctx, chat.AvatarURL)
	if err != nil {
		return nil, err
	}
	fileName := firstNonEmpty(asset.FileName, "beeper-avatar")
	mimeType := firstNonEmpty(asset.MimeType, "application/octet-stream")
	return &MatrixMedia{
		Content:   asset.Content,
		Close:     asset.Content.Close,
		FileName:  fileName,
		MimeType:  mimeType,
		SizeBytes: asset.SizeBytes,
	}, nil
}

func localAvatarMedia(rawPath string) (*MatrixMedia, bool, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return nil, false, nil
	}
	if strings.HasPrefix(path, "file://") {
		parsed, err := url.Parse(path)
		if err != nil {
			return nil, true, err
		}
		path = parsed.Path
	}
	if strings.Contains(path, "://") {
		return nil, false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, true, err
	}
	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, true, err
	}
	mimeType := mime.TypeByExtension(filepath.Ext(path))
	if mimeType == "" {
		head := make([]byte, 512)
		n, _ := file.Read(head)
		mimeType = http.DetectContentType(head[:n])
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			_ = file.Close()
			return nil, true, err
		}
	}
	return &MatrixMedia{
		Content:   file,
		Close:     file.Close,
		FileName:  filepath.Base(path),
		MimeType:  mimeType,
		SizeBytes: stat.Size(),
	}, true, nil
}

func portalAvatarSyncKey(chatID string) string {
	return "portal_avatar:" + chatID
}

func (s *Service) mirrorMessage(ctx context.Context, roomID string, msg Message) error {
	existing, ok, err := s.store.MessageByBeeperID(ctx, msg.ID)
	if err != nil {
		return err
	}
	version := MessageVersion(msg)
	if ok {
		if existing.Version == version {
			return nil
		}
		existing.Version = version
		existing.DeletedAt = nil
		return s.store.UpsertMessageMapping(ctx, existing)
	}
	body := messageBody(msg)
	if matrixEventID, ok, err := s.store.ConsumeOutboundEcho(ctx, msg.ChatID, body); err != nil {
		return err
	} else if ok {
		return s.store.UpsertMessageMapping(ctx, MessageMapping{
			BeeperMessageID: msg.ID,
			MatrixEventID:   matrixEventID,
			ChatID:          msg.ChatID,
			Version:         version,
		})
	}
	senderMXID, err := s.matrix.EnsurePuppet(ctx, Sender{
		ID:          msg.SenderID,
		DisplayName: msg.SenderName,
	})
	if err != nil {
		return err
	}
	outbound := MatrixOutbound{
		RoomID:        roomID,
		MessageID:     msg.ID,
		SenderID:      msg.SenderID,
		SenderName:    msg.SenderName,
		SenderMXID:    senderMXID,
		Body:          body,
		HTML:          msg.HTML,
		MsgType:       matrixMsgType(msg),
		Timestamp:     msg.Timestamp,
		TransactionID: DeterministicTxnID(msg.ChatID, msg.ID, MutationMessage, version),
	}
	if msg.LinkedMessageID != "" {
		replyTo, ok, err := s.store.MessageByBeeperID(ctx, msg.LinkedMessageID)
		if err != nil {
			return err
		}
		if ok {
			outbound.ReplyToEvent = replyTo.MatrixEventID
		}
	}
	if media, err := s.matrixMedia(ctx, msg); err != nil {
		outbound.MsgType = "m.notice"
		outbound.Body = fmt.Sprintf("Unsupported or unavailable Beeper media: %s", err)
	} else if media != nil {
		defer media.Close()
		outbound.Media = media
	}
	eventID, err := s.matrix.SendMessage(ctx, outbound)
	if err != nil && outbound.Media != nil {
		outbound.Media = nil
		outbound.MsgType = "m.notice"
		outbound.Body = fmt.Sprintf("Beeper media could not be mirrored to Matrix: %v", err)
		eventID, err = s.matrix.SendMessage(ctx, outbound)
	}
	if err != nil {
		return err
	}
	return s.store.UpsertMessageMapping(ctx, MessageMapping{
		BeeperMessageID: msg.ID,
		MatrixEventID:   eventID,
		ChatID:          msg.ChatID,
		Version:         version,
	})
}

func (s *Service) matrixMedia(ctx context.Context, msg Message) (*MatrixMedia, error) {
	if len(msg.Attachments) == 0 {
		return nil, nil
	}
	att := msg.Attachments[0]
	if att.URL == "" {
		return nil, nil
	}
	if s.cfg.Media.MaxUploadBytes > 0 && att.SizeBytes > s.cfg.Media.MaxUploadBytes {
		return nil, fmt.Errorf("%s is %d bytes, over configured Matrix upload limit %d", att.FileName, att.SizeBytes, s.cfg.Media.MaxUploadBytes)
	}
	asset, err := s.api.DownloadAsset(ctx, att.URL)
	if err != nil {
		return nil, err
	}
	fileName := firstNonEmpty(att.FileName, asset.FileName, "beeper-asset")
	mimeType := firstNonEmpty(att.MimeType, asset.MimeType, "application/octet-stream")
	sizeBytes := att.SizeBytes
	if sizeBytes == 0 {
		sizeBytes = asset.SizeBytes
	}
	return &MatrixMedia{
		Content:     asset.Content,
		Close:       asset.Content.Close,
		FileName:    fileName,
		MimeType:    mimeType,
		SizeBytes:   sizeBytes,
		Width:       att.Width,
		Height:      att.Height,
		DurationMS:  att.DurationMS,
		IsGIF:       att.IsGIF,
		IsVoiceNote: att.IsVoiceNote,
	}, nil
}

func (s *Service) HandleMatrixMessage(ctx context.Context, inbound MatrixInbound) error {
	if s.cfg.Safety.DisableMatrixToBeeper || s.cfg.Sync.Mode == SyncModeReadOnly {
		return ErrMatrixToBeeperDisabled
	}
	version := inbound.MatrixEventID
	if version == "" {
		version = inbound.Body
	}
	txnID := DeterministicTxnID(inbound.ChatID, inbound.MatrixEventID, MutationMessage, version)
	replyToID := ""
	if inbound.ReplyToEvent != "" {
		replyTo, ok, err := s.store.MessageByMatrixEventID(ctx, inbound.ReplyToEvent)
		if err != nil {
			return err
		}
		if ok {
			replyToID = replyTo.BeeperMessageID
		}
	}
	_, err := s.api.SendMessage(ctx, BeeperOutbound{
		ChatID:      inbound.ChatID,
		Text:        inbound.Body,
		HTML:        inbound.HTML,
		ReplyToID:   replyToID,
		ClientTxnID: txnID,
		Attachment:  inbound.Attachment,
	})
	if err != nil {
		return err
	}
	return s.store.RememberOutboundEcho(ctx, inbound.ChatID, inbound.Body, inbound.MatrixEventID, 10*time.Minute)
}

func (s *Service) HandleMatrixEdit(ctx context.Context, chatID, matrixTargetEventID, text string) error {
	mapping, ok, err := s.store.MessageByMatrixEventID(ctx, matrixTargetEventID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return s.api.UpdateMessage(ctx, chatID, mapping.BeeperMessageID, text)
}

func (s *Service) HandleMatrixRedaction(ctx context.Context, chatID, matrixTargetEventID string) error {
	mapping, ok, err := s.store.MessageByMatrixEventID(ctx, matrixTargetEventID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return s.api.DeleteMessage(ctx, chatID, mapping.BeeperMessageID, true)
}

func (s *Service) HandleMatrixReaction(ctx context.Context, chatID, matrixEventID, matrixTargetEventID, reactionKey string) error {
	mapping, ok, err := s.store.MessageByMatrixEventID(ctx, matrixTargetEventID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	txnID := DeterministicTxnID(chatID, matrixEventID, MutationReaction, reactionKey)
	return s.api.AddReaction(ctx, chatID, mapping.BeeperMessageID, reactionKey, txnID)
}

func messageBody(msg Message) string {
	if msg.IsDeleted {
		return "Message deleted"
	}
	if msg.Text != "" {
		return msg.Text
	}
	if len(msg.Attachments) > 0 {
		return msg.Attachments[0].FileName
	}
	return "Unsupported Beeper message"
}

func matrixMsgType(msg Message) string {
	if msg.IsDeleted {
		return "m.notice"
	}
	switch msg.Type {
	case MessageTypeImage:
		return "m.image"
	case MessageTypeVideo:
		return "m.video"
	case MessageTypeAudio, MessageTypeVoice:
		return "m.audio"
	case MessageTypeFile:
		return "m.file"
	case MessageTypeSticker:
		return "m.sticker"
	case MessageTypeNotice:
		return "m.notice"
	default:
		return "m.text"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
