package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// Exercise the daemon's production event registration, not only the direct
// store update. The same event persistence code also serves one-shot writes.
func installBoundaryReplayHandler(t *testing.T, a *App, f *fakeWA) {
	t.Helper()
	var stored, last atomic.Int64
	id, _ := a.addSyncEventHandler(t.Context(), SyncOptions{}, &stored, &last,
		make(chan struct{}, 1), make(chan struct{}, 1), make(chan staleReconnectRequest, 1),
		func(string, string) {}, nil, nil, &syncPresence{}, nil)
	t.Cleanup(func() { f.RemoveEventHandler(id) })
}

func boundaryReadEvent(chat types.JID, at time.Time, id string) *events.MarkChatAsRead {
	return &events.MarkChatAsRead{
		JID: chat, Timestamp: at.Add(time.Hour),
		Action: &waSyncAction.MarkChatAsReadAction{
			Read: proto.Bool(true),
			MessageRange: &waSyncAction.SyncActionMessageRange{
				LastMessageTimestamp: proto.Int64(at.Unix()),
				Messages: []*waSyncAction.SyncActionMessage{{
					Key: &waCommon.MessageKey{ID: proto.String(id)}, Timestamp: proto.Int64(at.Unix()),
				}},
			},
		},
	}
}

func assertBoundaryMarker(t *testing.T, a *App, chat types.JID, want bool) {
	t.Helper()
	if err := a.appStatePersist.waitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err := a.db.GetChat(chat.String())
	if err != nil || c.Unread != want || c.UnreadCount != 0 {
		t.Fatalf("boundary marker = %+v, %v; want unread=%t count=0", c, err, want)
	}
}

func TestExplicitReadBoundaryMarkerSurvivesAppStateEchoAndReplay(t *testing.T) {
	for _, timing := range []string{"before-apply", "after-apply", "after-completion", "full-replay-after-reopen"} {
		t.Run(timing, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			chat, at := seedExplicitReadBoundary(t, a, false)
			if err := a.db.SetChatUnreadCount(chat.String(), 0); err != nil {
				t.Fatal(err)
			}
			if err := a.db.SetChatUnread(chat.String(), true); err != nil {
				t.Fatal(err)
			}
			installBoundaryReplayHandler(t, a, f)
			echo := func() { f.emit(boundaryReadEvent(chat, at, "handled")) }
			if timing == "before-apply" {
				f.markReadBeforeApply = echo
			}
			if timing == "after-apply" {
				f.markReadAfterApply = echo
			}
			if err := a.MarkChatReadThrough(t.Context(), chat, "handled"); err != nil {
				t.Fatal(err)
			}
			if timing == "after-completion" {
				echo()
			}
			if timing == "full-replay-after-reopen" {
				// No in-memory origin flag survives this boundary. Replay must interpret
				// the event's exact message range using the persisted marker and rows.
				dir := a.opts.StoreDir
				a.Close()
				reopened, err := New(Options{StoreDir: dir})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(reopened.Close)
				a = reopened
				f = newFakeWA()
				a.wa = f
				installBoundaryReplayHandler(t, a, f)
				replayed := false
				f.appStateFetchEvent = func(_ string, fullSync, _ bool) any {
					if !fullSync {
						t.Fatal("expected full replay")
					}
					replayed = true
					evt := boundaryReadEvent(chat, at, "handled")
					evt.FromFullSync = true
					return evt
				}
				if err := a.db.MarkAppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil {
					t.Fatal(err)
				}
				if err := a.MarkChatReadThrough(t.Context(), chat, "handled"); err != nil {
					t.Fatal(err)
				}
				if !replayed {
					t.Fatal("read boundary was not replayed")
				}
			}
			assertBoundaryMarker(t, a, chat, true)
		})
	}
}

func TestExactReadReplayDoesNotResurrectClearedMarker(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat, at := seedExplicitReadBoundary(t, a, false)
	if err := a.db.SetChatUnreadCount(chat.String(), 0); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetChatUnread(chat.String(), true); err != nil {
		t.Fatal(err)
	}
	installBoundaryReplayHandler(t, a, f)
	// A normal no-flag mark-read captures the latest stored message and still
	// clears the marker. A later stale bounded echo must not restore it.
	if err := a.MarkChatRead(t.Context(), chat, true); err != nil {
		t.Fatal(err)
	}
	f.emit(boundaryReadEvent(chat, at, "newer"))
	assertBoundaryMarker(t, a, chat, false)
	evt := boundaryReadEvent(chat, at, "handled")
	evt.FromFullSync = true
	f.emit(evt)
	assertBoundaryMarker(t, a, chat, false)
}

func TestTimestampOnlyReadEventKeepsLegacyMarkerClearing(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat, at := seedExplicitReadBoundary(t, a, false)
	if err := a.db.SetChatUnreadCount(chat.String(), 0); err != nil {
		t.Fatal(err)
	}
	if err := a.db.SetChatUnread(chat.String(), true); err != nil {
		t.Fatal(err)
	}
	installBoundaryReplayHandler(t, a, f)
	f.emit(&events.MarkChatAsRead{
		JID: chat, Timestamp: at,
		Action: &waSyncAction.MarkChatAsReadAction{Read: proto.Bool(true)},
	})
	assertBoundaryMarker(t, a, chat, false)
}

func TestExactReadReceiptEventPreservesMarkerWithoutResurrection(t *testing.T) {
	for _, kind := range []types.ReceiptType{types.ReceiptTypeReadSelf, types.ReceiptTypeRead} {
		t.Run(string(kind), func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			chat, at := seedExplicitReadBoundary(t, a, false)
			if err := a.db.SetChatUnreadCount(chat.String(), 0); err != nil {
				t.Fatal(err)
			}
			if err := a.db.SetChatUnread(chat.String(), true); err != nil {
				t.Fatal(err)
			}
			installBoundaryReplayHandler(t, a, f)
			receipt := &events.Receipt{MessageSource: types.MessageSource{Chat: chat, IsFromMe: true},
				Type: kind, Timestamp: at.Add(time.Hour), MessageIDs: []types.MessageID{"handled"}}
			f.emit(receipt)
			assertBoundaryMarker(t, a, chat, true)
			if err := a.MarkChatRead(context.Background(), chat, true); err != nil {
				t.Fatal(err)
			}
			assertBoundaryMarker(t, a, chat, false)
			f.emit(receipt)
			assertBoundaryMarker(t, a, chat, false)
		})
	}
}

func TestTimestampOnlyReadReceiptKeepsLegacyMarkerClearing(t *testing.T) {
	for _, ids := range [][]types.MessageID{nil, {""}, {" "}} {
		a := newTestApp(t)
		f := newFakeWA()
		a.wa = f
		chat, at := seedExplicitReadBoundary(t, a, false)
		if err := a.db.SetChatUnreadCount(chat.String(), 0); err != nil {
			t.Fatal(err)
		}
		if err := a.db.SetChatUnread(chat.String(), true); err != nil {
			t.Fatal(err)
		}
		installBoundaryReplayHandler(t, a, f)
		f.emit(&events.Receipt{MessageSource: types.MessageSource{Chat: chat},
			Type: types.ReceiptTypeReadSelf, Timestamp: at, MessageIDs: ids})
		assertBoundaryMarker(t, a, chat, false)
	}
}

func TestPrivateReadSelfBoundarySurvivesReceiptEchoAndReopen(t *testing.T) {
	for _, timing := range []string{"during-dispatch", "after-completion", "after-reopen"} {
		t.Run(timing, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			chat, at := seedExplicitReadBoundary(t, a, false)
			// A positive count keeps this on the actual dispatch path. The
			// marker becomes count-free later, so echoes still exercise replay.
			if err := a.db.SetChatUnreadCount(chat.String(), 1); err != nil {
				t.Fatal(err)
			}
			if err := a.db.SetChatUnread(chat.String(), true); err != nil {
				t.Fatal(err)
			}
			installBoundaryReplayHandler(t, a, f)
			receipt := &events.Receipt{MessageSource: types.MessageSource{Chat: chat},
				Type: types.ReceiptTypeReadSelf, Timestamp: at.Add(time.Hour), MessageIDs: []types.MessageID{"handled"}}
			markOnly := func() {
				t.Helper()
				if err := a.db.SetChatUnreadCount(chat.String(), 0); err != nil {
					t.Fatal(err)
				}
				if err := a.db.SetChatUnread(chat.String(), true); err != nil {
					t.Fatal(err)
				}
			}
			if timing == "during-dispatch" {
				f.onReadReceipt = func(int) (types.ReceiptType, error) {
					markOnly()
					f.emit(receipt)
					return types.ReceiptTypeReadSelf, nil
				}
			}
			if n, _, err := a.MarkChatReadSelfThrough(t.Context(), chat, "handled"); err != nil || n != 2 || len(f.readSelfCalls) != 1 {
				t.Fatalf("expected actual private dispatch: %d %v", n, err)
			}
			if timing != "during-dispatch" {
				markOnly()
			}
			if timing == "after-reopen" {
				dir := a.opts.StoreDir
				a.Close()
				reopened, err := New(Options{StoreDir: dir})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(reopened.Close)
				a = reopened
				f = newFakeWA()
				a.wa = f
				installBoundaryReplayHandler(t, a, f)
			}
			if timing != "during-dispatch" {
				f.emit(receipt)
			}
			assertBoundaryMarker(t, a, chat, true)
			if err := a.MarkChatRead(t.Context(), chat, true); err != nil {
				t.Fatal(err)
			}
			f.emit(receipt)
			assertBoundaryMarker(t, a, chat, false)
		})
	}
}
