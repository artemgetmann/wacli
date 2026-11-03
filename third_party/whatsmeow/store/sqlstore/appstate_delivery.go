// Copyright (c) 2026 The wacli contributors
// SPDX-License-Identifier: MPL-2.0
package sqlstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go.mau.fi/whatsmeow/types"
	"math"

	"go.mau.fi/util/dbutil"
	"go.mau.fi/whatsmeow/store"
)

func (s *SQLStore) StageAppStateDelivery(ctx context.Context, t store.AppStateTransition) (*store.AppStateDelivery, error) {
	if s.retiredDelivery {
		return nil, fmt.Errorf("retired delivery cannot accept new protocol state")
	}
	if _, nested := s.db.Execable(ctx).(dbutil.Transaction); nested {
		return nil, fmt.Errorf("app state staging requires its own committed transaction")
	}
	if len(t.Payload) == 0 || len(t.Payload) > store.MaxAppStateDeliveryBytes {
		return nil, fmt.Errorf("invalid app state payload size %d", len(t.Payload))
	}
	if t.Name == "" || t.BeforeVersion > math.MaxInt64 || t.AfterVersion > math.MaxInt64 {
		return nil, fmt.Errorf("invalid app state transition")
	}
	switch t.Kind {
	case "delta", "snapshot", "recovery":
		if t.AfterVersion == 0 {
			return nil, fmt.Errorf("zero app state version")
		}
	case "reset":
		if t.AfterVersion != 0 || t.AfterHash != ([128]byte{}) {
			return nil, fmt.Errorf("invalid app state reset")
		}
	default:
		return nil, fmt.Errorf("unknown app state replacement kind")
	}
	if t.Kind == "delta" && t.AfterVersion <= t.BeforeVersion {
		return nil, fmt.Errorf("non-advancing app state delta")
	}
	digest := sha256.Sum256(t.Payload)
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return nil, err
	}
	delivery := &store.AppStateDelivery{Transition: t}
	err := s.db.DoTxn(ctx, nil, func(ctx context.Context) error {
		if err := s.requireDurableAppStateTransaction(ctx); err != nil {
			return err
		}
		err := s.ensureAppStateEpoch(ctx, hex.EncodeToString(entropy[:]))
		if err != nil {
			return err
		}
		// UPDATE locks the per-device row on both SQLite and PostgreSQL before
		// observing cursor state. A pending payload prevents any second delivery.
		err = s.db.QueryRow(ctx, `UPDATE whatsmeow_app_state_delivery SET predecessor_sequence=CASE WHEN sequence>0 THEN sequence ELSE NULL END,predecessor_digest=CASE WHEN sequence>0 THEN digest ELSE NULL END,sequence=sequence+1 WHERE jid=$1 AND payload IS NULL AND sequence<$2 RETURNING generation,sequence`, s.JID, int64(math.MaxInt64)).Scan(&delivery.ID.Generation, &delivery.ID.Sequence)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrAppStateDeliveryPending
		}
		if err != nil {
			return err
		}
		version, hash, err := s.GetAppStateVersion(ctx, t.Name)
		if err != nil {
			return err
		}
		if version != t.BeforeVersion || hash != t.BeforeHash {
			return fmt.Errorf("app state transition base changed")
		}
		if t.Kind != "delta" {
			if err = s.DeleteAppStateVersion(ctx, t.Name); err != nil {
				return err
			}
		}
		if t.Kind != "reset" {
			if err = s.PutAppStateVersion(ctx, t.Name, t.AfterVersion, t.AfterHash); err != nil {
				return err
			}
			if err = s.DeleteAppStateMutationMACs(ctx, t.Name, t.RemovedMACs); err != nil {
				return err
			}
			for _, m := range t.AddedMACs {
				_, err = s.db.Exec(ctx, `INSERT INTO whatsmeow_app_state_mutation_macs(jid,name,version,index_mac,value_mac) VALUES($1,$2,$3,$4,$5) ON CONFLICT(jid,name,version,index_mac) DO UPDATE SET value_mac=excluded.value_mac`, s.JID, t.Name, t.AfterVersion, m.IndexMAC, m.ValueMAC)
				if err != nil {
					return err
				}
			}
		}
		_, err = s.db.Exec(ctx, `UPDATE whatsmeow_app_state_delivery SET name=$2,kind=$3,before_version=$4,after_version=$5,before_hash=$6,after_hash=$7,digest=$8,payload=$9 WHERE jid=$1`, s.JID, t.Name, t.Kind, t.BeforeVersion, t.AfterVersion, t.BeforeHash[:], t.AfterHash[:], digest[:], t.Payload)
		return err
	})
	if err != nil {
		return nil, err
	}
	delivery.ID.Digest = digest
	boundary, err := s.AppStateDeliveryBoundary(ctx)
	if err != nil {
		return nil, err
	}
	delivery.Boundary = *boundary
	return delivery, nil
}

func (s *SQLStore) PendingAppStateDelivery(ctx context.Context) (*store.AppStateDelivery, error) {
	d := &store.AppStateDelivery{}
	var before, after, digest []byte
	err := s.db.QueryRow(ctx, `SELECT generation,sequence,name,kind,before_version,after_version,before_hash,after_hash,digest,payload FROM `+s.deliveryTable()+` WHERE jid=$1 AND payload IS NOT NULL`, s.JID).Scan(&d.ID.Generation, &d.ID.Sequence, &d.Transition.Name, &d.Transition.Kind, &d.Transition.BeforeVersion, &d.Transition.AfterVersion, &before, &after, &digest, &d.Transition.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(before) != 128 || len(after) != 128 || len(digest) != 32 || len(d.Transition.Payload) > store.MaxAppStateDeliveryBytes {
		return nil, fmt.Errorf("invalid saved app state delivery")
	}
	copy(d.Transition.BeforeHash[:], before)
	copy(d.Transition.AfterHash[:], after)
	copy(d.ID.Digest[:], digest)
	if sha256.Sum256(d.Transition.Payload) != d.ID.Digest {
		return nil, fmt.Errorf("app state delivery digest mismatch")
	}
	boundary, err := s.AppStateDeliveryBoundary(ctx)
	if err != nil {
		return nil, err
	}
	if boundary.ID != d.ID || !boundary.Pending {
		return nil, fmt.Errorf("app state pending boundary changed")
	}
	d.Boundary = *boundary
	return d, nil
}

func (s *SQLStore) AckAppStateDelivery(ctx context.Context, id store.AppStateDeliveryID) error {
	if _, nested := s.db.Execable(ctx).(dbutil.Transaction); nested {
		return fmt.Errorf("app state acknowledgment requires its own committed transaction")
	}
	// Retain generation, sequence and digest after acknowledgment. Repeating an
	// exact ack is harmless; an old ack cannot delete a later pending delivery.
	return s.db.DoTxn(ctx, nil, func(ctx context.Context) error {
		if err := s.requireDurableAppStateTransaction(ctx); err != nil {
			return err
		}
		result, err := s.db.Exec(ctx, `UPDATE `+s.deliveryTable()+` SET payload=NULL WHERE jid=$1 AND generation=$2 AND sequence=$3 AND digest=$4`, s.JID, id.Generation, id.Sequence, id.Digest[:])
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return store.ErrAppStateDeliveryIdentity
		}
		return nil
	})
}

var _ store.AppStateDeliveryStore = (*SQLStore)(nil)

func (s *SQLStore) requireDurableAppStateTransaction(ctx context.Context) error {
	if s.db.Dialect == dbutil.SQLite {
		var level int
		if err := s.db.QueryRow(ctx, "PRAGMA synchronous").Scan(&level); err != nil {
			return err
		}
		if level < 2 {
			return fmt.Errorf("durable app state handoff requires SQLite synchronous FULL or EXTRA")
		}
	}
	return nil
}

func (s *SQLStore) AppStateDeliveryBoundary(ctx context.Context) (*store.AppStateDeliveryBoundary, error) {
	if _, nested := s.db.Execable(ctx).(dbutil.Transaction); nested {
		return nil, fmt.Errorf("boundary requires its own committed transaction")
	}
	if !s.retiredDelivery {
		var entropy [16]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			return nil, err
		}
		if err := s.db.DoTxn(ctx, nil, func(ctx context.Context) error {
			if err := s.requireDurableAppStateTransaction(ctx); err != nil {
				return err
			}
			return s.ensureAppStateEpoch(ctx, hex.EncodeToString(entropy[:]))
		}); err != nil {
			return nil, err
		}
	}
	return s.readAppStateBoundary(ctx)
}

func (s *SQLStore) readAppStateBoundary(ctx context.Context) (*store.AppStateDeliveryBoundary, error) {
	b := &store.AppStateDeliveryBoundary{Device: s.JID, Retired: s.retiredDelivery}
	var name sql.NullString
	var version, prev sql.NullInt64
	var hash, digest, pred, prior []byte
	err := s.db.QueryRow(ctx, `SELECT generation,sequence,epoch_kind,name,after_version,after_hash,digest,payload IS NOT NULL,predecessor_sequence,predecessor_digest,prior_epoch FROM `+s.deliveryTable()+` WHERE jid=$1`, s.JID).Scan(&b.ID.Generation, &b.ID.Sequence, &b.Epoch, &name, &version, &hash, &digest, &b.Pending, &prev, &pred, &prior)
	if err != nil {
		return nil, err
	}
	if len(prior) > 0 {
		b.PriorEpoch = &store.AppStateEpochIdentity{}
		if err = json.Unmarshal(prior, b.PriorEpoch); err != nil {
			return nil, err
		}
		if b.PriorEpoch.Device == "" || b.PriorEpoch.ID.Generation == "" || b.PriorEpoch.ID.Sequence == 0 {
			return nil, fmt.Errorf("invalid prior epoch witness")
		}
	}
	if b.ID.Generation == "" || (b.Epoch != "fresh" && b.Epoch != "legacy17") {
		return nil, fmt.Errorf("invalid native app state epoch")
	}
	if b.ID.Sequence == 0 {
		if b.Epoch != "fresh" || name.Valid || version.Valid || len(hash) != 0 || len(digest) != 0 || b.Pending || prev.Valid || len(pred) != 0 {
			return nil, fmt.Errorf("invalid fresh app state epoch")
		}
		return b, nil
	}
	if !name.Valid || !version.Valid || version.Int64 < 0 || len(hash) != 128 || len(digest) != 32 {
		return nil, fmt.Errorf("invalid retained app state boundary")
	}
	b.Name = name.String
	b.Version = uint64(version.Int64)
	copy(b.Hash[:], hash)
	copy(b.ID.Digest[:], digest)
	if prev.Valid || len(pred) != 0 {
		if !prev.Valid || prev.Int64 <= 0 || uint64(prev.Int64)+1 != b.ID.Sequence || len(pred) != 32 {
			return nil, fmt.Errorf("invalid predecessor identity")
		}
		b.Predecessor = &store.AppStateDeliveryID{Generation: b.ID.Generation, Sequence: uint64(prev.Int64)}
		copy(b.Predecessor.Digest[:], pred)
	} else if b.ID.Sequence > 1 && b.Epoch != "legacy17" {
		return nil, fmt.Errorf("missing native predecessor identity")
	}
	if s.retiredDelivery {
		return b, nil
	}
	v, h, err := s.GetAppStateVersion(ctx, b.Name)
	if err != nil {
		return nil, err
	}
	if v != b.Version || h != b.Hash {
		return nil, fmt.Errorf("app state boundary cursor/hash mismatch; preserve matching stores")
	}
	return b, nil
}

func (s *SQLStore) deliveryTable() string {
	if s.retiredDelivery {
		return "whatsmeow_app_state_retired_delivery"
	}
	return "whatsmeow_app_state_delivery"
}

func (s *SQLStore) ensureAppStateEpoch(ctx context.Context, generation string) error {
	var count int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM whatsmeow_app_state_delivery WHERE jid=$1`, s.JID).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	retired, err := s.Container.RetiredAppStateDeliveryStore(ctx)
	if err != nil {
		return err
	}
	var prior []byte
	if retired != nil {
		b, err := retired.(*SQLStore).readAppStateBoundary(ctx)
		if err != nil {
			return err
		}
		if b.Pending {
			return store.ErrAppStateDeliveryPending
		}
		old, err := types.ParseJID(b.Device)
		if err != nil {
			return err
		}
		current, err := types.ParseJID(s.JID)
		if err != nil {
			return err
		}
		if old.User != current.User || old.Server != current.Server {
			return fmt.Errorf("replacement device belongs to a different account; use a separate store")
		}
		if b.ID.Sequence > 0 {
			prior, err = json.Marshal(store.AppStateEpochIdentity{Device: b.Device, ID: b.ID})
			if err != nil {
				return err
			}
		}
	}
	_, err = s.db.Exec(ctx, `INSERT INTO whatsmeow_app_state_delivery(jid,generation,sequence,epoch_kind,prior_epoch) VALUES($1,$2,0,'fresh',$3) ON CONFLICT(jid) DO NOTHING`, s.JID, generation, prior)
	return err
}
