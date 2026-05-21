package main

import (
	"testing"

	"github.com/Martin-Hausleitner/beeper-matrix-proxy/beepersource"
)

func TestApplyRoomsOnlySafetyForcesReadOnlyAndKillSwitch(t *testing.T) {
	cfg := beepersource.DefaultConfig()
	cfg.Sync.Mode = beepersource.SyncModeBidirectional
	cfg.Safety.DisableMatrixToBeeper = false

	applyRoomsOnlySafety(&cfg)

	if cfg.Sync.Mode != beepersource.SyncModeReadOnly {
		t.Fatalf("expected rooms-only mode to force read_only, got %q", cfg.Sync.Mode)
	}
	if !cfg.Safety.DisableMatrixToBeeper {
		t.Fatal("expected rooms-only mode to disable Matrix -> Beeper sends")
	}
	if !cfg.Matrix.Spaces {
		t.Fatal("expected rooms-only mode to enable Matrix spaces")
	}
	if !cfg.Matrix.PlatformAvatars {
		t.Fatal("expected rooms-only mode to enable platform avatars")
	}
}
