package app

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

	"github.com/openclaw/wacli/internal/sqliteutil"
	appstore "github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	sdkstore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	log "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

type ackBarrierStore struct {
	sdkstore.AppStateStore
	sdkstore.AppStateDeliveryStore
	barrier func()
}

func (s *ackBarrierStore) AckAppStateDelivery(ctx context.Context, id sdkstore.AppStateDeliveryID) error {
	err := s.AppStateDeliveryStore.AckAppStateDelivery(ctx, id)
	if err == nil && s.barrier != nil {
		s.barrier()
	}
	return err
}

type deliverySnapshotWA struct {
	*fakeWA
	sdk      *whatsmeow.Client
	snapshot *waServerSync.SyncdSnapshot
}

func (f *deliverySnapshotWA) FetchAppStateEvents(ctx context.Context, name string, full, only bool) ([]any, error) {
	if !full {
		return nil, fmt.Errorf("fixture requires full replay")
	}
	var held []any
	_, err := f.sdk.DangerousInternals().ApplyAppStatePatches(ctx, appstate.WAPatchName(name), appstate.HashState{}, &appstate.PatchList{Name: appstate.WAPatchName(name), Snapshot: f.snapshot}, true, &held)
	return held, err
}

func TestUncoveredDeliveryRecoversAfterProcessKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix SIGKILL status")
	}
	for _, feature := range []string{"star", "delete", "contact", "call"} {
		t.Run(feature, func(t *testing.T) {
			dir := t.TempDir()
			for _, stage := range []string{"produce", "restart"} {
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				cmd := exec.CommandContext(ctx, exe, "-test.run=^TestUncoveredDeliveryRecoveryWorker$", "-test.v")
				cmd.Env = append(os.Environ(), "WACLI_FEATURE_DIR="+dir, "WACLI_FEATURE="+feature, "WACLI_FEATURE_STAGE="+stage)
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
					if line == "UNCOVERED_ACK_BARRIER" {
						barrier = true
						break
					}
				}
				if err = scan.Err(); err != nil {
					t.Fatal(err)
				}
				if stage == "produce" {
					if !barrier {
						t.Fatal("missing ack barrier")
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
		})
	}
}
func TestUncoveredDeliveryRecoveryWorker(t *testing.T) {
	dir := os.Getenv("WACLI_FEATURE_DIR")
	if dir == "" {
		t.Skip("subprocess helper")
	}
	feature := os.Getenv("WACLI_FEATURE")
	stage := os.Getenv("WACLI_FEATURE_STAGE")
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
	f.journal = wa.NewContactStoreJournal(device.Contacts)
	device.Contacts = f.journal
	device.AppState = f.journal.AppStateStore(device.AppState)
	barrier := &ackBarrierStore{AppStateStore: device.AppState, AppStateDeliveryStore: device.AppState.(sdkstore.AppStateDeliveryStore)}
	device.AppState = barrier
	sdk := whatsmeow.NewClient(device, log.Noop)
	sdk.EmitAppStateEventsOnFullSync = true
	must(sdk.SetAppStateDeliveryConsumer(a.consumeAppStateDelivery))
	remove, err := a.AddChatStatePersistenceHandler(ctx)
	must(err)
	defer remove()
	sdk.AddEventHandler(f.emit)
	jid := types.NewJID("456", types.DefaultUserServer)
	at := time.Unix(1700000000, 0)
	collection := appstate.WAPatchRegularHigh
	if feature == "contact" {
		collection = appstate.WAPatchCriticalUnblockLow
	}
	raw, err := sql.Open("sqlite3", sqliteutil.FileURI(filepath.Join(dir, "wacli.db"), "_foreign_keys=on"))
	must(err)
	defer raw.Close()
	stored := func() int {
		t.Helper()
		var count int
		query := map[string]string{"star": `SELECT COUNT(*) FROM starred WHERE msg_id='handled'`, "delete": `SELECT COUNT(*) FROM messages WHERE msg_id='handled' AND deleted_for_me!=0`, "contact": `SELECT COUNT(*) FROM contacts WHERE full_name='Synthetic Contact'`, "call": `SELECT COUNT(*) FROM call_events WHERE call_id='synthetic-call'`}[feature]
		must(raw.QueryRow(query).Scan(&count))
		return count
	}
	assertGap := func() {
		t.Helper()
		if stored() != 0 {
			t.Fatal("unowned callback persisted before collection")
		}
		debt, e := a.db.AppStateRecoveryRequired(string(collection))
		must(e)
		if !debt {
			t.Fatal("uncovered work lost debt")
		}
		pending, e := device.AppState.(sdkstore.AppStateDeliveryStore).PendingAppStateDelivery(ctx)
		must(e)
		if pending != nil {
			t.Fatal("exact ack did not remove pending payload")
		}
	}
	if stage == "restart" {
		assertGap()
		data, e := os.ReadFile(filepath.Join(dir, "snapshot.pb"))
		must(e)
		snapshot := &waServerSync.SyncdSnapshot{}
		must(proto.Unmarshal(data, snapshot))
		a.wa = &deliverySnapshotWA{fakeWA: f, sdk: sdk, snapshot: snapshot}
		gen, _, e := a.db.BeginAppStateRecovery(string(collection))
		must(e)
		must(a.replayRequiredAppState(ctx, collection, gen, &appStatePersistenceTracker{}))
		if stored() != 1 {
			t.Fatalf("original %s handler did not restore projection", feature)
		}
		debt, e := a.db.AppStateRecoveryRequired(string(collection))
		must(e)
		if debt {
			t.Fatal("verified replay left its own correlated debt")
		}
		fmt.Println("UNCOVERED_RECOVERY_OK " + feature)
		return
	}
	must(a.db.UpsertChat(jid.String(), "dm", "synthetic", at))
	must(a.db.UpsertMessage(appstore.UpsertMessageParams{ChatJID: jid.String(), MsgID: "handled", SenderJID: jid.String(), Timestamp: at, Text: "synthetic"}))
	var mutation appstate.MutationInfo
	switch feature {
	case "star":
		mutation = appstate.BuildStar(jid, jid, "handled", false, true).Mutations[0]
	case "delete":
		mutation = appstate.MutationInfo{Index: []string{appstate.IndexDeleteMessageForMe, jid.String(), "handled", "0", "0"}, Version: 3, Value: &waSyncAction.SyncActionValue{DeleteMessageForMeAction: &waSyncAction.DeleteMessageForMeAction{}}}
	case "contact":
		mutation = appstate.MutationInfo{Index: []string{appstate.IndexContact, jid.String()}, Version: 2, Value: &waSyncAction.SyncActionValue{ContactAction: &waSyncAction.ContactAction{FullName: proto.String("Synthetic Contact"), FirstName: proto.String("Synthetic")}}}
	case "call":
		result := waSyncAction.CallLogRecord_CONNECTED
		kind := waSyncAction.CallLogRecord_REGULAR
		mutation = appstate.MutationInfo{Index: []string{"call_log", "synthetic-call"}, Version: 1, Value: &waSyncAction.SyncActionValue{CallLogAction: &waSyncAction.CallLogAction{CallLogRecord: &waSyncAction.CallLogRecord{CallID: proto.String("synthetic-call"), CallCreatorJID: proto.String(f.LinkedJID()), Participants: []*waSyncAction.CallLogRecord_ParticipantInfo{{UserJID: proto.String(jid.String()), CallResult: &result}}, CallResult: &result, CallType: &kind, Duration: proto.Int64(5), StartTime: proto.Int64(at.UnixMilli()), IsIncoming: proto.Bool(false), IsVideo: proto.Bool(false)}}}}
	}
	_, sender := gapDevice(t, filepath.Join(dir, "sender.db"))
	encoded, err := appstate.NewProcessor(sender, log.Noop).EncodePatch(ctx, []byte{1}, appstate.HashState{}, appstate.PatchInfo{Type: collection, Timestamp: at, Mutations: []appstate.MutationInfo{mutation}})
	must(err)
	patch := &waServerSync.SyncdPatch{}
	must(proto.Unmarshal(encoded, patch))
	patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(1)}
	snapshot := &waServerSync.SyncdSnapshot{Version: patch.Version, Mac: patch.SnapshotMAC, KeyID: patch.KeyID}
	for _, m := range patch.Mutations {
		snapshot.Records = append(snapshot.Records, m.Record)
	}
	data, err := proto.Marshal(snapshot)
	must(err)
	must(os.WriteFile(filepath.Join(dir, "snapshot.pb"), data, 0600))
	barrier.barrier = func() {
		assertGap()
		fmt.Println("UNCOVERED_ACK_BARRIER")
		var b [1]byte
		_, e := os.Stdin.Read(b[:])
		t.Fatalf("parent released ack barrier without SIGKILL: %v", e)
	}
	var held []any
	_, err = sdk.DangerousInternals().ApplyAppStatePatches(ctx, collection, appstate.HashState{}, &appstate.PatchList{Name: collection, Patches: []*waServerSync.SyncdPatch{patch}}, false, &held)
	must(err)
	t.Fatal("producer passed ack barrier")
}
