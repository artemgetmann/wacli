package app

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openclaw/wacli/internal/sqliteutil"
	sdkstore "go.mau.fi/whatsmeow/store"
)

func TestNativeAppStateDeliveryTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.db")
	_, d := gapDevice(t, path)
	s, ok := d.AppState.(sdkstore.AppStateDeliveryStore)
	if !ok {
		t.Fatal("native delivery capability absent")
	}
	raw, err := sql.Open("sqlite3", sqliteutil.FileURI(path, "_foreign_keys=on"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx := t.Context()
	nestedErr := errors.New("synthetic outer rollback")
	if err = d.EventBuffer.DoDecryptionTxn(ctx, func(txCtx context.Context) error {
		_, stageErr := s.StageAppStateDelivery(txCtx, sdkstore.AppStateTransition{Name: "regular_low", Kind: "delta", AfterVersion: 1, Payload: []byte("synthetic")})
		if stageErr == nil || !strings.Contains(stageErr.Error(), "own committed transaction") {
			t.Fatalf("nested staging did not reject: %v", stageErr)
		}
		return nestedErr
	}); !errors.Is(err, nestedErr) {
		t.Fatalf("outer rollback: %v", err)
	}

	first := sdkstore.AppStateTransition{Name: "regular_low", Kind: "delta", AfterVersion: 1, Payload: []byte("synthetic payload"), AddedMACs: []sdkstore.AppStateMutationMAC{{IndexMAC: make([]byte, 32), ValueMAC: make([]byte, 32)}}}
	first.AfterHash[0] = 1
	assertState := func(version uint64, hash [128]byte, pending bool, macs int) {
		t.Helper()
		v, h, e := d.AppState.GetAppStateVersion(ctx, first.Name)
		must(e)
		if v != version || h != hash {
			t.Fatalf("cursor/hash mismatch: %d/%x", v, h)
		}
		p, e := s.PendingAppStateDelivery(ctx)
		must(e)
		if (p != nil) != pending {
			t.Fatalf("pending=%+v", p)
		}
		var count int
		must(raw.QueryRow("SELECT COUNT(*) FROM whatsmeow_app_state_mutation_macs").Scan(&count))
		if count != macs {
			t.Fatalf("MAC count %d, want %d", count, macs)
		}
	}
	// The payload write occurs after cursor/MAC operations inside the same real
	// dbutil transaction. Failure must roll all three back, including sequence.
	_, err = raw.Exec(`CREATE TRIGGER fail_delivery BEFORE UPDATE OF payload ON whatsmeow_app_state_delivery BEGIN SELECT RAISE(FAIL,'synthetic payload write failure'); END`)
	must(err)
	if _, err = s.StageAppStateDelivery(ctx, first); err == nil {
		t.Fatal("payload failure was not surfaced")
	}
	assertState(0, [128]byte{}, false, 0)
	_, err = raw.Exec("DROP TRIGGER fail_delivery")
	must(err)
	delivery, err := s.StageAppStateDelivery(ctx, first)
	must(err)
	if delivery.ID.Generation == "" || delivery.ID.Sequence != 1 {
		t.Fatalf("failed transaction advanced identity: %+v", delivery.ID)
	}
	assertState(1, first.AfterHash, true, 1)
	next := first
	next.BeforeVersion = 1
	next.BeforeHash = first.AfterHash
	next.AfterVersion = 2
	next.AfterHash[0] = 2
	if _, err = s.StageAppStateDelivery(ctx, next); !errors.Is(err, sdkstore.ErrAppStateDeliveryPending) {
		t.Fatalf("second stage: %v", err)
	}
	assertState(1, first.AfterHash, true, 1)
	wrong := delivery.ID
	wrong.Digest[0] ^= 1
	if err = s.AckAppStateDelivery(ctx, wrong); !errors.Is(err, sdkstore.ErrAppStateDeliveryIdentity) {
		t.Fatalf("wrong digest ack: %v", err)
	}
	assertState(1, first.AfterHash, true, 1)
	must(s.AckAppStateDelivery(ctx, delivery.ID))
	must(s.AckAppStateDelivery(ctx, delivery.ID))
	// A replacement may reuse protocol version1; local identity must not collide.
	replacement := first
	replacement.Kind = "recovery"
	replacement.BeforeVersion = 1
	replacement.BeforeHash = first.AfterHash
	replacement.AfterHash[0] = 2
	replacement.Payload = []byte("distinct replacement at same protocol version")
	second, err := s.StageAppStateDelivery(ctx, replacement)
	must(err)
	if second.ID.Generation != delivery.ID.Generation || second.ID.Sequence != 2 || second.ID.Digest == delivery.ID.Digest {
		t.Fatalf("replacement identity collision: %+v", second.ID)
	}
	if err = s.AckAppStateDelivery(ctx, delivery.ID); !errors.Is(err, sdkstore.ErrAppStateDeliveryIdentity) {
		t.Fatalf("old ack deleted replacement: %v", err)
	}
	assertState(1, replacement.AfterHash, true, 1)
	reset := sdkstore.AppStateTransition{Name: first.Name, Kind: "reset", BeforeVersion: 1, BeforeHash: replacement.AfterHash, Payload: []byte("explicit reset")}
	if _, err = s.StageAppStateDelivery(ctx, reset); !errors.Is(err, sdkstore.ErrAppStateDeliveryPending) {
		t.Fatalf("reset passed pending delivery: %v", err)
	}
	must(s.AckAppStateDelivery(ctx, second.ID))
	_, err = raw.Exec(`CREATE TRIGGER fail_reset BEFORE DELETE ON whatsmeow_app_state_version BEGIN SELECT RAISE(FAIL,'synthetic reset failure'); END`)
	must(err)
	if _, err = s.StageAppStateDelivery(ctx, reset); err == nil {
		t.Fatal("reset failure not surfaced")
	}
	assertState(1, replacement.AfterHash, false, 1)
	_, err = raw.Exec("DROP TRIGGER fail_reset")
	must(err)
	third, err := s.StageAppStateDelivery(ctx, reset)
	must(err)
	if third.ID.Sequence != 3 {
		t.Fatalf("failed reset consumed sequence: %+v", third.ID)
	}
	assertState(0, [128]byte{}, true, 0)
	must(s.AckAppStateDelivery(ctx, third.ID))
	// CAS rejects stale bases and rolls back its sequence reservation.
	if _, err = s.StageAppStateDelivery(ctx, next); err == nil {
		t.Fatal("stale base accepted")
	}
	assertState(0, [128]byte{}, false, 0)
	first.Payload = make([]byte, sdkstore.MaxAppStateDeliveryBytes+1)
	if _, err = s.StageAppStateDelivery(ctx, first); err == nil {
		t.Fatal("oversized payload accepted")
	}
	assertState(0, [128]byte{}, false, 0)
}
