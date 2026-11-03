package app

import (
	"path/filepath"
	"testing"
	"time"

	appstore "github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	sdkstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	log "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

type durableProjectionWA struct{ *fakeWA }

func (*durableProjectionWA) DurableChatStateProjection() bool { return true }

func TestCoveredDeliveryCallbacksAndLocalCompletionPreserveNewerState(t *testing.T) {
	ctx := t.Context()
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = &durableProjectionWA{f}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, device := gapDevice(t, filepath.Join(t.TempDir(), "session.db"))
	sdk := whatsmeow.NewClient(device, log.Noop)
	must(sdk.SetAppStateDeliveryConsumer(a.consumeAppStateDelivery))
	remove, err := a.AddChatStatePersistenceHandler(ctx)
	must(err)
	defer remove()
	sdk.AddEventHandler(f.emit)
	_, sender := gapDevice(t, filepath.Join(t.TempDir(), "sender.db"))
	source := whatsmeow.NewClient(sender, log.Noop)
	jid := types.NewJID("456", types.DefaultUserServer)
	at := time.Unix(1700000000, 0)
	collection := appstate.WAPatchRegularLow
	must(a.db.UpsertChat(jid.String(), "dm", "synthetic", at))
	must(a.db.UpsertMessage(appstore.UpsertMessageParams{ChatJID: jid.String(), MsgID: "handled", SenderJID: jid.String(), Timestamp: at, Text: "synthetic"}))
	must(a.db.SetChatUnreadCount(jid.String(), 1))
	pending, err := a.beginLocalAppStateWrite(collection)
	must(err)
	pending.reserve(a)
	apply := func(seq int) []any {
		t.Helper()
		key := &waCommon.MessageKey{RemoteJID: proto.String(jid.String()), ID: proto.String("handled"), FromMe: proto.Bool(false)}
		archive := appstate.BuildArchive(jid, seq == 1, at, key)
		pin := appstate.BuildPin(jid, seq == 1)
		read := appstate.BuildMarkChatAsRead(jid, seq == 1, at, key)
		mutations := append(append(archive.Mutations[:1], pin.Mutations...), read.Mutations...)
		mutations = append(mutations, appstate.MutationInfo{Index: []string{appstate.IndexSettingUnarchiveChats}, Version: 4, Value: &waSyncAction.SyncActionValue{UnarchiveChatsSetting: &waSyncAction.UnarchiveChatsSetting{UnarchiveChats: proto.Bool(seq != 1)}}})
		v, h, e := sender.AppState.GetAppStateVersion(ctx, string(collection))
		must(e)
		raw, e := appstate.NewProcessor(sender, log.Noop).EncodePatch(ctx, []byte{1}, appstate.HashState{Version: v, Hash: h}, appstate.PatchInfo{Type: collection, Timestamp: at.Add(time.Duration(seq) * time.Millisecond), Mutations: mutations})
		must(e)
		patch := &waServerSync.SyncdPatch{}
		must(proto.Unmarshal(raw, patch))
		patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(v + 1)}
		list := &appstate.PatchList{Name: collection, Patches: []*waServerSync.SyncdPatch{patch}}
		var ignored, held []any
		_, e = source.DangerousInternals().ApplyAppStatePatches(ctx, collection, appstate.HashState{Version: v, Hash: h}, list, false, &ignored)
		must(e)
		_, e = sdk.DangerousInternals().ApplyAppStatePatches(ctx, collection, appstate.HashState{Version: v, Hash: h}, list, false, &held)
		must(e)
		return held
	}
	older := apply(1)
	must(a.db.UpsertMessage(appstore.UpsertMessageParams{ChatJID: jid.String(), MsgID: "newer", SenderJID: jid.String(), Timestamp: at, Text: "synthetic"}))
	must(a.db.IncrementChatUnread(jid.String()))
	newer := apply(2)
	for _, batch := range [][]any{newer, older} {
		for _, evt := range batch {
			sdk.DangerousInternals().DispatchEvent(evt)
		}
	}
	// The old disabled-setting callback is queued behind the local reservation.
	// It must not become an observation hint after the newer setting committed.
	_, _, disabled := a.observedArchiveBoundary(ctx, jid)
	if disabled {
		t.Fatal("old covered callback installed a stale archive-setting hint")
	}
	must(a.db.UpsertMessage(appstore.UpsertMessageParams{ChatJID: jid.String(), MsgID: "newest", SenderJID: jid.String(), Timestamp: at, Text: "synthetic"}))
	must(a.db.IncrementChatUnread(jid.String()))
	obsoleteWriteCalled := false
	must(a.completeCoveredLocalAppStateWrite(ctx, &pending, nil, func() error { obsoleteWriteCalled = true; return a.db.SetChatPinned(jid.String(), true) }))
	must(a.appStatePersist.waitIdle(ctx))
	chat, err := a.db.GetChat(jid.String())
	must(err)
	if obsoleteWriteCalled || chat.Archived || chat.Pinned || !chat.Unread || chat.UnreadCount != 2 {
		t.Fatalf("old writer regressed newer state: called=%t chat=%+v", obsoleteWriteCalled, chat)
	}
	nativePending, err := device.AppState.(sdkstore.AppStateDeliveryStore).PendingAppStateDelivery(ctx)
	must(err)
	if nativePending != nil {
		t.Fatal("unexpected pending projection")
	}
}
