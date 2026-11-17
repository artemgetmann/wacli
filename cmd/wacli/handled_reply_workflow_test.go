package main

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

// Drive the same send/save/completion workflow used by direct and delegated text
// sends. The fake replaces only WhatsApp transport, never selection/settlement.
func TestHandledReplyWorkflowExactScopeAndFailureGates(t *testing.T) {
	for _, mode := range []string{"success", "send_failed", "send_uncertain", "ack_uncertain", "unknown_history"} {
		t.Run(mode, func(t *testing.T) {
			db := openSendTestDB(t)
			to := types.NewJID("15550000001", types.DefaultUserServer)
			at := time.Now().UTC().Truncate(time.Second)
			if err := db.UpsertChat(to.String(), "dm", "synthetic", at); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"ANSWERED_A", "UNANSWERED_B"} {
				if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: to.String(), MsgID: id, SenderJID: to.String(), Timestamp: at, Text: id}); err != nil {
					t.Fatal(err)
				}
				if err := db.TrackLiveUnread(to.String(), id); err != nil {
					t.Fatal(err)
				}
			}
			wa := &historyTextWA{sender: &recordingTextSender{}}
			switch mode {
			case "send_failed":
				wa.sendFailure = fmt.Errorf("rejected fixture send")
			case "send_uncertain":
				wa.sendFailure = context.DeadlineExceeded
			case "ack_uncertain":
				wa.privateErr = context.DeadlineExceeded
			case "unknown_history":
				if err := db.SetChatUnreadCount(to.String(), 2); err != nil {
					t.Fatal(err)
				}
			}
			state := textReplyReadState{}
			id, warning, err := sendTextWithHistory(context.Background(), &fakeSendFileApp{wa: wa, db: db}, nil, to, "reply", "", "", nil, nil, textEphemeralOptions{}, textSendOptions{handledThrough: "ANSWERED_A", localRead: &state})
			if mode == "send_failed" || mode == "send_uncertain" {
				if err == nil || id != "" || len(wa.privateCalls) != 0 {
					t.Fatalf("failed send acknowledged: id=%q calls=%v err=%v", id, wa.privateCalls, err)
				}
			} else {
				if err != nil || id == "" {
					t.Fatalf("accepted identity lost: %q %v", id, err)
				}
				if mode == "unknown_history" {
					if len(wa.privateCalls) != 0 || warning == nil || state.Status != "deferred" {
						t.Fatalf("unknown membership: %+v %v", state, warning)
					}
				} else if len(wa.privateCalls) != 1 || !slices.Equal(wa.privateCalls[0], []types.MessageID{"ANSWERED_A"}) {
					t.Fatalf("scope widened: %v", wa.privateCalls)
				}
				if mode == "ack_uncertain" && (warning == nil || state.Status != "uncertain") {
					t.Fatalf("ack result=%+v warning=%v", state, warning)
				}
			}
			want := 2
			if mode == "success" {
				want = 1
			}
			chat, getErr := db.GetChat(to.String())
			if getErr != nil || !chat.Unread || chat.UnreadCount != want {
				t.Fatalf("pending=%+v want=%d error=%v", chat, want, getErr)
			}
			if wa.sender.textCalls != 1 {
				t.Fatalf("send retried %d times", wa.sender.textCalls)
			}
		})
	}
}
