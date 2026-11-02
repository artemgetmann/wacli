package app

import (
	"bytes"
	"github.com/openclaw/wacli/internal/lock"
	"testing"
)

func TestRetirementMigrationFailureCLI(t *testing.T) {
	root := newRetrievalCLI(t, newTestApp(t))
	for _, pending := range []bool{false, true} {
		name := "acked"
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
			r.exec(r.native, `DROP TABLE whatsmeow_app_state_retired_delivery;ALTER TABLE whatsmeow_app_state_delivery DROP COLUMN prior_epoch;UPDATE whatsmeow_version SET version=18,compat=18`)
			r.exec(r.native, `CREATE TRIGGER retirement_migration_fault BEFORE INSERT ON whatsmeow_version WHEN NEW.version=19 BEGIN SELECT RAISE(FAIL,'synthetic migration19 failure');END`)
			h := &retrievalCLI{t: t, bin: root.bin}
			defer func() { root.log.Write(h.log.Bytes()) }()
			h.invoke(r.a.StoreDir(), "failed to upgrade database", false, "auth", "status")
			var schema, columns int
			r.must(r.native.QueryRow(`SELECT version FROM whatsmeow_version`).Scan(&schema))
			r.must(r.native.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('whatsmeow_app_state_delivery') WHERE name='prior_epoch'`).Scan(&columns))
			if schema != 18 || columns != 0 || !bytes.Equal(before, r.nativeEvidence()) {
				t.Fatal("failed migration19 changed original boundary")
			}
			r.exec(r.native, `DROP TRIGGER retirement_migration_fault`)
			h.write(r.a.StoreDir(), "auth", "status")
			r.must(r.native.QueryRow(`SELECT version FROM whatsmeow_version`).Scan(&schema))
			if schema != 19 {
				t.Fatal("migration19 not committed")
			}
			chat, e := r.a.db.GetChat(r.jid.String())
			r.must(e)
			if chat.Pinned == pending {
				t.Fatal("migration lost projection")
			}
		})
	}
}

func TestDiagnosticsLockWaitCancellationAndUnlockedDrain(t *testing.T) {
	r := newPairingFixture(t)
	r.must(r.apply(true))
	r.pending()
	h := newRetrievalCLI(t, r.a)
	lk, e := lock.Acquire(r.a.StoreDir())
	r.must(e)
	defer lk.Release()
	before := r.nativeEvidence()
	h.invoke(r.a.StoreDir(), "context deadline exceeded", false, "--timeout", "20ms", "--lock-wait", "30s", "auth", "status")
	h.invoke(r.a.StoreDir(), "locked", false, "--lock-wait", "20ms", "auth", "status")
	h.invoke(r.a.StoreDir(), "", true, "auth", "status")
	if !bytes.Equal(before, r.nativeEvidence()) {
		t.Fatal("lock-wait or readonly diagnostic mutated pending")
	}
	r.must(lk.Release())
	h.write(r.a.StoreDir(), "auth", "status")
	var pending bool
	r.must(r.native.QueryRow(`SELECT payload IS NOT NULL FROM whatsmeow_app_state_delivery`).Scan(&pending))
	if pending {
		t.Fatal("unlocked write diagnostic did not drain")
	}
	// Offline doctor remains a usable read-only inspector on an ordinary store.
	h.write(r.a.StoreDir(), "doctor")
}
