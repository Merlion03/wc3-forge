//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// runInstaller launches the downloaded NSIS installer with the "runas" verb
// so Windows shows the UAC elevation prompt (the installer writes to Program
// Files). The caller (DownloadAndRunInstaller) quits wc3-forge after this
// returns, but quitting the editor is NOT enough to free the binary: the
// `wc3-forge.exe --mcp` server that Claude spawns keeps the installed image
// locked independently. So the installer doesn't rely on the app exiting —
// project.nsi renames the locked binary aside before its file-copy step
// (Windows allows renaming a running image), which is what closes the
// "Error opening file for writing" failure.
//
// ShellExecuteW is used (via syscall.NewLazyDLL, matching internal/casc's
// no-cgo DLL-binding style) rather than os/exec because os/exec can't request
// elevation — only the shell's "runas" verb triggers UAC.
func runInstaller(path string) error {
	verb, err := syscall.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}

	shell32 := syscall.NewLazyDLL("shell32.dll")
	shellExecute := shell32.NewProc("ShellExecuteW")

	const swShowNormal = 1
	// ShellExecuteW(hwnd, lpVerb, lpFile, lpParameters, lpDirectory, nShowCmd)
	ret, _, callErr := shellExecute.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		0,
		0,
		uintptr(swShowNormal),
	)
	// ShellExecuteW returns a value > 32 on success; <= 32 is an error code.
	if ret <= 32 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return fmt.Errorf("ShellExecuteW failed (code %d): %w", ret, callErr)
		}
		return fmt.Errorf("ShellExecuteW failed with code %d", ret)
	}
	return nil
}
