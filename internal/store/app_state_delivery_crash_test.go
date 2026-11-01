package store

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
)

func TestAppStateDeliveryTransactionProcessCrash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix SIGKILL status")
	}
	for _, point := range []string{"before-pin", "before-checkpoint"} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			run := func(stage string, kill bool) {
				t.Helper()
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, exe, "-test.run=^TestAppStateDeliveryTransactionWorker$", "-test.v")
				cmd.Env = append(os.Environ(), "WACLI_TX_DIR="+dir, "WACLI_TX_POINT="+point, "WACLI_TX_STAGE="+stage)
				input, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				defer input.Close()
				out, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				cmd.Stderr = os.Stderr
				if err = cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if cmd.ProcessState == nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
				}()
				scan := bufio.NewScanner(out)
				barrier := false
				for scan.Scan() {
					line := scan.Text()
					t.Log(line)
					if line == "APP_TX_BARRIER" {
						barrier = true
						break
					}
				}
				if err = scan.Err(); err != nil {
					t.Fatal(err)
				}
				if kill {
					if !barrier {
						t.Fatal("missing transaction barrier")
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
			}
			run("produce", true)
			run("restart", false)
		})
	}
}
func TestAppStateDeliveryTransactionWorker(t *testing.T) {
	dir := os.Getenv("WACLI_TX_DIR")
	if dir == "" {
		t.Skip("subprocess helper")
	}
	d, err := Open(filepath.Join(dir, "wacli.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx := t.Context()
	jid := "15550000001@s.whatsapp.net"
	at := time.Unix(1700000000, 0)
	id := DeliveryCheckpoint{Generation: "synthetic", Sequence: 1, Digest: make([]byte, 32)}
	actions := []ChatStateProjection{{Kind: "archive", JID: jid, Enabled: true, Timestamp: at}, {Kind: "pin", JID: jid, Enabled: true}, {Kind: "read", JID: jid, Enabled: true, ReadThrough: at, ReadIDs: []string{"handled"}}}
	assert := func(complete bool) {
		t.Helper()
		c, e := d.GetChat(jid)
		must(e)
		count := 2
		if complete {
			count = 0
		}
		if c.Archived != complete || c.Pinned != complete || c.Unread != !complete || c.UnreadCount != count {
			t.Fatalf("partial App state after process restart: %+v", c)
		}
		saved, e := d.HasAppStateDeliveryCheckpoint(ctx, id)
		must(e)
		if saved != complete {
			t.Fatalf("checkpoint %t", saved)
		}
		debt, e := d.AppStateRecoveryRequired("regular_high")
		must(e)
		if debt != complete {
			t.Fatalf("debt %t", debt)
		}
	}
	if os.Getenv("WACLI_TX_STAGE") == "restart" {
		assert(false)
		_, err = d.sql.Exec("DROP TRIGGER crash_app_tx")
		must(err)
		must(d.ApplyAppStateDelivery(ctx, id, actions, []string{"regular_high"}))
		assert(true)
		fmt.Println("APP_TX_ROLLBACK_AND_RETRY_OK")
		return
	}
	must(d.UpsertChat(jid, "dm", "synthetic", at))
	for _, message := range []string{"earlier", "handled"} {
		must(d.UpsertMessage(UpsertMessageParams{ChatJID: jid, MsgID: message, SenderJID: jid, Timestamp: at, Text: "synthetic"}))
	}
	must(d.SetChatUnreadCount(jid, 2))
	// Register a fixture-only SQL function on the exact single pooled connection.
	// The production transaction code and its native SQLite commit are unchanged.
	d.sql.SetMaxOpenConns(1)
	conn, err := d.sql.Conn(ctx)
	must(err)
	must(conn.Raw(func(raw any) error {
		return raw.(*sqlite3.SQLiteConn).RegisterFunc("fixture_pause", func() int {
			fmt.Println("APP_TX_BARRIER")
			var b [1]byte
			_, e := os.Stdin.Read(b[:])
			panic(fmt.Sprintf("parent released barrier without kill: %v", e))
		}, false)
	}))
	must(conn.Close())
	trigger := `CREATE TRIGGER crash_app_tx BEFORE UPDATE OF pinned ON chats WHEN NEW.pinned=1 BEGIN SELECT fixture_pause(); END`
	if os.Getenv("WACLI_TX_POINT") == "before-checkpoint" {
		trigger = `CREATE TRIGGER crash_app_tx BEFORE INSERT ON app_state_delivery_checkpoint BEGIN SELECT fixture_pause(); END`
	}
	_, err = d.sql.Exec(trigger)
	must(err)
	must(d.ApplyAppStateDelivery(ctx, id, actions, []string{"regular_high"}))
	t.Fatal("transaction passed crash barrier")
}
