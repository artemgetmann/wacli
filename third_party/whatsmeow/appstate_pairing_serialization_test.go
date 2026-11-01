// Copyright (c) 2026 The wacli contributors
// SPDX-License-Identifier: MPL-2.0
package whatsmeow

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/store"
	log "go.mau.fi/whatsmeow/util/log"
)

func TestAppStatePairingAdmissionSerializesAndCancels(t *testing.T) {
	d := deliveryTestDevice(t, filepath.Join(t.TempDir(), "session.db"))
	cli := NewClient(d, log.Noop)
	if e := cli.SetAppStateDeliveryConsumer(func(context.Context, *store.AppStateDelivery) error { return nil }); e != nil {
		t.Fatal(e)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var validations, queries atomic.Int32
	cli.SetAppStateDeliveryValidator(func(ctx context.Context, b *store.AppStateDeliveryBoundary) error {
		if validations.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	cli.appStateQuery = func(context.Context, infoQuery) (*waBinary.Node, error) {
		queries.Add(1)
		return nil, errors.New("unexpected transport")
	}
	owner := make(chan error, 1)
	go func() { owner <- cli.DrainAppStateDelivery(t.Context()) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("guard not entered")
	}
	ctx, cancel := context.WithCancel(t.Context())
	waiter := make(chan error, 1)
	go func() {
		_, e := cli.DangerousInternals().FetchAppState(ctx, appstate.WAPatchRegularLow, false, false)
		waiter <- e
	}()
	cancel()
	select {
	case e := <-waiter:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("producer admission did not cancel")
	}
	if validations.Load() != 1 || queries.Load() != 0 {
		t.Fatal("producer bypassed active validation")
	}
	close(release)
	if e := <-owner; e != nil {
		t.Fatal(e)
	}
	cli.SetAppStateDeliveryValidator(func(context.Context, *store.AppStateDeliveryBoundary) error { return errors.New("pairing denied") })
	if e := cli.ConnectContext(t.Context()); e == nil || e.Error() != "pairing denied" {
		t.Fatalf("connect did not refuse before network: %v", e)
	}
}
