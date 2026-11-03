package app

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/sqliteutil"
	appstore "github.com/openclaw/wacli/internal/store"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	sdkstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	log "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// This characterizes the unresolved consumption/delivery gap; it deliberately
// asserts the current bad state. A repair must change these expectations rather
// than treating a passing characterization as proof of end-to-end durability.
// No cursor injection, paired device, Connect, network or production store.
func TestAppStateDeliveryGapAcrossProcessKill(t *testing.T) {
	runGapScenarios(t, false, []string{"held", "partial", "delivered"})
}
func TestAppStateDurableDeliveryAcrossProcessKill(t *testing.T) {
	runGapScenarios(t, true, []string{"held", "partial", "delivered", "before-projection", "after-checkpoint", "projection-failure", "cursor-failure"})
}
func runGapScenarios(t *testing.T, durable bool, scenarios []string) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix SIGKILL process status")
	}
	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			run := func(stage string, kill bool) {
				t.Helper()
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, exe, "-test.run=^TestAppStateDeliveryGapWorker$", "-test.v")
				cmd.Env = append(os.Environ(), "WACLI_GAP_DIR="+dir, "WACLI_GAP_STAGE="+stage, "WACLI_GAP_SCENARIO="+scenario, "WACLI_GAP_FIXED="+fmt.Sprint(durable))
				stdin, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				defer stdin.Close()
				pipe, err := cmd.StdoutPipe()
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
				var output []string
				defer func() {
					if t.Failed() {
						t.Log(strings.Join(output, "\n"))
					}
				}()
				scanner := bufio.NewScanner(pipe)
				barrier := false
				for scanner.Scan() {
					line := scanner.Text()
					output = append(output, line)
					if strings.HasPrefix(line, "GAP ") {
						t.Log(line)
					}
					if line == "GAP_BARRIER" {
						barrier = true
						break
					}
				}
				if err = scanner.Err(); err != nil {
					t.Fatal(err)
				}
				if kill {
					if !barrier {
						t.Fatal("worker exited without durable-state barrier")
					}
					if err = cmd.Process.Kill(); err != nil {
						t.Fatal(err)
					}
					err = cmd.Wait()
					status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
					if err == nil || !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
						t.Fatalf("expected SIGKILL, got %v / %v", err, cmd.ProcessState)
					}
				} else if err = cmd.Wait(); err != nil {
					t.Fatal(err)
				}
			}
			run("produce", scenario != "delivered")
			if scenario == "after-checkpoint" {
				run("arrival", false)
			}
			// This is a new OS process, not a DB close/reopen in the producer.
			run("inspect", false)
		})
	}
}

func gapDevice(t *testing.T, path string) (*sqlstore.Container, *sdkstore.Device) {
	t.Helper()
	ctx := t.Context()
	c, err := sqlstore.New(ctx, "sqlite3", sqliteutil.FileURI(path, "_foreign_keys=on&_synchronous=FULL"), log.Noop)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	d, err := c.GetFirstDevice(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == nil {
		d.ID = &types.JID{User: "15550000000", Device: 7, Server: types.DefaultUserServer}
		d.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
		if err = d.Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = d.AppStateKeys.PutAppStateSyncKey(ctx, []byte{1}, sdkstore.AppStateSyncKey{Data: []byte(strings.Repeat("s", 32)), Fingerprint: []byte{1}, Timestamp: 1700000000000}); err != nil {
		t.Fatal(err)
	}
	return c, d
}

func TestAppStateDeliveryGapWorker(t *testing.T) {
	dir := os.Getenv("WACLI_GAP_DIR")
	if dir == "" {
		t.Skip("subprocess helper")
	}
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	a, err := New(Options{StoreDir: dir})
	must(err)
	defer a.Close()
	f := newFakeWA()
	a.wa = f
	_, device := gapDevice(t, filepath.Join(dir, "session.db"))
	sdk := whatsmeow.NewClient(device, log.Noop)
	remove, err := a.AddChatStatePersistenceHandler(ctx)
	must(err)
	defer remove()
	sdk.AddEventHandler(f.emit)
	jid := types.NewJID("456", types.DefaultUserServer)
	at := time.Unix(1700000000, 0)
	scenario := os.Getenv("WACLI_GAP_SCENARIO")
	durable := os.Getenv("WACLI_GAP_FIXED") == "true"
	stage := os.Getenv("WACLI_GAP_STAGE")
	inspect := func(delivered, pendingExpected bool) {
		chat, e := a.db.GetChat(jid.String())
		must(e)
		v, _, e := device.AppState.GetAppStateVersion(ctx, string(appstate.WAPatchRegularLow))
		must(e)
		debt, e := a.db.AppStateRecoveryRequired(string(appstate.WAPatchRegularLow))
		must(e)
		settings, e := device.ChatSettings.GetChatSettings(ctx, jid)
		must(e)
		expectedVersion := uint64(1)
		if scenario == "cursor-failure" {
			expectedVersion = 0
		}
		if v != expectedVersion || debt {
			t.Fatalf("cursor/debt = %d/%t", v, debt)
		}
		expectedCount := map[bool]int{true: 0, false: 2}[delivered]
		expectedUnread := !delivered
		if scenario == "after-checkpoint" && stage == "inspect" {
			expectedCount = 1
			expectedUnread = true
		}
		if chat.Archived != delivered || chat.Pinned != delivered || chat.Unread != expectedUnread || chat.UnreadCount != expectedCount {
			t.Fatalf("unexpected persisted App state: %+v", chat)
		}
		if scenario == "held" && !settings.Archived {
			t.Fatal("SDK collector did not update settings")
		}
		if scenario == "partial" && settings.Archived != durable {
			t.Fatal("partial batch SDK covered-state persistence mismatch")
		}
		pending, e := device.AppState.(sdkstore.AppStateDeliveryStore).PendingAppStateDelivery(ctx)
		must(e)
		if (pending != nil) != pendingExpected {
			t.Fatalf("pending=%+v want %t", pending, pendingExpected)
		}
		// This counter covers this helper's fake interface only. It is not a
		// cumulative SDK transport audit or outgoing-message deduplication proof.
		if len(f.archiveCalls)+len(f.markReadCalls)+len(f.readReceiptCalls)+len(f.readSelfCalls) != 0 {
			t.Fatal("unexpected fake send")
		}
		raw, e := json.Marshal(map[string]any{"scenario": scenario, "cursor": v, "debt": debt, "app_archived": chat.Archived, "app_pinned": chat.Pinned, "app_unread_count": chat.UnreadCount, "sdk_archived": settings.Archived, "pending": pending != nil})
		must(e)
		fmt.Println("GAP " + string(raw))
	}
	pause := func() {
		fmt.Println("GAP_BARRIER")
		var b [1]byte
		_, e := os.Stdin.Read(b[:])
		t.Fatalf("parent released crash barrier without SIGKILL: %v", e)
	}
	if durable {
		must(sdk.SetAppStateDeliveryConsumer(func(ctx context.Context, d *sdkstore.AppStateDelivery) error {
			if stage == "produce" && scenario == "before-projection" {
				inspect(false, true)
				pause()
			}
			err := a.consumeAppStateDelivery(ctx, d)
			if err == nil && stage == "produce" && scenario == "after-checkpoint" {
				inspect(true, true)
				pause()
			}
			return err
		}))
	}
	if stage == "arrival" {
		must(a.db.UpsertMessage(appstore.UpsertMessageParams{ChatJID: jid.String(), MsgID: "newer", SenderJID: jid.String(), Timestamp: at, Text: "synthetic"}))
		must(a.db.IncrementChatUnread(jid.String()))
		return
	}
	if stage == "inspect" {
		if durable && scenario != "cursor-failure" {
			if scenario == "before-projection" || scenario == "projection-failure" {
				inspect(false, true)
			}
			if scenario == "after-checkpoint" {
				inspect(true, true)
			}
			if scenario == "projection-failure" {
				raw, e := sql.Open("sqlite3", sqliteutil.FileURI(filepath.Join(dir, "wacli.db"), "_foreign_keys=on"))
				must(e)
				_, e = raw.Exec("DROP TRIGGER fail_projection")
				must(e)
				must(raw.Close())
			}
			must(sdk.DrainAppStateDelivery(ctx))
		}
		inspect(scenario == "delivered" || durable && scenario != "cursor-failure", false)
		return
	}
	must(a.db.UpsertChat(jid.String(), "dm", "synthetic", at))
	for _, id := range []string{"earlier", "handled"} {
		must(a.db.UpsertMessage(appstore.UpsertMessageParams{ChatJID: jid.String(), MsgID: id, SenderJID: jid.String(), Timestamp: at, Text: "synthetic"}))
	}
	must(a.db.SetChatUnreadCount(jid.String(), 2))
	_, sender := gapDevice(t, filepath.Join(dir, "sender.db"))
	source := whatsmeow.NewClient(sender, log.Noop)
	var patches []*waServerSync.SyncdPatch
	for n := 0; n < 2; n++ {
		v, h, e := sender.AppState.GetAppStateVersion(ctx, string(appstate.WAPatchRegularLow))
		must(e)
		key := &waCommon.MessageKey{RemoteJID: proto.String(jid.String()), ID: proto.String("handled"), FromMe: proto.Bool(false)}
		archive := appstate.BuildArchive(jid, n == 0, at, key)
		pin := appstate.BuildPin(jid, n == 0)
		read := appstate.BuildMarkChatAsRead(jid, n == 0, at, key)
		info := appstate.PatchInfo{Type: appstate.WAPatchRegularLow, Timestamp: at.Add(time.Duration(n) * time.Second), Mutations: append(append(archive.Mutations[:1], pin.Mutations...), read.Mutations...)}
		encoded, e := appstate.NewProcessor(sender, log.Noop).EncodePatch(ctx, []byte{1}, appstate.HashState{Version: v, Hash: h}, info)
		must(e)
		patch := &waServerSync.SyncdPatch{}
		must(proto.Unmarshal(encoded, patch))
		patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(v + 1)}
		var ignored []any
		_, e = source.DangerousInternals().ApplyAppStatePatches(ctx, appstate.WAPatchRegularLow, appstate.HashState{Version: v, Hash: h}, &appstate.PatchList{Name: appstate.WAPatchRegularLow, Patches: []*waServerSync.SyncdPatch{patch}}, false, &ignored)
		must(e)
		patches = append(patches, patch)
	}
	if scenario == "projection-failure" {
		raw, e := sql.Open("sqlite3", sqliteutil.FileURI(filepath.Join(dir, "wacli.db"), "_foreign_keys=on"))
		must(e)
		_, e = raw.Exec(`CREATE TRIGGER fail_projection BEFORE UPDATE OF pinned ON chats WHEN NEW.pinned=1 BEGIN SELECT RAISE(FAIL,'synthetic projection failure'); END`)
		must(e)
		must(raw.Close())
	}
	if scenario == "cursor-failure" {
		raw, e := sql.Open("sqlite3", sqliteutil.FileURI(filepath.Join(dir, "session.db"), "_foreign_keys=on"))
		must(e)
		_, e = raw.Exec(`CREATE TRIGGER fail_cursor BEFORE INSERT ON whatsmeow_app_state_version BEGIN SELECT RAISE(FAIL,'synthetic cursor failure'); END`)
		must(e)
		must(raw.Close())
	}
	if scenario == "partial" {
		patches[1].SnapshotMAC[0] ^= 1
	} else {
		patches = patches[:1]
	}
	var held []any
	_, err = sdk.DangerousInternals().ApplyAppStatePatches(ctx, appstate.WAPatchRegularLow, appstate.HashState{}, &appstate.PatchList{Name: appstate.WAPatchRegularLow, Patches: patches}, false, &held)
	if scenario == "partial" || scenario == "projection-failure" || scenario == "cursor-failure" {
		if err == nil || len(held) != 0 {
			t.Fatalf("expected decoder error before collector, got %v / %d events", err, len(held))
		}
		t.Logf("expected second-patch validation failure: %v", err)
	} else {
		must(err)
		if len(held) == 0 {
			t.Fatal("collector produced no events")
		}
	}
	if scenario == "delivered" {
		for _, evt := range held {
			sdk.DangerousInternals().DispatchEvent(evt)
		}
		must(a.appStatePersist.waitIdle(ctx))
	}
	inspect(scenario == "delivered" || durable && scenario != "projection-failure" && scenario != "cursor-failure", scenario == "projection-failure")
	if scenario != "delivered" {
		pause()
	}
}
