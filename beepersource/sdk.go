package beepersource

import (
	"context"
	"fmt"
	"time"

	beeperdesktopapi "github.com/beeper/desktop-api-go/v5"
	"github.com/beeper/desktop-api-go/v5/option"
	"github.com/beeper/desktop-api-go/v5/shared"
)

type SDKClient struct {
	Client beeperdesktopapi.Client
}

func NewSDKClient(cfg Config, token string) *SDKClient {
	client := beeperdesktopapi.NewClient(
		option.WithBaseURL(cfg.Beeper.BaseURL),
		option.WithAccessToken(token),
		option.WithRequestTimeout(20*time.Second),
	)
	return &SDKClient{Client: client}
}

type DesktopAPIAdapter struct {
	cfg Config
	sdk *SDKClient
}

func NewDesktopAPIAdapter(cfg Config, token string) *DesktopAPIAdapter {
	return &DesktopAPIAdapter{cfg: cfg, sdk: NewSDKClient(cfg, token)}
}

func (a *DesktopAPIAdapter) Health(ctx context.Context) error {
	_, err := a.sdk.Client.Info.Get(ctx)
	return err
}

func (a *DesktopAPIAdapter) ListChats(ctx context.Context) ([]Chat, error) {
	if len(a.cfg.Beeper.ChatIDs) > 0 {
		chats := make([]Chat, 0, len(a.cfg.Beeper.ChatIDs))
		for _, chatID := range a.cfg.Beeper.ChatIDs {
			chat, err := a.sdk.Client.Chats.Get(ctx, chatID, beeperdesktopapi.ChatGetParams{})
			if err != nil {
				return nil, err
			}
			chats = append(chats, convertSDKChat(*chat))
		}
		return chats, nil
	}
	page, err := a.sdk.Client.Chats.List(ctx, beeperdesktopapi.ChatListParams{})
	if err != nil {
		return nil, err
	}
	chats := make([]Chat, 0, len(page.Items))
	for _, item := range page.Items {
		chats = append(chats, convertSDKChat(item.Chat))
	}
	return chats, nil
}

func (a *DesktopAPIAdapter) ListMessages(ctx context.Context, chatID string, afterCursor string, limit int) ([]Message, string, error) {
	params := beeperdesktopapi.MessageListParams{}
	if afterCursor != "" {
		params.Cursor = beeperdesktopapi.String(afterCursor)
		params.Direction = beeperdesktopapi.MessageListParamsDirectionAfter
	}
	page, err := a.sdk.Client.Messages.List(ctx, chatID, params)
	if err != nil {
		return nil, "", err
	}
	messages := make([]Message, 0, len(page.Items))
	for _, item := range page.Items {
		messages = append(messages, convertSDKMessage(item))
	}
	return messages, page.NewestCursor, nil
}

func (a *DesktopAPIAdapter) DownloadAsset(ctx context.Context, assetURL string) (*AssetStream, error) {
	resp, err := a.sdk.Client.Assets.Serve(ctx, beeperdesktopapi.AssetServeParams{URL: assetURL})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("Beeper asset serve returned HTTP %d", resp.StatusCode)
	}
	return &AssetStream{
		Content:    resp.Body,
		MimeType:   resp.Header.Get("Content-Type"),
		SizeBytes:  resp.ContentLength,
		StatusCode: resp.StatusCode,
	}, nil
}

func (a *DesktopAPIAdapter) SendMessage(ctx context.Context, outbound BeeperOutbound) (string, error) {
	params := beeperdesktopapi.MessageSendParams{
		Text: beeperdesktopapi.String(outbound.Text),
	}
	if outbound.ReplyToID != "" {
		params.ReplyToMessageID = beeperdesktopapi.String(outbound.ReplyToID)
	}
	resp, err := a.sdk.Client.Messages.Send(ctx, outbound.ChatID, params)
	if err != nil {
		return "", err
	}
	return resp.PendingMessageID, nil
}

func convertSDKChat(in beeperdesktopapi.Chat) Chat {
	return Chat{
		ID:        in.ID,
		AccountID: in.AccountID,
		Name:      in.Title,
		AvatarURL: in.ImgURL,
		IsGroup:   string(in.Type) == "group",
	}
}

func convertSDKMessage(in shared.Message) Message {
	msg := Message{
		ID:              in.ID,
		ChatID:          in.ChatID,
		SenderID:        in.SenderID,
		SenderName:      in.SenderName,
		Type:            string(in.Type),
		Text:            in.Text,
		Timestamp:       in.Timestamp,
		IsDeleted:       in.IsDeleted,
		LinkedMessageID: in.LinkedMessageID,
		Attachments:     make([]Attachment, 0, len(in.Attachments)),
	}
	if !in.EditedTimestamp.IsZero() {
		edited := in.EditedTimestamp
		msg.EditedTimestamp = &edited
	}
	for _, att := range in.Attachments {
		msg.Attachments = append(msg.Attachments, Attachment{
			ID:          att.ID,
			URL:         att.SrcURL,
			FileName:    att.FileName,
			MimeType:    att.MimeType,
			SizeBytes:   int64(att.FileSize),
			Width:       int(att.Size.Width),
			Height:      int(att.Size.Height),
			DurationMS:  int(att.Duration * 1000),
			IsVoiceNote: att.IsVoiceNote,
			IsGIF:       att.IsGif,
			IsSticker:   att.IsSticker,
		})
	}
	return msg
}
