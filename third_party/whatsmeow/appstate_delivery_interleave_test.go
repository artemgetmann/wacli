// Copyright (c) 2026 The wacli contributors
// SPDX-License-Identifier: MPL-2.0
package whatsmeow

import (
	"context"
	"encoding/json"
	"path/filepath"
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
	"go.mau.fi/whatsmeow/types/events"
	log "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

func TestRecoveryCompletionOwnsOnlyItsCompleteBatchDuringInterleave(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	source := deliveryTestDevice(t, filepath.Join(t.TempDir(), "source.db"))
	device := deliveryTestDevice(t, filepath.Join(t.TempDir(), "client.db"))
	cli := NewClient(device, log.Noop)
	cli.EmitAppStateEventsOnFullSync = true
	must(cli.SetAppStateDeliveryConsumer(func(context.Context, *store.AppStateDelivery) error { return nil }))
	col := appstate.WAPatchRegularHigh
	jid := types.NewJID("456", types.DefaultUserServer)
	encoded, err := appstate.NewProcessor(source, log.Noop).EncodePatch(ctx, []byte{1}, appstate.HashState{}, appstate.BuildStar(jid, jid, "synthetic-message", false, true))
	must(err)
	patch := &waServerSync.SyncdPatch{}
	must(proto.Unmarshal(encoded, patch))
	patch.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(1)}
	mutations, state, err := appstate.NewProcessor(source, log.Noop).DecodePatches(ctx, &appstate.PatchList{Name: col, Patches: []*waServerSync.SyncdPatch{patch}}, appstate.HashState{}, true)
	must(err)
	recovery := &waSyncdSnapshotRecovery.SyncdSnapshotRecovery{CollectionName: proto.String(string(col)), Version: &waSyncdSnapshotRecovery.SyncdVersion{Version: proto.Uint64(1)}, CollectionLthash: state.Hash[:]}
	for _, m := range mutations {
		index, e := json.Marshal(m.Index)
		must(e)
		recovery.MutationRecords = append(recovery.MutationRecords, &waSyncdSnapshotRecovery.SyncdPlainTextRecord{KeyID: m.KeyID, Mac: m.ValueMAC, Value: &waSyncAction.SyncActionData{Index: index, Value: m.Action, Version: proto.Int32(m.Version)}})
	}
	raw, err := proto.Marshal(recovery)
	must(err)
	interleaved := false
	var withheld []any
	var completion *events.AppStateSyncComplete
	var typedIDs []uint64
	metadataEscaped := false
	cli.AddEventHandler(func(evt any) {
		switch e := evt.(type) {
		case *events.AppState:
			if e.DeliveryID == nil || e.DeliveryID.Sequence != 1 || interleaved {
				return
			}
			interleaved = true
			// A has released the producer lock before invoking arbitrary handlers.
			// Consume B via the real locked fetch, but dispatch only B's generic event.
			v, h, err := device.AppState.GetAppStateVersion(ctx, string(col))
			must(err)
			encoded, err := appstate.NewProcessor(device, log.Noop).EncodePatch(ctx, []byte{1}, appstate.HashState{Version: v, Hash: h}, appstate.BuildStar(jid, jid, "synthetic-message", false, false))
			must(err)
			newer := &waServerSync.SyncdPatch{}
			must(proto.Unmarshal(encoded, newer))
			newer.Version = &waServerSync.SyncdVersion{Version: proto.Uint64(v + 1)}
			encoded, err = proto.Marshal(newer)
			must(err)
			cli.appStateQuery = func(context.Context, infoQuery) (*waBinary.Node, error) {
				return &waBinary.Node{Tag: "iq", Content: []waBinary.Node{{Tag: "sync", Content: []waBinary.Node{{Tag: "collection", Attrs: waBinary.Attrs{"name": string(col)}, Content: []waBinary.Node{{Tag: "patches", Content: []waBinary.Node{{Tag: "patch", Content: encoded}}}}}}}}}, nil
			}
			withheld, err = cli.DangerousInternals().FetchAppState(ctx, col, false, false)
			must(err)
			found := false
			for _, candidate := range withheld {
				if generic, ok := candidate.(*events.AppState); ok {
					cli.DangerousInternals().DispatchEvent(generic)
					found = true
					break
				}
			}
			if !found {
				t.Fatal("B has no generic event")
			}
		case *events.Star:
			if e.DeliveryID != nil {
				typedIDs = append(typedIDs, e.DeliveryID.Sequence)
			}
		case *events.AppStateDeliveryBatchComplete:
			metadataEscaped = true
		case *events.AppStateSyncComplete:
			if e.Recovery {
				copy := *e
				copy.CompletedDeliveryIDs = append([]store.AppStateDeliveryID(nil), e.CompletedDeliveryIDs...)
				completion = &copy
			}
		}
	})
	result := []*waE2E.PeerDataOperationRequestResponseMessage_PeerDataOperationResult{{SyncdSnapshotFatalRecoveryResponse: &waE2E.PeerDataOperationRequestResponseMessage_PeerDataOperationResult_SyncDSnapshotFatalRecoveryResponse{CollectionSnapshot: raw}}}
	if !cli.handleAppStateRecovery(ctx, "synthetic-A", result) {
		t.Fatal("recovery dispatch failed")
	}
	if !interleaved || completion == nil || len(completion.CompletedDeliveryIDs) != 1 || completion.CompletedDeliveryIDs[0].Sequence != 1 {
		t.Fatalf("A claimed another delivery: %+v", completion)
	}
	if len(typedIDs) != 1 || typedIDs[0] != 1 {
		t.Fatalf("B typed callback was not withheld: %v", typedIDs)
	}
	if metadataEscaped {
		t.Fatal("private completion metadata escaped to global handlers")
	}
	version, _, err := device.AppState.GetAppStateVersion(ctx, string(col))
	must(err)
	if version != 2 {
		t.Fatal("B did not consume before A sentinel")
	}
	ownedB := completedAppStateDeliveries(col, withheld)
	if len(ownedB) != 1 || ownedB[0].Sequence != 2 {
		t.Fatalf("B batch boundary lost identity: %v", ownedB)
	}
}
