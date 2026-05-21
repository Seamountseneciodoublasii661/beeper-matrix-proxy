package beepersource

import (
	"os"
	"testing"
)

func TestDefaultConfigIsLocalBidirectionalAndSafe(t *testing.T) {
	t.Setenv("BEEPER_ACCESS_TOKEN", "token")

	cfg := DefaultConfig()

	if cfg.Beeper.BaseURL != "http://localhost:23373" {
		t.Fatalf("unexpected base URL %q", cfg.Beeper.BaseURL)
	}
	if cfg.Beeper.TokenEnv != "BEEPER_ACCESS_TOKEN" {
		t.Fatalf("unexpected token env %q", cfg.Beeper.TokenEnv)
	}
	if !cfg.Beeper.WebsocketEnabled {
		t.Fatal("expected websocket to be enabled by default")
	}
	if cfg.Sync.Mode != SyncModeBidirectional {
		t.Fatalf("expected bidirectional sync mode, got %q", cfg.Sync.Mode)
	}
	if cfg.Sync.MaxSendRPS <= 0 || cfg.Sync.MaxSendRPS > 2 {
		t.Fatalf("expected conservative send rate, got %.2f", cfg.Sync.MaxSendRPS)
	}
	if cfg.Safety.DisableMatrixToBeeper {
		t.Fatal("expected matrix->beeper to be enabled by default per requested plan")
	}
}

func TestConfigCanDisableMatrixToBeeperWithoutRedeploy(t *testing.T) {
	t.Setenv("BEEPER_MATRIX_PROXY_DISABLE_MATRIX_TO_BEEPER", "true")

	cfg := DefaultConfig()

	if !cfg.Safety.DisableMatrixToBeeper {
		t.Fatal("expected env kill switch to disable matrix->beeper")
	}
}

func TestAllowsBeeperChatUsesOptionalAllowlist(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.AllowsBeeperChat("!any:beeper") {
		t.Fatal("empty allowlist should allow all chats")
	}
	cfg.Beeper.ChatIDs = []string{"!test:beeper"}
	if !cfg.AllowsBeeperChat("!test:beeper") {
		t.Fatal("expected configured chat to be allowed")
	}
	if cfg.AllowsBeeperChat("!real-contact:beeper") {
		t.Fatal("expected unlisted chat to be blocked")
	}
}

func TestBeeperTokenLoadsFromConfiguredEnvironment(t *testing.T) {
	const tokenEnv = "BEEPER_SOURCE_TEST_TOKEN"
	t.Setenv(tokenEnv, "secret-token")
	cfg := DefaultConfig()
	cfg.Beeper.TokenEnv = tokenEnv

	token, err := cfg.BeeperToken()

	if err != nil {
		t.Fatalf("BeeperToken returned error: %v", err)
	}
	if token != "secret-token" {
		t.Fatalf("unexpected token %q", token)
	}
}

func TestBeeperTokenExplainsMissingEnvironment(t *testing.T) {
	const tokenEnv = "BEEPER_SOURCE_MISSING_TOKEN"
	_ = os.Unsetenv(tokenEnv)
	cfg := DefaultConfig()
	cfg.Beeper.TokenEnv = tokenEnv

	if _, err := cfg.BeeperToken(); err == nil {
		t.Fatal("expected missing token to return an error")
	}
}
