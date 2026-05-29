//go:build windows

package wc3launch

// binaryRelPath is the install-root-relative path to the launchable
// executable. filepath.Join in BinaryPath() prepends wc3InstallRoot().
var binaryRelPath = []string{"_retail_", "x86_64", "Warcraft III.exe"}
