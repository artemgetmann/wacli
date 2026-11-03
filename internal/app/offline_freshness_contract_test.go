package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestOfflineCompletionFreshnessRecorded(t *testing.T) {
	for _, name := range []string{"recorded", "invalid_receipt", "pending_app_state", "disconnected", "unauthenticated", "cancelled", "write_failure"} {
		t.Run(name, func(t *testing.T) {
			a := newTestApp(t)
			f := newFakeWA()
			f.connected = true
			a.wa = f
			prior := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			if err := a.db.RecordSuccessfulSync(prior); err != nil {
				t.Fatal(err)
			}
			var log bytes.Buffer
			a.opts.Events = out.NewEventWriter(&log, true)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			limits := &syncStorageLimits{app: a}
			var stored, last atomic.Int64
			a.addSyncEventHandler(ctx, SyncOptions{}, &stored, &last, make(chan struct{}, 1), make(chan struct{}, 1), make(chan staleReconnectRequest, 1), nil, nil, limits, &syncPresence{}, nil)
			switch name {
			case "invalid_receipt":
				f.emit(&events.Receipt{MessageSource: types.MessageSource{Chat: types.NewJID("synthetic", types.DefaultUserServer)}, MessageIDs: []types.MessageID{""}, Type: types.ReceiptTypeRead})
			case "pending_app_state":
				limits.appStatePending.Store(1)
			case "disconnected":
				f.connected = false
			case "unauthenticated":
				f.authed = false
			case "cancelled":
				cancel()
			case "write_failure":
				db, err := sql.Open("sqlite3", filepath.Join(a.StoreDir(), "wacli.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err = db.Exec(`CREATE TRIGGER reject_milestone BEFORE UPDATE OF last_sync_at ON retrieval_state BEGIN SELECT RAISE(ABORT,'synthetic milestone failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			f.emit(&events.OfflineSyncCompleted{Count: 7})
			var result *bool
			var count int
			var writeWarning bool
			for _, line := range bytes.Split(bytes.TrimSpace(log.Bytes()), []byte{'\n'}) {
				var event struct {
					Event string `json:"event"`
					Data  struct {
						Count    int    `json:"count"`
						Recorded *bool  `json:"freshness_recorded"`
						Code     string `json:"code"`
					} `json:"data"`
				}
				if err := json.Unmarshal(line, &event); err != nil {
					t.Fatal(err)
				}
				if event.Event == "offline_sync_completed" {
					result = event.Data.Recorded
					count = event.Data.Count
				}
				if event.Data.Code == "sync_state_write_failed" {
					writeWarning = true
				}
			}
			if result == nil {
				t.Fatal("offline completion must explicitly report whether this event recorded the milestone")
			}
			want := name == "recorded"
			if *result != want || count != 7 {
				t.Fatalf("recorded=%v count=%d want recorded=%v count=7", *result, count, want)
			}
			marker, err := a.db.LastSuccessfulSync()
			if err != nil || marker == nil {
				t.Fatalf("marker=%v err=%v", marker, err)
			}
			if want && !marker.After(prior) {
				t.Fatal("success did not advance persisted marker")
			}
			if !want && !marker.Equal(prior) {
				t.Fatal("failure changed prior persisted marker")
			}
			if writeWarning != (name == "write_failure") {
				t.Fatal("milestone write warning changed")
			}
		})
	}
}
