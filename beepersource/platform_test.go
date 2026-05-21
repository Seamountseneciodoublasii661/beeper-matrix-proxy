package beepersource

import "testing"

func TestPlatformDisplayNamePrefersBeeperNetwork(t *testing.T) {
	chat := Chat{AccountID: "local-whatsapp_abc", Network: "WhatsApp"}
	if got := PlatformDisplayName(chat); got != "WhatsApp" {
		t.Fatalf("unexpected platform name %q", got)
	}
}

func TestPlatformDisplayNameFallsBackFromAccountID(t *testing.T) {
	chat := Chat{AccountID: "local-whatsapp_abc"}
	if got := PlatformDisplayName(chat); got != "WhatsApp" {
		t.Fatalf("unexpected platform fallback %q", got)
	}
}

func TestPlatformAvatarMediaUsesMessengerInitials(t *testing.T) {
	avatar := platformAvatarMedia(Chat{AccountID: "whatsapp", Network: "WhatsApp"})
	if avatar == nil {
		t.Fatal("expected generated avatar")
	}
	if avatar.MimeType != "image/svg+xml" {
		t.Fatalf("unexpected mime %q", avatar.MimeType)
	}
	if avatar.FileName != "whatsapp-bridge.svg" {
		t.Fatalf("unexpected filename %q", avatar.FileName)
	}
}
