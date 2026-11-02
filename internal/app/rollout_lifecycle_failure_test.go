package app

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/openclaw/wacli/internal/sqliteutil"
	"go.mau.fi/whatsmeow/store/sqlstore"
)

func TestDeviceDeletionFailurePreservesBothSides(t *testing.T) {
	r := newPairingFixture(t)
	r.must(r.apply(true))
	r.pending()
	before := r.nativeEvidence()
	r.exec(r.native, `CREATE TRIGGER delete_fault BEFORE DELETE ON whatsmeow_device BEGIN SELECT RAISE(FAIL,'synthetic deletion failure');END`)
	if r.d.Delete(t.Context()) == nil {
		t.Fatal("expected failed deletion")
	}
	var devices, retired int
	r.must(r.native.QueryRow(`SELECT COUNT(*) FROM whatsmeow_device`).Scan(&devices))
	r.must(r.native.QueryRow(`SELECT COUNT(*) FROM whatsmeow_app_state_retired_delivery`).Scan(&retired))
	if devices != 1 || retired != 0 || !bytes.Equal(before, r.nativeEvidence()) || r.d.Deleted {
		t.Fatal("failed delete partially changed credentials/evidence")
	}
	r.exec(r.native, "DROP TRIGGER delete_fault")
	r.must(r.d.Delete(t.Context()))
	h := newRetrievalCLI(t, r.a)
	h.write(r.a.StoreDir(), "auth", "status")
}

func TestRetiredEpochRefusesAmbiguity(t *testing.T) {
	root := newRetrievalCLI(t, newTestApp(t))
	for _, which := range []string{"wrong-checkpoint", "missing-checkpoint", "pending-app-write-fails", "uncovered-debt", "missing-terminal-witness"} {
		t.Run(which, func(t *testing.T) {
			r := newPairingFixture(t)
			r.must(r.apply(true))
			r.pending()
			r.must(r.d.Delete(t.Context()))
			switch which {
			case "wrong-checkpoint":
				r.exec(r.app, `UPDATE app_state_delivery_checkpoint SET digest=zeroblob(32)`)
			case "missing-checkpoint":
				r.exec(r.app, `DELETE FROM app_state_delivery_checkpoint`)
			case "pending-app-write-fails":
				r.exec(r.app, `CREATE TRIGGER projection_fault BEFORE UPDATE ON chats BEGIN SELECT RAISE(FAIL,'synthetic App write failure');END`)
			case "uncovered-debt":
				r.must(r.a.db.MarkAppStateRecoveryRequired("regular_low"))
			case "missing-terminal-witness":
				r.exec(r.native, `DELETE FROM whatsmeow_app_state_retired_delivery`)
			}
			var before []byte
			r.must(r.native.QueryRow(`SELECT CAST(COALESCE((SELECT hex(payload) FROM whatsmeow_app_state_retired_delivery),'none') AS BLOB)`).Scan(&before))
			h := &retrievalCLI{t: t, bin: root.bin}
			defer func() { root.log.Write(h.log.Bytes()) }()
			h.invoke(r.a.StoreDir(), "", true, "auth", "status")
			// Every admitted mutation path must refuse; error wording differs for write failure.
			want := "store pairing refused"
			if which == "pending-app-write-fails" {
				want = "synthetic App write failure"
			}
			h.invoke(r.a.StoreDir(), want, false, "auth", "status")
			var after []byte
			r.must(r.native.QueryRow(`SELECT CAST(COALESCE((SELECT hex(payload) FROM whatsmeow_app_state_retired_delivery),'none') AS BLOB)`).Scan(&after))
			if !bytes.Equal(before, after) {
				t.Fatal("refusal discarded retired payload")
			}
		})
	}
}

func TestDeviceDeletionProcessKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires process kill")
	}
	r := newPairingFixture(t)
	r.must(r.apply(true))
	r.pending()
	before := r.nativeEvidence()
	r.exec(r.native, `CREATE TRIGGER retirement_barrier BEFORE DELETE ON whatsmeow_device BEGIN SELECT retirement_delete_barrier();END`)
	exe, err := os.Executable()
	r.must(err)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestDeviceDeletionWorker$", "-test.v")
	cmd.Env = append(os.Environ(), "WACLI_RETIREMENT_TEST_DIR="+r.a.StoreDir())
	in, err := cmd.StdinPipe()
	r.must(err)
	defer in.Close()
	out, err := cmd.StdoutPipe()
	r.must(err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	r.must(cmd.Start())
	scan := bufio.NewScanner(out)
	barrier := false
	for scan.Scan() {
		if scan.Text() == "RETIREMENT_DELETE_BARRIER" {
			barrier = true
			break
		}
	}
	if !barrier {
		t.Fatalf("no barrier: %s", stderr.String())
	}
	r.must(cmd.Process.Kill())
	if cmd.Wait() == nil {
		t.Fatal("child unexpectedly succeeded")
	}
	var devices, retired int
	r.must(r.native.QueryRow(`SELECT COUNT(*) FROM whatsmeow_device`).Scan(&devices))
	r.must(r.native.QueryRow(`SELECT COUNT(*) FROM whatsmeow_app_state_retired_delivery`).Scan(&retired))
	if devices != 1 || retired != 0 || !bytes.Equal(before, r.nativeEvidence()) {
		t.Fatal("killed transaction left partial retirement")
	}
	r.exec(r.native, "DROP TRIGGER retirement_barrier")
	r.must(r.d.Delete(t.Context()))
	h := newRetrievalCLI(t, r.a)
	h.write(r.a.StoreDir(), "auth", "status")
	t.Log("new CLI process recovered after killed credential-delete transaction")
}
func TestDeviceDeletionWorker(t *testing.T) {
	dir := os.Getenv("WACLI_RETIREMENT_TEST_DIR")
	if dir == "" {
		t.Skip("child only")
	}
	sql.Register("retirement-test-sqlite", &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		return c.RegisterFunc("retirement_delete_barrier", func() int {
			fmt.Println("RETIREMENT_DELETE_BARRIER")
			var b [1]byte
			_, _ = os.Stdin.Read(b[:])
			return 0
		}, false)
	}})
	raw, err := sql.Open("retirement-test-sqlite", sqliteutil.FileURI(filepath.Join(dir, "session.db"), "_foreign_keys=on&_synchronous=FULL"))
	if err != nil {
		t.Fatal(err)
	}
	c := sqlstore.NewWithDB(raw, "sqlite3", nil)
	defer c.Close()
	d, err := c.GetFirstDevice(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Delete(t.Context()); err != nil {
		t.Fatal(err)
	}
}
