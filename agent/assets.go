// Package agent packages the standalone agent workflow with the Go executable.
// Helpers are versioned with the binary; no framework checkout is consulted.
package agent

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed package.json dispatch.ts lib/runtime.ts lib/send.ts lib/transcribe.ts lib/owner/store.ts lib/owner/live.ts lib/owner/health.ts lib/read/recent-reply.ts lib/read/monitor-check.ts lib/read/reconciliation.ts lib/read/monitor.ts
var helpers embed.FS

// materialize repairs a cached helper before execution if it differs from the
// embedded version. Atomic rename avoids partially written code during parallel
// invocations. The hash separates installed versions during rollback.
func materialize() (string, error) {
	h := sha256.New()
	if err := fs.WalkDir(helpers, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := helpers.ReadFile(name)
		if err != nil {
			return err
		}
		h.Write([]byte(name))
		h.Write(data)
		return nil
	}); err != nil {
		return "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(cache, "wacli-agent", hex.EncodeToString(h.Sum(nil)))
	err = fs.WalkDir(helpers, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := helpers.ReadFile(name)
		if err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if current, err := os.ReadFile(target); err == nil && string(current) == string(data) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(target), ".helper-")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), target)
	})
	return root, err
}

// Run passes exact argv/stdin to Node without a shell. Raw wacli calls resolve
// to this same executable, including when an older Homebrew wacli is on PATH.
func Run(ctx context.Context, store string, readOnly bool, args []string) error {
	root, err := materialize()
	if err != nil {
		return fmt.Errorf("materialize standalone helpers: %w", err)
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "node", append([]string{"--experimental-strip-types", filepath.Join(root, "dispatch.ts")}, args...)...)
	cmd.Env = append(os.Environ(), "WACLI_AGENT_BIN="+binary, "WACLI_AGENT_BINARY="+binary, "WACLI_AGENT_STORE="+store, "PATH="+filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if readOnly {
		cmd.Env = append(cmd.Env, "WACLI_READONLY=1")
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
