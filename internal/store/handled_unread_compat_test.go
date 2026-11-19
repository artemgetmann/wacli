package store

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in actual-binary compatibility check. The previous executable receives
// only a disposable store with synthetic content and no WhatsApp credentials.
// Offline doctor deliberately omits --connect: it exercises writable App
// opening/migrations without starting a session or contacting WhatsApp.
func TestHandledUnreadPreviousBinaryOfflineCompatibility(t *testing.T) {
	previous := os.Getenv("WACLI_PREVIOUS_BINARY")
	if previous == "" {
		t.Skip("set WACLI_PREVIOUS_BINARY for actual previous-binary acceptance")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "wacli.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	handledInbound(t, d, "fresh", 100, true)
	snapshot, err := d.SnapshotHandledUnread(handledTestChat, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if err = d.ClaimHandledUnread(snapshot); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	for _, readOnly := range []bool{true, false} {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		args := []string{"--store", dir, "--json"}
		if readOnly {
			args = append(args, "--read-only")
		}
		args = append(args, "doctor")
		out, runErr := exec.CommandContext(ctx, previous, args...).CombinedOutput()
		cancel()
		if runErr != nil {
			t.Fatalf("previous readOnly=%t failed: %v %s", readOnly, runErr, out)
		}
		var result struct {
			Success bool `json:"success"`
			Data    struct {
				StoreError string `json:"store_error"`
				Connected  bool   `json:"connected"`
			} `json:"data"`
		}
		if err = json.Unmarshal(out, &result); err != nil || !result.Success || result.Data.StoreError != "" || result.Data.Connected {
			t.Fatalf("previous offline report=%s parse=%v", out, err)
		}
	}
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	handledCount(t, d, 1)
	guard, err := d.HasPrivateReadAttempt(handledTestChat, []string{"fresh"})
	if err != nil || !guard {
		t.Fatalf("previous writer lost guard: %t %v", guard, err)
	}
	if _, err = d.GetMessage(handledTestChat, "fresh"); err != nil {
		t.Fatal(err)
	}
}
