// Copyright (c) 2026 The wacli contributors
// SPDX-License-Identifier: MPL-2.0
package whatsmeow

import (
	"database/sql"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow/store"
)

func TestNativePredecessorCommitsAndRollsBackWithCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.db")
	d := deliveryTestDevice(t, path)
	s := d.AppState.(store.AppStateDeliveryStore)
	ctx := t.Context()
	first := store.AppStateTransition{Name: "regular_low", Kind: "delta", AfterVersion: 1, AfterHash: [128]byte{1}, Payload: []byte("synthetic first intent")}
	one, e := s.StageAppStateDelivery(ctx, first)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.AckAppStateDelivery(ctx, one.ID); e != nil {
		t.Fatal(e)
	}
	raw, e := sql.Open("sqlite3", "file:"+path+"?_foreign_keys=on&_synchronous=FULL")
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	_, e = raw.Exec(`CREATE TRIGGER predecessor_fault BEFORE UPDATE OF payload ON whatsmeow_app_state_delivery WHEN NEW.sequence=2 BEGIN SELECT RAISE(FAIL,'synthetic native failure');END`)
	if e != nil {
		t.Fatal(e)
	}
	second := store.AppStateTransition{Name: first.Name, Kind: "delta", BeforeVersion: 1, BeforeHash: first.AfterHash, AfterVersion: 2, AfterHash: [128]byte{2}, Payload: []byte("synthetic second intent")}
	if _, e = s.StageAppStateDelivery(ctx, second); e == nil {
		t.Fatal("native write fault not surfaced")
	}
	b, e := s.AppStateDeliveryBoundary(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if b.ID != one.ID || b.Predecessor != nil || b.Pending || b.Version != 1 || b.Hash != first.AfterHash {
		t.Fatal("failed transaction changed predecessor/cursor boundary")
	}
	if _, e = raw.Exec("DROP TRIGGER predecessor_fault"); e != nil {
		t.Fatal(e)
	}
	two, e := s.StageAppStateDelivery(ctx, second)
	if e != nil {
		t.Fatal(e)
	}
	if two.Boundary.Predecessor == nil || *two.Boundary.Predecessor != one.ID || two.ID.Sequence != 2 {
		t.Fatal("successor did not atomically retain exact predecessor")
	}
	if e = s.AckAppStateDelivery(ctx, two.ID); e != nil {
		t.Fatal(e)
	}
	b, e = s.AppStateDeliveryBoundary(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if b.ID != two.ID || b.Predecessor == nil || *b.Predecessor != one.ID || b.Pending {
		t.Fatal("ack discarded current/predecessor witness")
	}
}
