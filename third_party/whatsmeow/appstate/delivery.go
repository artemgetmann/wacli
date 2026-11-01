// Copyright (c) 2026 The wacli contributors
// SPDX-License-Identifier: MPL-2.0
package appstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/store"
	"google.golang.org/protobuf/proto"
)

type deliveryMutation struct {
	KeyID              []byte
	Operation          waServerSync.SyncdMutation_SyncdOperation
	Action             []byte
	Version            int32
	Index              []string
	IndexMAC, ValueMAC []byte
	PatchVersion       uint64
}
type deliveryPayload struct {
	Format    int
	Mutations []deliveryMutation
}

// DecodeDeliveryMutations reads the bounded versioned envelope, independently of
// cursor versions. Callers must not infer peer freshness from delivery identity.
func DecodeDeliveryMutations(payload []byte) ([]Mutation, error) {
	if len(payload) == 0 || len(payload) > store.MaxAppStateDeliveryBytes {
		return nil, fmt.Errorf("invalid app state delivery size")
	}
	var envelope deliveryPayload
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, err
	}
	if envelope.Format != 1 {
		return nil, fmt.Errorf("unsupported app state delivery format")
	}
	mutations := make([]Mutation, len(envelope.Mutations))
	for i, m := range envelope.Mutations {
		var action *waSyncAction.SyncActionValue
		if m.Action != nil {
			action = &waSyncAction.SyncActionValue{}
			if err := proto.Unmarshal(m.Action, action); err != nil {
				return nil, err
			}
		}
		mutations[i] = Mutation{KeyID: m.KeyID, Operation: m.Operation, Action: action, Version: m.Version, Index: m.Index, IndexMAC: m.IndexMAC, ValueMAC: m.ValueMAC, PatchVersion: m.PatchVersion}
	}
	return mutations, nil
}

func encodeDeliveryMutations(mutations []Mutation) ([]byte, error) {
	envelope := deliveryPayload{Format: 1, Mutations: make([]deliveryMutation, len(mutations))}
	for i, m := range mutations {
		var encoded []byte
		if m.Action != nil {
			var err error
			encoded, err = proto.Marshal(m.Action)
			if err != nil {
				return nil, err
			}
			if encoded == nil {
				encoded = []byte{}
			}
		}
		envelope.Mutations[i] = deliveryMutation{m.KeyID, m.Operation, encoded, m.Version, m.Index, m.IndexMAC, m.ValueMAC, m.PatchVersion}
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	if len(payload) > store.MaxAppStateDeliveryBytes {
		return nil, fmt.Errorf("app state delivery exceeds payload limit")
	}
	return payload, nil
}

// DrainAppStateDelivery retries the one pending projection. The consumer must
// commit its own atomic projection/checkpoint before returning nil. Exact ack
// occurs only afterwards; an ack failure leaves replay safe via that checkpoint.
// Configure DeliveryConsumer before connecting. It must not reenter SDK state
// producers, wait for asynchronous event handlers, or take their reserved tasks.
func (proc *Processor) DrainAppStateDelivery(ctx context.Context) error {
	if proc.DeliveryConsumer == nil {
		return nil
	}
	if proc.Store.ID == nil {
		if proc.DeliveryValidator != nil {
			return proc.DeliveryValidator(ctx, nil)
		}
		return nil
	}
	s, ok := proc.Store.AppState.(store.AppStateDeliveryStore)
	if !ok {
		return fmt.Errorf("durable app state consumer requires native delivery store")
	}
	boundary, err := s.AppStateDeliveryBoundary(ctx)
	if err != nil {
		return err
	}
	if boundary.Device != proc.Store.ID.String() {
		return fmt.Errorf("app state delivery device mismatch")
	}
	pending, err := s.PendingAppStateDelivery(ctx)
	if err != nil {
		return err
	}
	if (pending != nil) != boundary.Pending || pending != nil && pending.ID != boundary.ID {
		return fmt.Errorf("app state pending boundary changed")
	}
	if proc.DeliveryValidator != nil {
		if err = proc.DeliveryValidator(ctx, boundary); err != nil {
			return err
		}
	}
	if pending == nil {
		return nil
	}
	if err = proc.DeliveryConsumer(ctx, pending); err != nil {
		return errors.Join(store.ErrAppStateDeliveryPending, err)
	}
	if err = s.AckAppStateDelivery(ctx, pending.ID); err != nil {
		return errors.Join(store.ErrAppStateDeliveryPending, err)
	}
	return nil
}

func (proc *Processor) commitDelivery(ctx context.Context, name WAPatchName, before, after HashState, out *patchOutput, start int, kind string) error {
	if err := proc.DrainAppStateDelivery(ctx); err != nil {
		return err
	}
	s, ok := proc.Store.AppState.(store.AppStateDeliveryStore)
	if !ok {
		return fmt.Errorf("durable app state consumer requires native delivery store")
	}
	if kind != "delta" {
		var err error
		before.Version, before.Hash, err = proc.Store.AppState.GetAppStateVersion(ctx, string(name))
		if err != nil {
			return err
		}
	}
	payload, err := encodeDeliveryMutations(out.Mutations[start:])
	if err != nil {
		return err
	}
	staged, err := s.StageAppStateDelivery(ctx, store.AppStateTransition{Name: string(name), Kind: kind, BeforeVersion: before.Version, BeforeHash: before.Hash, AfterVersion: after.Version, AfterHash: after.Hash, Payload: payload, RemovedMACs: out.RemovedMACs, AddedMACs: out.AddedMACs})
	if err != nil {
		return err
	}
	for i := start; i < len(out.Mutations); i++ {
		id := staged.ID
		out.Mutations[i].DeliveryID = &id
	}
	return proc.DrainAppStateDelivery(ctx)
}
