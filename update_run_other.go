//go:build !windows

package main

import (
	"fmt"
	"runtime"
)

// runInstaller is Windows-only for now: the updater downloads the NSIS
// installer, which has no macOS/Linux equivalent yet. On other platforms we
// fail loudly rather than silently doing nothing — the frontend surfaces the
// error and users update via their platform's mechanism. Structured so a
// darwin (.dmg/.zip) path can slot in later behind the same App method.
func runInstaller(path string) error {
	return fmt.Errorf("in-app installer is not supported on %s yet", runtime.GOOS)
}
