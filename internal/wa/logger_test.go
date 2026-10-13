package wa

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/appstate"
)

func TestWhatsmeowLoggerWritesToConfiguredWriter(t *testing.T) {
	var stderr bytes.Buffer
	logger := newWhatsmeowLogger("Client", "ERROR", &stderr)

	logger.Warnf("hidden warning")
	if stderr.Len() != 0 {
		t.Fatalf("WARN was written for ERROR logger: %q", stderr.String())
	}

	logger.Errorf("Failed to issue privacy token for %s: %v", "123@s.whatsapp.net", "bad-request")
	got := stderr.String()
	if !strings.Contains(got, "[Client ERROR] Failed to issue privacy token for 123@s.whatsapp.net: bad-request") {
		t.Fatalf("unexpected log line: %q", got)
	}
}

func TestWhatsmeowLoggerSubmoduleSharesWriter(t *testing.T) {
	var stderr bytes.Buffer
	logger := newWhatsmeowLogger("Client", "ERROR", &stderr)

	sub := logger.Sub("Socket").(*whatsmeowLogger)
	if sub.w != logger.w || sub.mu != logger.mu || sub.min != logger.min {
		t.Fatal("submodule did not preserve writer, mutex, and filter")
	}
	sub.Errorf("boom")
	got := stderr.String()
	if !strings.Contains(got, "[Client/Socket ERROR] boom") {
		t.Fatalf("unexpected submodule log line: %q", got)
	}
}

func TestWhatsmeowRecoveryDiagnosticsRedactArguments(t *testing.T) {
	const hostile = "PRIVATE-REQUEST-KEY-SNAPSHOT\nforged log line"
	for _, tc := range []struct {
		name, level, template, reason string
		args                          []any
	}{
		{"empty", "WARN", "No app state recovery data received for %s", "empty_response", []any{hostile}},
		{"results", "WARN", "Unexpected number of app state recovery results for %s: %d", "unexpected_results", []any{hostile, hostile}},
		{"invalid", "WARN", "Failed to parse app state recovery blob for %s: %v", "snapshot_invalid_or_unusable", []any{hostile, errors.New(hostile)}},
		{"missing-key", "WARN", "Failed to parse app state recovery blob for %s: %v", "snapshot_key_missing", []any{hostile, fmt.Errorf("%s: %w", hostile, appstate.ErrKeyNotFound)}},
		{"unavailable-key", "WARN", "Failed to parse app state recovery blob for %s: %v", "snapshot_key_unavailable", []any{hostile, fmt.Errorf("%s: %w", hostile, ErrEmptyAppStateKeyShare)}},
		{"collect", "WARN", "Failed to collect app state events for %s: %v", "collect_events_failed", []any{hostile, errors.New(hostile)}},
		{"stale", "INFO", "Ignoring app state recovery response for %s as current version %d is newer than or equal to recovery version %d", "stale_version", []any{hostile, hostile, hostile}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Sanitization must also hold when the original level is enabled.
			for _, minLevel := range []string{"ERROR", "DEBUG"} {
				var output bytes.Buffer
				logger := newWhatsmeowLogger("Client", minLevel, &output)
				logger.Sub("Recovery").(*whatsmeowLogger).outputf(tc.level, tc.template, tc.args...)
				want := "[Client/Recovery " + tc.level + "] app state recovery: " + tc.reason + " (details redacted)\n"
				got := output.String()
				if !strings.HasSuffix(got, want) || strings.Count(got, "\n") != 1 || strings.Contains(got, "PRIVATE") || strings.Contains(got, "forged") {
					t.Fatalf("%s diagnostic exposed arguments or wrong category: %q", minLevel, got)
				}
			}
		})
	}
}

func TestWhatsmeowRecoveryAllowlistKeepsOtherLogsFiltered(t *testing.T) {
	var output bytes.Buffer
	logger := newWhatsmeowLogger("Client", "ERROR", &output)
	logger.Warnf("unrelated warning %s", "private")
	logger.Infof("unrelated info %s", "private")
	logger.Debugf("No app state recovery data received for %s", "private")
	logger.Infof("No app state recovery data received for %s", "private")
	logger.Warnf("No app state recovery data received for %s (changed template)", "private")
	logger.Warnf("Failed to parse app state recovery blob for %s: %v (changed template)", "private", errors.New("private"))
	if output.Len() != 0 {
		t.Fatalf("allowlist exposed unrelated or changed templates: %q", output.String())
	}
}
