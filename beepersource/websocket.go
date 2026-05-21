package beepersource

type SubscriptionCommand struct {
	Type      string   `json:"type"`
	RequestID string   `json:"requestID,omitempty"`
	ChatIDs   []string `json:"chatIDs"`
}

func SubscribeAllChatsCommand(requestID string) SubscriptionCommand {
	return SubscriptionCommand{
		Type:      "subscriptions.set",
		RequestID: requestID,
		ChatIDs:   []string{"*"},
	}
}

func PauseSubscriptionsCommand(requestID string) SubscriptionCommand {
	return SubscriptionCommand{
		Type:      "subscriptions.set",
		RequestID: requestID,
		ChatIDs:   []string{},
	}
}
