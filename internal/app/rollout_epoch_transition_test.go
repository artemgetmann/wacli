package app

import (
	"bytes"
	"context"
	"github.com/openclaw/wacli/internal/sqliteutil"
	sdkstore "go.mau.fi/whatsmeow/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	log "go.mau.fi/whatsmeow/util/log"
)

func TestReplacementEpochCheckpointTransaction(t *testing.T) {
	for _, which := range []string{"projection-failure", "wrong-native-prior", "different-account", "fresh-login"} {
		t.Run(which, func(t *testing.T) {
			r := newPairingFixture(t)
			if which == "fresh-login" {
				r.must(r.sdk.DrainAppStateDelivery(t.Context()))
				r.must(r.apply(true))
				return
			}
			r.must(r.apply(true))
			r.must(r.d.Delete(t.Context()))
			c, err := sqlstore.New(t.Context(), "sqlite3", sqliteutil.FileURI(filepath.Join(r.a.StoreDir(), "session.db"), "_foreign_keys=on&_synchronous=FULL"), nil)
			r.must(err)
			defer c.Close()
			d := c.NewDevice()
			jid := types.JID{User: "15550000000", Device: 8, Server: types.DefaultUserServer}
			if which == "different-account" {
				jid.User = "15550000001"
			}
			d.ID = &jid
			d.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
			r.must(d.Save(t.Context()))
			r.d = d
			r.sdk = whatsmeow.NewClient(d, log.Noop)
			r.must(r.sdk.SetAppStateDeliveryConsumer(r.a.consumeAppStateDelivery))
			r.sdk.SetAppStateDeliveryValidator(r.a.validateAppStateDeliveryBoundary)
			if which == "different-account" {
				if err = r.sdk.DrainAppStateDelivery(t.Context()); err == nil || !strings.Contains(err.Error(), "different account") {
					t.Fatalf("account mix admitted: %v", err)
				}
				return
			}
			r.must(d.AppStateKeys.PutAppStateSyncKey(t.Context(), []byte{1}, sdkstore.AppStateSyncKey{Data: []byte(strings.Repeat("s", 32)), Fingerprint: []byte{1}, Timestamp: 1700000000000}))
			r.must(r.sdk.DrainAppStateDelivery(t.Context()))
			var oldGeneration string
			var oldDigest []byte
			r.must(r.app.QueryRow(`SELECT generation,digest FROM app_state_delivery_checkpoint`).Scan(&oldGeneration, &oldDigest))
			if which == "wrong-native-prior" {
				r.exec(r.native, `UPDATE whatsmeow_app_state_delivery SET prior_epoch=json_set(CAST(prior_epoch AS TEXT),'$.ID.Sequence',99)`)
				if err = r.sdk.DrainAppStateDelivery(t.Context()); err == nil {
					t.Fatal("wrong prior epoch admitted")
				}
				return
			}
			r.exec(r.app, `CREATE TRIGGER switch_fault BEFORE UPDATE ON chats BEGIN SELECT RAISE(FAIL,'synthetic switch projection failure');END`)
			if err = r.apply(false); err == nil {
				t.Fatal("expected App transition failure")
			}
			var generation string
			var digest []byte
			r.must(r.app.QueryRow(`SELECT generation,digest FROM app_state_delivery_checkpoint`).Scan(&generation, &digest))
			if generation != oldGeneration || !bytes.Equal(digest, oldDigest) {
				t.Fatal("failed projection deleted prior checkpoint")
			}
			r.exec(r.app, "DROP TRIGGER switch_fault")
			r.must(r.sdk.DrainAppStateDelivery(t.Context()))
			r.must(r.app.QueryRow(`SELECT generation,digest FROM app_state_delivery_checkpoint`).Scan(&generation, &digest))
			if generation == oldGeneration {
				t.Fatal("new checkpoint not committed")
			}
			chat, err := r.a.db.GetChat(r.jid.String())
			r.must(err)
			if chat.Pinned {
				t.Fatal("new projection not committed")
			}
		})
	}
}

func TestPreviousPublishedWriterRefusesRetirementSchema(t *testing.T) {
	bin := os.Getenv("WACLI_PREVIOUS_TEST_BINARY")
	if bin == "" {
		t.Skip("exact previous source binary supplied by local validation")
	}
	r := newPairingFixture(t)
	r.must(r.apply(true))
	r.pending()
	before := r.nativeEvidence()
	cmd := exec.CommandContext(context.Background(), bin, "--store", r.a.StoreDir(), "--json", "auth", "status")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "unsupported database schema version") {
		t.Fatalf("old writer not fenced: %s %v", out, err)
	}
	if !bytes.Equal(before, r.nativeEvidence()) {
		t.Fatal("old writer changed pending evidence")
	}
}
