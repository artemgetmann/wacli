package app

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/sqliteutil"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// Reproduce the cursor/persistence gap against the integrated App and pinned
// SDK's real mutation collector and SQLite session store. Only send/fetch
// transport is fake. The held event slice models the documented pause between
// SDK application/cursor advance and asynchronous event delivery; no sleeps,
// network, paired account, or SDK source modification are involved.
func TestBackgroundBatchRemainsUnpersistedAfterSendButKeepsReplayDebt(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	ctx := t.Context()
	collection := appstate.WAPatchRegularLow
	target := types.NewJID("456", types.DefaultUserServer)
	remote := types.NewJID("789", types.DefaultUserServer)
	when := time.Unix(1700000000, 0)
	for _, jid := range []types.JID{target, remote} {
		if err := a.db.UpsertChat(jid.String(), "dm", "synthetic", when); err != nil {
			t.Fatal(err)
		}
	}

	// A disposable synthetic device supplies actual SDK stores; it never
	// connects or has real authentication credentials.
	container, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(filepath.Join(t.TempDir(), "session.db"), "_foreign_keys=on"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer container.Close()
	device := container.NewDevice()
	device.ID = &types.JID{User: "15550000000", Device: 7, Server: types.DefaultUserServer}
	device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := device.Save(ctx); err != nil {
		t.Fatal(err)
	}
	sdk := whatsmeow.NewClient(device, nil)
	var held []any
	var applyErr error
	f.archiveEvent = func() any {
		// DecodePatches stores the cursor before collectEventsToDispatch.
		// Exercise those actual store/collector APIs at that boundary without
		// emulating cryptography or the network protocol.
		applyErr = device.AppState.PutAppStateVersion(ctx, string(collection), 2, [128]byte{})
		if applyErr == nil {
			applyErr = sdk.DangerousInternals().CollectEventsToDispatch(ctx, collection, []appstate.Mutation{{
				Operation: waServerSync.SyncdMutation_SET,
				Index:     []string{appstate.IndexArchive, remote.String()},
				Action: &waSyncAction.SyncActionValue{Timestamp: proto.Int64(when.UnixMilli()),
					ArchiveChatAction: &waSyncAction.ArchiveChatAction{Archived: proto.Bool(true)}},
			}}, false, &held)
		}
		// A background fetch already consumed this mutation, leaving the send's
		// own post-send fetch empty. Event admission is deliberately withheld.
		return nil
	}
	remove, err := a.AddChatStatePersistenceHandler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer remove()
	if err := a.ArchiveChat(ctx, target, true); err != nil {
		t.Fatal(err)
	}
	if applyErr != nil {
		t.Fatal(applyErr)
	}
	if err := a.appStatePersist.waitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if fetchErr, persistErr := a.fetchAndPersistAppState(ctx, collection, false, &appStatePersistenceTracker{}); fetchErr != nil || persistErr != nil {
		t.Fatalf("empty delta: %v / %v", fetchErr, persistErr)
	}
	settings, err := device.ChatSettings.GetChatSettings(ctx, remote)
	if err != nil || !settings.Archived {
		t.Fatalf("SDK did not apply remote archive: %+v / %v", settings, err)
	}
	version, _, err := device.AppState.GetAppStateVersion(ctx, string(collection))
	if err != nil || version != 2 {
		t.Fatalf("SDK cursor = %d / %v", version, err)
	}
	stored, err := a.db.GetChat(remote.String())
	if err != nil || stored.Archived {
		t.Fatalf("expected withheld remote update missing from App: %+v / %v", stored, err)
	}
	required, err := a.db.AppStateRecoveryRequired(string(collection))
	if err != nil || !required {
		t.Fatalf("unadmitted background batch lost recovery protection: %t / %v", required, err)
	}
	t.Log("REPRODUCED: SDK cursor=2 and archived=true; App archived=false after send success, idle queue and empty delta; replay debt retained")

	// Verify upstream's actual degraded path tolerates known-broken snapshots
	// without pretending the held batch was persisted or clearing older debt.
	f.hasLocalAppState = true
	a.setAppStateFullSyncResult(collection, true)
	if err := a.PinChat(ctx, target, true); err != nil {
		t.Fatal(err)
	}
	if fullSyncFetches(f, collection) != 0 {
		t.Fatal("known-broken collection performed an unnecessary full replay")
	}
	required, err = a.db.AppStateRecoveryRequired(string(collection))
	stored, storeErr := a.db.GetChat(remote.String())
	if err != nil || !required || storeErr != nil || stored.Archived {
		t.Fatalf("degraded path hid missing state/debt: debt=%t remote=%+v errors=%v/%v", required, stored, err, storeErr)
	}

	// Release precisely the real SDK-produced batch through its dispatcher.
	// This verifies delayed admission explains the gap, rather than a malformed
	// synthetic event. Persistence completion remains distinct from send return.
	handler := sdk.AddEventHandler(f.emit)
	defer sdk.RemoveEventHandler(handler)
	for _, evt := range held {
		sdk.DangerousInternals().DispatchEvent(evt)
	}
	if err := a.appStatePersist.waitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err = a.db.GetChat(remote.String())
	if err != nil || !stored.Archived {
		t.Fatalf("delivered SDK batch not persisted: %+v / %v", stored, err)
	}
}
