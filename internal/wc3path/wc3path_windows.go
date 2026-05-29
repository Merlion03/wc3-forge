//go:build windows

package wc3path

// defaultInstallPath is Battle.net's default Reforged install location on
// Windows.
func defaultInstallPath() string {
	return `C:\Program Files (x86)\Warcraft III`
}
