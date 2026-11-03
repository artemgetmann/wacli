package store

import (
	"bytes"
	"context"
	"github.com/mattn/go-sqlite3"
	"testing"
	"time"
)

func TestAppStateDeliveryAtomicProjectionAndCheckpoint(t *testing.T) {
	d := openTestDB(t)
	ctx := t.Context()
	d.sql.SetMaxOpenConns(1)
	jid := "15550000001@s.whatsapp.net"
	at := time.Unix(1700000000, 0)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(d.UpsertChat(jid, "dm", "synthetic", at))
	for _, id := range []string{"earlier", "handled", "newer"} {
		must(d.UpsertMessage(UpsertMessageParams{ChatJID: jid, MsgID: id, SenderJID: jid, Timestamp: at, Text: "synthetic"}))
	}
	must(d.SetChatUnreadCount(jid, 3))
	id := DeliveryCheckpoint{Generation: "synthetic-generation", Sequence: 1, Digest: make([]byte, 32)}
	actions := []ChatStateProjection{{Kind: "archive", JID: jid, Enabled: true, Timestamp: at}, {Kind: "pin", JID: jid, Enabled: true}, {Kind: "read", JID: jid, Enabled: true, ReadThrough: at, ReadIDs: []string{"handled"}}}
	check := func(complete bool) {
		t.Helper()
		c, err := d.GetChat(jid)
		must(err)
		count := 3
		if complete {
			count = 1
		}
		if c.Archived != complete || c.Pinned != complete || !c.Unread || c.UnreadCount != count {
			t.Fatalf("partial or wrong projection: %+v", c)
		}
		done, err := d.HasAppStateDeliveryCheckpoint(ctx, id)
		must(err)
		if done != complete {
			t.Fatalf("checkpoint=%t", done)
		}
		debt, err := d.AppStateRecoveryRequired("regular_high")
		must(err)
		if debt != complete {
			t.Fatalf("uncovered debt=%t", debt)
		}
	}
	_, err := d.sql.Exec(`CREATE TRIGGER fail_projection BEFORE UPDATE OF pinned ON chats WHEN NEW.pinned=1 BEGIN SELECT RAISE(FAIL,'synthetic second projection failure'); END`)
	must(err)
	if err = d.ApplyAppStateDelivery(ctx, id, actions, []string{"regular_high"}); err == nil {
		t.Fatal("projection failure hidden")
	}
	check(false)
	_, err = d.sql.Exec("DROP TRIGGER fail_projection")
	must(err)
	_, err = d.sql.Exec(`CREATE TRIGGER fail_checkpoint BEFORE INSERT ON app_state_delivery_checkpoint BEGIN SELECT RAISE(FAIL,'synthetic checkpoint failure'); END`)
	must(err)
	if err = d.ApplyAppStateDelivery(ctx, id, actions, []string{"regular_high"}); err == nil {
		t.Fatal("checkpoint failure hidden")
	}
	check(false)
	_, err = d.sql.Exec("DROP TRIGGER fail_checkpoint")
	must(err)
	_, err = d.sql.Exec(`CREATE TRIGGER require_full_sync BEFORE INSERT ON app_state_delivery_checkpoint WHEN (SELECT synchronous FROM pragma_synchronous)<2 BEGIN SELECT RAISE(FAIL,'checkpoint used weak synchronization'); END`)
	must(err)
	must(d.ApplyAppStateDelivery(ctx, id, actions, []string{"regular_high"}))
	check(true)
	var restored int
	must(d.sql.QueryRow("PRAGMA synchronous").Scan(&restored))
	if restored != 1 {
		t.Fatalf("normal connection setting not restored: %d", restored)
	}

	// An exact replay must skip every projection, including unversioned pin and
	// unread updates, after a newer local/message-store update.
	must(d.SetChatPinned(jid, false))
	must(d.IncrementChatUnread(jid))
	must(d.ApplyAppStateDelivery(ctx, id, actions, []string{"regular_high"}))
	c, err := d.GetChat(jid)
	must(err)
	if c.Pinned || !c.Unread || c.UnreadCount != 2 {
		t.Fatalf("replay overwrote newer state: %+v", c)
	}
	conflict := id
	conflict.Digest = append([]byte{}, id.Digest...)
	conflict.Digest[0] = 1
	if err = d.ApplyAppStateDelivery(ctx, conflict, actions, nil); err == nil {
		t.Fatal("conflicting digest accepted")
	}
	gap := id
	gap.Sequence = 3
	if err = d.ApplyAppStateDelivery(ctx, gap, actions, nil); err == nil {
		t.Fatal("checkpoint gap accepted")
	}
}

func TestNativeDeliveryDebtCoalescesAndCannotEraseNewerWork(t *testing.T) {
	d := openTestDB(t)
	ctx := t.Context()
	collection := "regular_high"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	gen, _, err := d.BeginAppStateRecovery(collection)
	must(err)
	id := DeliveryCheckpoint{Generation: "synthetic-coalescing", Digest: make([]byte, 32)}
	for n := uint64(1); n <= 200; n++ {
		id.Sequence = n
		must(d.ApplyAppStateDelivery(ctx, id, nil, []string{collection}))
	}
	var mappings, intents int
	must(d.sql.QueryRow("SELECT COUNT(*) FROM app_state_delivery_recovery").Scan(&mappings))
	must(d.sql.QueryRow("SELECT COUNT(*) FROM app_state_recovery_intents").Scan(&intents))
	if mappings != 1 || intents != 2 {
		t.Fatalf("native debt grew per batch or deleted unrelated intent: mappings=%d intents=%d", mappings, intents)
	}
	older := id
	older.Sequence = 1
	must(d.ClearFetchedDeliveryRecovery(ctx, collection, []DeliveryCheckpoint{older}))
	must(d.ClearFetchedDeliveryRecovery(ctx, "regular_low", []DeliveryCheckpoint{id}))
	cleared, err := d.ClearAppStateRecoveryGeneration(collection, gen)
	must(err)
	if cleared {
		t.Fatal("older replay erased newer native work")
	}
	must(d.ClearFetchedDeliveryRecovery(ctx, collection, []DeliveryCheckpoint{id}))
	required, err := d.AppStateRecoveryRequired(collection)
	must(err)
	if required {
		t.Fatal("exact completed batch did not settle representative debt")
	}
}

func TestAppStateDeliveryCanceledBatchRollsBack(t *testing.T) {
	d := openTestDB(t)
	d.sql.SetMaxOpenConns(1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	jid := "15550000001@s.whatsapp.net"
	must(d.UpsertChat(jid, "dm", "synthetic", time.Unix(1700000000, 0)))
	conn, err := d.sql.Conn(t.Context())
	must(err)
	must(conn.Raw(func(raw any) error {
		return raw.(*sqlite3.SQLiteConn).RegisterFunc("cancel_projection", func() int { cancel(); return 0 }, false)
	}))
	must(conn.Close())
	_, err = d.sql.Exec(`CREATE TRIGGER cancel_projection BEFORE UPDATE OF pinned ON chats WHEN NEW.pinned=1 BEGIN SELECT cancel_projection(); END`)
	must(err)
	id := DeliveryCheckpoint{Generation: "synthetic-cancel", Sequence: 1, Digest: make([]byte, 32)}
	err = d.ApplyAppStateDelivery(ctx, id, []ChatStateProjection{{Kind: "archive", JID: jid, Enabled: true, Timestamp: time.Unix(1700000000, 0)}, {Kind: "pin", JID: jid, Enabled: true}}, nil)
	if err == nil || ctx.Err() != context.Canceled {
		t.Fatalf("cancellation not surfaced: %v", err)
	}
	chat, err := d.GetChat(jid)
	must(err)
	if chat.Archived || chat.Pinned {
		t.Fatalf("cancellation left partial projection: %+v", chat)
	}
	saved, err := d.HasAppStateDeliveryCheckpoint(t.Context(), id)
	must(err)
	if saved {
		t.Fatal("cancellation advanced checkpoint")
	}
	_, err = d.sql.Exec("DROP TRIGGER cancel_projection")
	must(err)
	must(d.ApplyAppStateDelivery(t.Context(), id, []ChatStateProjection{{Kind: "pin", JID: jid, Enabled: true}}, nil))
}

func TestBoundDeliveryChecksPredecessorInsideAppTransaction(t *testing.T) {
	d := openTestDB(t)
	ctx := t.Context()
	jid := "456@s.whatsapp.net"
	if e := d.UpsertChat(jid, "dm", "synthetic", time.Unix(1700000000, 0)); e != nil {
		t.Fatal(e)
	}
	one := DeliveryCheckpoint{Device: "synthetic-device", Generation: "synthetic-generation", Sequence: 1, Digest: bytes.Repeat([]byte{1}, 32)}
	if e := d.ApplyAppStateDelivery(ctx, one, []ChatStateProjection{{Kind: "pin", JID: jid, Enabled: true}}, nil); e != nil {
		t.Fatal(e)
	}
	two := DeliveryCheckpoint{Device: one.Device, Generation: one.Generation, Sequence: 2, Digest: bytes.Repeat([]byte{2}, 32), Predecessor: &DeliveryCheckpoint{Generation: one.Generation, Sequence: 1, Digest: bytes.Repeat([]byte{9}, 32)}}
	if e := d.ApplyAppStateDelivery(ctx, two, []ChatStateProjection{{Kind: "pin", JID: jid, Enabled: false}}, nil); e == nil {
		t.Fatal("wrong predecessor was applied")
	}
	chat, e := d.GetChat(jid)
	if e != nil || !chat.Pinned {
		t.Fatal("failed predecessor check partially projected")
	}
	two.Predecessor = &one
	if e = d.ApplyAppStateDelivery(ctx, two, []ChatStateProjection{{Kind: "pin", JID: jid, Enabled: false}}, nil); e != nil {
		t.Fatal(e)
	}
}
