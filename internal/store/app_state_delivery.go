package store

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"github.com/openclaw/wacli/internal/store/storedb"
	"time"
)

// DeliveryCheckpoint identifies local SDK consumption, not a protocol version.
type DeliveryCheckpoint struct {
	Predecessor *DeliveryCheckpoint
	PriorEpoch  *DeliveryCheckpoint
	Device      string
	Generation  string
	Sequence    uint64
	Digest      []byte
}

// ChatStateProjection is the covered, canonicalized portion of a verified
// delivery. Unknown actions are preserved through the existing recovery debt.
type ChatStateProjection struct {
	Kind, JID   string
	Enabled     bool
	Timestamp   time.Time
	MutedUntil  int64
	ReadThrough time.Time
	ReadIDs     []string
}

func migrateAppStateDeliveryCheckpoint(d *DB) error {
	_, err := d.sql.Exec(`CREATE TABLE IF NOT EXISTS app_state_delivery_checkpoint (
 generation TEXT PRIMARY KEY,sequence INTEGER NOT NULL CHECK(sequence>0),digest BLOB NOT NULL CHECK(length(digest)=32))`)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`CREATE TABLE IF NOT EXISTS app_state_delivery_recovery (
 generation TEXT NOT NULL,sequence INTEGER NOT NULL,digest BLOB NOT NULL CHECK(length(digest)=32),
 collection TEXT NOT NULL,intent_id INTEGER NOT NULL REFERENCES app_state_recovery_intents(id) ON DELETE CASCADE,
 PRIMARY KEY(generation,sequence,collection))`)
	return err
}

// ApplyAppStateDelivery commits projections, uncovered-work debt and checkpoint
// in one transaction. Replaying an exact checkpoint never reapplies old reads
// or pins over newer arrivals. A conflicting or skipped identity fails closed.
func (d *DB) ApplyAppStateDelivery(ctx context.Context, id DeliveryCheckpoint, actions []ChatStateProjection, debt []string) (err error) {
	if id.Generation == "" || id.Sequence == 0 || len(id.Digest) != 32 {
		return fmt.Errorf("invalid app state checkpoint")
	}
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return err
	}
	var previous int
	if err = conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&previous); err != nil {
		_ = conn.Close()
		return err
	}
	if previous < 0 || previous > 3 {
		_ = conn.Close()
		return fmt.Errorf("invalid SQLite synchronous mode")
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA synchronous=FULL"); err != nil {
		_ = conn.Close()
		return err
	}
	defer func() {
		// Never return a connection with uncertain settings to the normal pool.
		_, restoreErr := conn.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf("PRAGMA synchronous=%d", previous))
		if restoreErr != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		err = errors.Join(err, restoreErr, conn.Close())
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var seq uint64
	var digest []byte
	var device string
	err = tx.QueryRowContext(ctx, `SELECT sequence,digest,device_jid FROM app_state_delivery_checkpoint WHERE generation=?`, id.Generation).Scan(&seq, &digest, &device)
	if err == nil && device != "" && device != id.Device {
		return fmt.Errorf("app state checkpoint device conflict")
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && seq == id.Sequence {
		if !bytes.Equal(digest, id.Digest) {
			return fmt.Errorf("app state checkpoint digest conflict")
		}
		if device == "" && id.Device != "" {
			if _, err = tx.ExecContext(ctx, `UPDATE app_state_delivery_checkpoint SET device_jid=? WHERE generation=?`, id.Device, id.Generation); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	if seq+1 != id.Sequence {
		return fmt.Errorf("app state checkpoint sequence gap or stale delivery")
	}
	if id.Device != "" {
		if seq == 0 {
			var count int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_state_delivery_checkpoint`).Scan(&count); err != nil {
				return err
			}

			if id.Sequence != 1 {
				return fmt.Errorf("unvalidated app state epoch")
			}
			if count != 0 {
				if count != 1 || id.PriorEpoch == nil {
					return fmt.Errorf("unvalidated app state epoch")
				}
				var debt int
				if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_state_recovery_intents`).Scan(&debt); err != nil {
					return err
				}
				if debt != 0 {
					return fmt.Errorf("prior epoch has unresolved recovery debt")
				}
				p := id.PriorEpoch
				result, e := tx.ExecContext(ctx, `DELETE FROM app_state_delivery_checkpoint WHERE generation=? AND sequence=? AND digest=? AND device_jid=?`, p.Generation, p.Sequence, p.Digest, p.Device)
				if e != nil {
					return e
				}
				n, e := result.RowsAffected()
				if e != nil {
					return e
				}
				if n != 1 {
					return fmt.Errorf("prior epoch checkpoint mismatch")
				}
			} else if id.PriorEpoch != nil {
				return fmt.Errorf("prior epoch checkpoint missing")
			}

		} else if id.Predecessor == nil || id.Predecessor.Generation != id.Generation || id.Predecessor.Sequence != seq || !bytes.Equal(id.Predecessor.Digest, digest) {
			return fmt.Errorf("app state predecessor identity/digest mismatch")
		}
	}
	q := storedb.New(tx)
	for _, a := range actions {
		if a.Kind != "archive_setting" && a.JID == "" {
			return fmt.Errorf("empty app state chat JID")
		}
		switch a.Kind {
		case "archive":
			err = applyChatArchiveTx(tx, a.JID, a.Enabled, a.Timestamp)
		case "archive_setting":
			err = setChatArchiveSettingTx(tx, a.Enabled, a.Timestamp)
		case "pin":
			err = q.SetChatPinned(ctx, storedb.SetChatPinnedParams{Jid: a.JID, Pinned: boolToInt64(a.Enabled)})
		case "mute":
			err = q.SetChatMutedUntil(ctx, storedb.SetChatMutedUntilParams{Jid: a.JID, MutedUntil: a.MutedUntil})
		case "read":
			if !a.Enabled {
				err = setChatUnreadOn(tx, a.JID, true)
			} else {
				err = clearChatUnreadThroughOn(tx, a.JID, a.ReadThrough, a.ReadIDs, len(uniqueNonEmptyStrings(a.ReadIDs)) > 0)
			}
		default:
			return fmt.Errorf("unknown covered app state projection %q", a.Kind)
		}
		if err != nil {
			return err
		}
	}
	for _, collection := range uniqueNonEmptyStrings(debt) {
		// The newest exact intent conservatively represents all unresolved native
		// deliveries of this collection. Replace only our correlated markers; never
		// remove unrelated recovery attempts. A failed transaction restores the old
		// marker, and an old replay ID cannot settle the newer representative.
		if _, err = tx.ExecContext(ctx, `DELETE FROM app_state_recovery_intents WHERE id IN (SELECT intent_id FROM app_state_delivery_recovery WHERE collection=?)`, collection); err != nil {
			return err
		}
		var intent int64
		if err = tx.QueryRowContext(ctx, `INSERT INTO app_state_recovery_intents(collection) VALUES(?) RETURNING id`, collection).Scan(&intent); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO app_state_delivery_recovery(generation,sequence,digest,collection,intent_id) VALUES(?,?,?,?,?)`, id.Generation, id.Sequence, id.Digest, collection, intent); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO app_state_delivery_checkpoint(generation,sequence,digest,device_jid) VALUES(?,?,?,?) ON CONFLICT(generation) DO UPDATE SET sequence=excluded.sequence,digest=excluded.digest,device_jid=excluded.device_jid`, id.Generation, id.Sequence, id.Digest, id.Device)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) HasAppStateDeliveryCheckpoint(ctx context.Context, id DeliveryCheckpoint) (bool, error) {
	var seq uint64
	var digest []byte
	err := d.sql.QueryRowContext(ctx, `SELECT sequence,digest FROM app_state_delivery_checkpoint WHERE generation=?`, id.Generation).Scan(&seq, &digest)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if seq < id.Sequence {
		return false, nil
	}
	if seq == id.Sequence && !bytes.Equal(digest, id.Digest) {
		return false, fmt.Errorf("app state checkpoint digest conflict")
	}
	return true, nil
}

// ClearFetchedDeliveryRecovery settles only exact deliveries whose complete
// fetched event batch and contact journal persisted successfully. It cannot
// clear a concurrent newer delivery's debt or unrelated recovery generations.
func (d *DB) ClearFetchedDeliveryRecovery(ctx context.Context, collection string, ids []DeliveryCheckpoint) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, `DELETE FROM app_state_recovery_intents WHERE id IN (
 SELECT intent_id FROM app_state_delivery_recovery WHERE generation=? AND sequence=? AND digest=? AND collection=?)`, id.Generation, id.Sequence, id.Digest, collection); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// HasExactAppStateDeliveryCheckpoint is for replay admission; an App checkpoint
// ahead of the pending SDK delivery indicates mismatched restored stores.
func (d *DB) HasExactAppStateDeliveryCheckpoint(ctx context.Context, id DeliveryCheckpoint) (bool, error) {
	var seq uint64
	var digest []byte
	err := d.sql.QueryRowContext(ctx, `SELECT sequence,digest FROM app_state_delivery_checkpoint WHERE generation=?`, id.Generation).Scan(&seq, &digest)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if seq > id.Sequence {
		return false, fmt.Errorf("App checkpoint is ahead of pending SDK delivery; matching store recovery required")
	}
	if seq < id.Sequence {
		return false, nil
	}
	if !bytes.Equal(digest, id.Digest) {
		return false, fmt.Errorf("app state checkpoint digest conflict")
	}
	return true, nil
}

func migrateAppStateDeliveryDevice(d *DB) error {
	exists, err := d.tableHasColumn("app_state_delivery_checkpoint", "device_jid")
	if err != nil || exists {
		return err
	}
	_, err = d.sql.Exec(`ALTER TABLE app_state_delivery_checkpoint ADD COLUMN device_jid TEXT NOT NULL DEFAULT ''`)
	return err
}

// AppStateDeliveryCheckpoints exposes identity metadata only, never payloads.
func (d *DB) AppStateDeliveryCheckpoints(ctx context.Context) ([]DeliveryCheckpoint, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT generation,sequence,digest,device_jid FROM app_state_delivery_checkpoint`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeliveryCheckpoint
	for rows.Next() {
		var cp DeliveryCheckpoint
		if err = rows.Scan(&cp.Generation, &cp.Sequence, &cp.Digest, &cp.Device); err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}
