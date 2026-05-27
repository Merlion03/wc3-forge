package wtg

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// TriggerData is the parsed in-memory view of UI/TriggerData.txt and
// UI/TriggerStrings.txt. The wtg parser uses the ArgumentCounts table for
// ECA / Parameter parsing — argc per function name doesn't live in the .wtg
// binary, only in this txt file (the load-bearing fact behind the embedded
// snapshot strategy).
//
// The Sections map keeps the raw row-per-section view so callers (the MCP
// surface, future code generator) can introspect every key without us having
// to grow the type per kind. The companion `_<name>_<key>` keys live in the
// SAME section as their function — TriggerData.txt's convention is one section
// per function family (Events / Conditions / Actions / Calls / Params / …),
// and the per-function metadata sits inline at point-of-definition.
type TriggerData struct {
	// Sections is the raw "[Section]" → {key: comma-separated-values} map.
	// Both function definitions ("CreateUnit") AND their metadata companions
	// ("_CreateUnit_DisplayName") share the section they were defined in;
	// callers branch on the leading underscore.
	//
	// Values are the COMMA-separated tokens of the right-hand side, with
	// quoted strings unwrapped. Empty trailing values are preserved so the
	// _Foo_Parameters template's "~Arg," " literal" sequences survive
	// intact. (The split is comma-aware but ignores commas inside double-
	// quoted strings.)
	Sections map[string]map[string][]string

	// SectionOrder preserves the order sections appeared in the source file
	// so callers that iterate by section get a deterministic walk (used by
	// debug dumps + by registerObjectKind-style enumeration if it ever lands).
	SectionOrder []string

	// ArgumentCounts is name → argc derived per HiveWE's algorithm
	// (count non-empty / non-numeric / non-"nothing" tokens; subtract 1 from
	// [TriggerCalls] because col 1 there is the return type).
	//
	// MUST be populated for EVERY function the wtg parser might encounter —
	// missing key triggers a hard error in Parse rather than silent argc=0
	// (a silent zero would misalign every subsequent ECA in the file; see
	// the report's Risk #3).
	ArgumentCounts map[string]int

	// TriggerStrings is the parsed UI/TriggerStrings.txt — function-name →
	// hint text mapping for hover tooltips. Optional in 1a (the read-only
	// viewer doesn't surface tooltips yet); load_version_31 doesn't depend
	// on it. Kept here so the Phase 1b cross-ref work doesn't need a second
	// loader.
	TriggerStrings map[string]string
}

// ParseTriggerData decodes UI/TriggerData.txt into a *TriggerData with the
// ArgumentCounts table populated per HiveWE's algorithm
// (Triggers::load in triggers.ixx L546-573). Returns an error only on a true
// I/O / scan failure — unknown sections / malformed lines are silently
// tolerated, matching the World Editor's permissive behavior.
func ParseTriggerData(data []byte) (*TriggerData, error) {
	td := &TriggerData{
		Sections:       map[string]map[string][]string{},
		ArgumentCounts: map[string]int{},
		TriggerStrings: map[string]string{},
	}
	if err := td.loadInto(data); err != nil {
		return nil, err
	}
	td.computeArgumentCounts()
	return td, nil
}

// LoadTriggerStrings parses UI/TriggerStrings.txt and folds the function-name
// → hint mapping into td.TriggerStrings. Sections in that file are
// [TriggerEventStrings] / [TriggerConditionStrings] / [TriggerActionStrings] /
// [TriggerCallStrings]; the leaf key is either `FunctionName=Hint` or
// `FunctionNameHint="..."`. We collapse the trailing `Hint` suffix variant
// onto the function name so a single lookup serves both.
func (td *TriggerData) LoadTriggerStrings(data []byte) error {
	// Reuse the same INI scanner; the file shape is identical (sections + k=v).
	raw := map[string]map[string][]string{}
	tmp := &TriggerData{Sections: raw, ArgumentCounts: map[string]int{}, TriggerStrings: map[string]string{}}
	if err := tmp.loadInto(data); err != nil {
		return err
	}
	for _, section := range raw {
		for k, v := range section {
			if len(v) == 0 {
				continue
			}
			// The hint shape is a single string value (no comma-split needed
			// from the caller's perspective); rejoin if the parser split it.
			value := joinValues(v)
			// Collapse "FooHint" → "Foo" so a single lookup serves either
			// shape. Functions don't end in "Hint" so the suffix is safe to
			// drop unconditionally.
			name := strings.TrimSuffix(k, "Hint")
			td.TriggerStrings[name] = value
		}
	}
	return nil
}

// loadInto is the shared INI-style scanner. Section detection is `[name]`;
// k=v lines are split on the first `=`. Quoted values keep the quotes off.
// Comments (`//`) and blank lines are skipped.
func (td *TriggerData) loadInto(data []byte) error {
	// UTF-8 BOM strip — WC3 INIs ship one on the first line.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	var current map[string][]string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "//") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.TrimSpace(line[1 : len(line)-1])
			if name == "" {
				current = nil
				continue
			}
			if existing, ok := td.Sections[name]; ok {
				current = existing
			} else {
				current = map[string][]string{}
				td.Sections[name] = current
				td.SectionOrder = append(td.SectionOrder, name)
			}
			continue
		}
		if current == nil {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		raw := line[eq+1:]
		// Strip trailing inline comments — TriggerData.txt has a few
		// `Foo=1,2 // note` lines. The split is naive (no quoted-`//`
		// support), but the file in practice doesn't put `//` inside
		// values.
		if i := strings.Index(raw, "//"); i >= 0 {
			raw = raw[:i]
		}
		raw = strings.TrimSpace(raw)
		current[key] = splitCSV(raw)
	}
	return sc.Err()
}

// computeArgumentCounts mirrors HiveWE's Triggers::load L556-572: walk every
// [TriggerActions] / [TriggerEvents] / [TriggerConditions] / [TriggerCalls]
// entry whose key doesn't start with `_`, count non-empty / non-numeric /
// non-"nothing" tokens. Subtract 1 from [TriggerCalls] because col 1 there is
// the return type.
//
// CRITICAL: this must match HiveWE's count exactly or the wtg parser will
// silently misalign on real maps. The per-section short-circuit is the only
// asymmetry; everything else is one shared filter.
func (td *TriggerData) computeArgumentCounts() {
	for _, sect := range []string{"TriggerActions", "TriggerEvents", "TriggerConditions", "TriggerCalls"} {
		rows, ok := td.Sections[sect]
		if !ok {
			continue
		}
		for key, tokens := range rows {
			if len(key) > 0 && key[0] == '_' {
				continue // companion meta-key, not a function definition
			}
			count := 0
			for _, t := range tokens {
				if t == "" {
					continue
				}
				if t == "nothing" {
					continue
				}
				if isNumber(t) {
					continue
				}
				count++
			}
			if sect == "TriggerCalls" {
				count-- // the return type column was counted; HiveWE drops it
			}
			if count < 0 {
				count = 0
			}
			td.ArgumentCounts[key] = count
		}
	}
}

// isNumber reports whether s parses as an integer OR a float literal —
// matches HiveWE's is_number helper, which accepts both forms (TriggerData
// argc columns hold version numbers like "0" or "1" but also limits like
// "0.5"). The strconv path is fine for the ~10k call sites in production.
func isNumber(s string) bool {
	if s == "" {
		return false
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	return false
}

// splitCSV breaks a TriggerData.txt right-hand side into its
// comma-separated tokens, respecting double-quoted strings (quotes
// stripped from the returned value). Empty trailing tokens are preserved
// — they're load-bearing for _Foo_Parameters templates whose final
// segment is a literal string after a placeholder.
//
// The format is permissive: `"a, b",c` → ["a, b", "c"]; `"`-escapes are
// not supported (TriggerData.txt doesn't use them).
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var buf strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			// Drop the quote character itself; HiveWE's parser does the same
			// (the std::string value never contains the surrounding quotes).
		case c == ',' && !inQuote:
			out = append(out, strings.TrimSpace(buf.String()))
			buf.Reset()
		default:
			buf.WriteByte(c)
		}
	}
	out = append(out, strings.TrimSpace(buf.String()))
	return out
}

// joinValues rejoins a previously-split token list into the original CSV
// representation for callers that only want the right-hand-side as one
// string (TriggerStrings hints, for example).
func joinValues(v []string) string {
	return strings.Join(v, ",")
}

// Argc returns the argument count for the given function name, or (0, false)
// if the name is not present in the loaded TriggerData. Callers that must
// distinguish "no such function" from "function with zero args" should branch
// on the boolean — the wtg parser does.
func (td *TriggerData) Argc(name string) (int, bool) {
	n, ok := td.ArgumentCounts[name]
	return n, ok
}
