package app

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/openclaw/wacli/internal/sqliteutil"
	sdkstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
)

func TestSessionDeliveryMigrationPreservesVersion16Cursor(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "session.db")
	c, d := gapDevice(t, path)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	hash := [128]byte{7}
	must(d.AppState.PutAppStateVersion(ctx, "regular_low", 7, hash))
	must(c.Close())
	raw, err := sql.Open("sqlite3", sqliteutil.FileURI(path, "_foreign_keys=on&_synchronous=FULL"))
	must(err)
	_, err = raw.Exec(`DROP TABLE whatsmeow_app_state_delivery; UPDATE whatsmeow_version SET version=16,compat=16`)
	must(err)
	must(raw.Close())
	upgraded, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(path, "_foreign_keys=on&_synchronous=FULL"), nil)
	must(err)
	defer upgraded.Close()
	device, err := upgraded.GetFirstDevice(ctx)
	must(err)
	version, h, err := device.AppState.GetAppStateVersion(ctx, "regular_low")
	must(err)
	if version != 7 || h != hash {
		t.Fatal("migration changed existing cursor/hash")
	}
	native := device.AppState.(sdkstore.AppStateDeliveryStore)
	pending, err := native.PendingAppStateDelivery(ctx)
	must(err)
	if pending != nil {
		t.Fatal("migration invented replay payload")
	}
	delivery, err := native.StageAppStateDelivery(ctx, sdkstore.AppStateTransition{Name: "regular_low", Kind: "delta", BeforeVersion: 7, BeforeHash: hash, AfterVersion: 8, AfterHash: [128]byte{8}, Payload: []byte("synthetic verified fixture")})
	must(err)
	if delivery.ID.Sequence != 1 {
		t.Fatal("protocol version was used as delivery sequence")
	}
	raw, err = sql.Open("sqlite3", sqliteutil.FileURI(path, "_foreign_keys=on"))
	must(err)
	defer raw.Close()
	var schema, compat int
	must(raw.QueryRow("SELECT version,compat FROM whatsmeow_version").Scan(&schema, &compat))
	if schema != 17 || compat < 17 {
		t.Fatalf("old SDK compatibility was not fenced: schema=%d compat=%d", schema, compat)
	}
}
