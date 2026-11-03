// Copyright (c) 2026 The wacli contributors
// SPDX-License-Identifier: MPL-2.0
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go.mau.fi/util/dbutil"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// RetiredAppStateDeliveryStore exposes exactly one terminal, noncredential slot.
// The row is written in the credential-delete transaction, never by a callback.
func (c *Container) RetiredAppStateDeliveryStore(ctx context.Context) (store.AppStateDeliveryStore, error) {
	var jid string
	err := c.db.QueryRow(ctx, `SELECT jid FROM whatsmeow_app_state_retired_delivery WHERE slot=1`).Scan(&jid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parsed, err := types.ParseJID(jid)
	if err != nil {
		return nil, err
	}
	s := NewSQLStore(c, parsed)
	s.retiredDelivery = true
	return s, nil
}

func (c *Container) deleteDeviceRetainingDelivery(ctx context.Context, device *store.Device) error {
	if _, nested := c.db.Execable(ctx).(dbutil.Transaction); nested {
		return fmt.Errorf("credential deletion requires its own committed transaction")
	}
	s := NewSQLStore(c, *device.ID)
	return c.db.DoTxn(ctx, nil, func(ctx context.Context) error {
		if err := s.requireDurableAppStateTransaction(ctx); err != nil {
			return err
		}
		var count int
		if err := c.db.QueryRow(ctx, `SELECT COUNT(*) FROM whatsmeow_app_state_delivery WHERE jid=$1`, device.ID.String()).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			b, err := s.readAppStateBoundary(ctx)
			if err != nil {
				return err
			}
			previous, err := c.RetiredAppStateDeliveryStore(ctx)
			if err != nil {
				return err
			}
			// A never-consumed replacement preserves the last meaningful retired epoch.
			if b.ID.Sequence > 0 || previous == nil {
				if previous != nil {
					old, err := previous.(*SQLStore).readAppStateBoundary(ctx)
					if err != nil {
						return err
					}
					if old.Pending || b.PriorEpoch == nil || b.PriorEpoch.Device != old.Device || b.PriorEpoch.ID != old.ID {
						return fmt.Errorf("unresolved retired app state epoch; credential deletion not committed")
					}
				}
				const columns = "jid,generation,sequence,name,kind,before_version,after_version,before_hash,after_hash,digest,payload,epoch_kind,predecessor_sequence,predecessor_digest,prior_epoch"
				if _, err = c.db.Exec(ctx, `DELETE FROM whatsmeow_app_state_retired_delivery WHERE slot=1`); err != nil {
					return err
				}
				if _, err = c.db.Exec(ctx, `INSERT INTO whatsmeow_app_state_retired_delivery(slot,`+columns+`) SELECT 1,`+columns+` FROM whatsmeow_app_state_delivery WHERE jid=$1`, s.JID); err != nil {
					return err
				}
			}
		}
		_, err := c.db.Exec(ctx, deleteDeviceQuery, device.ID)
		return err
	})
}

var _ store.AppStateDeliveryLifecycle = (*Container)(nil)
