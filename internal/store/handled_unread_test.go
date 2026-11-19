package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const handledTestChat = "15550000001@s.whatsapp.net"

func handledInbound(t *testing.T, d *DB, id string, ts int64, track bool) {
	t.Helper()
	if err := d.UpsertChat(handledTestChat, "dm", "synthetic", time.Unix(ts, 0)); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertMessage(UpsertMessageParams{ChatJID: handledTestChat, MsgID: id, SenderJID: handledTestChat, Timestamp: time.Unix(ts, 0), Text: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	if track {
		if err := d.TrackLiveUnread(handledTestChat, id); err != nil {
			t.Fatal(err)
		}
	}
}
func handledCount(t *testing.T, d *DB, want int) {
	t.Helper()
	c, err := d.GetChat(handledTestChat)
	if err != nil {
		t.Fatal(err)
	}
	if c.UnreadCount != want {
		t.Fatalf("count=%d want=%d", c.UnreadCount, want)
	}
}
func TestHandledUnreadExactSubsetAndLateBackdatedArrival(t *testing.T) {
	d := openTestDB(t)
	handledInbound(t, d, "old-history", 10, false)
	handledInbound(t, d, "first", 100, true)
	handledInbound(t, d, "anchor", 200, true)
	handledInbound(t, d, "newer", 300, true)
	s, err := d.SnapshotHandledUnread(handledTestChat, "anchor")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Messages) != 2 || s.Messages[0].MsgID != "first" || s.Messages[1].MsgID != "anchor" {
		t.Fatalf("selection=%+v", s.Messages)
	}
	if err = d.ClaimHandledUnread(s); err != nil {
		t.Fatal(err)
	}
	handledInbound(t, d, "late-backdated", 50, true)
	ok, err := d.SettleHandledUnread(s)
	if err != nil || !ok {
		t.Fatalf("settle=%v %v", ok, err)
	}
	handledCount(t, d, 2)
	next, err := d.SnapshotHandledUnread(handledTestChat, "late-backdated")
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Messages) != 2 || next.Messages[0].MsgID != "newer" || next.Messages[1].MsgID != "late-backdated" {
		t.Fatalf("remaining=%+v", next.Messages)
	}
}
func TestHandledUnreadUnknownLegacyAndManualDefer(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "manual"}[manual], func(t *testing.T) {
			d := openTestDB(t)
			handledInbound(t, d, "history", 10, false)
			if manual {
				if err := d.SetChatUnread(handledTestChat, true); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := d.SetChatUnreadCount(handledTestChat, 2); err != nil {
					t.Fatal(err)
				}
			}
			handledInbound(t, d, "fresh", 20, true)
			if _, err := d.SnapshotHandledUnread(handledTestChat, "fresh"); err == nil {
				t.Fatal("unknown membership accepted")
			}
		})
	}
}
func TestHandledUnreadDuplicateAndDurableAttemptGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	handledInbound(t, d, "first", 100, true)
	if err = d.TrackLiveUnread(handledTestChat, "first"); err != nil {
		t.Fatal(err)
	}
	handledCount(t, d, 1)
	s, err := d.SnapshotHandledUnread(handledTestChat, "first")
	if err != nil {
		t.Fatal(err)
	}
	if err = d.ClaimHandledUnread(s); err != nil {
		t.Fatal(err)
	}
	if err = d.ClaimHandledUnread(s); err == nil {
		t.Fatal("duplicate claim accepted")
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	attempted, err := d.HasPrivateReadAttempt(handledTestChat, []string{"missing", "first"})
	if err != nil || !attempted {
		t.Fatalf("attempt=%v %v", attempted, err)
	}
	again, err := d.SnapshotHandledUnread(handledTestChat, "first")
	if err != nil {
		t.Fatal(err)
	}
	if err = d.ClaimHandledUnread(again); err == nil {
		t.Fatal("reopened duplicate claim accepted")
	}
	if err = d.RecordPrivateReadAttempt(handledTestChat, []string{"first"}); err == nil {
		t.Fatal("legacy duplicate accepted")
	}
}
func TestHandledUnreadCountMismatchAndMutationPreserve(t *testing.T) {
	d := openTestDB(t)
	handledInbound(t, d, "first", 100, true)
	s, err := d.SnapshotHandledUnread(handledTestChat, "first")
	if err != nil {
		t.Fatal(err)
	}
	s.Messages[0].MsgID = "other"
	if err = d.ClaimHandledUnread(s); err == nil {
		t.Fatal("mutated claim accepted")
	}
	s.Messages[0].MsgID = "first"
	if err = d.ClaimHandledUnread(s); err != nil {
		t.Fatal(err)
	}
	if err = d.SetChatUnreadCount(handledTestChat, 5); err != nil {
		t.Fatal(err)
	}
	ok, err := d.SettleHandledUnread(s)
	if err != nil || ok {
		t.Fatalf("ambiguous settle=%v %v", ok, err)
	}
	handledCount(t, d, 5)
	attempted, err := d.HasPrivateReadAttempt(handledTestChat, []string{"first"})
	if err != nil || !attempted {
		t.Fatal("guard lost")
	}
}
func TestPrivateReadAttemptLegacyAndReadOnlyOlderStore(t *testing.T) {
	d := openTestDB(t)
	handledInbound(t, d, "first", 100, false)
	if err := d.RecordPrivateReadAttempt(handledTestChat, []string{"first"}); err != nil {
		t.Fatal(err)
	}
	attempted, err := d.HasPrivateReadAttempt(handledTestChat, []string{"first"})
	if err != nil || !attempted {
		t.Fatal("guard absent")
	}
	if _, err = d.sql.Exec(`DROP TABLE inbound_read_state; DELETE FROM schema_migrations WHERE version=36`); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(d.path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	attempted, err = ro.HasPrivateReadAttempt(handledTestChat, []string{"first"})
	if err != nil || attempted {
		t.Fatalf("older read-only=%v %v", attempted, err)
	}
}
func TestHandledUnreadKnownZeroResetsStalePending(t *testing.T) {
	d := openTestDB(t)
	handledInbound(t, d, "old", 100, true)
	if err := d.SetChatUnread(handledTestChat, false); err != nil {
		t.Fatal(err)
	}
	handledInbound(t, d, "new", 200, true)
	s, err := d.SnapshotHandledUnread(handledTestChat, "new")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Messages) != 1 || s.Messages[0].MsgID != "new" {
		t.Fatalf("stale membership=%+v", s.Messages)
	}
}

func TestHandledUnreadIdempotentAliasMigrationPreservesNewKnownInput(t *testing.T) {
	d := openTestDB(t)
	handledInbound(t, d, "new", 200, true)
	// There is no old alias history left to merge. Repeating reconciliation
	// must not invalidate unrelated new canonical membership.
	if err := d.MigrateLIDToPN("123@lid", handledTestChat); err != nil {
		t.Fatal(err)
	}
	s, err := d.SnapshotHandledUnread(handledTestChat, "new")
	if err != nil || len(s.Messages) != 1 {
		t.Fatalf("idempotent reconciliation lost scope: %+v %v", s, err)
	}
}

func TestHandledUnreadInitialMigrationShapeKeepsAttemptsUncertain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wacli.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	handledInbound(t, d, "old-attempt", 100, true)
	if err = d.RecordPrivateReadAttempt(handledTestChat, []string{"old-attempt"}); err != nil {
		t.Fatal(err)
	}
	// Reopen the initial, unreleased migration36 layout. There is no evidence
	// of successful completion to infer from its attempted bit or unread count.
	if _, err = d.sql.Exec(`DROP INDEX idx_private_attempt_by_id; ALTER TABLE inbound_read_state DROP COLUMN completed`); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	m, err := d.GetMessage(handledTestChat, "old-attempt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.UnattemptedPrivateReadMessages([]MessageInfo{{MsgID: m.MsgID}}); err == nil {
		t.Fatal("old uncertain attempt became repeatable")
	}
}

func TestHandledUnreadSameCountManualProjectionInvalidates(t *testing.T) {
	for _, marker := range []bool{false, true} {
		t.Run(map[bool]string{false: "count", true: "marker"}[marker], func(t *testing.T) {
			d := openTestDB(t)
			handledInbound(t, d, "first", 100, true)
			if marker {
				if err := d.SetChatUnread(handledTestChat, true); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := d.SetChatUnreadCount(handledTestChat, 1); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := d.SnapshotHandledUnread(handledTestChat, "first"); err == nil {
				t.Fatal("same-count manual projection established false membership")
			}
		})
	}
}

func TestHandledUnreadLaterStoredRowExcludedEvenWhenBackdated(t *testing.T) {
	d := openTestDB(t)
	handledInbound(t, d, "anchor", 200, true)
	handledInbound(t, d, "late", 100, true)
	s, err := d.SnapshotHandledUnread(handledTestChat, "anchor")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Messages) != 1 || s.Messages[0].MsgID != "anchor" {
		t.Fatalf("included later stored row: %+v", s.Messages)
	}
	if err = d.ClaimHandledUnread(s); err != nil {
		t.Fatal(err)
	}
	ok, err := d.SettleHandledUnread(s)
	if err != nil || !ok {
		t.Fatalf("settle=%v %v", ok, err)
	}
	handledCount(t, d, 1)
}

func TestPrivateReadAttemptGlobalAliasGuard(t *testing.T) {
	d := openTestDB(t)
	old := "123@lid"
	if err := d.UpsertChat(old, "dm", "synthetic", time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertMessage(UpsertMessageParams{ChatJID: old, MsgID: "alias-message", Timestamp: time.Unix(100, 0), SenderJID: old, Text: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordPrivateReadAttempt(old, []string{"alias-message"}); err != nil {
		t.Fatal(err)
	}
	found, err := d.HasPrivateReadAttempt(handledTestChat, []string{"alias-message"})
	if err != nil || found {
		t.Fatalf("PN scoped lookup=%v %v", found, err)
	}
	found, err = d.HasPrivateReadAttempt("", []string{"alias-message"})
	if err != nil || !found {
		t.Fatalf("global alias lookup=%v %v", found, err)
	}
}

func TestPrivateReadAttemptAliasCannotRedispatch(t *testing.T) {
	d := openTestDB(t)
	for _, jid := range []string{"123@lid", handledTestChat} {
		if err := d.UpsertChat(jid, "dm", "synthetic", time.Unix(100, 0)); err != nil {
			t.Fatal(err)
		}
		if err := d.UpsertMessage(UpsertMessageParams{ChatJID: jid, MsgID: "same-inbound", Timestamp: time.Unix(100, 0), SenderJID: jid, Text: "synthetic"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.RecordPrivateReadAttempt("123@lid", []string{"same-inbound"}); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordPrivateReadAttempt(handledTestChat, []string{"same-inbound"}); err == nil {
		t.Fatal("alias redispatch accepted")
	}
}

func TestAppStatePrivateReadEchoPreservesUnreadAndCheckpoints(t *testing.T) {
	for _, ids := range [][]string{{"handled"}, {"handled", "newer"}, nil} {
		name := "ids"
		if len(ids) == 0 {
			name = "coarse"
		} else if len(ids) > 1 {
			name = "mixed"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wacli.db")
			d, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			handledInbound(t, d, "handled", 100, true)
			s, err := d.SnapshotHandledUnread(handledTestChat, "handled")
			if err != nil {
				t.Fatal(err)
			}
			if err = d.ClaimHandledUnread(s); err != nil {
				t.Fatal(err)
			}
			if ok, err := d.SettleHandledUnread(s); err != nil || !ok {
				t.Fatalf("settle=%v %v", ok, err)
			}
			if err = d.Close(); err != nil {
				t.Fatal(err)
			}
			d, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			handledInbound(t, d, "newer", 200, true)
			checkpoint := DeliveryCheckpoint{Generation: "private-echo", Sequence: 1, Digest: make([]byte, 32)}
			if err = d.ApplyAppStateDelivery(t.Context(), checkpoint, []ChatStateProjection{{Kind: "read", JID: handledTestChat, Enabled: true, ReadThrough: time.Unix(300, 0), ReadIDs: ids}}, nil); err != nil {
				t.Fatal(err)
			}
			handledCount(t, d, 1)
			complete, err := d.HasAppStateDeliveryCheckpoint(t.Context(), checkpoint)
			if err != nil || !complete {
				t.Fatalf("checkpoint=%v %v", complete, err)
			}
			pending, err := d.SnapshotHandledUnread(handledTestChat, "newer")
			if err != nil || len(pending.Messages) != 1 {
				t.Fatalf("pending lost: %+v %v", pending, err)
			}
		})
	}
}

func TestPrivateReadAliasMigrationRetainsCoarseEchoGuard(t *testing.T) {
	d := openTestDB(t)
	old := "123@lid"
	if err := d.UpsertChat(old, "dm", "synthetic", time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertMessage(UpsertMessageParams{ChatJID: old, MsgID: "handled-alias", Timestamp: time.Unix(100, 0), SenderJID: old, Text: "synthetic"}); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordPrivateReadAttempt(old, []string{"handled-alias"}); err != nil {
		t.Fatal(err)
	}
	tx, err := d.sql.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = migrateLIDChatToPN(tx, old, handledTestChat); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	found, err := d.HasPrivateReadAttempt(handledTestChat, []string{"handled-alias"})
	if err != nil || !found {
		t.Fatalf("canonical guard=%v %v", found, err)
	}
	handledInbound(t, d, "fresh", 200, true)
	checkpoint := DeliveryCheckpoint{Generation: "alias-coarse-echo", Sequence: 1, Digest: make([]byte, 32)}
	if err = d.ApplyAppStateDelivery(t.Context(), checkpoint, []ChatStateProjection{{Kind: "read", JID: handledTestChat, Enabled: true}}, nil); err != nil {
		t.Fatal(err)
	}
	handledCount(t, d, 1)
}

func TestPrivateAttemptHistoryUsesMessageIndex(t *testing.T) {
	d := openTestDB(t)
	var id, parent, unused int
	var plan string
	if err := d.sql.QueryRow("EXPLAIN QUERY PLAN "+privateAttemptsByID, "old-message").Scan(&id, &parent, &unused, &plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "idx_private_attempt_by_id") || !strings.Contains(plan, "SEARCH") {
		t.Fatalf("unindexed lookup: %s", plan)
	}
}
