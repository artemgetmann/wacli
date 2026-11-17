package store

import (
	"database/sql"
	"fmt"
	"reflect"
)

func migrateInboundReadState(d *DB) error {
	_, err := d.sql.Exec(`CREATE TABLE IF NOT EXISTS inbound_read_state (
 chat_jid TEXT NOT NULL, msg_id TEXT NOT NULL,
 pending INTEGER NOT NULL DEFAULT 0 CHECK(pending IN (0,1)),
 attempted INTEGER NOT NULL DEFAULT 0 CHECK(attempted IN (0,1)),
	 completed INTEGER NOT NULL DEFAULT 0 CHECK(completed IN (0,1)),
 PRIMARY KEY(chat_jid,msg_id)
 )`)
	if err != nil {
		return err
	}
	// Development stores may already contain migration36's initial shape.
	// Missing completion evidence stays uncertain, never inferred from a count.
	var has bool
	err = d.sql.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('inbound_read_state') WHERE name='completed')`).Scan(&has)
	if err != nil || has {
		return err
	}
	_, err = d.sql.Exec(`ALTER TABLE inbound_read_state ADD COLUMN completed INTEGER NOT NULL DEFAULT 0 CHECK(completed IN (0,1))`)
	return err
}

// HandledUnreadSnapshot binds an explicit handled anchor to known live arrivals.
// The private capture prevents callers from widening a snapshot before dispatch.
type HandledUnreadSnapshot struct {
	ChatJID       string
	ThroughID     string
	Messages      []MessageInfo
	BaselineCount int
	chat          string
	through       string
	captured      []handledUnreadRow
	baseline      int
	claimed       bool
	settled       bool
}
type handledUnreadRow struct {
	info  MessageInfo
	rowID int64
}

func unreadMembership(tx *sql.Tx, jid string) (count, known int, marked bool, err error) {
	err = tx.QueryRow(`SELECT unread_count, unread, (SELECT COUNT(*) FROM inbound_read_state WHERE chat_jid=? AND pending=1) FROM chats WHERE jid=?`, jid, jid).Scan(&count, &marked, &known)
	return
}

// TrackLiveUnread is only for a newly stored live inbound. Unknown legacy or
// manual unread state is never reconstructed from a suffix of message history.
func (d *DB) TrackLiveUnread(jid, id string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, _, err = readBoundary(tx, jid, id); err != nil {
		return err
	}
	var exists int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM inbound_read_state WHERE chat_jid=? AND msg_id=?`, jid, id).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return tx.Commit()
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO chats(jid,kind) VALUES(?,'unknown')`, jid); err != nil {
		return err
	}
	count, known, marked, err := unreadMembership(tx, jid)
	if err != nil {
		return err
	}
	if count == 0 && !marked {
		if _, err = tx.Exec(`UPDATE inbound_read_state SET pending=0 WHERE chat_jid=?`, jid); err != nil {
			return err
		}
		known = 0
	}
	pending := count == known && (count > 0 || !marked)
	if _, err = tx.Exec(`INSERT INTO inbound_read_state(chat_jid,msg_id,pending) VALUES(?,?,?)`, jid, id, boolToInt64(pending)); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE chats SET unread=1,unread_count=unread_count+1 WHERE jid=?`, jid); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) SnapshotHandledUnread(jid, throughID string) (*HandledUnreadSnapshot, error) {
	tx, err := d.sql.BeginTx(storeCtx(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, anchor, err := readBoundary(tx, jid, throughID)
	if err != nil {
		return nil, err
	}
	count, known, _, err := unreadMembership(tx, jid)
	if err != nil {
		return nil, err
	}
	if count <= 0 || count != known {
		return nil, fmt.Errorf("unread membership is unknown or ambiguous")
	}
	rows, err := tx.Query(`SELECT rowid,msg_id,COALESCE(sender_jid,''),ts FROM messages WHERE `+selfReceiptEligible+` AND rowid<=? AND msg_id IN (SELECT msg_id FROM inbound_read_state WHERE chat_jid=? AND pending=1) ORDER BY rowid`, jid, anchor, jid)
	if err != nil {
		return nil, err
	}
	s := &HandledUnreadSnapshot{ChatJID: jid, ThroughID: throughID, BaselineCount: count, chat: jid, through: throughID, baseline: count}
	for rows.Next() {
		r := handledUnreadRow{info: MessageInfo{ChatJID: jid}}
		var ts int64
		if err = rows.Scan(&r.rowID, &r.info.MsgID, &r.info.SenderJID, &ts); err != nil {
			rows.Close()
			return nil, err
		}
		r.info.Timestamp = fromUnix(ts)
		s.captured = append(s.captured, r)
		s.Messages = append(s.Messages, r.info)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(s.captured) == 0 {
		return nil, fmt.Errorf("no known unread messages through handled boundary")
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s, nil
}

func validateHandledSnapshot(tx *sql.Tx, s *HandledUnreadSnapshot, attempted bool) error {
	if s == nil || s.ChatJID != s.chat || s.ThroughID != s.through || s.BaselineCount != s.baseline || len(s.Messages) != len(s.captured) || len(s.captured) == 0 {
		return fmt.Errorf("invalid handled snapshot")
	}
	for i, r := range s.captured {
		if !reflect.DeepEqual(s.Messages[i], r.info) {
			return fmt.Errorf("handled snapshot changed")
		}
		m, row, err := readBoundary(tx, s.chat, r.info.MsgID)
		if err != nil {
			return err
		}
		if row != r.rowID || m.MsgID != r.info.MsgID || m.SenderJID != r.info.SenderJID || !m.Timestamp.Equal(r.info.Timestamp) {
			return fmt.Errorf("handled message identity changed")
		}
		var pending, attempt bool
		if err = tx.QueryRow(`SELECT pending,attempted FROM inbound_read_state WHERE chat_jid=? AND msg_id=?`, s.chat, r.info.MsgID).Scan(&pending, &attempt); err != nil {
			return err
		}
		if !pending || attempt != attempted {
			return fmt.Errorf("handled message is no longer eligible")
		}
	}
	return nil
}

// ClaimHandledUnread durably records dispatch intent before any network call.
// An uncertain transport result must not be retried, including after reopening.
func (d *DB) ClaimHandledUnread(s *HandledUnreadSnapshot) error {
	if s == nil || s.claimed {
		return fmt.Errorf("handled snapshot already claimed or invalid")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = validateHandledSnapshot(tx, s, false); err != nil {
		return err
	}
	count, known, _, err := unreadMembership(tx, s.chat)
	if err != nil {
		return err
	}
	if count != known {
		return fmt.Errorf("unread membership changed")
	}
	for _, r := range s.captured {
		if _, err = tx.Exec(`UPDATE inbound_read_state SET attempted=1 WHERE chat_jid=? AND msg_id=?`, s.chat, r.info.MsgID); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.claimed = true
	return nil
}

// SettleHandledUnread removes only captured members after successful dispatch.
// Later arrivals retain their membership even when their timestamp is backdated.
func (d *DB) SettleHandledUnread(s *HandledUnreadSnapshot) (bool, error) {
	if s == nil || !s.claimed || s.settled {
		return false, fmt.Errorf("handled snapshot is not an unsettled claim")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	count, known, _, err := unreadMembership(tx, s.chat)
	if err != nil {
		return false, err
	}
	if count != known {
		return false, nil
	}
	if err = validateHandledSnapshot(tx, s, true); err != nil {
		return false, err
	}
	for _, r := range s.captured {
		if _, err = tx.Exec(`UPDATE inbound_read_state SET pending=0,completed=1 WHERE chat_jid=? AND msg_id=?`, s.chat, r.info.MsgID); err != nil {
			return false, err
		}
	}
	remaining := count - len(s.captured)
	if _, err = tx.Exec(`UPDATE chats SET unread_count=?,unread=? WHERE jid=?`, remaining, boolToInt64(remaining > 0), s.chat); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	s.settled = true
	return true, nil
}

// UnattemptedPrivateReadMessages lets an explicit prefix read progress to new
// IDs without repeating successful old receipts. An uncertain attempt blocks
// the operation: it is never silently classified as completed or retried.
func (d *DB) UnattemptedPrivateReadMessages(messages []MessageInfo) ([]MessageInfo, error) {
	var result []MessageInfo
	for _, message := range messages {
		var attempted, uncertain int
		if err := d.sql.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN completed=0 THEN 1 ELSE 0 END),0) FROM inbound_read_state WHERE msg_id=? AND attempted=1`, message.MsgID).Scan(&attempted, &uncertain); err != nil {
			return nil, err
		}
		if uncertain > 0 {
			return nil, fmt.Errorf("private read previously attempted with uncertain completion")
		}
		if attempted == 0 {
			result = append(result, message)
		}
	}
	return result, nil
}

// CompletePrivateReadMessages records success only after the entire explicit
// operation and local settlement completed. Its echo guards remain permanent.
func (d *DB) CompletePrivateReadMessages(jid string, messages []MessageInfo) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, message := range messages {
		if _, err = tx.Exec(`UPDATE inbound_read_state SET completed=1 WHERE chat_jid=? AND msg_id=? AND attempted=1`, jid, message.MsgID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// HasPrivateReadAttempt accepts an empty chat for conservative ID-only echo
// suppression across LID/PN migration. A collision can only preserve unread;
// callers must never use this global lookup to authorize dispatch or settlement.
func (d *DB) HasPrivateReadAttempt(jid string, ids []string) (bool, error) {
	exists, err := d.tableExists("inbound_read_state")
	if err != nil || !exists {
		return false, err
	}
	if len(ids) == 0 {
		// A coarse replay carries no stronger identity than the prior attempt.
		// This is a suppression decision only, never permission to mark read.
		query := `SELECT EXISTS(SELECT 1 FROM inbound_read_state WHERE attempted=1`
		var args []any
		if jid != "" {
			query += ` AND chat_jid=?`
			args = append(args, jid)
		}
		query += `)`
		var found bool
		err = d.sql.QueryRow(query, args...).Scan(&found)
		return found, err
	}
	for _, id := range ids {
		var count int
		query := `SELECT COUNT(*) FROM inbound_read_state WHERE msg_id=? AND attempted=1`
		args := []any{id}
		if jid != "" {
			query += ` AND chat_jid=?`
			args = append(args, jid)
		}
		if err = d.sql.QueryRow(query, args...).Scan(&count); err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}

// RecordPrivateReadAttempt also guards legacy explicit private-receipt paths;
// it does not assert that their historical unread membership is known.
func (d *DB) RecordPrivateReadAttempt(jid string, ids []string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(ids) == 0 {
		return fmt.Errorf("private read attempt needs exact IDs")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return fmt.Errorf("duplicate private read ID")
		}
		seen[id] = true
		if _, _, err = readBoundary(tx, jid, id); err != nil {
			return err
		}
		var attempted bool
		err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM inbound_read_state WHERE msg_id=? AND attempted=1)`, id).Scan(&attempted)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if attempted {
			return fmt.Errorf("private read already attempted")
		}
		if _, err = tx.Exec(`INSERT INTO inbound_read_state(chat_jid,msg_id,attempted) VALUES(?,?,1) ON CONFLICT(chat_jid,msg_id) DO UPDATE SET attempted=1`, jid, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// InvalidateHandledUnread conservatively forgets membership after a manual or
// external count projection, while retaining every prior private dispatch guard.
func (d *DB) InvalidateHandledUnread(jid string) error {
	_, err := d.sql.ExecContext(storeCtx(), `UPDATE inbound_read_state SET pending=0 WHERE chat_jid=?`, jid)
	return err
}
