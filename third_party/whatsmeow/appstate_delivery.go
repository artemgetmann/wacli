// Copyright (c) 2026 The wacli contributors
// SPDX-License-Identifier: MPL-2.0
package whatsmeow

import (
	"context"
	"fmt"
	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waServerSync"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"sync"
	"time"
)

// SetAppStateDeliveryConsumer opts into the native one-slot durable handoff.
// Call before connecting. The consumer must durably commit its projection and
// checkpoint, must not reenter SDK producers or wait for asynchronous callbacks.
func (cli *Client) SetAppStateDeliveryConsumer(consumer func(context.Context, *store.AppStateDelivery) error) error {
	if consumer != nil && cli.Store.ID != nil {
		if _, ok := cli.Store.AppState.(store.AppStateDeliveryStore); !ok {
			return fmt.Errorf("app state store has no native delivery capability")
		}
	}
	if consumer == nil {
		cli.appStateProc.DeliveryConsumer = nil
	} else {
		cli.appStateProc.DeliveryConsumer = func(ctx context.Context, d *store.AppStateDelivery) error {
			if err := cli.persistCoveredSDKDeliveryState(ctx, d); err != nil {
				return err
			}
			return consumer(ctx, d)
		}
	}
	return nil
}

// DrainAppStateDelivery projects a pending committed payload before subsequent
// consumption. No network traffic is performed by the SDK through this method.
func (cli *Client) DrainAppStateDelivery(ctx context.Context) error {
	if err := cli.appStateSyncLock.Lock(ctx); err != nil {
		return err
	}
	defer cli.appStateSyncLock.Unlock()
	return cli.appStateProc.DrainAppStateDelivery(ctx)
}

// queryAppState separates producer logic from IQ transport in package tests.
// Production uses the normal IQ transport.
func (cli *Client) queryAppState(ctx context.Context, q infoQuery) (*waBinary.Node, error) {
	if cli.appStateQuery != nil {
		return cli.appStateQuery(ctx, q)
	}
	return cli.sendIQ(ctx, q)
}

// Covered SDK cache writes share the durable replay owner. Otherwise a crash
// after checkpoint/ack but before collector construction could strand those
// settings, and a delayed old collector could overwrite a newer SDK cache.
func (cli *Client) persistCoveredSDKDeliveryState(ctx context.Context, d *store.AppStateDelivery) error {
	mutations, err := appstate.DecodeDeliveryMutations(d.Transition.Payload)
	if err != nil {
		return err
	}
	for _, m := range mutations {
		if m.Operation != waServerSync.SyncdMutation_SET || m.Action == nil || len(m.Index) < 2 {
			continue
		}
		if m.Index[0] != appstate.IndexArchive && m.Index[0] != appstate.IndexPin && m.Index[0] != appstate.IndexMute {
			continue
		}
		jid, err := types.ParseJID(m.Index[1])
		if err != nil {
			return err
		}
		if cli.Store.ChatSettings == nil {
			return fmt.Errorf("covered SDK chat settings store unavailable")
		}
		switch m.Index[0] {
		case appstate.IndexArchive:
			if m.Action.ArchiveChatAction == nil {
				return fmt.Errorf("missing archive action")
			}
			err = cli.Store.ChatSettings.PutArchived(ctx, jid, m.Action.GetArchiveChatAction().GetArchived())
		case appstate.IndexPin:
			if m.Action.PinAction == nil {
				return fmt.Errorf("missing pin action")
			}
			err = cli.Store.ChatSettings.PutPinned(ctx, jid, m.Action.GetPinAction().GetPinned())
		case appstate.IndexMute:
			action := m.Action.GetMuteAction()
			if action == nil {
				return fmt.Errorf("missing mute action")
			}
			var until time.Time
			if action.GetMuted() {
				if action.GetMuteEndTimestamp() < 0 {
					until = store.MutedForever
				} else {
					until = time.UnixMilli(action.GetMuteEndTimestamp())
				}
			}
			err = cli.Store.ChatSettings.PutMutedUntil(ctx, jid, until)
		}
		if err != nil {
			return fmt.Errorf("persist covered SDK chat settings: %w", err)
		}
	}
	return nil
}

// appStateProducerLock permits canceled callers to leave without waiting for a
// different fetch/send's network deadline. The channel is initialized once.
type appStateProducerLock struct {
	mu   sync.Mutex
	gate chan struct{}
}

func (l *appStateProducerLock) Lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	if l.gate == nil {
		l.gate = make(chan struct{}, 1)
		l.gate <- struct{}{}
	}
	gate := l.gate
	l.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gate:
		if err := ctx.Err(); err != nil {
			gate <- struct{}{}
			return err
		}
		return nil
	}
}
func (l *appStateProducerLock) Unlock() { l.gate <- struct{}{} }

// This consumes only the producer's private constructed event slice, never a
// global handler capture. A boundary exists only after the whole batch builds.
func completedAppStateDeliveries(name appstate.WAPatchName, owned []any) []store.AppStateDeliveryID {
	seen := make(map[store.AppStateDeliveryID]bool)
	var ids []store.AppStateDeliveryID
	for _, evt := range owned {
		batch, ok := evt.(*events.AppStateDeliveryBatchComplete)
		if !ok || batch == nil || batch.Name != name {
			continue
		}
		for _, id := range batch.CompletedDeliveryIDs {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// SetAppStateDeliveryValidator configures local pairing admission before connect.
// It runs under the producer lock before cache writes, drain or consumption.
func (cli *Client) SetAppStateDeliveryValidator(validate func(context.Context, *store.AppStateDeliveryBoundary) error) {
	cli.appStateProc.DeliveryValidator = validate
}
