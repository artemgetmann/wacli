package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestAppStateDeliveryMigrationPreservesVersion33DataAndDebt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	jid := "15550000001@s.whatsapp.net"
	must(d.UpsertChat(jid, "dm", "synthetic", time.Unix(1700000000, 0)))
	must(d.SetChatPinned(jid, true))
	must(d.SetChatUnreadCount(jid, 3))
	gen, err := d.MarkAppStateRecoveryGeneration("regular_low")
	must(err)
	must(d.Close())
	raw, err := sql.Open("sqlite3", path)
	must(err)
	_, err = raw.Exec(`DROP TABLE app_state_delivery_recovery; DROP TABLE app_state_delivery_checkpoint; DELETE FROM schema_migrations WHERE version=34`)
	must(err)
	must(raw.Close())
	d, err = Open(path)
	must(err)
	defer d.Close()
	chat, err := d.GetChat(jid)
	must(err)
	if !chat.Pinned || !chat.Unread || chat.UnreadCount != 3 {
		t.Fatalf("migration altered existing state: %+v", chat)
	}
	var count int
	must(d.sql.QueryRow("SELECT COUNT(*) FROM app_state_recovery_intents WHERE id=? AND collection='regular_low'", gen).Scan(&count))
	if count != 1 {
		t.Fatal("migration lost existing recovery intent")
	}
	var version int
	must(d.sql.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version))
	if version != 34 {
		t.Fatalf("migration version %d", version)
	}
	for _, table := range []string{"app_state_delivery_checkpoint", "app_state_delivery_recovery"} {
		exists, e := d.tableExists(table)
		must(e)
		if !exists {
			t.Fatalf("missing %s", table)
		}
	}
	id := DeliveryCheckpoint{Generation: "first-new-native-generation", Sequence: 1, Digest: make([]byte, 32)}
	must(d.ApplyAppStateDelivery(t.Context(), id, []ChatStateProjection{{Kind: "pin", JID: jid, Enabled: false}}, nil))
	required, err := d.AppStateRecoveryRequired("regular_low")
	must(err)
	if !required {
		t.Fatal("first native delivery cleared older unrelated debt")
	}
}
