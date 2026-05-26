//go:build darwin

package main

// wc3InstallPathDefault is the canonical WC3 install root on macOS,
// used when the user hasn't set WC3FORGE_WC3_PATH. Matches the layout
// the macOS Battle.net client lays down.
const wc3InstallPathDefault = "/Applications/Warcraft III"
