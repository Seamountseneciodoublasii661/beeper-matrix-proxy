package beepersource

import "testing"

func TestEchoSuppressorIsBoundedAndConsumesKnownEvents(t *testing.T) {
	echo := NewEchoSuppressor(2)
	echo.Mark("$old")
	echo.Mark("$middle")
	echo.Mark("$new")

	if echo.Consume("$old") {
		t.Fatal("expected oldest echo marker to be evicted")
	}
	if !echo.Consume("$middle") {
		t.Fatal("expected middle echo marker to be consumed")
	}
	if echo.Consume("$middle") {
		t.Fatal("expected consumed echo marker to be removed")
	}
	if !echo.Consume("$new") {
		t.Fatal("expected newest echo marker to be consumed")
	}
}

func TestMediaPolicyUsesFallbackForOversizedMedia(t *testing.T) {
	policy := MediaPolicy{MaxUploadBytes: 1024}
	decision := policy.Decide(Attachment{
		ID:        "asset-1",
		FileName:  "big.mov",
		MimeType:  "video/quicktime",
		SizeBytes: 2048,
	})

	if decision.Upload {
		t.Fatal("expected oversized media not to upload")
	}
	if decision.FallbackBody == "" {
		t.Fatal("expected human-readable fallback body")
	}
}

func TestPlatformFromAccountIDUsesDeeperStyleKeywords(t *testing.T) {
	cases := map[string]string{
		"local-whatsapp_ba_123": "WhatsApp",
		"local-signal_abc":      "Signal",
		"discordgo":             "Discord",
		"imessagecloud":         "iMessage",
		"local-unknown":         "Unknown",
	}
	for input, want := range cases {
		if got := PlatformFromAccountID(input); got != want {
			t.Fatalf("PlatformFromAccountID(%q)=%q, want %q", input, got, want)
		}
	}
}
