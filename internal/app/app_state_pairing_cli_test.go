package app

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/sqliteutil"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	sdkstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	log "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

type pairingFixture struct {
	t           *testing.T
	a           *App
	d           *sdkstore.Device
	sdk         *whatsmeow.Client
	native, app *sql.DB
	jid         types.JID
}

func newPairingFixture(t *testing.T) *pairingFixture {
	t.Helper()
	a := newTestApp(t)
	a.wa = newFakeWA()
	_, d := gapDevice(t, filepath.Join(a.StoreDir(), "session.db"))
	r := &pairingFixture{t: t, a: a, d: d, sdk: whatsmeow.NewClient(d, log.Noop), jid: types.NewJID("456", types.DefaultUserServer)}
	r.must(r.sdk.SetAppStateDeliveryConsumer(a.consumeAppStateDelivery))
	r.sdk.SetAppStateDeliveryValidator(a.validateAppStateDeliveryBoundary)
	var e error
	r.native, e = sql.Open("sqlite3", sqliteutil.FileURI(filepath.Join(a.StoreDir(), "session.db"), "_foreign_keys=on&_synchronous=FULL"))
	r.must(e)
	r.app, e = sql.Open("sqlite3", sqliteutil.FileURI(filepath.Join(a.StoreDir(), "wacli.db"), "_foreign_keys=on"))
	r.must(e)
	t.Cleanup(func() { _ = r.native.Close(); _ = r.app.Close() })
	r.must(a.db.UpsertChat(r.jid.String(), "dm", "synthetic", time.Unix(1700000000, 0)))
	return r
}
func (r *pairingFixture) must(e error) {
	r.t.Helper()
	if e != nil {
		r.t.Fatal(e)
	}
}
func (r *pairingFixture) exec(db *sql.DB, q string) { r.t.Helper(); _, e := db.Exec(q); r.must(e) }
func (r *pairingFixture) apply(pin bool) error {
	v, h, e := r.d.AppState.GetAppStateVersion(r.t.Context(), "regular_low")
	r.must(e)
	s := appstate.HashState{Version: v, Hash: h}
	info := appstate.BuildPin(r.jid, pin)
	info.Timestamp = time.Unix(1700000000+int64(v), 0)
	b, e := appstate.NewProcessor(r.d, log.Noop).EncodePatch(r.t.Context(), []byte{1}, s, info)
	r.must(e)
	p := &waServerSync.SyncdPatch{}
	r.must(proto.Unmarshal(b, p))
	p.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(v + 1)}
	var held []any
	_, e = r.sdk.DangerousInternals().ApplyAppStatePatches(r.t.Context(), appstate.WAPatchRegularLow, s, &appstate.PatchList{Name: appstate.WAPatchRegularLow, Patches: []*waServerSync.SyncdPatch{p}}, false, &held)
	return e
}
func (r *pairingFixture) pending() {
	r.t.Helper()
	r.must(r.sdk.SetAppStateDeliveryConsumer(func(context.Context, *sdkstore.AppStateDelivery) error {
		return fmt.Errorf("synthetic withheld projection")
	}))
	if r.apply(false) == nil {
		r.t.Fatal("expected pending delivery")
	}
}
func (r *pairingFixture) legacy17() {
	r.t.Helper()
	r.exec(r.native, `DROP TABLE whatsmeow_app_state_retired_delivery; ALTER TABLE whatsmeow_app_state_delivery DROP COLUMN prior_epoch; ALTER TABLE whatsmeow_app_state_delivery DROP COLUMN epoch_kind; ALTER TABLE whatsmeow_app_state_delivery DROP COLUMN predecessor_sequence; ALTER TABLE whatsmeow_app_state_delivery DROP COLUMN predecessor_digest; UPDATE whatsmeow_version SET version=17,compat=17`)
	r.exec(r.app, `ALTER TABLE app_state_delivery_checkpoint DROP COLUMN device_jid; DELETE FROM schema_migrations WHERE version=35`)
}
func (r *pairingFixture) nativeEvidence() []byte {
	r.t.Helper()
	var b []byte
	e := r.native.QueryRow(`SELECT CAST(json_array(jid,generation,sequence,name,kind,before_version,after_version,hex(before_hash),hex(after_hash),hex(digest),hex(payload)) AS BLOB) FROM whatsmeow_app_state_delivery`).Scan(&b)
	if e == sql.ErrNoRows {
		return nil
	}
	r.must(e)
	return b
}

func TestAppStatePairingCLI(t *testing.T) {
	root := newRetrievalCLI(t, newTestApp(t))
	cases := []struct {
		name   string
		refuse bool
	}{
		{"normal16-upgrade", false}, {"normal-successor-ack", false}, {"successor-pending", false}, {"successor-wrong-predecessor", true}, {"successor-corrupt-native-predecessor", true}, {"successor-wrong-epoch", true}, {"successor-wrong-device", true}, {"ack-missing-checkpoint", true}, {"ack-wrong-digest", true},
		{"legacy17-ack", false}, {"legacy17-exact-current-retry", false}, {"legacy17-fresh-pending1", false}, {"legacy17-ambiguous-pending2", true}, {"legacy17-corrupt-predecessor", true}, {"legacy17-empty-wrong-epoch", true}, {"app-only-ack-restore", true}, {"session-only-ack-restore", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPairingFixture(t)
			h := &retrievalCLI{t: t, bin: root.bin}
			defer func() { root.log.Write(h.log.Bytes()) }()
			if tc.name == "normal16-upgrade" {
				r.must(r.a.db.SetChatPinned(r.jid.String(), true))
				r.must(r.d.AppState.PutAppStateVersion(t.Context(), "regular_low", 7, [128]byte{7}))
				r.exec(r.native, `DROP TABLE whatsmeow_app_state_retired_delivery;DROP TABLE whatsmeow_app_state_delivery;UPDATE whatsmeow_version SET version=16,compat=16`)
				h.write(r.a.StoreDir(), "auth", "status")
				var epoch string
				var seq uint64
				r.must(r.native.QueryRow("SELECT epoch_kind,sequence FROM whatsmeow_app_state_delivery").Scan(&epoch, &seq))
				v, hash, e := r.d.AppState.GetAppStateVersion(t.Context(), "regular_low")
				r.must(e)
				chat, e := r.a.db.GetChat(r.jid.String())
				r.must(e)
				if v != 7 || hash != ([128]byte{7}) || !chat.Pinned {
					t.Fatal("migration changed legacy state")
				}
				if epoch != "fresh" || seq != 0 {
					t.Fatal("upgrade did not establish explicit fresh epoch")
				}
				return
			}
			if tc.name != "legacy17-fresh-pending1" {
				r.must(r.apply(true))
			}
			var oldApp, oldNative string
			if strings.Contains(tc.name, "restore") {
				oldApp = filepath.Join(t.TempDir(), "wacli.db")
				oldNative = filepath.Join(t.TempDir(), "session.db")
				_, e := r.app.Exec("VACUUM INTO ?", oldApp)
				r.must(e)
				_, e = r.native.Exec("VACUUM INTO ?", oldNative)
				r.must(e)
				r.must(r.apply(false))
			}
			if strings.Contains(tc.name, "pending") || strings.Contains(tc.name, "predecessor") || tc.name == "successor-wrong-epoch" {
				r.pending()
			}
			if tc.name == "legacy17-exact-current-retry" {
				r.exec(r.native, `CREATE TRIGGER pairing_ack_fault BEFORE UPDATE OF payload ON whatsmeow_app_state_delivery WHEN OLD.payload IS NOT NULL AND NEW.payload IS NULL BEGIN SELECT RAISE(FAIL,'synthetic ack failure');END`)
				if r.apply(false) == nil {
					t.Fatal("expected ack failure")
				}
				r.exec(r.native, "DROP TRIGGER pairing_ack_fault")
			}
			switch tc.name {
			case "successor-wrong-predecessor", "legacy17-corrupt-predecessor", "ack-wrong-digest":
				r.exec(r.app, "UPDATE app_state_delivery_checkpoint SET digest=zeroblob(32)")
			case "successor-corrupt-native-predecessor":
				r.exec(r.native, "UPDATE whatsmeow_app_state_delivery SET predecessor_digest=zeroblob(32)")
			case "successor-wrong-epoch":
				r.exec(r.native, "UPDATE whatsmeow_app_state_delivery SET generation='eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee'")
			case "successor-wrong-device":
				r.exec(r.app, "UPDATE app_state_delivery_checkpoint SET device_jid='different:7@s.whatsapp.net'")
			case "ack-missing-checkpoint":
				r.exec(r.app, "DELETE FROM app_state_delivery_checkpoint")
			}
			if strings.HasPrefix(tc.name, "legacy17-") {
				r.legacy17()
				if tc.name == "legacy17-empty-wrong-epoch" {
					r.exec(r.native, "DELETE FROM whatsmeow_app_state_delivery")
				}
			}
			dir := r.a.StoreDir()
			if strings.Contains(tc.name, "restore") {
				dir = t.TempDir()
				for _, name := range []string{"session.db", "wacli.db"} {
					source := r.native
					if name == "wacli.db" {
						source = r.app
					}
					path := filepath.Join(dir, name)
					if tc.name == "app-only-ack-restore" && name == "wacli.db" || tc.name == "session-only-ack-restore" && name == "session.db" {
						old := oldApp
						if name == "session.db" {
							old = oldNative
						}
						var e error
						source, e = sql.Open("sqlite3", old)
						r.must(e)
						defer source.Close()
					}
					_, e := source.Exec("VACUUM INTO ?", path)
					r.must(e)
				}
			}
			before := r.nativeEvidence()
			if tc.refuse {
				h.invoke(dir, "store pairing refused", false, "auth", "status")
				if len(before) > 0 && !strings.Contains(tc.name, "restore") && !bytes.Equal(before, r.nativeEvidence()) {
					t.Fatal("refusal lost/changed native pending evidence")
				}
			} else {
				h.write(dir, "auth", "status")
				h.write(dir, "auth", "status")
				var pending bool
				r.must(r.native.QueryRow("SELECT payload IS NOT NULL FROM whatsmeow_app_state_delivery").Scan(&pending))
				if pending {
					t.Fatal("accepted class did not drain exact delivery")
				}
			}
			t.Logf("actual_CLI_class=%s refused=%t", tc.name, tc.refuse)
		})
	}
}

func TestAppStatePairingFencesPreviousCLI(t *testing.T) {
	previous := os.Getenv("WACLI_SUCCESSOR_PREVIOUS_CLI")
	if previous == "" {
		t.Skip("optional exact previous binary supplied only by isolated comparison")
	}
	r := newPairingFixture(t)
	r.must(r.apply(true))
	before := r.nativeEvidence()
	h := &retrievalCLI{t: t, bin: previous}
	h.invoke(r.a.StoreDir(), "unsupported database schema version", false, "auth", "status")
	if !bytes.Equal(before, r.nativeEvidence()) {
		t.Fatal("previous CLI changed native successor boundary")
	}
}
