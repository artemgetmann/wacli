// Copyright (c) 2026 The wacli contributors
// SPDX-License-Identifier: MPL-2.0
package whatsmeow

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow/store"
)

func TestNativeDeliveryTransactionProcessCrash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix SIGKILL status")
	}
	dir := t.TempDir()
	for _, stage := range []string{"produce", "restart"} {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		cmd := exec.CommandContext(ctx, exe, "-test.run=^TestNativeDeliveryTransactionWorker$", "-test.v")
		cmd.Env = append(os.Environ(), "SDK_TX_DIR="+dir, "SDK_TX_STAGE="+stage)
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = os.Stderr
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		scan := bufio.NewScanner(out)
		barrier := false
		for scan.Scan() {
			line := scan.Text()
			t.Log(line)
			if line == "SDK_TX_BARRIER" {
				barrier = true
				break
			}
		}
		if err = scan.Err(); err != nil {
			t.Fatal(err)
		}
		if stage == "produce" {
			if !barrier {
				t.Fatal("missing native transaction barrier")
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
			if err == nil || !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("not SIGKILL: %v", cmd.ProcessState)
			}
		} else if err = cmd.Wait(); err != nil {
			t.Fatal(err)
		}
		_ = input.Close()
		cancel()
	}
}
func TestNativeDeliveryTransactionWorker(t *testing.T) {
	dir := os.Getenv("SDK_TX_DIR")
	if dir == "" {
		t.Skip("subprocess helper")
	}
	// This fixture driver changes only the temporary SQL connection, not the
	// native SQLStore transaction. The trigger pauses after cursor/MAC writes.
	sql.Register("delivery-crash-sqlite", &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		return c.RegisterFunc("fixture_pause", func() int {
			fmt.Println("SDK_TX_BARRIER")
			var b [1]byte
			_, e := os.Stdin.Read(b[:])
			panic(fmt.Sprintf("parent released barrier without kill: %v", e))
		}, false)
	}})
	path := filepath.Join(dir, "session.db")
	device := deliveryTestDevice(t, path)
	native := device.AppState.(store.AppStateDeliveryStore)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx := t.Context()
	raw, err := sql.Open("sqlite3", "file:"+path+"?_foreign_keys=on")
	must(err)
	defer raw.Close()
	transition := store.AppStateTransition{Name: "regular_low", Kind: "delta", AfterVersion: 1, Payload: []byte("synthetic payload"), AddedMACs: []store.AppStateMutationMAC{{IndexMAC: make([]byte, 32), ValueMAC: make([]byte, 32)}}}
	transition.AfterHash[0] = 1
	if os.Getenv("SDK_TX_STAGE") == "restart" {
		v, h, e := device.AppState.GetAppStateVersion(ctx, transition.Name)
		must(e)
		pending, e := native.PendingAppStateDelivery(ctx)
		must(e)
		var macs, identityRows int
		must(raw.QueryRow("SELECT COUNT(*) FROM whatsmeow_app_state_mutation_macs").Scan(&macs))
		must(raw.QueryRow("SELECT COUNT(*) FROM whatsmeow_app_state_delivery").Scan(&identityRows))
		if v != 0 || h != ([128]byte{}) || pending != nil || macs != 0 || identityRows != 0 {
			t.Fatalf("partial native commit: cursor=%d pending=%+v macs=%d identities=%d", v, pending, macs, identityRows)
		}
		_, e = raw.Exec("DROP TRIGGER crash_sdk_tx")
		must(e)
		d, e := native.StageAppStateDelivery(ctx, transition)
		must(e)
		if d.ID.Sequence != 1 {
			t.Fatal("rollback advanced identity")
		}
		must(native.AckAppStateDelivery(ctx, d.ID))
		fmt.Println("SDK_TX_ROLLBACK_AND_RETRY_OK")
		return
	}
	_, err = raw.Exec(`CREATE TRIGGER crash_sdk_tx BEFORE UPDATE OF payload ON whatsmeow_app_state_delivery BEGIN SELECT fixture_pause(); END`)
	must(err)
	_, err = native.StageAppStateDelivery(ctx, transition)
	must(err)
	t.Fatal("native transaction passed crash barrier")
}
