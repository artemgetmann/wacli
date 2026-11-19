package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// The packaged agent helper sets this for the sync child and redirects stderr
// to its append-only log. Ordinary sync invocations have no agent owner record.
const agentOwnerLogEnv = "WACLI_AGENT_OWNER_LOG"

type agentOwnerLog struct {
	RunID  string `json:"runId"`
	LogDev int64  `json:"logDev"`
	LogIno int64  `json:"logIno"`
}

// The caller holds the store lock for the lifetime of this sync. A child
// that fails to acquire it cannot replace a previous owner's evidence.
func publishAgentSyncOwner(storeDir, logIdentity string) error {
	var log agentOwnerLog
	if err := json.Unmarshal([]byte(logIdentity), &log); err != nil || log.RunID == "" {
		return fmt.Errorf("invalid agent owner log identity")
	}
	// Use an independent append descriptor so the position after the marker
	// write identifies that marker even when other processes append errors.
	markers, err := os.OpenFile(filepath.Join(storeDir, "openclaw-sync.log"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer markers.Close()
	info, err := markers.Stat()
	if err != nil {
		return err
	}
	stderrInfo, err := os.Stderr.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, stderrInfo) {
		return fmt.Errorf("agent sync log does not match the inherited log")
	}
	host, err := os.Hostname()
	if err != nil {
		return err
	}
	marker, err := json.Marshal(struct {
		RunID string `json:"agentOwnerRun"`
	}{log.RunID})
	if err != nil {
		return err
	}
	marker = append(marker, '\n')
	if _, err := markers.Write(marker); err != nil {
		return err
	}
	end, err := markers.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	record := struct {
		agentOwnerLog
		PID            int       `json:"pid"`
		StartedAt      time.Time `json:"startedAt"`
		StoreDir       string    `json:"storeDir"`
		Command        []string  `json:"command"`
		Host           string    `json:"host"`
		LogStartOffset int64     `json:"logStartOffset"`
	}{log, os.Getpid(), time.Now(), storeDir, os.Args, host, end - int64(len(marker))}

	// Prepare a private record and replace the old one atomically so status
	// sees a complete record. Publish it only after the run marker is written.
	tmp, err := os.CreateTemp(storeDir, ".sync-owner-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	writeErr := json.NewEncoder(tmp).Encode(record)
	closeErr := tmp.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp.Name(), filepath.Join(storeDir, "openclaw-sync-owner.json"))
}
