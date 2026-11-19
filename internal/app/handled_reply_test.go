package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Emit receipt and app-state echoes through registered production handlers.
// The snapshot/dispatch/settlement path remains real; only transport is faked.
func TestHandledReplyGroupEchoFailureAndReopen(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial_failure=%t", fail), func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			chat := types.NewJID("123", types.GroupServer)
			one := types.NewJID("111", types.DefaultUserServer)
			two := types.NewJID("222", types.DefaultUserServer)
			f.groups[chat] = &types.GroupInfo{JID: chat, AddressingMode: types.AddressingModeLID}
			at := time.Unix(1700000000, 0)
			if err := a.db.UpsertChat(chat.String(), "group", "synthetic", at); err != nil {
				t.Fatal(err)
			}
			add := func(id string, sender types.JID, ts time.Time) {
				t.Helper()
				if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: id, SenderJID: sender.String(), Timestamp: ts, Text: id}); err != nil {
					t.Fatal(err)
				}
				if err := a.db.TrackLiveUnread(chat.String(), id); err != nil {
					t.Fatal(err)
				}
			}
			add("A1", one, at)
			add("B1", two, at.Add(time.Second))
			add("A2", one, at.Add(2*time.Second))
			snap, err := a.db.SnapshotHandledUnread(chat.String(), "A2")
			if err != nil {
				t.Fatal(err)
			}
			installBoundaryReplayHandler(t, a, f)
			f.onReadReceipt = func(index int) (types.ReceiptType, error) {
				if index == 0 {
					f.emit(&events.Receipt{MessageSource: types.MessageSource{Chat: chat}, Type: types.ReceiptTypeReadSelf, Timestamp: time.Now(), MessageIDs: f.readSelfCalls[0].ids})
					add("LATE_BACKDATED", two, at.Add(-time.Minute))
				}
				if fail && index == 1 {
					return "", context.DeadlineExceeded
				}
				return types.ReceiptTypeReadSelf, nil
			}
			state, err := CompleteHandledReply(t.Context(), a.db, f, chat, snap)
			want := 1
			if fail {
				want = 4
				if err == nil || state.Status != "uncertain" {
					t.Fatalf("state=%+v error=%v", state, err)
				}
			} else if err != nil || state.Status != "dispatched" {
				t.Fatalf("state=%+v error=%v", state, err)
			}
			if len(f.readSelfCalls) != 2 || len(f.readReceiptCalls) != 0 || len(f.markReadCalls) != 0 || len(f.appStateFetches) != 0 {
				t.Fatal("wrong transport, fallback or retry")
			}
			for _, call := range f.readSelfCalls {
				for _, id := range call.ids {
					if id == "LATE_BACKDATED" {
						t.Fatal("new input acknowledged")
					}
				}
			}
			check := func() {
				t.Helper()
				c, e := a.db.GetChat(chat.String())
				if e != nil || !c.Unread || c.UnreadCount != want {
					t.Fatalf("pending=%+v want=%d error=%v", c, want, e)
				}
			}
			check()
			// Neither in-flight nor replayed echoes may reinterpret selected IDs
			// as permission to clear a timestamp prefix or a whole-chat badge.
			dir := a.opts.StoreDir
			a.Close()
			a, err = New(Options{StoreDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.Close)
			f = newFakeWA()
			a.wa = f
			installBoundaryReplayHandler(t, a, f)
			f.emit(&events.Receipt{MessageSource: types.MessageSource{Chat: chat}, Type: types.ReceiptTypeReadSelf, Timestamp: time.Now(), MessageIDs: []types.MessageID{"A2"}})
			f.emit(boundaryReadEvent(chat, at.Add(2*time.Second), "A2"))
			if err = a.appStatePersist.waitIdle(t.Context()); err != nil {
				t.Fatal(err)
			}
			check()
		})
	}
}

func TestHandledReplyPreflightFailureDoesNotConsumeAttempt(t *testing.T) {
	for _, invalidSender := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid_sender=%t", invalidSender), func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			chat := types.NewJID("123", types.GroupServer)
			sender := types.NewJID("111", types.DefaultUserServer)
			if invalidSender {
				sender = types.JID{}
			} else {
				f.groupInfoErr = fmt.Errorf("temporary group lookup failure")
			}
			if err := a.db.UpsertChat(chat.String(), "group", "synthetic", time.Unix(100, 0)); err != nil {
				t.Fatal(err)
			}
			put := func(sender types.JID) {
				t.Helper()
				if err := a.db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: "shown", SenderJID: sender.String(), Timestamp: time.Unix(100, 0), Text: "shown"}); err != nil {
					t.Fatal(err)
				}
			}
			put(sender)
			if err := a.db.TrackLiveUnread(chat.String(), "shown"); err != nil {
				t.Fatal(err)
			}
			snapshot, err := a.db.SnapshotHandledUnread(chat.String(), "shown")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = CompleteHandledReply(t.Context(), a.db, f, chat, snapshot); err == nil {
				t.Fatal("preflight failure hidden")
			}
			attempted, err := a.db.HasPrivateReadAttempt(chat.String(), []string{"shown"})
			if err != nil || attempted || len(f.readSelfCalls) != 0 {
				t.Fatalf("preflight consumed attempt: %v %v", attempted, err)
			}
			f.groupInfoErr = nil
			f.groups[chat] = &types.GroupInfo{JID: chat}
			put(types.NewJID("111", types.DefaultUserServer))
			// Repeating completion after repair is safe because no receipt was attempted.
			snapshot, err = a.db.SnapshotHandledUnread(chat.String(), "shown")
			if err != nil {
				t.Fatal(err)
			}
			if state, err := CompleteHandledReply(t.Context(), a.db, f, chat, snapshot); err != nil || state.Status != "dispatched" {
				t.Fatalf("completion after repair=%+v %v", state, err)
			}
		})
	}
}
