package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

type handledDelegateFixture struct {
	*fakeSendFileApp
	dir string
}

func (f *handledDelegateFixture) StoreDir() string { return f.dir }
func (f *handledDelegateFixture) Connect(context.Context, bool, func(string)) error {
	return fmt.Errorf("unexpected reconnect")
}

// Exercise the production Unix socket, request framing, delegate executor,
// accepted-message persistence and exact private completion in one offline run.
func TestHandledReplyActualDelegateSocket(t *testing.T) {
	dir := shortPresenceDelegateStoreDir(t)
	db, err := store.Open(filepath.Join(dir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	chat := types.NewJID("15550000001", types.DefaultUserServer)
	at := time.Now().UTC()
	if err = db.UpsertChat(chat.String(), "dm", "synthetic", at); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ANSWERED", "NEWER"} {
		if err = db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat.String(), MsgID: id, SenderJID: chat.String(), Timestamp: at, Text: id}); err != nil {
			t.Fatal(err)
		}
		if err = db.TrackLiveUnread(chat.String(), id); err != nil {
			t.Fatal(err)
		}
	}
	wa := &historyTextWA{sender: &recordingTextSender{}}
	a := &handledDelegateFixture{fakeSendFileApp: &fakeSendFileApp{wa: wa, db: db}, dir: dir}
	stop, err := startSendDelegateServerForStore(t.Context(), dir, sendSpacing{}, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
		if req.Kind != handledTextKind {
			return sendDelegateResponse{}, fmt.Errorf("unexpected kind")
		}
		return executeDelegatedText(ctx, a, req)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	resp, err := delegateSend(t.Context(), &rootFlags{storeDir: dir, timeout: 2 * time.Second}, sendDelegateRequest{Kind: handledTextKind, To: chat.String(), Message: "synthetic reply", HandledThrough: "ANSWERED", NoPreview: true})
	if err != nil || !resp.Sent || resp.ID == "" || resp.LocalRead == nil || resp.LocalRead.Status != "dispatched" {
		t.Fatalf("response=%+v error=%v", resp, err)
	}
	if len(wa.privateCalls) != 1 || len(wa.privateCalls[0]) != 1 || wa.privateCalls[0][0] != "ANSWERED" {
		t.Fatalf("scope=%v", wa.privateCalls)
	}
	state, err := db.GetChat(chat.String())
	if err != nil || !state.Unread || state.UnreadCount != 1 {
		t.Fatalf("pending=%+v error=%v", state, err)
	}
	if _, err = db.GetMessage(chat.String(), resp.ID); err != nil {
		t.Fatalf("accepted reply not saved: %v", err)
	}
}

func TestHandledReplyLegacyDelegateKindRefusesBeforeSend(t *testing.T) {
	// The production dispatcher rejects a scoped payload smuggled into an old
	// text kind, and rejects a scoped kind without its mandatory boundary.
	for _, req := range []sendDelegateRequest{
		{Version: sendDelegateVersion, Kind: "text", HandledThrough: "ANSWERED"},
		{Version: sendDelegateVersion, Kind: handledTextKind},
		{Version: sendDelegateVersion, Kind: handledTextKind, HandledThrough: "ANSWERED", PreserveUnread: true},
	} {
		if _, err := executeDelegatedSend(t.Context(), nil, req); err == nil {
			t.Fatal("invalid scoped request reached dispatch")
		}
	}
}
