package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

// fakeSendFileWA stubs only the methods sendFile touches; any other call
// panics through the embedded nil interface, catching accidental coupling.
type fakeSendFileWA struct {
	app.WAClient
	sentID     types.MessageID
	mappedChat types.JID
}

func (f *fakeSendFileWA) Upload(context.Context, []byte, whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	return whatsmeow.UploadResponse{}, nil
}

func (f *fakeSendFileWA) SendProtoMessage(context.Context, types.JID, *waProto.Message) (types.MessageID, error) {
	return f.sentID, nil
}

func (f *fakeSendFileWA) ResolveChatName(context.Context, types.JID, string) string {
	return "Test Chat"
}

func (f *fakeSendFileWA) ResolveLIDToPN(_ context.Context, jid types.JID) types.JID {
	if !f.mappedChat.IsEmpty() {
		return f.mappedChat
	}
	return jid
}

type fakeSendFileApp struct {
	wa app.WAClient
	db *store.DB
}

func (f *fakeSendFileApp) WA() app.WAClient { return f.wa }
func (f *fakeSendFileApp) DB() *store.DB    { return f.db }

func newSendFileFixture(t *testing.T) (*fakeSendFileApp, *store.DB, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "wacli.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	filePath := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	a := &fakeSendFileApp{wa: &fakeSendFileWA{sentID: "MSG1"}, db: db}
	return a, db, filePath
}

func TestSendFilePersistsHistory(t *testing.T) {
	a, db, filePath := newSendFileFixture(t)
	to := types.NewJID("15550000001", types.DefaultUserServer)

	res, err := sendFile(context.Background(), a, to, filePath, sendFileOptions{caption: "hi"})
	if err != nil {
		t.Fatalf("sendFile: %v", err)
	}
	if res.id != "MSG1" || res.storeWarning != nil {
		t.Fatalf("expected clean outcome, got %+v", res)
	}
	msg, err := db.GetMessage(to.String(), "MSG1")
	if err != nil {
		t.Fatalf("sent message not persisted: %v", err)
	}
	if !msg.FromMe || msg.Text != "hi" {
		t.Fatalf("unexpected persisted message: %+v", msg)
	}
}

func TestSendFilePersistsCanonicalPhoneChat(t *testing.T) {
	a, db, filePath := newSendFileFixture(t)
	phone := types.NewJID("15550000001", types.DefaultUserServer)
	lid := types.NewJID("20001", types.HiddenUserServer)
	a.wa.(*fakeSendFileWA).mappedChat = phone
	if _, err := sendFile(context.Background(), a, lid, filePath, sendFileOptions{caption: "caption\n"}); err != nil {
		t.Fatal(err)
	}
	message, err := db.GetMessage(phone.String(), "MSG1")
	if err != nil || message.MediaCaption != "caption\n" {
		t.Fatalf("canonical exact attachment history: %+v, %v", message, err)
	}
	if _, err := db.GetMessage(lid.String(), "MSG1"); err == nil {
		t.Fatal("attachment was duplicated under its transport-only identity")
	}
}

// Regression for the silently-swallowed store failures: a delivered message
// whose local persistence fails must keep the ID and surface a warning, not
// return a hard error that invites callers to retry an already-sent message.
func TestSendFileStoreFailureKeepsDeliveredID(t *testing.T) {
	a, db, filePath := newSendFileFixture(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	to := types.NewJID("15550000001", types.DefaultUserServer)

	res, err := sendFile(context.Background(), a, to, filePath, sendFileOptions{})
	if err != nil {
		t.Fatalf("store failure must not fail the send, got %v", err)
	}
	if res.id != "MSG1" {
		t.Fatalf("delivered ID lost, got %+v", res)
	}
	if res.storeWarning == nil {
		t.Fatal("expected store warning for failed history update")
	}
}

func TestSendFileStatusStoreFailureKeepsDeliveredID(t *testing.T) {
	a, db, filePath := newSendFileFixture(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	res, err := sendFile(context.Background(), a, types.StatusBroadcastJID, filePath, sendFileOptions{})
	if err != nil {
		t.Fatalf("store failure must not fail the send, got %v", err)
	}
	if res.id != "MSG1" || res.storeWarning == nil {
		t.Fatalf("expected delivered ID with store warning, got %+v", res)
	}
}

func TestSendFileStatusPersistsHistory(t *testing.T) {
	a, db, filePath := newSendFileFixture(t)

	res, err := sendFile(context.Background(), a, types.StatusBroadcastJID, filePath, sendFileOptions{caption: "st"})
	if err != nil {
		t.Fatalf("sendFile: %v", err)
	}
	if res.storeWarning != nil {
		t.Fatalf("unexpected store warning: %v", res.storeWarning)
	}
	if _, err := db.GetStatusMessage("MSG1"); err != nil {
		t.Fatalf("status message not persisted: %v", err)
	}
}
