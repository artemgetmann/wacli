package main

import (
	"os"
	"strings"
	"testing"
)

func TestAgentLeadingGlobalFlagsRejectReadOnlySendBeforeTouchingStore(t *testing.T) {
	store := t.TempDir()
	err := execute([]string{"--store", store, "--read-only", "agent", "send-text", "--to", "15551234567@s.whatsapp.net", "--message", "harmless"})
	if err == nil || !strings.Contains(err.Error(), "read-only mode") {
		t.Fatalf("expected read-only refusal, got %v", err)
	}
	entries, err := os.ReadDir(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("read-only refusal touched store: %v", entries)
	}
}
