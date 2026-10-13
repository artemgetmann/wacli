package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/store"
)

func monitorNativeFixture(t *testing.T) monitorStateAckOptions {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wacli.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	chat := "15551234567@s.whatsapp.net"
	when := time.Unix(1700000000, 0)
	if err := db.UpsertChat(chat, "dm", "Fixture", when); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"older", "newer"} {
		if err := db.UpsertMessage(store.UpsertMessageParams{ChatJID: chat, MsgID: id, SenderJID: chat, Timestamp: when, Text: id}); err != nil {
			t.Fatal(err)
		}
	}
	return monitorStateAckOptions{stateFile: filepath.Join(dir, "baseline.json"), expectedHash: monitorBaselineHash(nil, false), tokenHash: strings.Repeat("a", 64), target: chat, dbPath: dbPath, chat: chat, id: "older", ts: when.Unix()}
}

func TestNativeMonitorAckConcurrentCASAndIdempotentRetry(t *testing.T) {
	opts := monitorNativeFixture(t)
	other := opts
	other.id, other.tokenHash = "newer", strings.Repeat("b", 64)
	start := make(chan struct{})
	results := make(chan struct {
		opts monitorStateAckOptions
		err  error
	}, 2)
	var workers sync.WaitGroup
	for _, request := range []monitorStateAckOptions{opts, other} {
		workers.Add(1)
		go func(request monitorStateAckOptions) {
			defer workers.Done()
			<-start
			_, err := commitMonitorStateAck(context.Background(), request)
			results <- struct {
				opts monitorStateAckOptions
				err  error
			}{request, err}
		}(request)
	}
	close(start)
	workers.Wait()
	close(results)
	var winner monitorStateAckOptions
	wins, rejected := 0, 0
	for result := range results {
		if result.err == nil {
			wins++
			winner = result.opts
		} else if strings.Contains(result.err.Error(), "changed since check") {
			rejected++
		} else {
			t.Fatal(result.err)
		}
	}
	if wins != 1 || rejected != 1 {
		t.Fatalf("wins=%d stale=%d", wins, rejected)
	}
	status, err := commitMonitorStateAck(context.Background(), winner)
	if err != nil || status != "already_acknowledged" {
		t.Fatalf("exact retry: %q %v", status, err)
	}
	raw, err := os.ReadFile(opts.stateFile)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if json.Unmarshal(raw, &state) != nil || state["lastProcessedMsgId"] != winner.id {
		t.Fatal("concurrent stale writer changed the completed baseline")
	}
	stat, _ := os.Stat(opts.stateFile)
	if stat.Mode().Perm() != 0o600 {
		t.Fatal("baseline is not owner-only")
	}
}

func TestNativeMonitorAckKernelLockSurvivesContentionAndReleasesAfterCrash(t *testing.T) {
	if guard := os.Getenv("WACLI_MONITOR_NATIVE_TEST_GUARD"); guard != "" {
		lk, err := lock.Acquire(guard)
		if err != nil {
			t.Fatal(err)
		}
		defer lk.Release()
		os.Stdout.WriteString("LOCKED\n")
		select {}
	}
	opts := monitorNativeFixture(t)
	child := exec.Command(os.Args[0], "-test.run=^TestNativeMonitorAckKernelLockSurvivesContentionAndReleasesAfterCrash$")
	child.Env = append(os.Environ(), "WACLI_MONITOR_NATIVE_TEST_GUARD="+opts.stateFile+".ack-guard")
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "LOCKED\n" {
		t.Fatalf("child lock startup: %q %v", line, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := commitMonitorStateAck(ctx, opts); err == nil {
		t.Fatal("live kernel lock was stolen")
	}
	if _, err := os.Stat(opts.stateFile); !os.IsNotExist(err) {
		t.Fatal("failed lock attempt changed baseline")
	}
	child.Process.Kill()
	child.Wait()
	status, err := commitMonitorStateAck(context.Background(), opts)
	if err != nil || status != "acknowledged" {
		t.Fatalf("crashed lock did not release: %q %v", status, err)
	}
}

func TestNativeMonitorAckReadOnlyRefusesBeforeFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.json")
	err := execute([]string{"--read-only", "agent-monitor-state-ack", "--state-file", path})
	if err == nil || !strings.Contains(err.Error(), "read-only mode") {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".ack-guard"); !os.IsNotExist(err) {
		t.Fatal("read-only acknowledgement created a guard")
	}
}
