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
	"syscall"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
	"github.com/openclaw/wacli/internal/sqliteutil"
	"go.mau.fi/whatsmeow/store/sqlstore"
)

func TestAppStatePairingMigrationFailureCLI(t *testing.T) {
	root := newRetrievalCLI(t, newTestApp(t))
	for _, stage := range []string{"app35-after-ddl", "native18-before-commit"} {
		t.Run(stage, func(t *testing.T) {
			r := newPairingFixture(t)
			r.must(r.apply(true))
			r.legacy17()
			before := r.nativeEvidence()
			h := &retrievalCLI{t: t, bin: root.bin}
			defer func() { root.log.Write(h.log.Bytes()) }()
			if stage == "app35-after-ddl" {
				r.exec(r.app, `CREATE TRIGGER migration_fault BEFORE INSERT ON schema_migrations WHEN NEW.version=35 BEGIN SELECT RAISE(FAIL,'synthetic migration failure');END`)
				h.invoke(r.a.StoreDir(), "record migration 035", false, "auth", "status")
				r.exec(r.app, "DROP TRIGGER migration_fault")
			} else {
				r.exec(r.native, `CREATE TRIGGER migration_fault BEFORE INSERT ON whatsmeow_version WHEN NEW.version=18 BEGIN SELECT RAISE(FAIL,'synthetic migration failure');END`)
				h.invoke(r.a.StoreDir(), "failed to upgrade database", false, "auth", "status")
				var version, columns int
				r.must(r.native.QueryRow("SELECT version FROM whatsmeow_version").Scan(&version))
				r.must(r.native.QueryRow("SELECT COUNT(*) FROM pragma_table_info('whatsmeow_app_state_delivery') WHERE name='predecessor_digest'").Scan(&columns))
				if version != 17 || columns != 0 {
					t.Fatalf("partial native migration committed: version=%d columns=%d", version, columns)
				}
				r.exec(r.native, "DROP TRIGGER migration_fault")
			}
			if !bytes.Equal(before, r.nativeEvidence()) {
				t.Fatal("failed migration changed retained identity")
			}
			h.write(r.a.StoreDir(), "auth", "status")
			h.write(r.a.StoreDir(), "auth", "status")
			chat, e := r.a.db.GetChat(r.jid.String())
			r.must(e)
			if !chat.Pinned {
				t.Fatal("retry changed existing projection")
			}
		})
	}
}

func TestAppStatePairingMigrationProcessKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires SIGKILL")
	}
	r := newPairingFixture(t)
	r.must(r.apply(true))
	r.legacy17()
	before := r.nativeEvidence()
	r.exec(r.native, `CREATE TRIGGER migration_barrier BEFORE INSERT ON whatsmeow_version WHEN NEW.version=18 BEGIN SELECT successor_migration_barrier();END`)
	exe, e := os.Executable()
	r.must(e)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestAppStatePairingMigrationWorker$", "-test.v")
	cmd.Env = append(os.Environ(), "WACLI_SUCCESSOR_MIGRATION_DIR="+r.a.StoreDir())
	in, e := cmd.StdinPipe()
	r.must(e)
	defer in.Close()
	out, e := cmd.StdoutPipe()
	r.must(e)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	r.must(cmd.Start())
	scan := bufio.NewScanner(out)
	barrier := false
	for scan.Scan() {
		t.Log(scan.Text())
		if scan.Text() == "MIGRATION18_COMMIT_BARRIER" {
			barrier = true
			break
		}
	}
	r.must(scan.Err())
	if !barrier {
		t.Fatalf("no migration barrier: %s", stderr.String())
	}
	r.must(cmd.Process.Kill())
	e = cmd.Wait()
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if e == nil || !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("not SIGKILL: %v", cmd.ProcessState)
	}
	var version, columns int
	r.must(r.native.QueryRow("SELECT version FROM whatsmeow_version").Scan(&version))
	r.must(r.native.QueryRow("SELECT COUNT(*) FROM pragma_table_info('whatsmeow_app_state_delivery') WHERE name='predecessor_digest'").Scan(&columns))
	if version != 17 || columns != 0 || !bytes.Equal(before, r.nativeEvidence()) {
		t.Fatal("process death did not preserve complete legacy17 boundary")
	}
	r.exec(r.native, "DROP TRIGGER migration_barrier")
	h := newRetrievalCLI(t, r.a)
	h.write(r.a.StoreDir(), "auth", "status")
	t.Log("real CLI restarted after killed native migration; original identity preserved")
}
func TestAppStatePairingMigrationWorker(t *testing.T) {
	dir := os.Getenv("WACLI_SUCCESSOR_MIGRATION_DIR")
	if dir == "" {
		t.Skip("subprocess helper")
	}
	sql.Register("successor-migration-sqlite", &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		return conn.RegisterFunc("successor_migration_barrier", func() int {
			fmt.Println("MIGRATION18_COMMIT_BARRIER")
			var b [1]byte
			_, _ = os.Stdin.Read(b[:])
			return 0
		}, false)
	}})
	raw, e := sql.Open("successor-migration-sqlite", sqliteutil.FileURI(filepath.Join(dir, "session.db"), "_foreign_keys=on&_synchronous=FULL"))
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	c := sqlstore.NewWithDB(raw, "sqlite3", nil)
	defer c.Close()
	if e = c.Upgrade(t.Context()); e != nil {
		t.Fatal(e)
	}
}
