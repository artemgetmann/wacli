// Copyright (c) 2026 The wacli contributors
// SPDX-License-Identifier: MPL-2.0
package whatsmeow

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	log "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

func deliveryTestDevice(t *testing.T, path string) *store.Device {
	t.Helper()
	var c *sqlstore.Container
	var err error
	if os.Getenv("SDK_TX_DIR") != "" {
		var db *sql.DB
		db, err = sql.Open("delivery-crash-sqlite", "file:"+path+"?_foreign_keys=on&_synchronous=FULL")
		if err == nil {
			c = sqlstore.NewWithDB(db, "sqlite3", log.Noop)
			err = c.Upgrade(t.Context())
		}
	} else {
		c, err = sqlstore.New(t.Context(), "sqlite3", "file:"+path+"?_foreign_keys=on&_synchronous=FULL", log.Noop)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	d, err := c.GetFirstDevice(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == nil {
		d.ID = &types.JID{User: "15550000000", Device: 7, Server: types.DefaultUserServer}
		d.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
		if err = d.Save(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err = d.AppStateKeys.PutAppStateSyncKey(t.Context(), []byte{1}, store.AppStateSyncKey{Data: []byte(strings.Repeat("s", 32)), Fingerprint: []byte{1}, Timestamp: 1700000000000}); err != nil {
		t.Fatal(err)
	}
	return d
}

// The real SendAppState/EncodePatch/DecodePatches path is used. Only the IQ
// transport is substituted; the synthetic server validates the signed request.
func TestDurableAppStateSendProducer(t *testing.T) {
	for _, mode := range []string{"success", "conflict", "reject", "malformed", "cancel", "accepted-stage-failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			dir := t.TempDir()
			path := filepath.Join(dir, "client.db")
			device := deliveryTestDevice(t, path)
			server := deliveryTestDevice(t, filepath.Join(dir, "server.db"))
			cli := NewClient(device, log.Noop)
			collection := appstate.WAPatchRegularLow
			jid := types.NewJID("456", types.DefaultUserServer)
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			var consumed []uint64
			must(cli.SetAppStateDeliveryConsumer(func(ctx context.Context, d *store.AppStateDelivery) error {
				records, err := appstate.DecodeDeliveryMutations(d.Transition.Payload)
				if err != nil {
					return err
				}
				if len(records) != 1 || records[0].Index[0] != appstate.IndexPin {
					t.Fatalf("unexpected verified payload: %+v", records)
				}
				consumed = append(consumed, d.Transition.AfterVersion)
				return nil
			}))
			var patches []*waServerSync.SyncdPatch
			serverApply := func(p *waServerSync.SyncdPatch) {
				t.Helper()
				v, h, e := server.AppState.GetAppStateVersion(ctx, string(collection))
				must(e)
				_, _, e = appstate.NewProcessor(server, log.Noop).DecodePatches(ctx, &appstate.PatchList{Name: collection, Patches: []*waServerSync.SyncdPatch{p}}, appstate.HashState{Version: v, Hash: h}, true)
				must(e)
				patches = append(patches, p)
			}
			if mode == "conflict" {
				encoded, e := appstate.NewProcessor(server, log.Noop).EncodePatch(ctx, []byte{1}, appstate.HashState{}, appstate.BuildPin(jid, false))
				must(e)
				p := &waServerSync.SyncdPatch{}
				must(proto.Unmarshal(encoded, p))
				p.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(1)}
				serverApply(p)
			}
			attempts, accepted, fetches := 0, 0, 0
			cli.appStateQuery = func(ctx context.Context, q infoQuery) (*waBinary.Node, error) {
				col := q.Content.([]waBinary.Node)[0].GetChildByTag("collection")
				patchNode := col.GetChildByTag("patch")
				out := waBinary.Node{Tag: "collection", Attrs: waBinary.Attrs{"name": string(collection)}}
				if patchNode.Tag == "patch" {
					attempts++
					switch mode {
					case "cancel":
						return nil, context.Canceled
					case "reject":
						out.Attrs["type"] = "error"
						out.Content = []waBinary.Node{{Tag: "error", Attrs: waBinary.Attrs{"code": "500"}}}
					case "malformed":
						return &waBinary.Node{Tag: "iq"}, nil
					default:
						base, _ := col.Attrs["version"].(uint64)
						version, _, e := server.AppState.GetAppStateVersion(ctx, string(collection))
						if e != nil {
							return nil, e
						}
						if base != version {
							out.Attrs["type"] = "error"
							var nodes []waBinary.Node
							for _, p := range patches {
								encoded, e := proto.Marshal(p)
								if e != nil {
									return nil, e
								}
								nodes = append(nodes, waBinary.Node{Tag: "patch", Content: encoded})
							}
							out.Content = []waBinary.Node{{Tag: "error", Attrs: waBinary.Attrs{"code": "409"}}, {Tag: "patches", Content: nodes}}
						} else {
							raw, ok := patchNode.Content.([]byte)
							if !ok {
								t.Fatal("missing signed request")
							}
							p := &waServerSync.SyncdPatch{}
							if e = proto.Unmarshal(raw, p); e != nil {
								return nil, e
							}
							p.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(base + 1)}
							serverApply(p)
							accepted++
						}
					}
				} else {
					fetches++
					base, _ := col.Attrs["version"].(uint64)
					var nodes []waBinary.Node
					for _, p := range patches {
						if p.GetVersion().GetVersion() > base {
							encoded, e := proto.Marshal(p)
							if e != nil {
								return nil, e
							}
							nodes = append(nodes, waBinary.Node{Tag: "patch", Content: encoded})
						}
					}
					out.Content = []waBinary.Node{{Tag: "patches", Content: nodes}}
				}
				return &waBinary.Node{Tag: "iq", Content: []waBinary.Node{{Tag: "sync", Content: []waBinary.Node{out}}}}, nil
			}
			raw, e := sql.Open("sqlite3", "file:"+path+"?_foreign_keys=on&_synchronous=FULL")
			must(e)
			defer raw.Close()
			if mode == "accepted-stage-failure" {
				_, e = raw.Exec(`CREATE TRIGGER fail_cursor BEFORE INSERT ON whatsmeow_app_state_version BEGIN SELECT RAISE(FAIL,'synthetic accepted stage failure'); END`)
				must(e)
			}
			err := cli.SendAppState(ctx, appstate.BuildPin(jid, true))
			pending, e := device.AppState.(store.AppStateDeliveryStore).PendingAppStateDelivery(ctx)
			must(e)
			version, _, e := device.AppState.GetAppStateVersion(ctx, string(collection))
			must(e)
			if mode == "success" || mode == "conflict" {
				must(err)
				want := uint64(1)
				if mode == "conflict" {
					want = 2
				}
				if version != want || accepted != 1 || uint64(len(consumed)) != want || pending != nil || fetches != 1 {
					t.Fatalf("cursor=%d accepts=%d consumed=%v pending=%+v fetches=%d", version, accepted, consumed, pending, fetches)
				}
			} else {
				if err == nil || version != 0 || len(consumed) != 0 || pending != nil {
					t.Fatalf("failure consumed state: err=%v cursor=%d consumed=%v pending=%+v", err, version, consumed, pending)
				}
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
				if mode == "accepted-stage-failure" {
					if accepted != 1 || !strings.Contains(err.Error(), "accepted; local persistence incomplete") {
						t.Fatalf("acceptance hidden: %v", err)
					}
					must(cli.DrainAppStateDelivery(ctx))
					if len(consumed) != 0 {
						t.Fatal("drain invented a missing payload")
					}
					_, e = raw.Exec("DROP TRIGGER fail_cursor")
					must(e)
					must(cli.FetchAppState(ctx, collection, false, false))
					if attempts != 1 || accepted != 1 || len(consumed) != 1 {
						t.Fatalf("refetch resent or failed: attempts=%d accepts=%d consumed=%v", attempts, accepted, consumed)
					}
				} else if accepted != 0 {
					t.Fatal("rejected request accepted")
				}
			}
		})
	}
}
