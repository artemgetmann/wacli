package app

import (
	"bufio"
	"database/sql"
	"fmt"
	"github.com/openclaw/wacli/internal/sqliteutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	appstore "github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow/appstate"
	sdkstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types/events"
)

// This models the App-side atomic-consumer boundary. The companion SDK test
// exercises the genuine verified producer and unlocked dispatch ordering.
func TestInterleavedRecoveryDebtSurvivesProcessKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires SIGKILL")
	}
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint(late), func(t *testing.T) {
			dir := t.TempDir()
			for _, stage := range []string{"produce", "restart"} {
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(exe, "-test.run=^TestInterleavedRecoveryDebtWorker$", "-test.v")
				cmd.Env = append(os.Environ(), "WACLI_INTERLEAVE_DIR="+dir, "WACLI_INTERLEAVE_STAGE="+stage, fmt.Sprintf("WACLI_INTERLEAVE_LATE=%t", late))
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				cmd.Stderr = os.Stderr
				if err = cmd.Start(); err != nil {
					t.Fatal(err)
				}
				timer := time.AfterFunc(10*time.Second, func() { _ = cmd.Process.Kill() })
				scan := bufio.NewScanner(stdout)
				barrier := false
				for scan.Scan() {
					t.Log(scan.Text())
					if scan.Text() == "INTERLEAVE_DEBT_BARRIER" {
						barrier = true
						break
					}
				}
				if err = scan.Err(); err != nil {
					t.Fatal(err)
				}
				if stage == "produce" {
					if !barrier {
						t.Fatal("missing debt barrier")
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
				timer.Stop()
			}
		})
	}
}

func TestInterleavedRecoveryDebtWorker(t *testing.T) {
	dir := os.Getenv("WACLI_INTERLEAVE_DIR")
	if dir == "" {
		t.Skip("subprocess helper")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	a, err := New(Options{StoreDir: dir})
	must(err)
	defer a.Close()
	col := appstate.WAPatchRegularHigh
	if os.Getenv("WACLI_INTERLEAVE_STAGE") == "produce" {
		f := newFakeWA()
		a.wa = f
		gen, _, err := a.db.BeginAppStateRecovery(string(col))
		must(err)
		idA := sdkstore.AppStateDeliveryID{Generation: "interleave-restart", Sequence: 1}
		idB := idA
		idB.Sequence = 2
		emit := func() {
			for _, id := range []sdkstore.AppStateDeliveryID{idA, idB} {
				must(a.db.ApplyAppStateDelivery(t.Context(), appstore.DeliveryCheckpoint{Generation: id.Generation, Sequence: id.Sequence, Digest: id.Digest[:]}, nil, []string{string(col)}))
				f.emit(&events.AppState{Index: []string{appstate.IndexStar, "456@s.whatsapp.net", "withheld"}, DeliveryID: &id})
			}
			f.emit(&events.AppStateSyncComplete{Name: col, Recovery: true, CompletedDeliveryIDs: []sdkstore.AppStateDeliveryID{idA}})
		}
		if os.Getenv("WACLI_INTERLEAVE_LATE") == "true" {
			capture := a.newAppStateRecoveryCapture(t.Context(), col, "late_interleave")
			emit()
			a.runLateAppStateRecovery(col, &lateAppStateRecovery{capture: capture, generation: gen, superseded: make(chan struct{})}, make(chan struct{}))
		} else {
			f.onAppStateRecovery = func(string) { emit() }
			err = a.recoverMismatchingAppState(t.Context(), col, gen, &appStatePersistenceTracker{}, nil)
			if err == nil || !strings.Contains(err.Error(), "recovery changed") {
				t.Fatalf("must surface newer debt: %v", err)
			}
		}
	}
	debt, err := a.db.AppStateRecoveryRequired(string(col))
	must(err)
	if !debt {
		t.Fatal("newer incomplete delivery debt lost")
	}
	// No typed Star was dispatched or persisted. Check the durable table directly.
	var stars int
	raw, err := sql.Open("sqlite3", sqliteutil.FileURI(filepath.Join(dir, "wacli.db"), "_foreign_keys=on"))
	must(err)
	defer raw.Close()
	must(raw.QueryRow("SELECT count(*) FROM starred").Scan(&stars))
	if stars != 0 {
		t.Fatalf("partial foreign projection: %d", stars)
	}
	if os.Getenv("WACLI_INTERLEAVE_STAGE") == "produce" {
		fmt.Println("INTERLEAVE_DEBT_BARRIER")
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		select {}
	}
	fmt.Println("INTERLEAVE_RESTART_DEBT_PRESERVED")
}
