package app

import (
	"io"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// Public group-delivery shape: https://github.com/devlikeapro/waha/issues/2337
// All values are synthetic. The outer stanza ID is not a message in the store.
// Only an absent legacy key permits the alias; malformed explicit keys still
// reach the existing App rejection and block milestone recording.
func TestGroupedReceiptMessageIDAliasContract(t *testing.T) {
	const target, other = "SYNTHETIC_TARGET", "SYNTHETIC_OTHER"
	for _, tc := range []struct {
		name   string
		attrs  waBinary.Attrs
		wantID string
		valid  bool
	}{
		{"legacy_key", waBinary.Attrs{"key": target}, target, true},
		{"reported_message_id", waBinary.Attrs{"message_id": target}, target, true},
		{"legacy_key_precedence", waBinary.Attrs{"key": target, "message_id": other}, target, true},
		{"neither", waBinary.Attrs{}, "", false},
		{"explicit_empty_key_not_overridden", waBinary.Attrs{"key": "", "message_id": target}, "", false},
		{"explicit_whitespace_key_not_overridden", waBinary.Attrs{"key": " \t", "message_id": target}, " \t", false},
		{"explicit_nonstring_key_not_overridden", waBinary.Attrs{"key": 42, "message_id": target}, "", false},
		{"empty_alias", waBinary.Attrs{"message_id": ""}, "", false},
		{"whitespace_alias", waBinary.Attrs{"message_id": " \t"}, " \t", false},
		{"nonstring_alias", waBinary.Attrs{"message_id": 42}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			f.connected = true
			a.wa = f
			a.opts.Events = out.NewEventWriter(io.Discard, true)
			chat := types.NewJID("group-fixture", types.GroupServer)
			at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := a.db.UpsertChat(chat.String(), "group", "synthetic", at); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{target, other} {
				if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: id, Timestamp: at, FromMe: true, Text: "synthetic"}); err != nil {
					t.Fatal(err)
				}
			}
			var stored, last atomic.Int64
			limits := &syncStorageLimits{app: a}
			a.addSyncEventHandler(t.Context(), SyncOptions{}, &stored, &last, make(chan struct{}, 1), make(chan struct{}, 1), make(chan staleReconnectRequest, 1), nil, nil, limits, &syncPresence{}, nil)
			own := types.NewJID("own-pn-fixture", types.DefaultUserServer)
			sdk := whatsmeow.NewClient(&waStore.Device{ID: &own, LID: types.NewJID("own-lid-fixture", types.HiddenUserServer)}, waLog.Noop)
			var seen []string
			sdk.AddEventHandler(func(evt any) {
				if receipt, ok := evt.(*events.Receipt); ok {
					if receipt.IsFromMe {
						t.Fatal("fixture incorrectly bypasses App receipt validation")
					}
					seen = append(seen, receipt.MessageIDs...)
					f.emit(receipt)
				}
			})
			n := &waBinary.Node{Tag: "receipt", Attrs: waBinary.Attrs{"from": chat, "id": "STANZA_NOT_A_MESSAGE", "t": "1767225600"}, Content: []waBinary.Node{{
				Tag: "participants", Attrs: tc.attrs, Content: []waBinary.Node{{Tag: "user", Attrs: waBinary.Attrs{"jid": types.NewJID("viewer-fixture", types.HiddenUserServer), "t": "1767225600", "type": "delivery"}}},
			}}}
			partial, groups, err := sdk.DangerousInternals().ParseReceipt(n)
			if err != nil || len(groups) != 1 {
				t.Fatal("fixture did not select grouped receipt parsing")
			}
			sdk.DangerousInternals().HandleGroupedReceipt(*partial, &groups[0])
			if !reflect.DeepEqual(seen, []string{tc.wantID}) {
				t.Errorf("emitted IDs=%q want=%q", seen, []string{tc.wantID})
			}
			if limits.persistenceFailed.Load() == tc.valid {
				t.Errorf("failure flag=%v valid=%v", limits.persistenceFailed.Load(), tc.valid)
			}
			for _, id := range []string{target, other} {
				counts, err := a.db.MessageReceipts(chat.String(), id)
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				if tc.valid && id == target {
					want = 1
				}
				if counts.Delivered != want {
					t.Errorf("fixture message=%s delivered=%d want=%d", id, counts.Delivered, want)
				}
			}
			f.emit(&events.OfflineSyncCompleted{Count: 1})
			marker, err := a.db.LastSuccessfulSync()
			if err != nil {
				t.Fatal(err)
			}
			if (marker != nil) != tc.valid {
				t.Errorf("milestone recorded=%v valid=%v", marker != nil, tc.valid)
			}
		})
	}
}
