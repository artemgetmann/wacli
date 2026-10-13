package wa

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openclaw/wacli/internal/out"
	signalLog "go.mau.fi/libsignal/logger"
	"go.mau.fi/whatsmeow/appstate"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// libsignal has a separate process-global logger that defaults to stdout.
// Install it once, before any session work or concurrent logging can start.
func init() {
	var logger signalLog.Loggable = libsignalLogger{}
	signalLog.Setup(&logger)
}

var libsignalEvents atomic.Pointer[out.EventWriter]

// SetLibsignalEvents selects the process-wide diagnostic sink before a command runs.
func SetLibsignalEvents(events *out.EventWriter) {
	libsignalEvents.Store(events)
}

type libsignalLogger struct{}

// Debug can contain keys and ciphertext. Info has no production callers in
// libsignal v0.2.2. Neither level is enabled, including via Configure("all").
func (libsignalLogger) Debug(string, string) {}
func (libsignalLogger) Info(string, string)  {}
func (libsignalLogger) Configure(string)     {}

func (l libsignalLogger) Warning(caller, message string) { l.output("warning", caller, message) }
func (l libsignalLogger) Error(caller, message string)   { l.output("error", caller, message) }

func (libsignalLogger) output(level, caller, message string) {
	message = safeLibsignalMessage(message)
	if events := libsignalEvents.Load(); events.Enabled() {
		// Even ERROR may describe a failed attempt before a successful fallback,
		// not a terminal command error. Preserve its level inside a warning event.
		_ = events.Emit("warning", map[string]any{
			"code": "libsignal_diagnostic", "level": level, "source": "libsignal",
			"caller": caller, "message": message,
		})
		return
	}
	_ = out.WriteError(os.Stderr, false, fmt.Errorf("[libsignal %s] %s: %s", level, caller, message))
}

func safeLibsignalMessage(message string) string {
	// Error details can include raw input (e.g. bytehelper.SplitThree).
	// Retain only known operation labels, never their dynamic suffixes.
	operation, _, _ := strings.Cut(message, ":")
	switch operation {
	case "Error getting receiverchain",
		"Unable to get plain text from ciphertext", "Unable to decrypt message with state",
		"Unable to get or create chain key", "Unable to get or create message keys",
		"Unable to verify ciphertext mac", "Error split signal message",
		"Error serializing signal message", "Error deserializing signal message",
		"Error serializing prekey signal message", "Error deserializing prekey signal message",
		"Error serializing senderkey distribution message", "Error deserializing senderkey distribution message",
		"Error deserializing senderkey message",
		"Error serializing signed prekey record", "Error deserializing signed prekey record",
		"Error serializing prekey record", "Error deserializing prekey record",
		"Error serializing session state", "Error deserializing session state",
		"Error serializing session", "Error deserializing session":
		return operation + " (details redacted)"
	}
	// The warning on session fallback can consist of a bare error.
	switch message {
	case "uninitialized session", "wrong message version", "mismatching MAC in signal message",
		"message index is over 2000 messages into the future", "received message with old counter":
		return message
	default:
		return "Signal diagnostic (details redacted)"
	}
}

type whatsmeowLogger struct {
	module string
	min    int
	w      io.Writer
	mu     *sync.Mutex
}

var _ waLog.Logger = (*whatsmeowLogger)(nil)

var whatsmeowLogLevels = map[string]int{
	"":      -1,
	"DEBUG": 0,
	"INFO":  1,
	"WARN":  2,
	"ERROR": 3,
}

func newWhatsmeowLogger(module, minLevel string, w io.Writer) *whatsmeowLogger {
	if w == nil {
		w = io.Discard
	}
	min, ok := whatsmeowLogLevels[strings.ToUpper(minLevel)]
	if !ok {
		min = whatsmeowLogLevels["ERROR"]
	}
	return &whatsmeowLogger{
		module: module,
		min:    min,
		w:      w,
		mu:     &sync.Mutex{},
	}
}

func (l *whatsmeowLogger) Errorf(msg string, args ...any) { l.outputf("ERROR", msg, args...) }
func (l *whatsmeowLogger) Warnf(msg string, args ...any)  { l.outputf("WARN", msg, args...) }
func (l *whatsmeowLogger) Infof(msg string, args ...any)  { l.outputf("INFO", msg, args...) }
func (l *whatsmeowLogger) Debugf(msg string, args ...any) { l.outputf("DEBUG", msg, args...) }

func (l *whatsmeowLogger) Sub(module string) waLog.Logger {
	return &whatsmeowLogger{
		module: fmt.Sprintf("%s/%s", l.module, module),
		min:    l.min,
		w:      l.w,
		mu:     l.mu,
	}
}

func (l *whatsmeowLogger) outputf(level, msg string, args ...any) {
	levelValue, ok := whatsmeowLogLevels[level]
	if !ok {
		return
	}
	// Recovery failures otherwise disappear under the production ERROR filter.
	// Sanitize these exact pinned SDK templates before formatting at any level:
	// their arguments can include request IDs, key IDs, or raw snapshot errors.
	safeRecovery, recoveryDiagnostic := safeAppStateRecoveryDiagnostic(level, msg, args)
	if levelValue < l.min && !recoveryDiagnostic {
		return
	}
	if recoveryDiagnostic {
		msg, args = safeRecovery, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "%s [%s %s] %s\n", time.Now().Format("15:04:05.000"), l.module, level, fmt.Sprintf(msg, args...))
}

// Match both level and literal template from whatsmeow c386243a72ba. Unknown
// templates keep the ordinary filter; a dependency update must review this list.
// ParseRecovery and ProcessRecovery share one template, so unknown errors cannot
// safely distinguish malformed bytes from an unusable snapshot. Only known key
// sentinels refine that category, without rendering any dynamic argument.
func safeAppStateRecoveryDiagnostic(level, msg string, args []any) (string, bool) {
	var reason string
	if level == "WARN" {
		switch msg {
		case "No app state recovery data received for %s":
			reason = "empty_response"
		case "Unexpected number of app state recovery results for %s: %d":
			reason = "unexpected_results"
		case "Failed to parse app state recovery blob for %s: %v":
			reason = "snapshot_invalid_or_unusable"
			if len(args) == 2 {
				if err, ok := args[1].(error); ok {
					if errors.Is(err, appstate.ErrKeyNotFound) {
						reason = "snapshot_key_missing"
					} else if errors.Is(err, ErrEmptyAppStateKeyShare) {
						reason = "snapshot_key_unavailable"
					}
				}
			}
		case "Failed to collect app state events for %s: %v":
			reason = "collect_events_failed"
		}
	} else if level == "INFO" && msg == "Ignoring app state recovery response for %s as current version %d is newer than or equal to recovery version %d" {
		reason = "stale_version"
	}
	if reason == "" {
		return "", false
	}
	return "app state recovery: " + reason + " (details redacted)", true
}
