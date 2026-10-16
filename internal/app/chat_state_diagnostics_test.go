package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func diagnosticEvents(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var result []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var envelope struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Event == "chat_state_phase" {
			result = append(result, envelope.Data)
		}
	}
	return result
}

func diagnosticPhases(events []map[string]any) []string {
	var result []string
	for _, event := range events {
		result = append(result, event["phase"].(string)+":"+event["result"].(string))
	}
	return result
}

func TestChatStateDiagnosticsImmediateRecoveryOrder(t *testing.T) {
	a := newTestApp(t)
	var buf bytes.Buffer
	a.opts.Events = out.NewEventWriter(&buf, true)
	f := newFakeWA()
	a.wa = f
	f.appStateFetchErrs = []error{appstate.ErrMismatchingLTHash, appstate.ErrMismatchingLTHash}
	// Synchronous completion deliberately arrives before the request ack.
	f.onAppStateRecovery = func(string) {
		f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularHigh, Recovery: true})
		f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow})
		f.emit(&events.AppStateSyncComplete{Name: appstate.WAPatchRegularLow, Recovery: true})
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	release, err := a.beginChatStateWrite(ctx, appstate.WAPatchRegularLow)
	if err != nil {
		t.Fatal(err)
	}
	release()
	got := diagnosticEvents(t, &buf)
	want := []string{"operation:started", "gate:started", "gate:ok", "fetch_delta:started", "fetch_delta:hash_mismatch", "persistence_drain:started", "persistence_drain:ok", "fetch_full:started", "fetch_full:hash_mismatch", "persistence_drain:started", "persistence_drain:ok", "recovery_complete:started", "recovery_request:started", "recovery_complete:ok", "recovery_request:ok", "persistence_drain:started", "persistence_drain:ok", "operation:ok"}
	if phases := diagnosticPhases(got); !reflect.DeepEqual(phases, want) {
		t.Fatalf("phases=%v", phases)
	}
	for _, event := range got {
		if event["operation"] != got[0]["operation"] || event["collection"] != "regular_low" {
			t.Fatalf("correlation=%v", event)
		}
		for _, key := range []string{"elapsed_ms", "duration_ms", "remaining_ms"} {
			if event[key].(float64) < 0 {
				t.Fatalf("invalid timing %v", event)
			}
		}
	}
}

func TestChatStateDiagnosticsRecoveryIgnoresOtherCompletion(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(fmt.Sprint(recovery), func(t *testing.T) {
			a := newTestApp(t)
			var buf bytes.Buffer
			a.opts.Events = out.NewEventWriter(&buf, true)
			f := newFakeWA()
			a.wa = f
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx = a.chatStateDiagnosticContext(ctx)
			f.onAppStateRecovery = func(string) {
				name := appstate.WAPatchRegularLow
				if recovery {
					name = appstate.WAPatchRegularHigh
				}
				f.emit(&events.AppStateSyncComplete{Name: name, Recovery: recovery})
				cancel()
			}
			_, err := a.waitForPrimaryAppStateRecovery(ctx, appstate.WAPatchRegularLow, nil)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error=%v", err)
			}
			want := []string{"recovery_complete:started", "recovery_request:started", "recovery_request:ok", "recovery_complete:canceled"}
			if got := diagnosticPhases(diagnosticEvents(t, &buf)); !reflect.DeepEqual(got, want) {
				t.Fatalf("phases=%v", got)
			}
		})
	}
}

func TestChatStateDiagnosticsGateDeadlineAndFetchFailure(t *testing.T) {
	for _, gate := range []bool{false, true} {
		t.Run(fmt.Sprint(gate), func(t *testing.T) {
			a := newTestApp(t)
			var buf bytes.Buffer
			a.opts.Events = out.NewEventWriter(&buf, true)
			f := newFakeWA()
			a.wa = f
			f.appStateFetchErr = errors.New("private-jid@s.whatsapp.net private-key private-payload")
			ctx := t.Context()
			if gate {
				<-a.chatStateSync
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			_, err := a.beginChatStateWrite(ctx, appstate.WAPatchRegularLow)
			if err == nil {
				t.Fatal("expected error")
			}
			result := "failed"
			if gate {
				result = "deadline"
			}
			got := diagnosticPhases(diagnosticEvents(t, &buf))
			if got[len(got)-1] != "operation:"+result {
				t.Fatalf("phases=%v", got)
			}
			if strings.Contains(buf.String(), "private-") {
				t.Fatal("diagnostic leaked error")
			}
		})
	}
}

func TestChatStateDiagnosticsAllowlistAndDisabled(t *testing.T) {
	a := newTestApp(t)
	var buf bytes.Buffer
	a.opts.Events = out.NewEventWriter(&buf, false)
	ctx := a.chatStateDiagnosticContext(t.Context())
	a.beginChatStatePhase(ctx, appstate.WAPatchRegularLow, "gate")(nil)
	if buf.Len() != 0 || ctx.Value(chatStateDiagnosticKey{}) != nil {
		t.Fatal("disabled diagnostics emitted or allocated correlation")
	}
	a.opts.Events = out.NewEventWriter(&buf, true)
	ctx = a.chatStateDiagnosticContext(t.Context())
	private := "private-jid@s.whatsapp.net"
	a.beginChatStatePhase(ctx, appstate.WAPatchName(private), "gate")(errors.New(private))
	a.emitChatStatePhase(ctx, appstate.WAPatchRegularLow, private, "ok", time.Now())
	a.emitChatStatePhase(ctx, appstate.WAPatchRegularLow, "gate", private, time.Now())
	got := diagnosticEvents(t, &buf)
	if len(got) != 2 || strings.Contains(buf.String(), private) {
		t.Fatalf("unsafe output=%s", buf.String())
	}
	for _, event := range got {
		if len(event) != 7 || event["collection"] != "unknown" || event["remaining_ms"] != float64(-1) {
			t.Fatalf("unexpected data=%v", event)
		}
	}
	// Opaque correlations are unique across operations and retained by children.
	other := a.chatStateDiagnosticContext(t.Context())
	if other.Value(chatStateDiagnosticKey{}) == ctx.Value(chatStateDiagnosticKey{}) {
		t.Fatal("reused operation")
	}
	if a.chatStateDiagnosticContext(ctx) != ctx {
		t.Fatal("nested operation lost correlation")
	}
}

func TestChatStateDiagnosticResultCategories(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "ok"}, {context.Canceled, "canceled"}, {context.DeadlineExceeded, "deadline"},
		{fmt.Errorf("private: %w", appstate.ErrKeyNotFound), "missing_key"},
		{appstate.ErrMismatchingLTHash, "hash_mismatch"}, {errors.New("private"), "failed"},
		{wa.ErrEmptyAppStateKeyShare, "empty_key"},
	} {
		if got := diagnosticResult(tc.err); got != tc.want {
			t.Fatalf("result=%s want=%s", got, tc.want)
		}
	}
}

func TestChatStateDiagnosticsRecoveryDeadline(t *testing.T) {
	a := newTestApp(t)
	var buf bytes.Buffer
	a.opts.Events = out.NewEventWriter(&buf, true)
	a.wa = newFakeWA()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, err := a.waitForPrimaryAppStateRecovery(a.chatStateDiagnosticContext(ctx), appstate.WAPatchRegularLow, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	got := diagnosticEvents(t, &buf)
	last := got[len(got)-1]
	if last["phase"] != "recovery_complete" || last["result"] != "deadline" || last["remaining_ms"] != float64(0) {
		t.Fatalf("terminal=%v", last)
	}
}

func TestChatStateDiagnosticsBlockedPersistenceDrain(t *testing.T) {
	a := newTestApp(t)
	var buf bytes.Buffer
	a.opts.Events = out.NewEventWriter(&buf, true)
	f := newFakeWA()
	a.wa = f
	// An earlier pending ticket keeps this fetch's successful result queued.
	// Cancel after fetching so evidence distinguishes fetch success from drain.
	previous := a.appStatePersist.reserve()
	defer a.appStatePersist.complete(previous, func() {})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.appStateFetchEvent = func(string, bool, bool) any { cancel(); return nil }
	fetchErr, persistErr := a.fetchAndPersistAppState(a.chatStateDiagnosticContext(ctx), appstate.WAPatchRegularLow, false, &appStatePersistenceTracker{})
	if fetchErr != nil || !errors.Is(persistErr, context.Canceled) {
		t.Fatalf("fetch=%v persistence=%v", fetchErr, persistErr)
	}
	want := []string{"fetch_delta:started", "fetch_delta:ok", "persistence_drain:started", "persistence_drain:canceled"}
	if got := diagnosticPhases(diagnosticEvents(t, &buf)); !reflect.DeepEqual(got, want) {
		t.Fatalf("phases=%v", got)
	}
}

// Verify request errors remain errors and cannot report observed completion.
func TestChatStateDiagnosticsRequestFailure(t *testing.T) {
	a := newTestApp(t)
	var buf bytes.Buffer
	a.opts.Events = out.NewEventWriter(&buf, true)
	wantErr := errors.New("private request id and key")
	f := &appStateContextWA{fakeWA: newFakeWA()}
	f.requestAppStateRecovery = func(context.Context, string) (types.MessageID, error) { return "private-id", wantErr }
	a.wa = f
	_, err := a.waitForPrimaryAppStateRecovery(a.chatStateDiagnosticContext(t.Context()), appstate.WAPatchRegularLow, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v", err)
	}
	want := []string{"recovery_complete:started", "recovery_request:started", "recovery_request:failed", "recovery_complete:failed"}
	if got := diagnosticPhases(diagnosticEvents(t, &buf)); !reflect.DeepEqual(got, want) {
		t.Fatalf("phases=%v", got)
	}
	if strings.Contains(buf.String(), "private") {
		t.Fatal("request details leaked")
	}
}
