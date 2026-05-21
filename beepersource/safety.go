package beepersource

import (
	"fmt"
	"strings"
	"sync"
)

type EchoSuppressor struct {
	mu    sync.Mutex
	limit int
	seen  map[string]struct{}
	order []string
}

func NewEchoSuppressor(limit int) *EchoSuppressor {
	if limit <= 0 {
		limit = 1024
	}
	return &EchoSuppressor{
		limit: limit,
		seen:  make(map[string]struct{}, limit),
	}
}

func (e *EchoSuppressor) Mark(eventID string) {
	if eventID == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.seen[eventID]; ok {
		return
	}
	e.seen[eventID] = struct{}{}
	e.order = append(e.order, eventID)
	for len(e.order) > e.limit {
		oldest := e.order[0]
		e.order = e.order[1:]
		delete(e.seen, oldest)
	}
}

func (e *EchoSuppressor) Consume(eventID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.seen[eventID]; !ok {
		return false
	}
	delete(e.seen, eventID)
	return true
}

type MediaPolicy struct {
	MaxUploadBytes int64
}

type MediaDecision struct {
	Upload       bool
	FallbackBody string
}

func (p MediaPolicy) Decide(att Attachment) MediaDecision {
	if p.MaxUploadBytes > 0 && att.SizeBytes > p.MaxUploadBytes {
		return MediaDecision{
			Upload:       false,
			FallbackBody: fmt.Sprintf("Attachment %q is too large to mirror safely (%d bytes > %d bytes).", att.FileName, att.SizeBytes, p.MaxUploadBytes),
		}
	}
	return MediaDecision{Upload: true}
}

func PlatformFromAccountID(accountID string) string {
	id := strings.ToLower(accountID)
	switch {
	case strings.Contains(id, "imessage"):
		return "iMessage"
	case strings.Contains(id, "whatsapp"):
		return "WhatsApp"
	case strings.Contains(id, "instagram"):
		return "Instagram"
	case strings.Contains(id, "telegram"):
		return "Telegram"
	case strings.Contains(id, "signal"):
		return "Signal"
	case strings.Contains(id, "twitter") || strings.Contains(id, "x-"):
		return "X"
	case strings.Contains(id, "facebook") || strings.Contains(id, "messenger"):
		return "Messenger"
	case strings.Contains(id, "discord"):
		return "Discord"
	case strings.Contains(id, "slack"):
		return "Slack"
	case strings.Contains(id, "linkedin"):
		return "LinkedIn"
	case strings.Contains(id, "gmessages") || strings.Contains(id, "googlechat"):
		return "Google"
	case strings.Contains(id, "androidsms"):
		return "SMS"
	default:
		return "Unknown"
	}
}
