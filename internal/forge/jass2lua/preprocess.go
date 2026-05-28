package jass2lua

// preprocess.go — vJASS textmacro preprocessor (Phase 1 of vJASS support).
//
// JassHelper supports a small "preprocessor" syntax embedded in `//!` line
// comments. The most important construct is the textmacro — a parameterized
// block of JASS that can be expanded at one or more call sites:
//
//	//! textmacro MyMacro takes A, B
//	    local integer $A$ = $B$
//	//! endtextmacro
//
//	//! runtextmacro MyMacro("foo", "0")     // → expands to: local integer foo = 0
//
// This file implements that preprocessor pass. After preprocessing, a map
// that ONLY used textmacros (no library/scope/struct/module) becomes pure
// JASS and flows through the existing TranspileScript pipeline unchanged.
// vJASS keywords that are NOT consumed by this pass (library, scope, struct,
// module, …) continue to block conversion (handled by Phases 2-4).
//
// Semantics implemented:
//   - `//! textmacro Name takes p1, p2, …` … `//! endtextmacro` — definition;
//     stripped from the output.
//   - `//! runtextmacro Name(arg1, arg2)` — expand verbatim; substitute
//     `$pN$` tokens in the body with the matching arg.
//   - `//! runtextmacroonce Name(...)` — same as runtextmacro but only fires
//     once per unique (name, args) tuple. Subsequent calls produce an empty
//     expansion + a comment noting the suppression.
//   - Recursive expansion: a macro body containing `//! runtextmacro Other(…)`
//     is expanded eagerly (bounded by a depth limit to catch self-recursion).
//   - Unknown `//!` directives (e.g. `//! i //…`, `//! external …`) become
//     pass-through comments + a non-fatal PreprocessError.
//
// Intentionally NOT implemented (out of scope for Phase 1; Phases 2-4):
//   - `//! import` / `//! zinc { … }` / `//! novjass { … }`
//   - JassHelper's `//! i` directive that injects literal JASS text into the
//     preprocess output — for now we just round-trip it as a comment.
//   - Conditional compilation (`//! if … //! endif`).

import (
	"fmt"
	"regexp"
	"strings"
)

// PreprocessResult is the output of running the textmacro preprocessor.
type PreprocessResult struct {
	Expanded string            // The source with all textmacros expanded.
	Macros   map[string]Macro  // Definitions seen (for debugging / inspection).
	Calls    []MacroCall       // Call sites (for diagnostics).
	Errors   []PreprocessError // Non-fatal issues (unknown macro, arg count mismatch, etc).
}

// Macro is one parsed textmacro definition.
type Macro struct {
	Name   string
	Params []string
	Body   string // The raw body bytes between textmacro/endtextmacro.
	Line   int    // Definition line in source (for error reporting).
}

// MacroCall is one observed runtextmacro / runtextmacroonce call site.
type MacroCall struct {
	Name string
	Args []string
	Line int
	Once bool // true for runtextmacroonce
}

// PreprocessError is a non-fatal preprocessor diagnostic (unknown macro,
// arg-count mismatch, unrecognized `//!` directive, etc.). Surfaced through
// PreprocessResult.Errors and propagated to the UI as a per-section warning.
type PreprocessError struct {
	Line    int
	Message string
}

// maxExpandDepth bounds runtextmacro recursion. JassHelper itself has no
// fixed limit, but real-world macros never recurse beyond a handful of
// levels. 32 is comfortable headroom while still catching self-recursion
// from a typo'd macro body.
const maxExpandDepth = 32

// Regex helpers.

// textmacroDef matches `//! textmacro <Name> [takes <p1>, <p2>, …]`.
// Leading whitespace is allowed before //!. Params are optional (some macros
// take none — the `takes` clause is omitted entirely in that case). We're
// permissive on the takes-list separator since JassHelper accepts both
// `a, b` and `a,b`.
var textmacroDefRe = regexp.MustCompile(`^\s*//!\s*textmacro\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:takes\s+(.+))?$`)

// endtextmacro is the terminator for a definition block.
var endtextmacroRe = regexp.MustCompile(`^\s*//!\s*endtextmacro\s*$`)

// runtextmacroCallRe matches both `//! runtextmacro Name(...)` and the
// `runtextmacroonce` variant. Group 1 is empty for the plain form, "once"
// for the once form.
var runtextmacroCallRe = regexp.MustCompile(`^\s*//!\s*runtextmacro(once)?\s+([A-Za-z_][A-Za-z0-9_]*)\s*\((.*)\)\s*$`)

// otherDirectiveRe matches any other `//!` line (so we can pass it through
// with a diagnostic rather than crashing the preprocessor). We deliberately
// don't try to parse `//! i`, `//! external`, etc. — they round-trip as
// comments and the user is told.
var otherDirectiveRe = regexp.MustCompile(`^\s*//!\s*(\S+)`)

// paramSubRe captures `$ident$` tokens inside a macro body for arg substitution.
// We intentionally restrict to identifier-shaped names — `$1$` literal
// (used in some old macros as a dummy) would NOT match and is left as-is.
var paramSubRe = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)\$`)

// Preprocess parses the JASS source for textmacro definitions + call sites
// and returns the source with all call sites expanded.
//
// Conservative on errors: unknown macros, arg-count mismatches, and
// malformed `//!` lines produce a comment placeholder and a PreprocessError,
// but processing continues for the rest of the file. This matches
// JassHelper's non-fatal behavior and keeps the diff view usable.
//
// runtextmacroonce tracks an internal "fired" set keyed by (name, joined-args).
// Subsequent matching calls produce an empty expansion + a comment noting
// the suppression.
func Preprocess(jass string) PreprocessResult {
	res := PreprocessResult{
		Macros: map[string]Macro{},
	}

	// Pass 1: scan for textmacro definitions. We strip them from the source
	// as we go so the remainder can be expanded without re-tripping the
	// definition matcher.
	stripped := extractDefinitions(jass, &res)

	// Pass 2: expand all call sites. runtextmacroonce dedupe lives here
	// (not in the recursive expander) so bodies referring to a once-macro
	// also honor the suppression.
	onceFired := map[string]bool{}
	expanded := expandCalls(stripped, res.Macros, onceFired, &res, 0)

	res.Expanded = expanded
	return res
}

// extractDefinitions consumes `//! textmacro Name [takes p, q]` … `//! endtextmacro`
// blocks from src, populates res.Macros, and returns the source with those
// blocks removed (replaced by a single blank line so line numbers downstream
// stay close to the original).
//
// Nested definitions are NOT supported (JassHelper itself forbids them at
// the same scope) — an inner `//! textmacro` inside a body is treated as
// part of the outer body. Maps in the wild don't nest definitions; the few
// that try are pathological.
func extractDefinitions(src string, res *PreprocessResult) string {
	var out strings.Builder
	lines := splitLinesKeepEnd(src)
	i := 0
	for i < len(lines) {
		line := lines[i]
		m := textmacroDefRe.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
		if m == nil {
			out.WriteString(line)
			i++
			continue
		}
		// Found a definition opener. Collect body until endtextmacro.
		name := m[1]
		var params []string
		if rawParams := strings.TrimSpace(m[2]); rawParams != "" {
			for _, p := range strings.Split(rawParams, ",") {
				if t := strings.TrimSpace(p); t != "" {
					params = append(params, t)
				}
			}
		}
		defLine := i + 1
		var body strings.Builder
		i++ // step past the opener
		closed := false
		for i < len(lines) {
			inner := lines[i]
			if endtextmacroRe.MatchString(strings.TrimRight(inner, "\r\n")) {
				closed = true
				i++ // consume the endtextmacro
				break
			}
			body.WriteString(inner)
			i++
		}
		if !closed {
			res.Errors = append(res.Errors, PreprocessError{
				Line:    defLine,
				Message: fmt.Sprintf("textmacro %q: missing //! endtextmacro before EOF", name),
			})
			// Fall back to keeping the body in the output so users can see
			// what went wrong; the source already accumulated into `body`.
		}
		res.Macros[name] = Macro{
			Name:   name,
			Params: params,
			Body:   body.String(),
			Line:   defLine,
		}
		// Emit a single blank line so subsequent line-number-based errors
		// still roughly align with the user's source view.
		out.WriteString("\n")
	}
	return out.String()
}

// expandCalls scans src for runtextmacro / runtextmacroonce call sites and
// substitutes the expansion in-place. depth bounds recursion (a macro body
// can contain runtextmacro lines that trigger further expansion).
func expandCalls(src string, macros map[string]Macro, onceFired map[string]bool, res *PreprocessResult, depth int) string {
	if depth > maxExpandDepth {
		res.Errors = append(res.Errors, PreprocessError{
			Line:    0,
			Message: fmt.Sprintf("textmacro expansion exceeded depth %d — likely infinite recursion", maxExpandDepth),
		})
		return src + "\n// jass2lua: textmacro recursion limit reached\n"
	}
	var out strings.Builder
	lines := splitLinesKeepEnd(src)
	for idx, line := range lines {
		trimmed := strings.TrimRight(line, "\r\n")
		// runtextmacro / runtextmacroonce.
		if m := runtextmacroCallRe.FindStringSubmatch(trimmed); m != nil {
			once := m[1] == "once"
			name := m[2]
			args := parseCallArgs(m[3])
			call := MacroCall{Name: name, Args: args, Line: idx + 1, Once: once}
			res.Calls = append(res.Calls, call)

			mac, ok := macros[name]
			if !ok {
				res.Errors = append(res.Errors, PreprocessError{
					Line:    idx + 1,
					Message: fmt.Sprintf("runtextmacro %q: unknown macro", name),
				})
				out.WriteString(fmt.Sprintf("// jass2lua: unknown textmacro %q\n", name))
				continue
			}
			if len(args) != len(mac.Params) {
				res.Errors = append(res.Errors, PreprocessError{
					Line:    idx + 1,
					Message: fmt.Sprintf("runtextmacro %q: arg count %d does not match param count %d", name, len(args), len(mac.Params)),
				})
				out.WriteString(fmt.Sprintf("// jass2lua: arg count mismatch for %q\n", name))
				continue
			}
			if once {
				key := name + "|" + strings.Join(args, "|")
				if onceFired[key] {
					out.WriteString(fmt.Sprintf("// runtextmacroonce suppressed: %s(%s)\n", name, strings.Join(args, ", ")))
					continue
				}
				onceFired[key] = true
			}
			body := substituteParams(mac.Body, mac.Params, args)
			// Recurse: the substituted body may itself contain runtextmacro
			// lines that need expanding.
			body = expandCalls(body, macros, onceFired, res, depth+1)
			out.WriteString(body)
			// Ensure the expansion ends with a newline so downstream tokens
			// don't run together with the next source line.
			if !strings.HasSuffix(body, "\n") {
				out.WriteByte('\n')
			}
			continue
		}
		// Any other `//!` directive (e.g. `//! i …`, `//! external …`) —
		// pass through as a regular comment + diagnostic.
		if dm := otherDirectiveRe.FindStringSubmatch(trimmed); dm != nil {
			// Filter out the directives the matcher already handled in the
			// definition-extraction pass (textmacro / endtextmacro) and our
			// own call directives (runtextmacro*).
			switch dm[1] {
			case "textmacro", "endtextmacro", "runtextmacro", "runtextmacroonce":
				// Should not happen after extractDefinitions stripped the
				// definitions — but if a stray opener slipped through (e.g.
				// missing endtextmacro that we left in the source), pass it
				// through silently.
				out.WriteString(line)
				continue
			}
			// Don't add an error for very common pragma-style directives —
			// they're informational, not blockers. We still log so the UI
			// can surface them if it wants.
			res.Errors = append(res.Errors, PreprocessError{
				Line:    idx + 1,
				Message: fmt.Sprintf("unknown //! directive %q — passed through as comment", dm[1]),
			})
			// Rewrite `//!` to `//` so the downstream tokenizer treats it
			// as a plain comment rather than potentially choking. Preserve
			// the trailing newline / EOL bytes verbatim.
			rewritten := strings.Replace(line, "//!", "//", 1)
			out.WriteString(rewritten)
			continue
		}
		out.WriteString(line)
	}
	return out.String()
}

// substituteParams walks body and replaces every `$paramN$` token with its
// bound arg. Args not in the params list (i.e. literal `$thing$` in the
// source that doesn't correspond to a parameter) are preserved verbatim —
// JassHelper has the same behavior, and some maps use bare `$` literals in
// strings that we shouldn't munge.
func substituteParams(body string, params []string, args []string) string {
	if len(params) == 0 {
		return body
	}
	bind := make(map[string]string, len(params))
	for i, p := range params {
		bind[p] = args[i]
	}
	return paramSubRe.ReplaceAllStringFunc(body, func(tok string) string {
		// tok includes the leading + trailing `$`.
		inner := tok[1 : len(tok)-1]
		if v, ok := bind[inner]; ok {
			return v
		}
		return tok
	})
}

// parseCallArgs splits the argument list inside `runtextmacro Name(...)` on
// commas, respecting double-quoted strings (so `"a,b"` stays as one arg).
// JassHelper's tokenizer is similarly lax — it does NOT honor parens or
// brackets inside args; macro args are always flat strings.
func parseCallArgs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var args []string
	var cur strings.Builder
	inStr := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c == '"' && (i == 0 || raw[i-1] != '\\'):
			inStr = !inStr
			cur.WriteByte(c)
		case c == ',' && !inStr:
			args = append(args, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 || len(args) > 0 {
		args = append(args, strings.TrimSpace(cur.String()))
	}
	// Strip surrounding double-quotes from each arg — JassHelper treats
	// `"foo"` and `foo` interchangeably for substitution, and stripping is
	// what produces the expected literal text in the expansion.
	for i, a := range args {
		if len(a) >= 2 && a[0] == '"' && a[len(a)-1] == '"' {
			args[i] = a[1 : len(a)-1]
		}
	}
	return args
}

// splitLinesKeepEnd splits s on '\n' boundaries, keeping the '\n' attached
// to each resulting line. This preserves byte-exact round-tripping for lines
// we pass through unchanged.
func splitLinesKeepEnd(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
