package beepersource

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

type MatrixPortalAccessChecker interface {
	PortalAccessible(context.Context, string) (bool, error)
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
		if !s.cfg.AllowsBeeperChatRecord(chat) {
			continue
		}
		roomID, err := s.ensurePortal(ctx, chat)
		if err != nil {
			return err
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

func (s *Service) ReconcilePortalsOnly(ctx context.Context) error {
	if err := s.api.Health(ctx); err != nil {
		return err
	}
	chats, err := s.api.ListChats(ctx)
	if err != nil {
		return err
	}
	missing := make([]Chat, 0, len(chats))
	for _, chat := range chats {
		if !s.cfg.AllowsBeeperChatRecord(chat) {
			continue
		}
		if roomID, ok, err := s.store.PortalRoomID(ctx, chat.ID); err != nil {
			return err
		} else if ok {
			if checker, ok := s.matrix.(MatrixPortalAccessChecker); ok {
				accessible, err := checker.PortalAccessible(ctx, roomID)
				if err != nil {
					return fmt.Errorf("check Matrix portal accessibility for %s: %w", chat.ID, err)
				}
				if !accessible {
					if err := s.store.DeletePortal(ctx, chat.ID); err != nil {
						return err
					}
					missing = append(missing, chat)
				}
			}
			continue
		}
		missing = append(missing, chat)
	}
	if len(missing) == 0 {
		return nil
	}
	workers := minPositive(s.cfg.Sync.PortalWorkers, len(missing))
	backpressure := newPortalBackpressure(workers)
	jobs := make(chan Chat)
	errs := make(chan error, len(missing))
	var wg sync.WaitGroup
	timeout := time.Duration(s.cfg.Sync.PortalTimeoutSeconds) * time.Second
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for chat := range jobs {
				chatCtx, chatCancel := context.WithTimeout(ctx, timeout)
				err := s.ensurePortalWithBackoff(chatCtx, chat, backpressure)
				chatCancel()
				if err != nil {
					errs <- err
				}
			}
		}()
	}
	for _, chat := range missing {
		select {
		case jobs <- chat:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	close(errs)
	var firstErr error
	failures := 0
	for err := range errs {
		failures++
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return fmt.Errorf("%d portal room(s) failed and can be retried by rerunning rooms-only: %w", failures, firstErr)
	}
	return nil
}

func (s *Service) ensurePortalWithBackoff(ctx context.Context, chat Chat, backpressure *portalBackpressure) error {
	attempt := 0
	for {
		release, err := backpressure.beforeAttempt(ctx)
		if err != nil {
			return err
		}
		_, err = s.ensurePortal(ctx, chat)
		if err == nil {
			release(true)
			return nil
		}
		var rateErr *MatrixRateLimitError
		if !errors.As(err, &rateErr) {
			release(false)
			return err
		}
		attempt++
		retryAfter := rateErr.RetryAfter
		if retryAfter <= 0 {
			retryAfter = adaptivePortalRetryDelay(attempt)
		}
		backpressure.noteRateLimit(retryAfter)
		release(false)
	}
}

type portalBackpressure struct {
	mu       sync.Mutex
	active   int
	limit    int
	maxLimit int
	resumeAt time.Time
}

func newPortalBackpressure(workers int) *portalBackpressure {
	workers = maxPositive(workers, 1)
	return &portalBackpressure{limit: workers, maxLimit: workers}
}

func (p *portalBackpressure) beforeAttempt(ctx context.Context) (func(bool), error) {
	for {
		p.mu.Lock()
		now := time.Now()
		if p.active < p.limit && !now.Before(p.resumeAt) {
			p.active++
			p.mu.Unlock()
			return func(success bool) {
				p.mu.Lock()
				if p.active > 0 {
					p.active--
				}
				if success && p.limit < p.maxLimit {
					p.limit++
				}
				p.mu.Unlock()
			}, nil
		}
		wait := 10 * time.Millisecond
		if now.Before(p.resumeAt) {
			wait = time.Until(p.resumeAt)
		}
		p.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		}
	}
}

func (p *portalBackpressure) noteRateLimit(retryAfter time.Duration) {
	if retryAfter <= 0 {
		retryAfter = adaptivePortalRetryDelay(1)
	}
	p.mu.Lock()
	if p.limit > 1 {
		p.limit = maxPositive(1, p.limit/2)
	}
	next := time.Now().Add(retryAfter)
	if next.After(p.resumeAt) {
		p.resumeAt = next
	}
	p.mu.Unlock()
}

func adaptivePortalRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	delay := time.Duration(250*(1<<(attempt-1))) * time.Millisecond
	if delay > 5*time.Second {
		return 5 * time.Second
	}
	return delay
}

func minPositive(a, b int) int {
	if a <= 0 || (b > 0 && b < a) {
		return b
	}
	return a
}

func maxPositive(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (s *Service) ensurePortal(ctx context.Context, chat Chat) (string, error) {
	avatar, err := s.portalAvatar(ctx, chat)
	if err != nil {
		return "", fmt.Errorf("prepare Matrix portal avatar for %s: %w", chat.ID, err)
	}
	roomID, err := s.matrix.EnsurePortal(ctx, chat, avatar)
	if avatar != nil && avatar.Close != nil {
		_ = avatar.Close()
	}
	if err != nil {
		return "", fmt.Errorf("ensure Matrix portal for %s: %w", chat.ID, err)
	}
	cursor, err := s.store.PortalCursor(ctx, chat.ID)
	if err != nil {
		return "", err
	}
	if err := s.store.UpsertPortal(ctx, chat, roomID, cursor); err != nil {
		return "", err
	}
	if avatar != nil {
		if err := s.store.SetValue(ctx, portalAvatarSyncKey(chat.ID), portalAvatarSyncValue(chat)); err != nil {
			return "", err
		}
	}
	return roomID, nil
}

func (s *Service) portalAvatar(ctx context.Context, chat Chat) (*MatrixMedia, error) {
	if s.cfg.Matrix.PlatformAvatars {
		lastAvatar, err := s.store.GetValue(ctx, portalAvatarSyncKey(chat.ID))
		if err != nil || lastAvatar == platformAvatarSyncValue(chat) {
			return nil, err
		}
		return platformAvatarMedia(chat), nil
	}
	if chat.AvatarURL == "" {
		lastAvatar, err := s.store.GetValue(ctx, portalAvatarSyncKey(chat.ID))
		if err != nil || lastAvatar == platformAvatarSyncValue(chat) {
			return nil, err
		}
		return platformAvatarMedia(chat), nil
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

func platformAvatarMedia(chat Chat) *MatrixMedia {
	platform := PlatformDisplayName(chat)
	initials := PlatformInitials(platform)
	bg := PlatformColor(chat)
	fg := "#ffffff"
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="256" height="256" viewBox="0 0 256 256"><rect width="256" height="256" rx="56" fill="%s"/><text x="128" y="148" text-anchor="middle" font-family="Arial, Helvetica, sans-serif" font-size="72" font-weight="700" fill="%s">%s</text></svg>`, bg, fg, html.EscapeString(initials))
	return &MatrixMedia{
		AssetID:   platformAvatarSyncValue(chat),
		Content:   bytes.NewReader([]byte(svg)),
		FileName:  strings.ToLower(strings.ReplaceAll(platform, " ", "-")) + "-bridge.svg",
		MimeType:  "image/svg+xml",
		SizeBytes: int64(len(svg)),
	}
}

func platformAvatarSyncValue(chat Chat) string {
	return "platform:" + PlatformDisplayName(chat)
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

func portalAvatarSyncValue(chat Chat) string {
	if chat.AvatarURL != "" {
		return chat.AvatarURL
	}
	return platformAvatarSyncValue(chat)
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
