package app

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// A receipt echo from a completed participant batch must not complete the
// other participant's batch when that batch fails to dispatch.
func TestPrivateReceiptEchoDoesNotClearUnsentParticipant(t *testing.T) {
	a := newTestApp(t)
	f := newFakeWA()
	a.wa = f
	var stored, last atomic.Int64
	handler, _ := a.addSyncEventHandler(t.Context(), SyncOptions{}, &stored, &last, make(chan struct{}, 1), make(chan struct{}, 1), make(chan staleReconnectRequest, 1), nil, nil, nil, &syncPresence{}, nil)
	t.Cleanup(func() { f.RemoveEventHandler(handler) })
	chat := types.NewJID("123", types.GroupServer)
	one := types.NewJID("111", types.DefaultUserServer)
	two := types.NewJID("222", types.DefaultUserServer)
	f.groups[chat] = &types.GroupInfo{JID: chat, AddressingMode: types.AddressingModeLID}
	seedReceiptChat(t, a, chat, "group", []store.UpsertMessageParams{
		{MsgID: "A1", SenderJID: one.String()},
		{MsgID: "B1_UNSENT", SenderJID: two.String()},
		{MsgID: "A2", SenderJID: one.String()},
	}, 3)
	f.onReadReceipt = func(index int) (types.ReceiptType, error) {
		if index == 0 {
			first := f.readSelfCalls[0]
			f.emit(&events.Receipt{
				MessageSource: types.MessageSource{Chat: chat}, Type: types.ReceiptTypeReadSelf,
				Timestamp: time.Now(), MessageIDs: first.ids,
			})
			return types.ReceiptTypeReadSelf, nil
		}
		return "", fmt.Errorf("second participant receipt failed")
	}
	_, _, err := a.MarkChatReadSelfThrough(t.Context(), chat, "A2")
	if err == nil {
		t.Fatal("expected partial receipt failure")
	}
	state, err := a.db.GetChat(chat.String())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Unread || state.UnreadCount < 1 {
		t.Fatalf("unsent participant lost after first-batch echo: unread=%t count=%d", state.Unread, state.UnreadCount)
	}
}
