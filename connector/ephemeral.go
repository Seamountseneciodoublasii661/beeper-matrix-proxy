package connector

import (
	"context"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const remoteTypingTimeout = 30 * time.Second

var (
	_ bridgev2.TypingHandlingNetworkAPI      = (*MyNetworkClient)(nil)
	_ bridgev2.ReadReceiptHandlingNetworkAPI = (*MyNetworkClient)(nil)
)

func (nc *MyNetworkClient) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	if nc.mx == nil || msg == nil || msg.Portal == nil {
		return nil
	}
	timeout := time.Duration(0)
	if msg.IsTyping {
		timeout = remoteTypingTimeout
	}
	_, err := nc.mx.UserTyping(ctx, id.RoomID(msg.Portal.ID), msg.IsTyping, timeout)
	return err
}

func (nc *MyNetworkClient) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	if nc.mx == nil || msg == nil || msg.Portal == nil || msg.ExactMessage == nil || msg.ExactMessage.ID == "" {
		return nil
	}
	content := event.ReadReceipt{
		Timestamp: msg.ReadUpTo,
		ThreadID:  msg.Receipt.ThreadID,
	}
	return nc.mx.SendReceipt(ctx, id.RoomID(msg.Portal.ID), id.EventID(msg.ExactMessage.ID), event.ReceiptTypeRead, content)
}

func (nc *MyNetworkClient) handleLocalMatrixTyping(ctx context.Context, evt *event.Event) {
	if evt == nil || evt.RoomID == "" || nc.mx == nil {
		return
	}
	content := evt.Content.AsTyping()
	started, stopped := nc.updateRemoteTyping(evt.RoomID, content.UserIDs)
	for _, userID := range stopped {
		nc.queueRemoteTyping(ctx, evt.RoomID, userID, false)
	}
	for _, userID := range started {
		nc.queueRemoteTyping(ctx, evt.RoomID, userID, true)
	}
}

func (nc *MyNetworkClient) updateRemoteTyping(roomID id.RoomID, current []id.UserID) (started, stopped []id.UserID) {
	next := make(map[id.UserID]struct{}, len(current))
	for _, userID := range current {
		if userID == "" || (nc.mx != nil && userID == nc.mx.UserID) {
			continue
		}
		next[userID] = struct{}{}
	}
	nc.typingMu.Lock()
	defer nc.typingMu.Unlock()
	if nc.remoteTyping == nil {
		nc.remoteTyping = make(map[id.RoomID]map[id.UserID]struct{})
	}
	previous := nc.remoteTyping[roomID]
	for userID := range next {
		if _, ok := previous[userID]; !ok {
			started = append(started, userID)
		}
	}
	for userID := range previous {
		if _, ok := next[userID]; !ok {
			stopped = append(stopped, userID)
		}
	}
	if len(next) == 0 {
		delete(nc.remoteTyping, roomID)
	} else {
		nc.remoteTyping[roomID] = next
	}
	return started, stopped
}

func (nc *MyNetworkClient) queueRemoteTyping(ctx context.Context, roomID id.RoomID, userID id.UserID, typing bool) {
	timeout := time.Duration(0)
	if typing {
		timeout = remoteTypingTimeout
	}
	nc.bridge.QueueRemoteEvent(nc.login, &simplevent.Typing{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventTyping,
			PortalKey: networkid.PortalKey{ID: networkid.PortalID(roomID), Receiver: nc.login.ID},
			Sender: bridgev2.EventSender{
				Sender: networkid.UserID(userID),
			},
			CreatePortal: true,
			Timestamp:    time.Now(),
		},
		Timeout: timeout,
		Type:    bridgev2.TypingTypeText,
	})
}

func (nc *MyNetworkClient) handleLocalMatrixReceipt(ctx context.Context, evt *event.Event) {
	if evt == nil || evt.RoomID == "" || nc.mx == nil {
		return
	}
	receipts := evt.Content.AsReceipt()
	for eventID, byType := range *receipts {
		readReceipts := byType[event.ReceiptTypeRead]
		for userID, receipt := range readReceipts {
			if userID == "" || userID == nc.mx.UserID {
				continue
			}
			readUpTo := receipt.Timestamp
			if readUpTo.IsZero() {
				readUpTo = eventTimestamp(evt)
			}
			nc.bridge.QueueRemoteEvent(nc.login, &simplevent.Receipt{
				EventMeta: simplevent.EventMeta{
					Type:      bridgev2.RemoteEventReadReceipt,
					PortalKey: networkid.PortalKey{ID: networkid.PortalID(evt.RoomID), Receiver: nc.login.ID},
					Sender: bridgev2.EventSender{
						Sender: networkid.UserID(userID),
					},
					CreatePortal: true,
					Timestamp:    readUpTo,
				},
				Targets:  []networkid.MessageID{networkid.MessageID(eventID)},
				ReadUpTo: readUpTo,
			})
		}
	}
}
