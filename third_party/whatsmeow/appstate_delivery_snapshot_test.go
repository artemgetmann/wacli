// Copyright (c) 2026 The wacli contributors
// SPDX-License-Identifier: MPL-2.0
package whatsmeow

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/proto/waSyncdSnapshotRecovery"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	log "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

func TestDurableSnapshotReplacementFailureAndPendingGate(t *testing.T) {
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	device := deliveryTestDevice(t, filepath.Join(t.TempDir(), "client.db"))
	cli := NewClient(device, log.Noop)
	col := appstate.WAPatchRegularLow
	jid := types.NewJID("456", types.DefaultUserServer)
	fail := false
	var deliveries []store.AppStateDeliveryID
	must(cli.SetAppStateDeliveryConsumer(func(ctx context.Context, d *store.AppStateDelivery) error {
		if fail {
			return fmt.Errorf("synthetic projection unavailable")
		}
		deliveries = append(deliveries, d.ID)
		return nil
	}))
	snapshot := func(pin bool) *waServerSync.SyncdSnapshot {
		source := deliveryTestDevice(t, filepath.Join(t.TempDir(), "source.db"))
		encoded, e := appstate.NewProcessor(source, log.Noop).EncodePatch(ctx, []byte{1}, appstate.HashState{}, appstate.BuildPin(jid, pin))
		must(e)
		p := &waServerSync.SyncdPatch{}
		must(proto.Unmarshal(encoded, p))
		return &waServerSync.SyncdSnapshot{Version: &waServerSync.SyncdVersion{Version: proto.Uint64(1)}, Records: []*waServerSync.SyncdRecord{p.Mutations[0].Record}, Mac: p.SnapshotMAC, KeyID: p.KeyID}
	}
	current := snapshot(true)
	queries := 0
	cli.appStateBlobDownload = func(context.Context, *waServerSync.ExternalBlobReference) ([]byte, error) {
		return proto.Marshal(current)
	}
	ref, e := proto.Marshal(&waServerSync.ExternalBlobReference{})
	must(e)
	cli.appStateQuery = func(context.Context, infoQuery) (*waBinary.Node, error) {
		queries++
		return &waBinary.Node{Tag: "iq", Content: []waBinary.Node{{Tag: "sync", Content: []waBinary.Node{{Tag: "collection", Attrs: waBinary.Attrs{"name": string(col)}, Content: []waBinary.Node{{Tag: "snapshot", Content: ref}}}}}}}, nil
	}
	must(cli.FetchAppState(ctx, col, true, false))
	first := deliveries[0]
	current = snapshot(false)
	must(cli.FetchAppState(ctx, col, true, false))
	second := deliveries[1]
	if first.Generation != second.Generation || first.Sequence+1 != second.Sequence || first.Digest == second.Digest {
		t.Fatal("equal-version replacement identity collision")
	}
	version, hash, e := device.AppState.GetAppStateVersion(ctx, string(col))
	must(e)
	bad := proto.Clone(current).(*waServerSync.SyncdSnapshot)
	bad.Mac[0] ^= 1
	current = bad
	if e = cli.FetchAppState(ctx, col, true, false); e == nil {
		t.Fatal("corrupt replacement accepted")
	}
	v, h, e := device.AppState.GetAppStateVersion(ctx, string(col))
	must(e)
	if v != version || h != hash || len(deliveries) != 2 {
		t.Fatal("failed full snapshot reset or projected old state")
	}
	current = snapshot(true)
	fail = true
	if e = cli.FetchAppState(ctx, col, true, false); e == nil {
		t.Fatal("projection failure hidden")
	}
	native := device.AppState.(store.AppStateDeliveryStore)
	pending, e := native.PendingAppStateDelivery(ctx)
	must(e)
	if pending == nil {
		t.Fatal("failed projection lost payload")
	}
	before := queries
	if e = cli.FetchAppState(ctx, col, true, false); e == nil || queries != before {
		t.Fatal("pending projection allowed network/reset")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if e = cli.FetchAppState(canceled, col, true, false); e != context.Canceled || queries != before {
		t.Fatalf("canceled waiter progressed: %v", e)
	}
	fail = false
	must(cli.DrainAppStateDelivery(ctx))
	pending, e = native.PendingAppStateDelivery(ctx)
	must(e)
	if pending != nil || len(deliveries) != 3 {
		t.Fatal("pending projection did not retry exactly")
	}
}

func TestDurablePeerRecoveryKeepsFreshnessGate(t *testing.T) {
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	col := appstate.WAPatchRegularLow
	jid := types.NewJID("456", types.DefaultUserServer)
	source := deliveryTestDevice(t, filepath.Join(t.TempDir(), "source.db"))
	device := deliveryTestDevice(t, filepath.Join(t.TempDir(), "client.db"))
	cli := NewClient(device, log.Noop)
	consumed := 0
	must(cli.SetAppStateDeliveryConsumer(func(context.Context, *store.AppStateDelivery) error { consumed++; return nil }))
	var first *waServerSync.SyncdPatch
	recoveries := make(map[uint64]*waSyncdSnapshotRecovery.SyncdSnapshotRecovery)
	for seq := uint64(1); seq <= 2; seq++ {
		v, h, e := source.AppState.GetAppStateVersion(ctx, string(col))
		must(e)
		encoded, e := appstate.NewProcessor(source, log.Noop).EncodePatch(ctx, []byte{1}, appstate.HashState{Version: v, Hash: h}, appstate.BuildPin(jid, seq == 1))
		must(e)
		patch := &waServerSync.SyncdPatch{}
		must(proto.Unmarshal(encoded, patch))
		patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(seq)}
		mutations, state, e := appstate.NewProcessor(source, log.Noop).DecodePatches(ctx, &appstate.PatchList{Name: col, Patches: []*waServerSync.SyncdPatch{patch}}, appstate.HashState{Version: v, Hash: h}, true)
		must(e)
		recovery := &waSyncdSnapshotRecovery.SyncdSnapshotRecovery{CollectionName: proto.String(string(col)), Version: &waSyncdSnapshotRecovery.SyncdVersion{Version: proto.Uint64(seq)}, CollectionLthash: state.Hash[:]}
		for _, m := range mutations {
			index, e := json.Marshal(m.Index)
			must(e)
			recovery.MutationRecords = append(recovery.MutationRecords, &waSyncdSnapshotRecovery.SyncdPlainTextRecord{KeyID: m.KeyID, Mac: m.ValueMAC, Value: &waSyncAction.SyncActionData{Index: index, Value: m.Action, Version: proto.Int32(m.Version)}})
		}
		recoveries[seq] = recovery
		if seq == 1 {
			first = patch
		}
	}
	var held []any
	_, err := cli.DangerousInternals().ApplyAppStatePatches(ctx, col, appstate.HashState{}, &appstate.PatchList{Name: col, Patches: []*waServerSync.SyncdPatch{first}}, false, &held)
	must(err)
	reply := func(recovery *waSyncdSnapshotRecovery.SyncdSnapshotRecovery, compressed bool) {
		t.Helper()
		encoded, e := proto.Marshal(recovery)
		must(e)
		if compressed {
			var out bytes.Buffer
			w := gzip.NewWriter(&out)
			_, e = w.Write(encoded)
			must(e)
			must(w.Close())
			encoded = out.Bytes()
		}
		result := []*waE2E.PeerDataOperationRequestResponseMessage_PeerDataOperationResult{{SyncdSnapshotFatalRecoveryResponse: &waE2E.PeerDataOperationRequestResponseMessage_PeerDataOperationResult_SyncDSnapshotFatalRecoveryResponse{CollectionSnapshot: encoded, IsCompressed: proto.Bool(compressed)}}}
		if !cli.handleAppStateRecovery(ctx, "synthetic-request", result) {
			t.Fatal("recovery handler failed")
		}
	}
	reply(recoveries[1], false)
	if consumed != 1 {
		t.Fatal("equal-version peer recovery bypassed freshness gate")
	}
	reply(recoveries[2], true)
	if consumed != 2 {
		t.Fatal("newer gzip peer recovery not committed")
	}
	version, _, err := device.AppState.GetAppStateVersion(ctx, string(col))
	must(err)
	if version != 2 {
		t.Fatalf("peer cursor %d", version)
	}
	reply(recoveries[1], true)
	reply(recoveries[2], false)
	if consumed != 2 {
		t.Fatal("older/equal peer response was reapplied")
	}
}

func TestDurableProducerWaitIsCancelable(t *testing.T) {
	device := deliveryTestDevice(t, filepath.Join(t.TempDir(), "client.db"))
	cli := NewClient(device, log.Noop)
	if err := cli.SetAppStateDeliveryConsumer(func(context.Context, *store.AppStateDelivery) error { return nil }); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	var queries atomic.Int32
	cli.appStateQuery = func(ctx context.Context, q infoQuery) (*waBinary.Node, error) {
		queries.Add(1)
		close(entered)
		select {
		case <-release:
			return &waBinary.Node{Tag: "iq"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	first := make(chan error, 1)
	go func() {
		first <- cli.SendAppState(ctx, appstate.BuildPin(types.NewJID("456", types.DefaultUserServer), true))
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("producer did not enter IQ transport")
	}
	waiting, stop := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() { result <- cli.FetchAppState(waiting, appstate.WAPatchRegularLow, true, false) }()
	stop()
	select {
	case err := <-result:
		if err != context.Canceled {
			t.Fatalf("wait cancellation lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled producer remained blocked behind IQ")
	}
	if queries.Load() != 1 {
		t.Fatal("canceled producer performed IQ work")
	}
	close(release)
	released = true
	select {
	case err := <-first:
		if err == nil {
			t.Fatal("malformed synthetic IQ unexpectedly succeeded")
		}
	case <-ctx.Done():
		t.Fatal("first producer did not finish")
	}

}

func TestDurableExplicitResetWithPatchList(t *testing.T) {
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	col := appstate.WAPatchRegularLow
	jid := types.NewJID("456", types.DefaultUserServer)
	device := deliveryTestDevice(t, filepath.Join(t.TempDir(), "client.db"))
	cli := NewClient(device, log.Noop)
	var kinds []string
	must(cli.SetAppStateDeliveryConsumer(func(ctx context.Context, d *store.AppStateDelivery) error {
		kinds = append(kinds, d.Transition.Kind)
		return nil
	}))
	encode := func(pin bool) *waServerSync.SyncdPatch {
		source := deliveryTestDevice(t, filepath.Join(t.TempDir(), "source.db"))
		data, e := appstate.NewProcessor(source, log.Noop).EncodePatch(ctx, []byte{1}, appstate.HashState{}, appstate.BuildPin(jid, pin))
		must(e)
		p := &waServerSync.SyncdPatch{}
		must(proto.Unmarshal(data, p))
		p.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(1)}
		return p
	}
	first := encode(true)
	var ignored []any
	_, err := cli.DangerousInternals().ApplyAppStatePatches(ctx, col, appstate.HashState{}, &appstate.PatchList{Name: col, Patches: []*waServerSync.SyncdPatch{first}}, false, &ignored)
	must(err)
	replacement := encode(false)
	data, err := proto.Marshal(replacement)
	must(err)
	cli.appStateQuery = func(context.Context, infoQuery) (*waBinary.Node, error) {
		return &waBinary.Node{Tag: "iq", Content: []waBinary.Node{{Tag: "sync", Content: []waBinary.Node{{Tag: "collection", Attrs: waBinary.Attrs{"name": string(col)}, Content: []waBinary.Node{{Tag: "patches", Content: []waBinary.Node{{Tag: "patch", Content: data}}}}}}}}}, nil
	}
	must(cli.FetchAppState(ctx, col, true, false))
	settings, err := device.ChatSettings.GetChatSettings(ctx, jid)
	must(err)
	if settings.Pinned || len(kinds) != 2 || kinds[1] != "snapshot" {
		t.Fatalf("from-zero reset was not a verified replacement: pin=%t kinds=%v", settings.Pinned, kinds)
	}
}

type failingDeliveryPinStore struct {
	store.ChatSettingsStore
	fail bool
}

func (s *failingDeliveryPinStore) PutPinned(ctx context.Context, jid types.JID, pin bool) error {
	if s.fail {
		return fmt.Errorf("synthetic SDK cache failure")
	}
	return s.ChatSettingsStore.PutPinned(ctx, jid, pin)
}
func TestCoveredSDKCacheFailureKeepsPendingDelivery(t *testing.T) {
	ctx := t.Context()
	device := deliveryTestDevice(t, filepath.Join(t.TempDir(), "client.db"))
	chatStore := &failingDeliveryPinStore{ChatSettingsStore: device.ChatSettings, fail: true}
	device.ChatSettings = chatStore
	cli := NewClient(device, log.Noop)
	consumed := 0
	if err := cli.SetAppStateDeliveryConsumer(func(context.Context, *store.AppStateDelivery) error { consumed++; return nil }); err != nil {
		t.Fatal(err)
	}
	jid := types.NewJID("456", types.DefaultUserServer)
	encoded, err := appstate.NewProcessor(device, log.Noop).EncodePatch(ctx, []byte{1}, appstate.HashState{}, appstate.BuildPin(jid, true))
	if err != nil {
		t.Fatal(err)
	}
	patch := &waServerSync.SyncdPatch{}
	if err = proto.Unmarshal(encoded, patch); err != nil {
		t.Fatal(err)
	}
	patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(1)}
	var held []any
	_, err = cli.DangerousInternals().ApplyAppStatePatches(ctx, appstate.WAPatchRegularLow, appstate.HashState{}, &appstate.PatchList{Name: appstate.WAPatchRegularLow, Patches: []*waServerSync.SyncdPatch{patch}}, false, &held)
	native := device.AppState.(store.AppStateDeliveryStore)
	pending, e := native.PendingAppStateDelivery(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if err == nil || pending == nil || consumed != 0 || len(held) != 0 {
		t.Fatalf("failed SDK cache write consumed delivery: err=%v pending=%+v consumed=%d held=%d", err, pending, consumed, len(held))
	}
	chatStore.fail = false
	if err = cli.DrainAppStateDelivery(ctx); err != nil {
		t.Fatal(err)
	}
	pending, e = native.PendingAppStateDelivery(ctx)
	if e != nil {
		t.Fatal(e)
	}
	settings, e := device.ChatSettings.GetChatSettings(ctx, jid)
	if e != nil {
		t.Fatal(e)
	}
	if pending != nil || consumed != 1 || !settings.Pinned {
		t.Fatal("pending SDK cache write was not repaired before ack")
	}
}
