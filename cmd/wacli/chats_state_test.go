package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/types"
)

func TestMarkReadDelegateKindSeparatesReceipts(t *testing.T) {
	if got := markReadDelegateKind(false, ""); got != markReadKind {
		t.Fatalf("plain mark-read kind = %q, want %q", got, markReadKind)
	}
	// Its own kind is what makes an old daemon reject the request instead of
	// clearing the unread count the receipts come from.
	if got := markReadDelegateKind(true, ""); got != markReadReceiptsKind {
		t.Fatalf("--receipts kind = %q, want %q", got, markReadReceiptsKind)
	}
	if got := markReadDelegateKind(false, "handled"); got != markReadThroughKind {
		t.Fatalf("--through-message-id kind = %q, want %q", got, markReadThroughKind)
	}
}

func TestBoundedOrdinaryReceiptsUseThroughKind(t *testing.T) {
	if got := markReadDelegateKind(true, "handled"); got != markReadThroughKind {
		t.Fatalf("bounded receipts kind = %q, want %q", got, markReadThroughKind)
	}
}

func TestExplainReceiptsDelegateErrorNamesTheRestart(t *testing.T) {
	rejected := fmt.Errorf(`unsupported send kind %q`, markReadReceiptsKind)
	err := explainReceiptsDelegateError(rejected, true)
	if err == nil || !strings.Contains(err.Error(), "left the chat unread") || !strings.Contains(err.Error(), "restart `wacli sync`") {
		t.Fatalf("error = %v, want the restart hint and the untouched-state note", err)
	}
	// Anything else passes through untouched.
	other := errors.New("connection refused")
	if got := explainReceiptsDelegateError(other, true); got != other {
		t.Fatalf("unrelated error = %v, want it unchanged", got)
	}
	if got := explainReceiptsDelegateError(rejected, false); got != rejected {
		t.Fatalf("without --receipts = %v, want it unchanged", got)
	}
}

func TestChatsMarkReadReceiptsFlagOnlyOnMarkRead(t *testing.T) {
	flags := &rootFlags{}
	if newChatsMarkReadCmd(flags, true).Flags().Lookup("receipts") == nil {
		t.Fatal("mark-read has no --receipts flag")
	}
	if newChatsMarkReadCmd(flags, false).Flags().Lookup("receipts") != nil {
		t.Fatal("mark-unread must not accept --receipts")
	}
	if newChatsMarkReadCmd(flags, true).Flags().Lookup("through-message-id") == nil {
		t.Fatal("mark-read has no --through-message-id flag")
	}
	if newChatsMarkReadCmd(flags, false).Flags().Lookup("through-message-id") != nil {
		t.Fatal("mark-unread must not accept --through-message-id")
	}
}

func TestChatsMarkReadRejectsEmptyBoundaryAndReceiptCombinationBeforeStoreOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "empty boundary", args: []string{"--through-message-id", ""}, want: "requires an exact nonempty message ID"},
		{name: "padded boundary", args: []string{"--through-message-id", " handled"}, want: "requires an exact nonempty message ID"},
		{name: "self without boundary", args: []string{"--self-only"}, want: "requires mark-read"},
		{name: "self with empty boundary", args: []string{"--self-only", "--through-message-id", ""}, want: "requires an exact nonempty"},
		{name: "self with receipts", args: []string{"--self-only", "--through-message-id", "handled", "--receipts"}, want: "cannot be combined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--store", t.TempDir(), "chats", "mark-read", "--chat", "123@s.whatsapp.net"}, tc.args...)
			var commandErr error
			captureRootStderr(t, func() { commandErr = execute(args) })
			if commandErr == nil || !strings.Contains(commandErr.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", commandErr, tc.want)
			}
		})
	}
}

func TestExecuteDelegatedReadSelfThroughUsesPrivateFixedBoundary(t *testing.T) {
	fake := &orderedMarkReadApp{receipts: 4}
	resp, err := executeDelegatedMarkRead(context.Background(), fake, sendDelegateRequest{Kind: markReadSelfThroughKind, To: "123@s.whatsapp.net", ThroughMessageID: "handled"})
	if err != nil || !slices.Equal(fake.steps, []string{"self-through 123@s.whatsapp.net id=handled"}) || resp.Receipts == nil || *resp.Receipts != 4 || resp.ReceiptType != string(types.ReceiptTypeReadSelf) {
		t.Fatalf("steps=%v response=%+v error=%v", fake.steps, resp, err)
	}
	read := false
	for _, req := range []sendDelegateRequest{
		{Kind: markReadSelfThroughKind, To: "123@s.whatsapp.net"},
		{Kind: markReadSelfThroughKind, To: "123@s.whatsapp.net", ThroughMessageID: "handled", Receipts: true},
		{Kind: markReadSelfThroughKind, To: "123@s.whatsapp.net", ThroughMessageID: "handled", Read: &read},
	} {
		if _, err := executeDelegatedMarkRead(context.Background(), fake, req); err == nil {
			t.Fatalf("accepted invalid private request %+v", req)
		}
	}
	if len(fake.steps) != 1 {
		t.Fatalf("invalid private request changed state: %v", fake.steps)
	}
	if newChatsMarkReadCmd(&rootFlags{}, false).Flags().Lookup("self-only") != nil {
		t.Fatal("mark-unread must reject --self-only")
	}
}

type orderedMarkReadApp struct {
	steps       []string
	receipts    int
	receiptType types.ReceiptType
	receiptErr  error
}

func (f *orderedMarkReadApp) DB() *store.DB { return nil }

func (f *orderedMarkReadApp) MarkChatReadWithReceipts(_ context.Context, chat types.JID) (int, types.ReceiptType, error) {
	f.steps = append(f.steps, "receipts "+chat.String())
	kind := f.receiptType
	if kind == "" {
		kind = types.ReceiptType("unknown")
	}
	return f.receipts, kind, f.receiptErr
}

func (f *orderedMarkReadApp) MarkChatReadWithReceiptsThrough(ctx context.Context, chat types.JID, id string) (int, types.ReceiptType, error) {
	f.steps = append(f.steps, "receipts-through "+id)
	return f.MarkChatReadWithReceipts(ctx, chat)
}

func (f *orderedMarkReadApp) MarkChatRead(_ context.Context, chat types.JID, read bool) error {
	f.steps = append(f.steps, fmt.Sprintf("mark %s read=%t", chat, read))
	return nil
}

func (f *orderedMarkReadApp) MarkChatReadThrough(_ context.Context, chat types.JID, id string) error {
	f.steps = append(f.steps, fmt.Sprintf("through %s id=%s", chat, id))
	return nil
}

func (f *orderedMarkReadApp) MarkChatReadSelfThrough(_ context.Context, chat types.JID, id string) (int, types.ReceiptType, error) {
	f.steps = append(f.steps, fmt.Sprintf("self-through %s id=%s", chat, id))
	return f.receipts, types.ReceiptTypeReadSelf, f.receiptErr
}

func TestExecuteDelegatedMarkReadThroughUsesFixedBoundary(t *testing.T) {
	fake := &orderedMarkReadApp{}
	resp, err := executeDelegatedMarkRead(context.Background(), fake, sendDelegateRequest{Kind: markReadThroughKind, To: "123@s.whatsapp.net", ThroughMessageID: "handled"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.steps, []string{"through 123@s.whatsapp.net id=handled"}) || resp.Receipts != nil {
		t.Fatalf("steps=%q, response=%+v", fake.steps, resp)
	}
	for _, req := range []sendDelegateRequest{
		{Kind: markReadThroughKind, To: "123@s.whatsapp.net"},
		{Kind: markReadThroughKind, To: "123@s.whatsapp.net", ThroughMessageID: " handled"},
		{Kind: markReadKind, To: "123@s.whatsapp.net", ThroughMessageID: "handled"},
	} {
		if _, err := executeDelegatedMarkRead(context.Background(), fake, req); err == nil {
			t.Fatalf("accepted malformed request %+v", req)
		}
	}
	if len(fake.steps) != 1 {
		t.Fatalf("malformed requests wrote state: %v", fake.steps)
	}
}

func TestOldOwnerRejectsThroughKindWithoutMutation(t *testing.T) {
	// Version 1 owners switch on Kind. The dedicated kind cannot be interpreted
	// as legacy mark_read by an owner that predates this flag.
	err := fmt.Errorf(`unsupported send kind %q`, markReadThroughKind)
	got := explainThroughDelegateError(err, true)
	if got == nil || !strings.Contains(got.Error(), "did not mark the chat read") || !strings.Contains(got.Error(), "restart `wacli sync`") {
		t.Fatalf("old-owner error=%v", got)
	}
}

func TestExecuteDelegatedMarkReadUsesIndependentReceiptPath(t *testing.T) {
	fake := &orderedMarkReadApp{receipts: 3}
	resp, err := executeDelegatedMarkRead(context.Background(), fake, sendDelegateRequest{
		Kind:     "mark_read",
		To:       "123@s.whatsapp.net",
		Receipts: true,
	})
	if err != nil {
		t.Fatalf("executeDelegatedMarkRead: %v", err)
	}
	// Marking the chat read clears the unread count the receipts are based on.
	want := []string{"receipts 123@s.whatsapp.net"}
	if !slices.Equal(fake.steps, want) {
		t.Fatalf("steps = %q, want %q", fake.steps, want)
	}
	if resp.Receipts == nil || *resp.Receipts != 3 || resp.Action != "mark-read" || resp.ReceiptType != "unknown" {
		t.Fatalf("response = %+v, want mark-read with 3 dispatched receipts", resp)
	}
}

func TestExecuteDelegatedMarkReadReportsHiddenReceipts(t *testing.T) {
	fake := &orderedMarkReadApp{receipts: 1, receiptType: types.ReceiptTypeReadSelf}
	resp, err := executeDelegatedMarkRead(context.Background(), fake, sendDelegateRequest{
		Kind:     "mark_read",
		To:       "123@s.whatsapp.net",
		Receipts: true,
	})
	if err != nil {
		t.Fatalf("executeDelegatedMarkRead: %v", err)
	}
	if resp.ReceiptType != string(types.ReceiptTypeReadSelf) {
		t.Fatalf("receipt type = %q, want read-self reported back to the caller", resp.ReceiptType)
	}
}

func TestExecuteDelegatedMarkReadWithoutReceiptsLeavesCountUnset(t *testing.T) {
	fake := &orderedMarkReadApp{}
	resp, err := executeDelegatedMarkRead(context.Background(), fake, sendDelegateRequest{
		Kind: "mark_read",
		To:   "123@s.whatsapp.net",
	})
	if err != nil {
		t.Fatalf("executeDelegatedMarkRead: %v", err)
	}
	if len(fake.steps) != 1 || !strings.HasPrefix(fake.steps[0], "mark ") || resp.Receipts != nil {
		t.Fatalf("steps = %q, receipts = %v; want only mark-read and no count", fake.steps, resp.Receipts)
	}
}

func TestExecuteDelegatedMarkReadRejectsReceiptsForMarkUnread(t *testing.T) {
	fake := &orderedMarkReadApp{}
	unread := false
	_, err := executeDelegatedMarkRead(context.Background(), fake, sendDelegateRequest{
		Kind:     "mark_read",
		To:       "123@s.whatsapp.net",
		Read:     &unread,
		Receipts: true,
	})
	if err == nil || !strings.Contains(err.Error(), "--receipts only applies to mark-read") {
		t.Fatalf("error = %v, want --receipts validation", err)
	}
	if len(fake.steps) != 0 {
		t.Fatalf("steps = %q, want nothing sent", fake.steps)
	}
}

func TestExecuteDelegatedMarkReadStopsWhenReceiptsFail(t *testing.T) {
	fake := &orderedMarkReadApp{receiptErr: errors.New("not connected")}
	_, err := executeDelegatedMarkRead(context.Background(), fake, sendDelegateRequest{
		Kind:     "mark_read",
		To:       "123@s.whatsapp.net",
		Receipts: true,
	})
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("error = %v, want the receipt error", err)
	}
	if len(fake.steps) != 1 {
		t.Fatalf("steps = %q, want the chat left unread after failed receipts", fake.steps)
	}
}

func TestDelegatedReceiptsRequireSupportingSyncProcess(t *testing.T) {
	if got, err := delegatedReceipts(sendDelegateResponse{Chat: "123@s.whatsapp.net"}, false); got != nil || err != nil {
		t.Fatalf("not requested = %v, %v; want nil, nil", got, err)
	}
	two := 2
	got, err := delegatedReceipts(sendDelegateResponse{
		Chat:        "123@s.whatsapp.net",
		Receipts:    &two,
		ReceiptType: string(types.ReceiptTypeReadSelf),
	}, true)
	if err != nil || got == nil || got.count != 2 || got.senderNotified() == nil || *got.senderNotified() {
		t.Fatalf("supported = %+v, %v; want 2 receipts the sender does not see", got, err)
	}
	// A sync process started before --receipts existed drops the field.
	_, err = delegatedReceipts(sendDelegateResponse{Chat: "123@s.whatsapp.net"}, true)
	if err == nil || !strings.Contains(err.Error(), "restart `wacli sync`") {
		t.Fatalf("old sync process error = %v, want restart hint", err)
	}
}

func TestReceiptModeDoesNotUseAppState(t *testing.T) {
	fake := &orderedMarkReadApp{receipts: 1}
	_, err := executeDelegatedMarkRead(context.Background(), fake, sendDelegateRequest{Kind: markReadReceiptsKind, To: "123@s.whatsapp.net", Receipts: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.steps, []string{"receipts 123@s.whatsapp.net"}) {
		t.Fatalf("receipt mode entered app-state recovery: %v", fake.steps)
	}
}

func TestReceiptJSONReportsUncertainSenderNotification(t *testing.T) {
	var result map[string]any
	raw := captureRootStdout(t, func() {
		if err := writeChatStateResult(&rootFlags{asJSON: true}, "mark-read", "123@s.whatsapp.net", &chatStateReceipts{count: 2, kind: "unknown"}); err != nil {
			t.Fatal(err)
		}
	})
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	data := result["data"].(map[string]any)
	value, ok := data["sender_notified"]
	if !ok || value != nil || data["receipt"] != "unknown" {
		t.Fatalf("uncertainty lost: %s", raw)
	}
}

func TestReceiptModeHonorsReadOnlyBeforeOpeningStore(t *testing.T) {
	var commandErr error
	captureRootStderr(t, func() {
		commandErr = execute([]string{"--store", t.TempDir(), "--read-only", "--json", "chats", "mark-read", "--chat", "123@s.whatsapp.net", "--receipts"})
	})
	if commandErr == nil || !strings.Contains(commandErr.Error(), "read-only mode") {
		t.Fatalf("error=%v, want read-only rejection", commandErr)
	}
}

func TestExplainChatStateDelegateErrorNamesTheRestart(t *testing.T) {
	rejected := fmt.Errorf(`unsupported send kind %q`, chatStateKind)
	err := explainChatStateDelegateError(rejected, "archive")
	if err == nil || !strings.Contains(err.Error(), "delegated chats archive") || !strings.Contains(err.Error(), "restart `wacli sync`") {
		t.Fatalf("error = %v, want the restart hint", err)
	}
	other := errors.New("connection refused")
	if got := explainChatStateDelegateError(other, "archive"); got != other {
		t.Fatalf("unrelated error = %v, want it unchanged", got)
	}
}
