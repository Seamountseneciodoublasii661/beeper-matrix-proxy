package beepersource

import "strings"

func PlatformDisplayName(chat Chat) string {
	if name := strings.TrimSpace(chat.Network); name != "" {
		return name
	}
	account := strings.TrimSpace(chat.AccountID)
	if account == "" {
		return "Beeper"
	}
	base := strings.TrimPrefix(account, "local-")
	if idx := strings.IndexByte(base, '_'); idx >= 0 {
		base = base[:idx]
	}
	switch strings.ToLower(base) {
	case "whatsapp", "wa":
		return "WhatsApp"
	case "signal":
		return "Signal"
	case "telegram", "tg":
		return "Telegram"
	case "discord", "discordgo":
		return "Discord"
	case "slack":
		return "Slack"
	case "facebook", "messenger", "meta":
		return "Messenger"
	case "instagram", "ig":
		return "Instagram"
	case "imessage", "bluebubbles":
		return "iMessage"
	case "twitter", "x":
		return "X"
	case "linkedin":
		return "LinkedIn"
	case "matrix":
		return "Matrix"
	default:
		return titleAccount(base)
	}
}

func PlatformInitials(platform string) string {
	words := strings.Fields(strings.ReplaceAll(platform, "-", " "))
	if len(words) == 0 {
		return "B"
	}
	if strings.EqualFold(platform, "WhatsApp") {
		return "WA"
	}
	if strings.EqualFold(platform, "iMessage") {
		return "iM"
	}
	if len(words) == 1 {
		runes := []rune(words[0])
		if len(runes) == 1 {
			return strings.ToUpper(string(runes[0]))
		}
		return strings.ToUpper(string(runes[:min(2, len(runes))]))
	}
	return strings.ToUpper(string([]rune(words[0])[0]) + string([]rune(words[1])[0]))
}

func PlatformColor(chat Chat) string {
	switch strings.ToLower(PlatformDisplayName(chat)) {
	case "whatsapp":
		return "#25D366"
	case "signal":
		return "#3A76F0"
	case "telegram":
		return "#229ED9"
	case "discord":
		return "#5865F2"
	case "slack":
		return "#4A154B"
	case "messenger":
		return "#0084FF"
	case "instagram":
		return "#C13584"
	case "imessage":
		return "#34C759"
	case "x":
		return "#111111"
	case "linkedin":
		return "#0A66C2"
	case "matrix":
		return "#000000"
	default:
		return "#5662F6"
	}
}

func titleAccount(account string) string {
	parts := strings.FieldsFunc(account, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
	for i, part := range parts {
		if part == "" {
			continue
		}
		runes := []rune(strings.ToLower(part))
		runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
		parts[i] = string(runes)
	}
	if len(parts) == 0 {
		return "Beeper"
	}
	return strings.Join(parts, " ")
}
