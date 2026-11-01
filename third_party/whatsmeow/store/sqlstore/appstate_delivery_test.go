// Copyright (c) 2026 The wacli contributors
// SPDX-License-Identifier: MPL-2.0
package sqlstore

import (
	"database/sql"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/util/dbutil"
	"go.mau.fi/whatsmeow/store"
)

func TestNativeDeliveryRejectsWeakSQLiteSynchronization(t *testing.T) {
	raw, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/fixture.db?_synchronous=NORMAL")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	db, err := dbutil.NewWithDB(raw, "sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	s := &SQLStore{Container: &Container{db: db}, JID: "synthetic"}
	_, err = s.StageAppStateDelivery(t.Context(), store.AppStateTransition{Name: "regular_low", Kind: "delta", AfterVersion: 1, Payload: []byte("synthetic")})
	if err == nil || !strings.Contains(err.Error(), "synchronous FULL") {
		t.Fatalf("weak staging was not refused before writes: %v", err)
	}
	err = s.AckAppStateDelivery(t.Context(), store.AppStateDeliveryID{Generation: "synthetic", Sequence: 1})
	if err == nil || !strings.Contains(err.Error(), "synchronous FULL") {
		t.Fatalf("weak acknowledgment was not refused: %v", err)
	}
}
