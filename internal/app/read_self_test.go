package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
)

func TestReadSelfThroughSnapshotsHistoryAndKeepsNewerUnread(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("456", types.DefaultUserServer)
	when := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	if err := a.db.UpsertChat(chat.String(), "dm", "Chat", when); err != nil {
		t.Fatal(err)
	}
	add := func(id string, ts time.Time, mutate func(*store.UpsertMessageParams)) {
		t.Helper()
		m := store.UpsertMessageParams{ChatJID: chat.String(), MsgID: id, SenderJID: chat.String(), Timestamp: ts, Text: id}
		if mutate != nil {
			mutate(&m)
		}
		if err := a.db.UpsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	var want []types.MessageID
	for i := 0; i < 101; i++ {
		id := fmt.Sprintf("old-%03d", i)
		add(id, when.Add(-time.Minute), nil)
		want = append(want, types.MessageID(id))
	}
	add("reaction", when.Add(-time.Second), func(m *store.UpsertMessageParams) { m.ReactionToID, m.ReactionEmoji = "old-000", "x" })
	add("revoked", when.Add(-time.Second), func(m *store.UpsertMessageParams) { m.Revoked = true })
	add("deleted", when.Add(-time.Second), func(m *store.UpsertMessageParams) { m.DeletedForMe = true })
	add("protocol", when.Add(-time.Second), func(m *store.UpsertMessageParams) { m.Text, m.DisplayText = "", "(message)" })
	add("mine", when.Add(-time.Second), func(m *store.UpsertMessageParams) { m.FromMe = true })
	add("handled", when, nil)
	want = append(want, "handled")
	add("same-second-newer", when, nil)
	add("later", when.Add(time.Second), nil)
	// The counter omits old unread history. Selection must still include every
	// eligible older ID and ignore all newer rows, including arrivals mid-send.
	if err := a.db.SetChatUnreadCount(chat.String(), 3); err != nil {
		t.Fatal(err)
	}
	if err := a.db.MarkAppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil {
		t.Fatal(err)
	}
	f.onReadReceipt = func(index int) (types.ReceiptType, error) {
		if index == 0 {
			add("during-send", when, nil)
			if err := a.db.SetChatUnreadCount(chat.String(), 4); err != nil {
				t.Fatal(err)
			}
		}
		return types.ReceiptTypeReadSelf, nil
	}
	n, kind, err := a.MarkChatReadSelfThrough(context.Background(), chat, "handled")
	if err != nil || n != 102 || kind != types.ReceiptTypeReadSelf {
		t.Fatalf("sent=%d kind=%s error=%v", n, kind, err)
	}
	var got []types.MessageID
	for _, call := range f.readSelfCalls {
		if len(call.ids) > 100 || call.chat != chat || !call.sender.IsEmpty() {
			t.Fatalf("wrong private batch: %+v", call)
		}
		got = append(got, call.ids...)
	}
	if !slices.Equal(got, want) || len(f.readSelfCalls) != 2 || len(f.readReceiptCalls) != 0 || len(f.appStateFetches) != 0 || len(f.markReadCalls) != 0 {
		t.Fatalf("private IDs=%v, expected=%v; wrong transport or recovery used", got, want)
	}
	stored, err := a.db.GetChat(chat.String())
	if err != nil || !stored.Unread || stored.UnreadCount != 3 {
		t.Fatalf("newer unread state=%+v error=%v", stored, err)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || !required {
		t.Fatalf("private read changed recovery marker: %v, %v", required, err)
	}
}

func TestReadSelfThroughStaleZeroMarkerSkipsGroupTransport(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("123", types.GroupServer)
	f.groups[chat] = &types.GroupInfo{JID: chat}
	var msgs []store.UpsertMessageParams
	// The zero counter might omit genuinely unread older history. Retaining
	// the marker deliberately takes priority over acknowledging that prefix.
	for i := range 102 {
		msgs = append(msgs, store.UpsertMessageParams{MsgID: fmt.Sprintf("old-%03d", i), SenderJID: "111@s.whatsapp.net"})
	}
	msgs = append(msgs, store.UpsertMessageParams{MsgID: "handled", SenderJID: "111@s.whatsapp.net"}, store.UpsertMessageParams{MsgID: "newer", SenderJID: "222@s.whatsapp.net"})
	seedReceiptChat(t, a, chat, "group", msgs, 0)
	if err := a.db.SetChatUnread(chat.String(), true); err != nil {
		t.Fatal(err)
	}
	n, kind, err := a.MarkChatReadSelfThrough(t.Context(), chat, "handled")
	if err != nil || n != 0 || kind != "" || len(f.readSelfCalls) != 0 || f.groupInfoCalls != 0 {
		t.Fatalf("marker-only prefix used group transport: %d %q %v group lookups=%d", n, kind, err, f.groupInfoCalls)
	}
	c, err := a.db.GetChat(chat.String())
	if err != nil || !c.Unread || c.UnreadCount != 0 {
		t.Fatalf("stale-zero pending state lost: %+v %v", c, err)
	}
}

func TestReadSelfThroughRejectsInvalidBoundaryBeforeDispatch(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("456", types.DefaultUserServer)
	other := types.NewJID("789", types.DefaultUserServer)
	seedReceiptChat(t, a, chat, "dm", []store.UpsertMessageParams{
		{MsgID: "valid", SenderJID: chat.String()},
		{MsgID: "outbound", FromMe: true},
		{MsgID: "reaction", ReactionToID: "valid", ReactionEmoji: "x"},
		{MsgID: "revoked", Revoked: true},
		{MsgID: "deleted", DeletedForMe: true},
	}, 1)
	seedReceiptChat(t, a, other, "dm", []store.UpsertMessageParams{{MsgID: "other-chat", SenderJID: other.String()}}, 1)
	for _, m := range []store.UpsertMessageParams{
		{ChatJID: chat.String(), MsgID: "protocol", SenderJID: chat.String(), Timestamp: time.Now(), DisplayText: "(message)"},
		{ChatJID: chat.String(), MsgID: "undated", SenderJID: chat.String(), Text: "undated"},
	} {
		if err := a.db.UpsertMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"", " valid ", "missing", "other-chat", "outbound", "reaction", "revoked", "deleted", "protocol", "undated"} {
		if _, _, err := a.MarkChatReadSelfThrough(context.Background(), chat, id); err == nil {
			t.Errorf("accepted invalid boundary %q", id)
		}
	}
	if len(f.readSelfCalls) != 0 || len(f.readReceiptCalls) != 0 || len(f.appStateFetches) != 0 {
		t.Fatal("invalid boundary dispatched network work")
	}
	stored, err := a.db.GetChat(chat.String())
	if err != nil || stored.UnreadCount != 1 || !stored.Unread {
		t.Fatalf("invalid boundary changed local unread state: %+v %v", stored, err)
	}
}

func TestReadSelfThroughGroupBatchesAndPartialFailurePreserveLocalState(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	chat := types.NewJID("120363000000000001", types.GroupServer)
	alice := types.NewJID("111", types.DefaultUserServer)
	bob := types.NewJID("222", types.HiddenUserServer)
	f.groups[chat] = &types.GroupInfo{JID: chat, AddressingMode: types.AddressingModeLID}
	seedReceiptChat(t, a, chat, "group", []store.UpsertMessageParams{
		{MsgID: "a", SenderJID: alice.String()},
		{MsgID: "b", SenderJID: bob.String()},
	}, 1)
	if err := a.db.MarkAppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("second receipt failed")
	f.onReadReceipt = func(index int) (types.ReceiptType, error) {
		if index == 1 {
			return "", failure
		}
		return types.ReceiptTypeReadSelf, nil
	}
	n, _, err := a.MarkChatReadSelfThrough(context.Background(), chat, "b")
	if !errors.Is(err, failure) || n != 1 {
		t.Fatalf("partial dispatch=%d error=%v", n, err)
	}
	if len(f.readSelfCalls) != 2 || f.readSelfCalls[0].sender != alice || f.readSelfCalls[1].sender != bob || f.readSelfCalls[0].addressing != types.AddressingModeLID {
		t.Fatalf("group addressing/batching=%+v", f.readSelfCalls)
	}
	stored, err := a.db.GetChat(chat.String())
	if err != nil || stored.UnreadCount != 1 || !stored.Unread {
		t.Fatalf("partial dispatch changed local unread state: %+v %v", stored, err)
	}
	if required, err := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow)); err != nil || !required {
		t.Fatalf("partial dispatch changed recovery marker: %v %v", required, err)
	}
	// A retry can safely resend the private first batch and finish the same range.
	f.onReadReceipt = nil
	if n, kind, err := a.MarkChatReadSelfThrough(context.Background(), chat, "b"); err != nil || n != 2 || kind != types.ReceiptTypeReadSelf {
		t.Fatalf("retry=%d %s %v", n, kind, err)
	}
	stored, err = a.db.GetChat(chat.String())
	if err != nil || stored.UnreadCount != 0 || stored.Unread {
		t.Fatalf("successful retry did not clear handled range: %+v %v", stored, err)
	}
}
