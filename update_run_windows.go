//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// runInstaller launches the downloaded NSIS installer with the "runas" verb
// so Windows shows the UAC elevation prompt (the installer writes to Program
// Files). The caller (DownloadAndRunInstaller) quits wc3-forge right after
// this returns so the running .exe releases its lock — but ShellExecuteW is
// async, so the elevated installer races that shutdown. The installer does
// NOT rely on timing: project.nsi waits (bounded) for the old wc3-forge.exe
// to become deletable before its file-copy step, which is what actually
// closes the "Error opening file for writing" race.
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
