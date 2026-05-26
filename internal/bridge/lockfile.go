package bridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Per-process lock layout: ~/.wc3-forge/mcp/<pid>.lock
//
// Multiple wc3-forge instances can run side-by-side; each writes its own
// pid-named lock so external MCP servers can enumerate them via
// `sessions_list`. On startup we prune any lock files whose pid is no
// longer alive (force-kill / crash leaves them behind).
//
// Override the lock-dir location with WC3FORGE_MCP_LOCK_DIR.

func lockDir() string {
	if v := os.Getenv("WC3FORGE_MCP_LOCK_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Fall back to cwd if we somehow can't find HOME — the bridge
		// should still work even if multi-instance enumeration breaks.
		home = "."
	}
	return filepath.Join(home, ".wc3-forge", "mcp")
}

func ownLockPath() string {
	return filepath.Join(lockDir(), strconv.Itoa(os.Getpid())+".lock")
}

type lockFile struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	Token     string `json:"token"`
	StartedAt string `json:"started_at"`
}

func writeLockfile(port int, token string) error {
	pruneStaleLocks()

	path := ownLockPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create lockdir: %w", err)
	}

	doc := lockFile{
		PID:       os.Getpid(),
		Port:      port,
		Token:     token,
		StartedAt: time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lockfile: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write lockfile: %w", err)
	}
	return nil
}

func deleteLockfile() {
	_ = os.Remove(ownLockPath())
}

func pruneStaleLocks() {
	dir := lockDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		// ENOENT is fine; nothing to prune.
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".lock")
		pid, err := strconv.Atoi(stem)
		if err != nil {
			continue
		}
		if !pidAlive(pid) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
