//go:build windows

package wc3launch

// wc3InstallRootDefault is the canonical WC3 install root on this OS.
const wc3InstallRootDefault = `C:\Program Files (x86)\Warcraft III`

// binaryRelPath is the install-root-relative path to the launchable
// executable. filepath.Join in BinaryPath() prepends wc3InstallRoot().
var binaryRelPath = []string{"_retail_", "x86_64", "Warcraft III.exe"}
