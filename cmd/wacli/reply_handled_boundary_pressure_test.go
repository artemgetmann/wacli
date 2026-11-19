package main

import (
	"context"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

// Acceptance regression: a reply to A must not acknowledge B merely because B
// arrived before dispatch. This is intentionally red on candidate 77030fa.
func TestReplyDoesNotAcknowledgeMessageArrivingWhileDrafting(t *testing.T) {
	db := openSendTestDB(t)
	to := types.NewJID("15550000001", types.DefaultUserServer)
	at := time.Now().UTC().Truncate(time.Second)
	if err := db.UpsertChat(to.String(), "dm", "Fixture", at); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"ANSWERED_A", "UNANSWERED_B"} {
		if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: to.String(), MsgID: id, SenderJID: to.String(), Timestamp: at.Add(time.Duration(i) * time.Second), Text: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetChatUnreadCount(to.String(), 2); err != nil {
		t.Fatal(err)
	}
	transport := &historyTextWA{sender: &recordingTextSender{}}
	a := &fakeSendFileApp{wa: transport, db: db}
	id, warning, err := sendTextWithHistory(context.Background(), a, nil, to, "reply to A", "ANSWERED_A", "", nil, nil, textEphemeralOptions{}, textSendOptions{})
	if err != nil || warning != nil || id == "" {
		t.Fatalf("reply failed: id=%q warning=%v error=%v", id, warning, err)
	}
	chat, err := db.GetChat(to.String())
	if err != nil {
		t.Fatal(err)
	}
	if !chat.Unread || chat.UnreadCount < 1 {
		t.Fatalf("unanswered B lost: unread=%t count=%d; latest-at-send is not proof of handled input", chat.Unread, chat.UnreadCount)
	}
}

// A message stored after the snapshot remains unanswered even if its timestamp
// is older than the handled anchor, e.g. because it arrived late.
func TestReplyDoesNotAcknowledgeLateBackdatedInbound(t *testing.T) {
	db := openSendTestDB(t)
	to := types.NewJID("15550000001", types.DefaultUserServer)
	at := time.Now().UTC().Truncate(time.Second)
	if err := db.UpsertChat(to.String(), "dm", "Fixture", at); err != nil {
		t.Fatal(err)
	}
	add := func(id string, ts time.Time) {
		t.Helper()
		if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: to.String(), MsgID: id, SenderJID: to.String(), Timestamp: ts, Text: id}); err != nil {
			t.Fatal(err)
		}
	}
	add("ANSWERED_A", at)
	if err := db.TrackLiveUnread(to.String(), "ANSWERED_A"); err != nil {
		t.Fatal(err)
	}
	transport := &historyTextWA{sender: &recordingTextSender{}, afterSend: func() {
		add("LATE_UNANSWERED_B", at.Add(-time.Minute))
		if err := db.TrackLiveUnread(to.String(), "LATE_UNANSWERED_B"); err != nil {
			t.Fatal(err)
		}
	}}
	a := &fakeSendFileApp{wa: transport, db: db}
	_, warning, err := sendTextWithHistory(context.Background(), a, nil, to, "reply to A", "ANSWERED_A", "", nil, nil, textEphemeralOptions{}, textSendOptions{handledThrough: "ANSWERED_A"})
	if err != nil || warning != nil {
		t.Fatalf("warning=%v error=%v", warning, err)
	}
	chat, err := db.GetChat(to.String())
	if err != nil {
		t.Fatal(err)
	}
	if !chat.Unread || chat.UnreadCount < 1 {
		t.Fatalf("late unanswered B lost: unread=%t count=%d; timestamp prefix included an arrival absent from the captured input", chat.Unread, chat.UnreadCount)
	}
}
