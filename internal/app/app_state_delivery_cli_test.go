package app

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/sqliteutil"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	sdkstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	log "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// auth status opens/drains the session store but never Connects. This executes
// the real CLI against only disposable synthetic stores, both failure and repair.
func TestAppStateDeliveryCLIStartupPendingAndRepair(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	jid := types.NewJID("456", types.DefaultUserServer)
	must(a.db.UpsertChat(jid.String(), "dm", "synthetic", time.Unix(1700000000, 0)))
	_, device := gapDevice(t, filepath.Join(a.StoreDir(), "session.db"))
	sdk := whatsmeow.NewClient(device, log.Noop)
	must(sdk.SetAppStateDeliveryConsumer(func(context.Context, *sdkstore.AppStateDelivery) error {
		return fmt.Errorf("synthetic pending projection")
	}))
	_, source := gapDevice(t, filepath.Join(t.TempDir(), "sender.db"))
	encoded, err := appstate.NewProcessor(source, log.Noop).EncodePatch(ctx, []byte{1}, appstate.HashState{}, appstate.BuildPin(jid, true))
	must(err)
	patch := &waServerSync.SyncdPatch{}
	must(proto.Unmarshal(encoded, patch))
	patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(1)}
	var held []any
	_, err = sdk.DangerousInternals().ApplyAppStatePatches(ctx, appstate.WAPatchRegularLow, appstate.HashState{}, &appstate.PatchList{Name: appstate.WAPatchRegularLow, Patches: []*waServerSync.SyncdPatch{patch}}, false, &held)
	if err == nil {
		t.Fatal("fixture did not retain pending payload")
	}
	raw, err := sql.Open("sqlite3", sqliteutil.FileURI(filepath.Join(a.StoreDir(), "wacli.db"), "_foreign_keys=on"))
	must(err)
	defer raw.Close()
	_, err = raw.Exec(`CREATE TRIGGER fail_cli_checkpoint BEFORE INSERT ON app_state_delivery_checkpoint BEGIN SELECT RAISE(FAIL,'synthetic CLI checkpoint failure'); END`)
	must(err)
	h := newRetrievalCLI(t, a)
	h.invoke(a.StoreDir(), "pending app state projection", false, "auth", "status")
	chat, err := a.db.GetChat(jid.String())
	must(err)
	if chat.Pinned {
		t.Fatal("failed CLI projection partially committed")
	}
	native := device.AppState.(sdkstore.AppStateDeliveryStore)
	pending, err := native.PendingAppStateDelivery(ctx)
	must(err)
	if pending == nil {
		t.Fatal("failed CLI discarded payload")
	}
	_, err = raw.Exec("DROP TRIGGER fail_cli_checkpoint")
	must(err)
	report := h.write(a.StoreDir(), "auth", "status")
	if report["authenticated"] != true {
		t.Fatalf("synthetic offline auth status: %+v", report)
	}
	chat, err = a.db.GetChat(jid.String())
	must(err)
	if !chat.Pinned {
		t.Fatal("real CLI startup did not project pending pin")
	}
	pending, err = native.PendingAppStateDelivery(ctx)
	must(err)
	if pending != nil {
		t.Fatal("real CLI did not acknowledge completed checkpoint")
	}
	settings, err := device.ChatSettings.GetChatSettings(ctx, jid)
	must(err)
	if !settings.Pinned {
		t.Fatal("SDK covered cache not restored by startup drain")
	}
	// Keep the session but lose the App checkpoint: a later genuine verified
	// delivery must fail closed, not silently bootstrap over missing history.
	version, hash, err := device.AppState.GetAppStateVersion(ctx, string(appstate.WAPatchRegularLow))
	must(err)
	encoded, err = appstate.NewProcessor(device, log.Noop).EncodePatch(ctx, []byte{1}, appstate.HashState{Version: version, Hash: hash}, appstate.BuildPin(jid, false))
	must(err)
	patch = &waServerSync.SyncdPatch{}
	must(proto.Unmarshal(encoded, patch))
	patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(version + 1)}
	_, err = sdk.DangerousInternals().ApplyAppStatePatches(ctx, appstate.WAPatchRegularLow, appstate.HashState{Version: version, Hash: hash}, &appstate.PatchList{Name: appstate.WAPatchRegularLow, Patches: []*waServerSync.SyncdPatch{patch}}, false, &held)
	if err == nil {
		t.Fatal("fixture failed to retain second delivery")
	}
	_, err = raw.Exec("DELETE FROM app_state_delivery_checkpoint")
	must(err)
	h.invoke(a.StoreDir(), "checkpoint sequence gap", false, "auth", "status")
	pending, err = native.PendingAppStateDelivery(ctx)
	must(err)
	if pending == nil || pending.ID.Sequence != 2 {
		t.Fatal("mismatched paired store discarded pending delivery")
	}
	chat, err = a.db.GetChat(jid.String())
	must(err)
	if !chat.Pinned {
		t.Fatal("checkpoint loss silently applied later state")
	}

}
