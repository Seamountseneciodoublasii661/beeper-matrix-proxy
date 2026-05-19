package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var (
	_ bridgev2.EditHandlingNetworkAPI      = (*MyNetworkClient)(nil)
	_ bridgev2.PollHandlingNetworkAPI      = (*MyNetworkClient)(nil)
	_ bridgev2.ReactionHandlingNetworkAPI  = (*MyNetworkClient)(nil)
	_ bridgev2.RedactionHandlingNetworkAPI = (*MyNetworkClient)(nil)
)

func (nc *MyNetworkClient) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	if nc.mx == nil {
		return fmt.Errorf("remote Matrix client is not connected")
	}
	content := cloneMessageContent(msg.Content)
	if content == nil {
		content = messageContentFromEventContent(msg.Event.Content)
	}
	if msg.EditTarget != nil {
		content.SetEdit(id.EventID(msg.EditTarget.ID))
	}
	if err := nc.reuploadContentToLocalMatrix(ctx, content); err != nil {
		return err
	}
	resp, err := nc.mx.SendMessageEvent(ctx, id.RoomID(msg.Portal.ID), event.EventMessage, content)
	if err != nil {
		return err
	}
	nc.markSentEvent(resp.EventID)
	return nil
}

func (nc *MyNetworkClient) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	if nc.mx == nil {
		return fmt.Errorf("remote Matrix client is not connected")
	}
	if msg.TargetMessage == nil || msg.TargetMessage.ID == "" {
		return fmt.Errorf("redaction target missing")
	}
	resp, err := nc.mx.RedactEvent(ctx, id.RoomID(msg.Portal.ID), id.EventID(msg.TargetMessage.ID))
	if err != nil {
		return err
	}
	nc.markSentEvent(resp.EventID)
	return nil
}

func (nc *MyNetworkClient) HandleMatrixPollStart(ctx context.Context, msg *bridgev2.MatrixPollStart) (*bridgev2.MatrixMessageResponse, error) {
	if nc.mx == nil {
		return nil, fmt.Errorf("remote Matrix client is not connected")
	}
	content := clonePollStartContent(msg.Content)
	resp, err := nc.mx.SendMessageEvent(ctx, id.RoomID(msg.Portal.ID), event.EventUnstablePollStart, content)
	if err != nil {
		return nil, err
	}
	nc.markSentEvent(resp.EventID)
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:       networkid.MessageID(resp.EventID),
			PartID:   networkid.PartID(""),
			SenderID: networkid.UserID(nc.mx.UserID),
		},
	}, nil
}

func (nc *MyNetworkClient) HandleMatrixPollVote(ctx context.Context, msg *bridgev2.MatrixPollVote) (*bridgev2.MatrixMessageResponse, error) {
	if nc.mx == nil {
		return nil, fmt.Errorf("remote Matrix client is not connected")
	}
	content := clonePollResponseContent(msg.Content)
	if msg.VoteTo != nil && content.RelatesTo.GetReferenceID() == "" {
		content.RelatesTo.Type = event.RelReference
		content.RelatesTo.EventID = id.EventID(msg.VoteTo.ID)
	}
	resp, err := nc.mx.SendMessageEvent(ctx, id.RoomID(msg.Portal.ID), event.EventUnstablePollResponse, content)
	if err != nil {
		return nil, err
	}
	nc.markSentEvent(resp.EventID)
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        networkid.MessageID(resp.EventID),
			PartID:    networkid.PartID(""),
			SenderID:  networkid.UserID(nc.mx.UserID),
			Timestamp: time.Now(),
		},
	}, nil
}

func (nc *MyNetworkClient) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	relatesTo := msg.Content.GetRelatesTo()
	emoji := relatesTo.GetAnnotationKey()
	return bridgev2.MatrixReactionPreResponse{
		SenderID: networkid.UserID(nc.mx.UserID),
		EmojiID:  networkid.EmojiID(emoji),
		Emoji:    emoji,
	}, nil
}

func (nc *MyNetworkClient) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	if nc.mx == nil {
		return nil, fmt.Errorf("remote Matrix client is not connected")
	}
	if msg.TargetMessage == nil || msg.TargetMessage.ID == "" {
		return nil, fmt.Errorf("reaction target missing")
	}
	emoji := msg.Content.GetRelatesTo().GetAnnotationKey()
	resp, err := nc.mx.SendReaction(ctx, id.RoomID(msg.Portal.ID), id.EventID(msg.TargetMessage.ID), emoji)
	if err != nil {
		return nil, err
	}
	nc.markSentEvent(resp.EventID)
	return &database.Reaction{
		Room:      msg.Portal.PortalKey,
		MessageID: msg.TargetMessage.ID,
		SenderID:  networkid.UserID(nc.mx.UserID),
		EmojiID:   networkid.EmojiID(emoji),
		Emoji:     emoji,
		Timestamp: time.Now(),
		Metadata: &ReactionMetadata{
			RemoteEventID: string(resp.EventID),
		},
	}, nil
}

func (nc *MyNetworkClient) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	if nc.mx == nil {
		return fmt.Errorf("remote Matrix client is not connected")
	}
	if msg.TargetReaction == nil {
		return fmt.Errorf("reaction target missing")
	}
	var remoteEventID id.EventID
	if meta, ok := msg.TargetReaction.Metadata.(*ReactionMetadata); ok {
		remoteEventID = id.EventID(meta.RemoteEventID)
	}
	if remoteEventID == "" {
		nc.log.Warn().Msg("Reaction remove has no stored remote Matrix event ID")
		return nil
	}
	resp, err := nc.mx.RedactEvent(ctx, id.RoomID(msg.Portal.ID), remoteEventID)
	if err != nil {
		return err
	}
	nc.markSentEvent(resp.EventID)
	return nil
}

func clonePollStartContent(content *event.PollStartEventContent) *event.PollStartEventContent {
	if content == nil {
		return &event.PollStartEventContent{}
	}
	var dup event.PollStartEventContent
	if raw, err := json.Marshal(content); err == nil && json.Unmarshal(raw, &dup) == nil {
		return &dup
	}
	shallow := *content
	return &shallow
}

func clonePollResponseContent(content *event.PollResponseEventContent) *event.PollResponseEventContent {
	if content == nil {
		return &event.PollResponseEventContent{}
	}
	var dup event.PollResponseEventContent
	if raw, err := json.Marshal(content); err == nil && json.Unmarshal(raw, &dup) == nil {
		return &dup
	}
	shallow := *content
	return &shallow
}
