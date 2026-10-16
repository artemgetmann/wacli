package app

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
)

type chatStateDiagnosticKey struct{}
type chatStateDiagnostic struct {
	id      string
	started time.Time
}

var chatStateDiagnosticSequence atomic.Uint64

// Correlation is local to this process and contains no WhatsApp identifiers.
// Carry it in context so nested recovery work retains one operation boundary.
func (a *App) chatStateDiagnosticContext(ctx context.Context) context.Context {
	if !a.eventsEnabled() || ctx.Value(chatStateDiagnosticKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, chatStateDiagnosticKey{}, &chatStateDiagnostic{
		id: fmt.Sprintf("op-%d", chatStateDiagnosticSequence.Add(1)), started: time.Now(),
	})
}

// Only protocol-defined collection names may reach diagnostic output. In
// particular, an unexpected caller value must never become an identifier leak.
func diagnosticCollection(collection appstate.WAPatchName) string {
	switch string(collection) {
	case "critical_block", "critical_unblock_low", "regular_low", "regular_high", "regular":
		return string(collection)
	default:
		return "unknown"
	}
}

func diagnosticResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, appstate.ErrKeyNotFound):
		return "missing_key"
	case errors.Is(err, appstate.ErrMismatchingLTHash):
		return "hash_mismatch"
	case errors.Is(err, wa.ErrEmptyAppStateKeyShare):
		return "empty_key"
	default:
		return "failed"
	}
}

// Emit fixed categories and timing only; never add errors, payloads, request
// IDs, key material or event contents. time.Since retains monotonic timing.
func (a *App) emitChatStatePhase(ctx context.Context, collection appstate.WAPatchName, phase, result string, started time.Time) {
	diagnostic, _ := ctx.Value(chatStateDiagnosticKey{}).(*chatStateDiagnostic)
	if diagnostic == nil || !a.eventsEnabled() {
		return
	}
	switch phase {
	case "operation", "gate", "fetch_delta", "fetch_full", "recovery_request", "recovery_complete", "persistence_drain", "late_recovery_complete", "late_persistence_drain":
	default:
		return
	}
	switch result {
	case "started", "ok", "deadline", "canceled", "missing_key", "hash_mismatch", "empty_key", "failed":
	default:
		return
	}
	remaining := int64(-1) // No deadline; zero means the deadline has elapsed.
	if deadline, ok := ctx.Deadline(); ok {
		remaining = max(0, time.Until(deadline).Milliseconds())
	}
	a.emitEvent("chat_state_phase", map[string]any{
		"operation": diagnostic.id, "collection": diagnosticCollection(collection),
		"phase": phase, "result": result,
		"elapsed_ms":  time.Since(diagnostic.started).Milliseconds(),
		"duration_ms": time.Since(started).Milliseconds(), "remaining_ms": remaining,
	})
}

func (a *App) beginChatStatePhase(ctx context.Context, collection appstate.WAPatchName, phase string) func(error) {
	started := time.Now()
	a.emitChatStatePhase(ctx, collection, phase, "started", started)
	return func(err error) {
		a.emitChatStatePhase(ctx, collection, phase, diagnosticResult(err), started)
	}
}
