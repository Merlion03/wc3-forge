// Package wts parses war3map.wts — WC3's trigger-strings table.
//
// Format is a plain UTF-8 text file (often with a BOM), structured as a
// sequence of named blocks:
//
//	STRING 9661
//	{
//	Just another Warcraft III map
//	}
//
//	STRING 9664  // optional trailing comment
//	{
//	Map Author
//	}
//
// Most modern editors store map metadata (name, author, description, force
// names, player names, etc.) as integer IDs and stash the actual display
// strings here. The w3i parser surfaces those IDs as "TRIGSTR_<n>" strings;
// callers can pass them through [Strings.Resolve] to get the display value.
//
// Strings may span multiple lines. Comments outside string blocks (lines
// starting with "//") are ignored. Trailing whitespace on the closing "}"
// line is allowed.
package wts

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Strings maps a STRING id to its display value. Multi-line values keep
// their interior newlines verbatim (no normalization).
type Strings map[uint32]string

var (
	stringHeaderRe = regexp.MustCompile(`^STRING\s+(\d+)`)
	trigstrRe      = regexp.MustCompile(`^TRIGSTR_(\d+)$`)
	// WC3 inline color codes: opening |cAARRGGBB and closing |r.
	// Case-insensitive on the hex digits; Reforged maps mix cases.
	colorCodeRe = regexp.MustCompile(`\|[cC][0-9a-fA-F]{8}|\|[rR]`)
)

// Parse decodes a war3map.wts file. Returns an empty map (not nil) if the
// file is well-formed but contains no STRING blocks.
func Parse(data []byte) (Strings, error) {
	// Strip UTF-8 BOM if present — many editors prepend it.
	if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
		data = data[3:]
	}

	out := Strings{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Some maps have very long description blocks; bump the buffer cap.
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var (
		currentID         uint32
		inString          bool
		awaitingOpenBrace bool
		body              strings.Builder
	)

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if !inString {
			if awaitingOpenBrace {
				if strings.HasPrefix(trimmed, "{") {
					inString = true
					awaitingOpenBrace = false
					body.Reset()
				}
				// Tolerate stray text between STRING header and "{".
				continue
			}
			if m := stringHeaderRe.FindStringSubmatch(trimmed); m != nil {
				id, err := strconv.ParseUint(m[1], 10, 32)
				if err != nil {
					continue
				}
				currentID = uint32(id)
				awaitingOpenBrace = true
			}
			continue
		}

		// Inside a string block.
		if trimmed == "}" || strings.HasPrefix(trimmed, "}") {
			out[currentID] = body.String()
			inString = false
			continue
		}
		if body.Len() > 0 {
			body.WriteByte('\n')
		}
		body.WriteString(line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan wts: %w", err)
	}
	return out, nil
}

// StripColorCodes removes inline WC3 color formatting (|cAARRGGBB ... |r)
// from a string. The game engine renders these as colored runs; in a plain
// text UI they're noise.
func StripColorCodes(s string) string {
	return colorCodeRe.ReplaceAllString(s, "")
}

// Display is the convenience the UI typically wants: resolve the TRIGSTR
// reference (if any), strip WC3 color codes, trim surrounding whitespace.
// All three transforms are no-ops for inputs that don't need them.
func (s Strings) Display(ref string) string {
	return strings.TrimSpace(StripColorCodes(s.Resolve(ref)))
}

// Resolve substitutes a TRIGSTR_<n> reference with its display value from
// the table. Strings that don't match the TRIGSTR pattern are returned
// unchanged — safe to call on any field whose origin you don't know.
// If the referenced id is missing from the table, returns ref unchanged
// rather than empty (so the missing id is still visible in the UI).
//
// A nil receiver is also safe — returns ref unchanged.
func (s Strings) Resolve(ref string) string {
	if s == nil {
		return ref
	}
	m := trigstrRe.FindStringSubmatch(ref)
	if m == nil {
		return ref
	}
	id, err := strconv.ParseUint(m[1], 10, 32)
	if err != nil {
		return ref
	}
	if v, ok := s[uint32(id)]; ok {
		return v
	}
	return ref
}
