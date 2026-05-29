// Package wc3path resolves the Warcraft III install root that wc3-forge
// reads CASC assets from (asset_handler) and launches for "Test Map"
// (wc3launch), and persists a user-chosen location.
//
// Resolution order (first hit wins):
//
//  1. WC3FORGE_WC3_PATH env var — the power-user / CI override.
//  2. The path the user picked in the GUI, persisted to
//     ~/.wc3-forge/config.json.
//  3. The OS-default install location (see defaultInstallPath, per-platform).
//
// Centralizing this here keeps the asset-serving CASC mount and the Test-Map
// launcher pointed at the same install — they previously each had their own
// copy of the env+default logic and could drift.
package wc3path

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// EnvVar overrides every other source when set. Exported so callers and tests
// don't duplicate the literal.
const EnvVar = "WC3FORGE_WC3_PATH"

// ConfigDirEnv overrides where config.json lives (default ~/.wc3-forge),
// mirroring the bridge's WC3FORGE_MCP_LOCK_DIR. Mainly for tests.
const ConfigDirEnv = "WC3FORGE_CONFIG_DIR"

type config struct {
	// WC3InstallPath is the install ROOT (the directory containing
	// .build.info), not a subfolder like _retail_/x86_64.
	WC3InstallPath string `json:"wc3_install_path"`
}

func configPath() (string, error) {
	dir := os.Getenv(ConfigDirEnv)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".wc3-forge")
	}
	return filepath.Join(dir, "config.json"), nil
}

// SavedPath returns the install path the user persisted via Save, or "" if
// none is set or the config is missing/unreadable (all non-fatal — we just
// fall through to the default).
func SavedPath() string {
	p, err := configPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var c config
	if json.Unmarshal(data, &c) != nil {
		return ""
	}
	return c.WC3InstallPath
}

// Save persists the install path so future sessions resolve to it (at lower
// precedence than the env var). Creates the config dir if needed.
func Save(path string) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config{WC3InstallPath: path}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// Resolve returns the install root to use: env override, else the saved path,
// else the OS default. The result is NOT validated for existence — callers
// that need a real install should gate on IsValidInstall.
func Resolve() string {
	if p := os.Getenv(EnvVar); p != "" {
		return p
	}
	if p := SavedPath(); p != "" {
		return p
	}
	return defaultInstallPath()
}

// IsValidInstall reports whether path looks like a real WC3 install root: it
// contains a CASC ".build.info" manifest. That's the same marker CascLib keys
// on, and it's what distinguishes the install ROOT from a subfolder such as
// _retail_/x86_64 (a common mis-pick).
func IsValidInstall(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(path, ".build.info"))
	return err == nil && !info.IsDir()
}
