package connector

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// BackfillingNetworkAPI is responsible for loading historic messages
var _ bridgev2.BackfillingNetworkAPI = (*MyNetworkClient)(nil)

// FetchMessages implements [bridgev2.BackfillingNetworkAPI].
// This wil get called when the user opens a room and wants to load historical messages
func (nc *MyNetworkClient) FetchMessages(ctx context.Context, fetchParams bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	if nc.mx == nil {
		return nil, fmt.Errorf("VCVM Matrix client is not connected")
	}
	roomID := id.RoomID(fetchParams.Portal.ID)
	limit := fetchParams.Count
	if limit <= 0 {
		limit = 50
	}
	from := string(fetchParams.Cursor)
	if from == "" && fetchParams.AnchorMessage != nil {
		ctxResp, err := nc.mx.Context(ctx, roomID, id.EventID(fetchParams.AnchorMessage.ID), nil, 0)
		if err != nil {
			nc.log.Warn().Err(err).Str("room_id", string(roomID)).Str("anchor", string(fetchParams.AnchorMessage.ID)).Msg("Failed to get Matrix pagination token from anchor")
			return &bridgev2.FetchMessagesResponse{
				Messages:                nil,
				Cursor:                  "",
				HasMore:                 false,
				Forward:                 fetchParams.Forward,
				MarkRead:                !fetchParams.Forward,
				AggressiveDeduplication: true,
			}, nil
		} else if fetchParams.Forward {
			from = ctxResp.End
		} else {
			from = ctxResp.Start
		}
	}
	direction := mautrix.DirectionBackward
	if fetchParams.Forward && from != "" {
		direction = mautrix.DirectionForward
	}
	resp, err := nc.mx.Messages(ctx, roomID, from, "", direction, nil, limit)
	if err != nil {
		return nil, err
	}
	messages := make([]*bridgev2.BackfillMessage, 0, len(resp.Chunk))
	for _, evt := range resp.Chunk {
		msg := nc.backfillMessageFromEvent(ctx, evt)
		if msg != nil {
			messages = append(messages, msg)
		}
	}
	if direction == mautrix.DirectionBackward {
		for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
			messages[i], messages[j] = messages[j], messages[i]
		}
	}
	return &bridgev2.FetchMessagesResponse{
		Messages:                messages,
		Cursor:                  networkid.PaginationCursor(resp.End),
		HasMore:                 resp.End != "",
		Forward:                 fetchParams.Forward,
		MarkRead:                !fetchParams.Forward,
		AggressiveDeduplication: true,
	}, nil
}

func (nc *MyNetworkClient) backfillMessageFromEvent(ctx context.Context, evt *event.Event) *bridgev2.BackfillMessage {
	if evt == nil || evt.RoomID == "" || evt.ID == "" {
		return nil
	}
	if evt.Type != event.EventMessage && evt.Type != event.EventSticker {
		return nil
	}
	content := cloneMessageContent(messageContentFromEventContent(evt.Content))
	if content == nil {
		return nil
	}
	if nc.bridge != nil && nc.bridge.Bot != nil {
		if err := nc.reuploadContentToBeeper(ctx, nc.bridge.Bot, content); err != nil {
			nc.log.Warn().
				Err(err).
				Str("room_id", string(evt.RoomID)).
				Str("event_id", string(evt.ID)).
				Str("msgtype", string(content.MsgType)).
				Msg("Failed to reupload backfill media to Beeper")
		}
	}
	return &bridgev2.BackfillMessage{
		ConvertedMessage: &bridgev2.ConvertedMessage{
			Parts: []*bridgev2.ConvertedMessagePart{{
				Type:    evt.Type,
				Content: content,
			}},
		},
		Sender: bridgev2.EventSender{
			Sender:      networkid.UserID(evt.Sender),
			SenderLogin: nc.login.ID,
			IsFromMe:    evt.Sender == nc.mx.UserID,
		},
		ID:        networkid.MessageID(evt.ID),
		Timestamp: eventTimestamp(evt),
	}
}
