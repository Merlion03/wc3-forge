package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/StephenSHorton/wc3-forge/internal/update"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// GetAppVersion returns the running binary's release version, or "dev" for an
// unstamped local build. AppVersion (main package var) is injected at release
// time via -ldflags "-X main.AppVersion=...".
func (a *App) GetAppVersion() string {
	if strings.TrimSpace(AppVersion) == "" {
		return "dev"
	}
	return AppVersion
}

// CheckForUpdate queries GitHub for the latest published release and reports
// whether it's newer than this binary. Returns the comparison result (never
// nil on success) so the frontend can both decide whether to show the update
// banner and, on a manual check, tell the user they're already current.
func (a *App) CheckForUpdate() (*update.CheckResult, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	return update.Check(ctx, a.GetAppVersion(), nil)
}

// DownloadAndRunInstaller downloads the release installer at rawURL to a temp
// file (emitting eventUpdateProgress as it streams), launches it, and quits
// wc3-forge so the installer can replace the running binary. The caller
// (frontend) is responsible for confirming/saving unsaved work first — this
// quits unconditionally once the installer is launched.
//
// rawURL must be a GitHub-hosted https URL; anything else is rejected so a
// poisoned update-check response can't make us download and run an arbitrary
// executable.
func (a *App) DownloadAndRunInstaller(rawURL string) error {
	if err := validateInstallerURL(rawURL); err != nil {
		return err
	}

	dst, err := a.downloadInstaller(rawURL)
	if err != nil {
		return err
	}

	// Verify the download's SHA-256 against the release's published SHA256SUMS
	// BEFORE we UAC-elevate and run it. On mismatch the temp file is removed and
	// we refuse to launch — a corrupted or tampered installer never executes.
	if err := a.verifyInstaller(rawURL, dst); err != nil {
		os.Remove(dst)
		return err
	}

	if err := runInstaller(dst); err != nil {
		return fmt.Errorf("launch installer: %w", err)
	}

	// The installer is a separate process; quitting releases our lock on
	// wc3-forge.exe so the upgrade can overwrite it. forceQuitting bypasses
	// the unsaved-changes guard (the frontend already handled that).
	a.forceQuitting = true
	runtime.Quit(a.ctx)
	return nil
}

// validateInstallerURL ensures we only ever download+run binaries served by
// GitHub over TLS.
func validateInstallerURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid update URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("refusing non-https update URL %q", rawURL)
	}
	host := strings.ToLower(u.Hostname())
	if host != "github.com" && !strings.HasSuffix(host, ".github.com") &&
		!strings.HasSuffix(host, ".githubusercontent.com") {
		return fmt.Errorf("refusing update URL from untrusted host %q", host)
	}
	return nil
}

// verifyInstaller confirms the downloaded installer matches the SHA-256 the
// release published in its SHA256SUMS asset. The checksums file is the sibling
// asset of the installer URL (same release-download path). On a release that
// predates SHA256SUMS (older versions) it's absent — we log and proceed, since
// TLS to github already protects the transport; the checksum is defense against
// a corrupted/tampered download for releases that publish it (v0.9.0+).
func (a *App) verifyInstaller(installerURL, filePath string) error {
	sumsURL, err := update.ChecksumsURL(installerURL)
	if err != nil {
		return fmt.Errorf("derive checksums URL: %w", err)
	}
	// Same github-over-https guard the installer URL passed.
	if err := validateInstallerURL(sumsURL); err != nil {
		return err
	}
	expected, found, err := a.fetchExpectedSHA256(sumsURL, installerURL)
	if err != nil {
		return fmt.Errorf("fetch checksums: %w", err)
	}
	if !found {
		log.Printf("update: release has no %s; skipping installer hash verification", update.ChecksumsAssetName)
		return nil
	}
	actual, err := sha256File(filePath)
	if err != nil {
		return fmt.Errorf("hash installer: %w", err)
	}
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("installer verification failed: SHA-256 %s does not match the published %s — the download may be corrupt or tampered, so it was NOT run", actual, expected)
	}
	log.Printf("update: installer SHA-256 verified (%s)", actual)
	return nil
}

// fetchExpectedSHA256 downloads the SHA256SUMS asset and returns the hash for
// the installer's base name. found=false (no error) when the asset is absent
// (404) — i.e. an older release without checksums.
func (a *App) fetchExpectedSHA256(sumsURL, installerURL string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sumsURL, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("User-Agent", "wc3-forge-updater/"+a.GetAppVersion())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("unexpected status %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // SHA256SUMS is tiny; cap at 1 MiB
	if err != nil {
		return "", false, err
	}
	sum, ok := update.ParseSHA256SUMS(data, installerURL)
	return sum, ok, nil
}

// sha256File returns the lowercase hex SHA-256 of the file at path.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// downloadInstaller streams rawURL into a temp .exe, emitting throttled
// progress events, and returns the file path.
func (a *App) downloadInstaller(rawURL string) (string, error) {
	// Generous deadline for a ~10MB download on a slow link; the context
	// (not the client) bounds it so a stall can't hang forever.
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("build download request: %w", err)
	}
	req.Header.Set("User-Agent", "wc3-forge-updater/"+a.GetAppVersion())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download installer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download installer: unexpected status %s", resp.Status)
	}

	f, err := os.CreateTemp("", "wc3-forge-*-installer.exe")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	dst := f.Name()

	pw := &progressWriter{
		app:   a,
		total: resp.ContentLength,
		start: time.Now(),
	}
	if _, err := io.Copy(f, io.TeeReader(resp.Body, pw)); err != nil {
		f.Close()
		os.Remove(dst)
		return "", fmt.Errorf("write installer: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(dst)
		return "", fmt.Errorf("close installer: %w", err)
	}

	// Make sure the bar lands on 100% even if the last chunk didn't tick.
	pw.emit(true)

	// Normalize the extension so the OS treats it as an executable.
	if filepath.Ext(dst) != ".exe" {
		exe := dst + ".exe"
		if err := os.Rename(dst, exe); err == nil {
			dst = exe
		}
	}
	return dst, nil
}

// progressWriter counts bytes written and emits eventUpdateProgress at most
// ~5x/second so the frontend bar animates without flooding the event bus.
type progressWriter struct {
	app      *App
	total    int64
	received int64
	start    time.Time
	lastEmit time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n := len(b)
	p.received += int64(n)
	if time.Since(p.lastEmit) >= 200*time.Millisecond {
		p.emit(false)
	}
	return n, nil
}

func (p *progressWriter) emit(final bool) {
	p.lastEmit = time.Now()
	var pct float64
	if p.total > 0 {
		pct = float64(p.received) / float64(p.total) * 100
		if pct > 100 {
			pct = 100
		}
	} else if final {
		pct = 100
	}
	if p.app.ctx == nil {
		return
	}
	runtime.EventsEmit(p.app.ctx, eventUpdateProgress, map[string]any{
		"received": p.received,
		"total":    p.total,
		"pct":      pct,
	})
}
