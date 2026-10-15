package app

import (
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// A valid handled anchor does not account for unread messages absent from the
// local history. Refuse before any receipt or group-addressing lookup.
func TestReadSelfIncompleteHistoryRejectsBeforeDispatch(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "group"}[group], func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			chat := types.NewJID("456", types.DefaultUserServer)
			kind := "dm"
			if group {
				chat.Server, kind = types.GroupServer, "group"
			}
			seedReceiptChat(t, a, chat, kind, []store.UpsertMessageParams{
				{MsgID: "handled", SenderJID: "111@s.whatsapp.net"},
				{MsgID: "mine", FromMe: true},
				{MsgID: "reaction", ReactionToID: "handled", ReactionEmoji: "x"},
				{MsgID: "revoked", Revoked: true},
				{MsgID: "deleted", DeletedForMe: true},
			}, 3)
			n, receipt, err := a.MarkChatReadSelfThrough(t.Context(), chat, "handled")
			if err == nil || !strings.Contains(err.Error(), "3 unread messages but only 1 eligible") || n != 0 || receipt != "" {
				t.Fatalf("dispatch=%d receipt=%q error=%v; want incomplete-history rejection", n, receipt, err)
			}
			if len(f.readSelfCalls) != 0 || len(f.readReceiptCalls) != 0 || f.groupInfoCalls != 0 {
				t.Fatal("incomplete history performed transport work")
			}
			c, err := a.db.GetChat(chat.String())
			if err != nil || !c.Unread || c.UnreadCount != 3 {
				t.Fatalf("unknown pending state changed: %+v %v", c, err)
			}
		})
	}
}

// History-sync metadata can arrive after selection but before completion.
// Preserve the whole count, report the already-dispatched receipt, and prove
// that its later exact-ID echo cannot bypass the same protection.
func TestReadSelfIncompleteHistoryArrivingDuringDispatchStaysPending(t *testing.T) {
	for _, knownNewer := range []bool{false, true} {
		t.Run(map[bool]string{false: "anchor_only", true: "known_newer"}[knownNewer], func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			a.wa = f
			chat := types.NewJID("456", types.DefaultUserServer)
			msgs := []store.UpsertMessageParams{{MsgID: "handled", SenderJID: chat.String()}}
			if knownNewer {
				msgs = append(msgs, store.UpsertMessageParams{MsgID: "newer", SenderJID: chat.String()})
			}
			seedReceiptChat(t, a, chat, "dm", msgs, 1)
			count := len(msgs) + 2
			f.onReadReceipt = func(int) (types.ReceiptType, error) {
				return types.ReceiptTypeReadSelf, a.db.SetChatUnreadCount(chat.String(), count)
			}
			n, receipt, err := a.MarkChatReadSelfThrough(t.Context(), chat, "handled")
			if err == nil || !strings.Contains(err.Error(), "private receipts sent") || n != 1 || receipt != types.ReceiptTypeReadSelf || len(f.readSelfCalls) != 1 {
				t.Errorf("completion dispatch=%d receipt=%q error=%v", n, receipt, err)
			}
			check := func(stage string) {
				t.Helper()
				c, err := a.db.GetChat(chat.String())
				if err != nil || !c.Unread || c.UnreadCount != count {
					t.Errorf("%s discarded unknown pending messages: %+v %v; want count=%d", stage, c, err, count)
				}
			}
			check("completion")
			a.handleReceiptPersistenceEvent(t.Context(), &events.Receipt{
				MessageSource: types.MessageSource{Chat: chat}, Type: types.ReceiptTypeReadSelf,
				Timestamp: time.Now(), MessageIDs: []types.MessageID{"handled"},
			})
			check("receipt echo")
		})
	}
}
