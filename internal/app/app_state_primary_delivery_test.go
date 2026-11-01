package app

import (
	"fmt"
	appstore "github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/appstate"
	sdkstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
	"strings"
	"testing"
)

func TestReviewerPrimaryRecoveryDeliveryDebt(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	ctx := t.Context()
	col := appstate.WAPatchRegularHigh
	gen, _, err := a.db.BeginAppStateRecovery(string(col))
	if err != nil {
		t.Fatal(err)
	}
	id := sdkstore.AppStateDeliveryID{Generation: "reviewer", Sequence: 1}
	f.onAppStateRecovery = func(name string) {
		// Model the real consumer's atomic checkpoint+uncovered debt before dispatch.
		if err := a.db.ApplyAppStateDelivery(ctx, appstore.DeliveryCheckpoint{Generation: id.Generation, Sequence: id.Sequence, Digest: id.Digest[:]}, nil, []string{name}); err != nil {
			t.Fatal(err)
		}
		f.emit(&events.AppState{Index: []string{"unknown"}, DeliveryID: &id})
		f.emit(&events.AppStateSyncComplete{Name: col, Recovery: true})
	}
	err = a.recoverMismatchingAppState(ctx, col, gen, &appStatePersistenceTracker{}, nil)
	required, e := a.db.AppStateRecoveryRequired(string(col))
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("complete primary recovery result=%v remaining_debt=%t", err, required)
	if err != nil || required {
		t.Fatalf("successful primary recovery cannot settle its own new delivery debt: err=%v debt=%t", err, required)
	}
}

func TestPrimaryDeliveryRecoveryPreservesConcurrentNewerDebt(t *testing.T) {
	for _, late := range []bool{false, true} {
		for _, newer := range []bool{false, true} {
			t.Run(fmt.Sprintf("late=%t/newer=%t", late, newer), func(t *testing.T) {
				a := newTestApp(t)
				f := newFakeWA()
				a.wa = f
				ctx := t.Context()
				col := appstate.WAPatchRegularHigh
				must := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				gen, _, err := a.db.BeginAppStateRecovery(string(col))
				must(err)
				id := sdkstore.AppStateDeliveryID{Generation: "synthetic-primary", Sequence: 1}
				emit := func() {
					must(a.db.ApplyAppStateDelivery(ctx, appstore.DeliveryCheckpoint{Generation: id.Generation, Sequence: 1, Digest: id.Digest[:]}, nil, []string{string(col)}))
					f.emit(&events.AppState{Index: []string{"uncovered-synthetic"}, DeliveryID: &id})
					if newer {
						must(a.db.ApplyAppStateDelivery(ctx, appstore.DeliveryCheckpoint{Generation: id.Generation, Sequence: 2, Digest: id.Digest[:]}, nil, []string{string(col)}))
					}
					f.emit(&events.AppStateSyncComplete{Name: col, Recovery: true})
				}
				if late {
					capture := a.newAppStateRecoveryCapture(ctx, col, "late_recovery_complete")
					emit()
					a.runLateAppStateRecovery(col, &lateAppStateRecovery{capture: capture, generation: gen, superseded: make(chan struct{})}, make(chan struct{}))
				} else {
					f.onAppStateRecovery = func(string) { emit() }
					err = a.recoverMismatchingAppState(ctx, col, gen, &appStatePersistenceTracker{}, nil)
					if newer {
						if err == nil || !strings.Contains(err.Error(), "recovery changed") {
							t.Fatalf("newer debt not reported: %v", err)
						}
					} else {
						must(err)
					}
				}
				required, err := a.db.AppStateRecoveryRequired(string(col))
				must(err)
				if required != newer {
					t.Fatalf("debt=%t want newer=%t", required, newer)
				}
			})
		}
	}
}
