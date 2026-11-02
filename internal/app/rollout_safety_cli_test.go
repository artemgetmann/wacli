package app

import (
	"github.com/openclaw/wacli/internal/lock"
	"go.mau.fi/whatsmeow"
	sdkstore "go.mau.fi/whatsmeow/store"
	log "go.mau.fi/whatsmeow/util/log"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnosticsRespectWriterLock(t *testing.T) {
	root := newRetrievalCLI(t, newTestApp(t))
	for _, command := range [][]string{{"auth", "status"}, {"doctor"}, {"doctor", "--connect"}} {
		t.Run(strings.Join(command, "-"), func(t *testing.T) {
			r := newPairingFixture(t)
			r.must(r.apply(true))
			r.pending()
			before := r.nativeEvidence()
			lk, err := lock.Acquire(r.a.StoreDir())
			r.must(err)
			defer lk.Release()
			h := &retrievalCLI{t: t, bin: root.bin}
			defer func() { root.log.Write(h.log.Bytes()) }()
			if command[0] == "auth" {
				h.invoke(r.a.StoreDir(), "locked", false, command...)
			} else {
				h.invoke(r.a.StoreDir(), "", false, command...)
			}
			chat, err := r.a.db.GetChat(r.jid.String())
			r.must(err)
			pending, err := r.d.AppState.(sdkstore.AppStateDeliveryStore).PendingAppStateDelivery(t.Context())
			r.must(err)
			if !chat.Pinned || pending == nil || string(before) != string(r.nativeEvidence()) {
				t.Fatalf("diagnostic modified pending state while writer LOCK held: pin=%v pending=%v", chat.Pinned, pending != nil)
			}
			if command[0] == "doctor" && len(command) > 1 {
				h.invoke(r.a.StoreDir(), "read-only", true, command...)
			} else {
				h.run(r.a.StoreDir(), "", command...)
			}
		})
	}
}

func TestDeviceDeletionRetainsDeliveryEvidence(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "acknowledged"
		if pending {
			name = "pending"
		}
		t.Run(name, func(t *testing.T) {
			r := newPairingFixture(t)
			r.must(r.apply(true))
			if pending {
				r.pending()
			}
			before := r.nativeEvidence()
			r.must(r.d.Delete(t.Context()))
			var devices, rows, checkpoints int
			r.must(r.native.QueryRow(`SELECT COUNT(*) FROM whatsmeow_device`).Scan(&devices))
			r.must(r.native.QueryRow(`SELECT COUNT(*) FROM whatsmeow_app_state_retired_delivery`).Scan(&rows))
			r.must(r.app.QueryRow(`SELECT COUNT(*) FROM app_state_delivery_checkpoint`).Scan(&checkpoints))
			var retained []byte
			r.must(r.native.QueryRow(`SELECT CAST(json_array(jid,generation,sequence,name,kind,before_version,after_version,hex(before_hash),hex(after_hash),hex(digest),hex(payload)) AS BLOB) FROM whatsmeow_app_state_retired_delivery`).Scan(&retained))
			if devices != 0 || rows != 1 || checkpoints != 1 || string(before) != string(retained) {
				t.Fatalf("credential deletion lost delivery evidence: devices=%d delivery=%d checkpoints=%d", devices, rows, checkpoints)
			}
			r.must(r.d.Delete(t.Context()))
			h := newRetrievalCLI(t, r.a)
			for i := 0; i < 2; i++ {
				data := h.write(r.a.StoreDir(), "auth", "status")
				if data["authenticated"] != false {
					t.Fatalf("deleted credentials still authenticated: %v", data)
				}
			}
			var outstanding bool
			r.must(r.native.QueryRow(`SELECT payload IS NOT NULL FROM whatsmeow_app_state_retired_delivery`).Scan(&outstanding))
			if outstanding {
				t.Fatal("covered pending retirement did not drain")
			}
			chat, err := r.a.db.GetChat(r.jid.String())
			r.must(err)
			if chat.Pinned == pending {
				t.Fatal("retired projection missing")
			}
			// Save a new synthetic device for the same account; no pairing/network.
			_, replacement := gapDevice(t, filepath.Join(r.a.StoreDir(), "session.db"))
			r.d = replacement
			r.sdk = whatsmeow.NewClient(replacement, log.Noop)
			r.must(r.sdk.SetAppStateDeliveryConsumer(r.a.consumeAppStateDelivery))
			r.sdk.SetAppStateDeliveryValidator(r.a.validateAppStateDeliveryBoundary)
			r.must(r.sdk.DrainAppStateDelivery(t.Context()))
			r.must(r.apply(true))
			var count int
			r.must(r.app.QueryRow(`SELECT COUNT(*) FROM app_state_delivery_checkpoint`).Scan(&count))
			if count != 1 {
				t.Fatalf("epoch switch left ambiguous checkpoints: %d", count)
			}
			r.must(r.d.Delete(t.Context()))
			h.write(r.a.StoreDir(), "auth", "status")
			r.must(r.native.QueryRow(`SELECT COUNT(*) FROM whatsmeow_app_state_retired_delivery`).Scan(&count))
			if count != 1 {
				t.Fatal("retirement slot grew beyond one")
			}
		})
	}
}
