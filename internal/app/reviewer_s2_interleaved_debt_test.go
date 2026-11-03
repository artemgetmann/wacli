package app

import (
	appstore "github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/appstate"
	sdkstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
	"testing"
)

func TestReviewerS2InterleavedGenericMustNotSettleNewerIncompleteBatch(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	ctx := t.Context()
	col := appstate.WAPatchRegularHigh
	gen, _, err := a.db.BeginAppStateRecovery(string(col))
	if err != nil {
		t.Fatal(err)
	}
	f.onAppStateRecovery = func(name string) {
		for seq := uint64(1); seq <= 2; seq++ {
			id := sdkstore.AppStateDeliveryID{Generation: "review-interleave", Sequence: seq}
			if e := a.db.ApplyAppStateDelivery(ctx, appstore.DeliveryCheckpoint{Generation: id.Generation, Sequence: seq, Digest: id.Digest[:]}, nil, []string{name}); e != nil {
				t.Fatal(e)
			}
			// Only generic admission of the newer delivery interleaves before the older
			// recovery sentinel. Its typed Star event has not been dispatched/persisted.
			index := []string{"unknown-old-recovery-action"}
			if seq == 2 {
				index = []string{appstate.IndexStar, "456@s.whatsapp.net", "unpersisted"}
			}
			f.emit(&events.AppState{Index: index, DeliveryID: &id})
		}
		f.emit(&events.AppStateSyncComplete{Name: col, Recovery: true})
	}
	err = a.recoverMismatchingAppState(ctx, col, gen, &appStatePersistenceTracker{}, nil)
	required, e := a.db.AppStateRecoveryRequired(string(col))
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("recovery_result=%v newer_incomplete_delivery_debt=%t", err, required)
	if !required {
		t.Fatal("older recovery settled newer delivery after generic event only")
	}
}
