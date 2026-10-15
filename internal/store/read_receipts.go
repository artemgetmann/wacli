package store

import (
	"database/sql"
	"fmt"
	"strings"
)

const receiptEligible = "chat_jid = ? AND from_me = 0 AND revoked = 0 AND deleted_for_me = 0 AND deleted_at IS NULL AND COALESCE(reaction_to_id,'') = '' AND COALESCE(reaction_emoji,'') = '' AND (COALESCE(display_text,'') != '(message)' OR TRIM(COALESCE(text,'')) != '')"

// Private receipts require dated readable inbound content. Use the same
// predicate for selection and completion so excluded rows cannot hide missing
// history behind an inflated available-message count.
const selfReceiptEligible = receiptEligible + " AND ts > 0"

// ReadBoundary resolves an exact inbound anchor before app-state recovery can
// start. The chat predicate prevents an ID from another chat becoming a cutoff.
func (d *DB) ReadBoundary(jid, id string) (MessageInfo, error) {
	m, _, err := readBoundary(d.sql, jid, id)
	return m, err
}

func readBoundary(db interface {
	QueryRow(string, ...any) *sql.Row
}, jid, id string) (MessageInfo, int64, error) {
	if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
		return MessageInfo{}, 0, fmt.Errorf("--through-message-id requires an exact nonempty message ID")
	}
	m := MessageInfo{ChatJID: jid}
	var rowID, ts int64
	err := db.QueryRow("SELECT rowid, msg_id, COALESCE(sender_jid,''), ts FROM messages WHERE "+receiptEligible+" AND msg_id = ?", jid, id).Scan(&rowID, &m.MsgID, &m.SenderJID, &ts)
	if err != nil {
		return MessageInfo{}, 0, fmt.Errorf("read boundary %q must be a stored inbound message in chat %s: %w", id, jid, err)
	}
	m.Timestamp = fromUnix(ts)
	if m.Timestamp.IsZero() {
		return MessageInfo{}, 0, fmt.Errorf("message %q in chat %s has no read boundary", id, jid)
	}
	return m, rowID, nil
}

// ReadReceiptCandidates takes one snapshot of the unread count and eligible
// messages, then returns the oldest bounded slice so repeated calls progress.
func (d *DB) ReadReceiptCandidates(jid string, limit int) ([]MessageInfo, error) {
	return d.readReceiptCandidates(jid, "", limit)
}

// ReadReceiptCandidatesThrough intersects the current unread suffix with the
// handled prefix. Both are selected in one snapshot; newer same-second rows are
// excluded using the anchor's row position, and the existing batch cap remains.
func (d *DB) ReadReceiptCandidatesThrough(jid, id string, limit int) ([]MessageInfo, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("--through-message-id requires an exact nonempty message ID")
	}
	return d.readReceiptCandidates(jid, id, limit)
}

func (d *DB) readReceiptCandidates(jid, throughID string, limit int) ([]MessageInfo, error) {
	tx, err := d.sql.BeginTx(storeCtx(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	boundary := ""
	var boundaryArgs []any
	if throughID != "" {
		m, rowID, err := readBoundary(tx, jid, throughID)
		if err != nil {
			return nil, err
		}
		boundary = " WHERE ts < ? OR (ts = ? AND rowid <= ?)"
		boundaryArgs = []any{unix(m.Timestamp), unix(m.Timestamp), rowID}
	}
	var count int
	var marked bool
	if err := tx.QueryRow("SELECT unread_count, unread FROM chats WHERE jid = ?", jid).Scan(&count, &marked); err != nil {
		return nil, err
	}
	if count == 0 && !marked {
		return nil, tx.Commit()
	}
	var available int
	if err := tx.QueryRow("SELECT COUNT(*) FROM messages WHERE "+receiptEligible, jid).Scan(&available); err != nil {
		return nil, err
	}
	if count > available {
		return nil, fmt.Errorf("chat has %d unread messages but only %d eligible messages stored; sync its history before sending receipts", count, available)
	}
	want := count
	if want == 0 {
		want = 1
	}
	args := append([]any{jid, want}, boundaryArgs...)
	args = append(args, limit)
	rows, err := tx.Query("SELECT msg_id, COALESCE(sender_jid,''), ts FROM (SELECT msg_id, sender_jid, ts, rowid FROM messages WHERE "+receiptEligible+" ORDER BY ts DESC, rowid DESC LIMIT ?)"+boundary+" ORDER BY ts ASC, rowid ASC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []MessageInfo
	for rows.Next() {
		m := MessageInfo{ChatJID: jid}
		var ts int64
		if err := rows.Scan(&m.MsgID, &m.SenderJID, &ts); err != nil {
			return nil, err
		}
		m.Timestamp = fromUnix(ts)
		messages = append(messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return messages, tx.Commit()
}

func (d *DB) ClearChatUnreadMarker(jid string) error {
	_, err := d.sql.ExecContext(storeCtx(), "UPDATE chats SET unread = unread_count > 0 WHERE jid = ?", jid)
	return err
}

// SelfReadReceiptCandidatesThrough resolves the inbound anchor and its entire
// eligible history in one read-only snapshot. A marker-only partial range returns
// an empty selection: even older read-self receipts can clear a device's coarse
// manual unread badge. Other selections still cover the full handled history.
func (d *DB) SelfReadReceiptCandidatesThrough(jid, id string) ([]MessageInfo, error) {
	tx, err := d.sql.BeginTx(storeCtx(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	const eligible = selfReceiptEligible
	var ts, rowID int64
	if err := tx.QueryRow("SELECT ts, rowid FROM messages WHERE "+eligible+" AND msg_id = ?", jid, id).Scan(&ts, &rowID); err != nil {
		return nil, fmt.Errorf("handled boundary must be an eligible stored inbound message in this chat: %w", err)
	}
	// An anchor bounds known content; it cannot prove where messages missing
	// from local history belong. Refuse before any wire or group lookup.
	var count, available int
	if err := tx.QueryRow("SELECT unread_count, (SELECT COUNT(*) FROM messages WHERE "+eligible+") FROM chats WHERE jid = ?", jid, jid).Scan(&count, &available); err != nil {
		return nil, err
	}
	if count > available {
		return nil, fmt.Errorf("chat has %d unread messages but only %d eligible messages stored; sync its history before sending private receipts", count, available)
	}
	// Read the marker and later eligible rows in this same snapshot. A stale
	// zero counter can defer older receipts too; keeping work pending is safer
	// than clearing the marker while the caller still excludes newer messages.
	var preserveMarker bool
	if err := tx.QueryRow("SELECT unread != 0 AND unread_count = 0 AND EXISTS (SELECT 1 FROM messages WHERE "+eligible+" AND (ts > ? OR (ts = ? AND rowid > ?))) FROM chats WHERE jid = ?", jid, ts, ts, rowID, jid).Scan(&preserveMarker); err != nil {
		return nil, err
	}
	if preserveMarker {
		return nil, tx.Commit()
	}
	rows, err := tx.Query("SELECT msg_id, COALESCE(sender_jid,''), ts FROM messages WHERE "+eligible+" AND (ts < ? OR (ts = ? AND rowid <= ?)) ORDER BY ts ASC, rowid ASC", jid, ts, ts, rowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []MessageInfo
	for rows.Next() {
		m := MessageInfo{ChatJID: jid}
		var messageTS int64
		if err := rows.Scan(&m.MsgID, &m.SenderJID, &messageTS); err != nil {
			return nil, err
		}
		m.Timestamp = fromUnix(messageTS)
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return msgs, tx.Commit()
}
