package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/lock"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
)

type monitorStateAckOptions struct {
	stateFile, expectedHash, tokenHash, target, dbPath, chat, id string
	ts                                                           int64
}

var monitorHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// The packaged Node workflow validates recipient/message identity first. This
// narrow native boundary owns the entire compare-and-write under the existing
// OS lock. A process death releases it without a racy pathname reclamation.
func newMonitorStateAckCmd(flags *rootFlags) *cobra.Command {
	var opts monitorStateAckOptions
	cmd := &cobra.Command{
		Use: "agent-monitor-state-ack", Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd.Context(), flags)
			defer cancel()
			status, err := commitMonitorStateAck(ctx, opts)
			if err != nil {
				return err
			}
			return out.WriteJSON(os.Stdout, map[string]string{"status": status})
		},
	}
	cmd.Flags().StringVar(&opts.stateFile, "state-file", "", "exact monitor baseline path")
	cmd.Flags().StringVar(&opts.expectedHash, "expected-hash", "", "checked baseline hash")
	cmd.Flags().StringVar(&opts.tokenHash, "ack-token-hash", "", "exact acknowledgement token hash")
	cmd.Flags().StringVar(&opts.target, "target", "", "checked monitor target")
	cmd.Flags().StringVar(&opts.dbPath, "db", "", "checked database path")
	cmd.Flags().StringVar(&opts.chat, "chat", "", "checked inbound chat")
	cmd.Flags().StringVar(&opts.id, "id", "", "checked inbound message")
	cmd.Flags().Int64Var(&opts.ts, "ts", 0, "checked inbound timestamp")
	return cmd
}

func monitorBaselineHash(raw []byte, exists bool) string {
	input := "missing"
	if exists {
		input = "present:" + string(raw)
	}
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

func commitMonitorStateAck(ctx context.Context, opts monitorStateAckOptions) (string, error) {
	if !filepath.IsAbs(opts.stateFile) || !filepath.IsAbs(opts.dbPath) ||
		!monitorHashPattern.MatchString(opts.expectedHash) || !monitorHashPattern.MatchString(opts.tokenHash) ||
		opts.target == "" || opts.target != strings.TrimSpace(opts.target) || opts.chat == "" || opts.id == "" || opts.ts <= 0 {
		return "", fmt.Errorf("invalid exact monitor acknowledgement scope")
	}
	// Even the hidden native entry point must refuse outbound/deleted/empty
	// anchors. Identity resolution remains the packaged workflow's responsibility.
	db, err := store.OpenReadOnly(opts.dbPath)
	if err != nil {
		return "", err
	}
	message, err := db.GetMessage(opts.chat, opts.id)
	db.Close()
	if err != nil || message.FromMe || message.Revoked || message.DeletedForMe || message.DeletedAt != nil ||
		message.ReactionToID != "" || message.ReactionEmoji != "" || message.Timestamp.Unix() != opts.ts ||
		(strings.TrimSpace(message.Text) == "" && strings.TrimSpace(message.MediaType) == "" &&
			(strings.TrimSpace(message.DisplayText) == "" || message.DisplayText == "(message)")) {
		return "", fmt.Errorf("monitor acknowledgement requires the exact stored actionable inbound anchor")
	}
	// Keep a persistent kernel lock inode. Never delete a lock file to recover
	// stale metadata: flock/LockFileEx automatically releases crashed owners.
	guard := opts.stateFile + ".ack-guard"
	for _, name := range []string{guard, filepath.Join(guard, "LOCK")} {
		if stat, err := os.Lstat(name); err == nil && stat.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("monitor acknowledgement guard cannot be a symlink")
		} else if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	lk, err := lock.AcquireWithTimeout(ctx, guard, 2*time.Second)
	if err != nil {
		return "", fmt.Errorf("monitor acknowledgement is busy; baseline unchanged: %w", err)
	}
	defer lk.Release()

	var raw []byte
	exists := false
	state := map[string]any{}
	if stat, err := os.Lstat(opts.stateFile); err == nil {
		if !stat.Mode().IsRegular() || stat.Size() > 64*1024 {
			return "", fmt.Errorf("monitor baseline must be a regular file up to 64 KiB")
		}
		raw, err = os.ReadFile(opts.stateFile)
		if err != nil {
			return "", err
		}
		exists = true
		if err := json.Unmarshal(raw, &state); err != nil || state == nil {
			return "", fmt.Errorf("monitor baseline is not a valid object")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if (state["target"] != nil && state["target"] != opts.target) ||
		(state["dbPath"] != nil && state["dbPath"] != opts.dbPath) {
		return "", fmt.Errorf("monitor baseline belongs to another target or database")
	}
	// Only this exact already-committed token is idempotent. An old token must
	// not overwrite a newer acknowledgement, even when the same ID is present.
	if state["ackTokenHash"] == opts.tokenHash && state["lastProcessedMsgId"] == opts.id &&
		state["chatJid"] == opts.chat && state["target"] == opts.target && state["dbPath"] == opts.dbPath {
		return "already_acknowledged", nil
	}
	if monitorBaselineHash(raw, exists) != opts.expectedHash {
		return "", fmt.Errorf("monitor baseline changed since check; refusing stale acknowledgement")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Publish a complete owner-only baseline on the same filesystem while the
	// kernel lock remains held. Failed writes leave the prior baseline intact.
	payload, err := json.MarshalIndent(map[string]any{
		"lastProcessedMsgId": opts.id, "msgId": opts.id, "ts": opts.ts,
		"target": opts.target, "dbPath": opts.dbPath, "chatJid": opts.chat, "ackTokenHash": opts.tokenHash,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(filepath.Dir(opts.stateFile), ".monitor-ack-")
	if err != nil {
		return "", err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(append(payload, '\n')); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temp.Name(), opts.stateFile); err != nil {
		return "", err
	}
	return "acknowledged", nil
}
