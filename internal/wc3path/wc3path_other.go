//go:build !windows

package wc3path

// defaultInstallPath is the conventional WC3 install location on
// non-Windows (macOS). When WC3 lives in a Mead/D4Mac/CrossOver bottle the
// real path differs, so the user typically points wc3-forge at it via the
// GUI (persisted by Save) or WC3FORGE_WC3_PATH — this is only the fallback.
func defaultInstallPath() string {
	return "/Applications/Warcraft III"
}
