package jass2lua

import (
	"strings"
	"testing"
)

// Golden-file style: each test pairs a JASS input with the expected Lua. We
// compare line-by-line after trimming trailing whitespace and skipping
// blank lines so the assertions are robust to minor formatting drift.

func normalize(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		l := strings.TrimRight(line, " \t")
		if l == "" {
			continue
		}
		out = append(out, l)
	}
	return out
}

func assertLuaEqual(t *testing.T, got, want string) {
	t.Helper()
	g := normalize(got)
	w := normalize(want)
	if len(g) != len(w) {
		t.Errorf("line count mismatch: got=%d want=%d\n--- got ---\n%s\n--- want ---\n%s", len(g), len(w), got, want)
		return
	}
	for i := range g {
		if g[i] != w[i] {
			t.Errorf("line %d mismatch:\n  got:  %q\n  want: %q\n--- full got ---\n%s", i+1, g[i], w[i], got)
		}
	}
}

func TestTranspile_MinimalFunction(t *testing.T) {
	jass := `function Init takes nothing returns nothing
    local integer i = 0
    loop
        exitwhen i >= 10
        if i == 5 then
            call BJDebugMsg("hit five")
        elseif i == 7 then
            call BJDebugMsg("hit seven")
        else
            call BJDebugMsg("other")
        endif
        set i = i + 1
    endloop
endfunction
`
	want := `function Init()
	local i = 0
	while true do
		if i >= 10 then break end
		if i == 5 then
			BJDebugMsg("hit five")
		elseif i == 7 then
			BJDebugMsg("hit seven")
		else
			BJDebugMsg("other")
		end
		i = i + 1
	end
end
`
	got, err := TranspileScript(jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	assertLuaEqual(t, got, want)
}

func TestTranspile_GlobalsBlock(t *testing.T) {
	jass := `globals
    integer udg_count = 5
    real udg_pi = 3.14
    string udg_name = "hero"
    boolean udg_flag = true
    code udg_callback = null
    unit udg_player_unit = null
    integer array udg_scores
endglobals
`
	want := `udg_count = 5
udg_pi = 3.14
udg_name = "hero"
udg_flag = true
udg_callback = nil
udg_player_unit = nil
udg_scores = __jarray(0)
`
	got, err := TranspileScript(jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	assertLuaEqual(t, got, want)
}

func TestTranspile_NothingNothing(t *testing.T) {
	jass := `function Empty takes nothing returns nothing
endfunction
`
	got, err := TranspileScript(jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	want := `function Empty()
end
`
	assertLuaEqual(t, got, want)
}

func TestTranspile_MultipleParams(t *testing.T) {
	jass := `function Add takes integer a, integer b returns integer
    return a + b
endfunction
`
	got, err := TranspileScript(jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	want := `function Add(a, b)
	return a + b
end
`
	assertLuaEqual(t, got, want)
}

func TestTranspile_StringConcat(t *testing.T) {
	jass := `function S takes nothing returns string
    return "foo" + "bar"
endfunction
`
	got, err := TranspileScript(jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, `"foo" .. "bar"`) {
		t.Errorf("expected `..` for string concat, got: %s", got)
	}
}

func TestTranspile_FourCC(t *testing.T) {
	jass := `function Spawn takes nothing returns nothing
    call CreateUnit(Player(0), 'hpea', 0., 0., 0.)
endfunction
`
	got, err := TranspileScript(jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, `FourCC("hpea")`) {
		t.Errorf("expected FourCC literal, got: %s", got)
	}
}

func TestTranspile_NativeAsComment(t *testing.T) {
	jass := `native CreateUnit takes player whichPlayer, integer unitid, real x, real y, real face returns unit
`
	got, err := TranspileScript(jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(got), "-- native") {
		t.Errorf("expected native as comment, got: %s", got)
	}
}

func TestTranspile_HexAndRealLiterals(t *testing.T) {
	jass := `function Lits takes nothing returns nothing
    local integer h1 = $1A2B
    local integer h2 = 0xFF
    local integer d = 42
    local real r1 = 1.5
    local real r2 = .5
endfunction
`
	got, err := TranspileScript(jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	wantSub := []string{"0x1A2B", "0xFF", "42", "1.5", ".5"}
	for _, w := range wantSub {
		if !strings.Contains(got, w) {
			t.Errorf("expected %q in output, got: %s", w, got)
		}
	}
}

func TestTranspile_BareReturn(t *testing.T) {
	jass := `function Bail takes nothing returns nothing
    return
endfunction
`
	got, err := TranspileScript(jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	want := `function Bail()
	return
end
`
	assertLuaEqual(t, got, want)
}

func TestTranspile_BoolOps(t *testing.T) {
	jass := `function B takes boolean a, boolean b returns boolean
    return not a and (a or b)
endfunction
`
	got, err := TranspileScript(jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "not ") || !strings.Contains(got, " and ") || !strings.Contains(got, " or ") {
		t.Errorf("expected `not`/`and`/`or` operators, got: %s", got)
	}
}

func TestTranspile_NestedIfInLoop(t *testing.T) {
	jass := `function N takes nothing returns nothing
    local integer i = 0
    loop
        exitwhen i > 100
        if i == 50 then
            call BJDebugMsg("halfway")
        endif
        set i = i + 1
    endloop
endfunction
`
	got, err := TranspileScript(jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	// Just sanity-check the key landmarks.
	wantSub := []string{"while true do", "if i > 100 then break end", "if i == 50 then", "BJDebugMsg", "end", "end", "end"}
	pos := 0
	for _, w := range wantSub {
		idx := strings.Index(got[pos:], w)
		if idx < 0 {
			t.Errorf("expected %q after pos %d in: %s", w, pos, got)
			return
		}
		pos += idx + len(w)
	}
}

func TestTranspile_CommentPreservation(t *testing.T) {
	jass := `// top-level comment
function F takes nothing returns nothing
    // inside comment
    call DoNothing()
endfunction
`
	got, err := TranspileScript(jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "-- top-level comment") {
		t.Errorf("expected top-level comment, got: %s", got)
	}
	if !strings.Contains(got, "-- inside comment") {
		t.Errorf("expected inside comment, got: %s", got)
	}
}

func TestTranspile_NeqOperator(t *testing.T) {
	jass := `function NEQ takes integer x returns boolean
    return x != 5
endfunction
`
	got, err := TranspileScript(jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "x ~= 5") {
		t.Errorf("expected `~=` operator, got: %s", got)
	}
}

func TestTranspile_FunctionLiteral(t *testing.T) {
	jass := `function Hook takes nothing returns nothing
    call TriggerAddAction(t, function MyAction)
endfunction
`
	got, err := TranspileScript(jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "TriggerAddAction(t, MyAction)") {
		t.Errorf("expected bare function reference, got: %s", got)
	}
}

func TestTranspile_ArrayIndexAssign(t *testing.T) {
	jass := `function A takes nothing returns nothing
    set udg_arr[3] = 10
endfunction
`
	got, err := TranspileScript(jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "udg_arr[3] = 10") {
		t.Errorf("expected array assignment, got: %s", got)
	}
}

// TestTranspileFunction_BodyOnly checks the auto-wrap path used for per-trigger
// custom_text blocks that are just statements.
func TestTranspileFunction_BodyOnly(t *testing.T) {
	jass := `call BJDebugMsg("hello")
call DoNothing()
`
	got, err := TranspileFunction("CustomScript", jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	want := `function CustomScript()
	BJDebugMsg("hello")
	DoNothing()
end
`
	assertLuaEqual(t, got, want)
}

// TestTranspileFunction_FullScript checks that a full-script input bypasses
// the auto-wrap.
func TestTranspileFunction_FullScript(t *testing.T) {
	jass := `function Already takes nothing returns nothing
    call DoNothing()
endfunction
`
	got, err := TranspileFunction("ignored", jass)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "function Already()") {
		t.Errorf("expected full function decl, got: %s", got)
	}
	if strings.Contains(got, "function ignored()") {
		t.Errorf("auto-wrap should not fire when full function decl present, got: %s", got)
	}
}

// TestTranspileScriptWithErrors_SurfacesParseErrors is the P0#1 regression at
// the transpiler level: a script with multiple unparseable top-level
// statements recovers each into a RawStmt/error() marker AND reports each one
// in the returned parseErrors slice. Previously TranspileScript discarded
// File.Errors, so callers under-reported the failure count to 0.
func TestTranspileScriptWithErrors_SurfacesParseErrors(t *testing.T) {
	// Three bare statements at top level — the top-level parser rejects each
	// (`call`/`set`/`if` aren't valid file-scope decls), recovering each into
	// an error() marker.
	jass := "call DoNothing()\nset x = 1\nif y then\nendif\n"
	lua, parseErrs, lexErr := TranspileScriptWithErrors(jass)
	if lexErr != nil {
		t.Fatalf("unexpected lex error: %v", lexErr)
	}
	inlineCount := strings.Count(lua, "error(")
	if inlineCount == 0 {
		t.Fatalf("expected inline error() markers in output, got none:\n%s", lua)
	}
	if len(parseErrs) == 0 {
		t.Fatalf("expected non-zero parseErrors, got 0 (under-reporting bug)")
	}
	if len(parseErrs) != inlineCount {
		t.Errorf("parseErrors count (%d) should match inline error() count (%d)\nlua:\n%s\nerrs: %v",
			len(parseErrs), inlineCount, lua, parseErrs)
	}
}

// TestTranspileScriptWithErrors_CleanScriptZeroErrors confirms a well-formed
// script reports zero errors (no false positives).
func TestTranspileScriptWithErrors_CleanScriptZeroErrors(t *testing.T) {
	jass := "function F takes nothing returns nothing\n    call DoNothing()\nendfunction\n"
	lua, parseErrs, lexErr := TranspileScriptWithErrors(jass)
	if lexErr != nil {
		t.Fatalf("unexpected lex error: %v", lexErr)
	}
	if len(parseErrs) != 0 {
		t.Errorf("expected zero parseErrors for clean script, got %d: %v", len(parseErrs), parseErrs)
	}
	if strings.Contains(lua, "error(") {
		t.Errorf("clean script should have no inline error() markers, got:\n%s", lua)
	}
}

// TestTranspileFunctionWithErrors_BareStatementBodyClean is the P0#2 regression:
// a bare-statement body (top-level if/call/set, no `function`/`globals`) must
// route through the body-fragment parser and transpile to CLEAN Lua with ZERO
// errors. Previously, if such a body reached the top-level parser it produced
// a wall of error() (one per line).
func TestTranspileFunctionWithErrors_BareStatementBodyClean(t *testing.T) {
	jass := "if x then\n    call F()\n    set y = 1\nendif\n"
	lua, parseErrs, lexErr := TranspileFunctionWithErrors("Trig", jass)
	if lexErr != nil {
		t.Fatalf("unexpected lex error: %v", lexErr)
	}
	if len(parseErrs) != 0 {
		t.Fatalf("expected zero parse errors for bare-statement body, got %d: %v\nlua:\n%s", len(parseErrs), parseErrs, lua)
	}
	if strings.Contains(lua, "error(") {
		t.Errorf("bare-statement body should transpile clean (no error() markers), got:\n%s", lua)
	}
	for _, want := range []string{"function Trig()", "if x then", "F()", "y = 1", "end"} {
		if !strings.Contains(lua, want) {
			t.Errorf("expected %q in transpiled Lua, got:\n%s", want, lua)
		}
	}
}

// TestHasTopLevelDecl confirms the routing predicate the convert orchestrator
// uses to distinguish full scripts from bare-statement bodies.
func TestHasTopLevelDecl(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{"bare statements", "if x then\ncall F()\nendif\n", false},
		{"bare call", "call DoNothing()\n", false},
		{"function decl", "function F takes nothing returns nothing\nendfunction\n", true},
		{"globals block", "globals\n    integer x = 0\nendglobals\n", true},
		{"native decl", "native Foo takes nothing returns nothing\n", true},
		// hasTopLevelFunction trims leading whitespace per line, so an indented
		// `function` still counts (intentional — JASS files don't indent decls).
		{"indented function still counts", "    function F takes nothing returns nothing\nendfunction\n", true},
		{"keyword as substring not matched", "callback()\nsetters = 1\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HasTopLevelDecl(c.src); got != c.want {
				t.Errorf("HasTopLevelDecl(%q) = %v, want %v", c.src, got, c.want)
			}
		})
	}
}

// TestFindVJASSKeyword exercises the vJASS detector against representative
// inputs. After Phase 4 the keyword list is empty — every known vJASS
// construct has a preprocessor pass and no source-level keyword should trip
// the gate. The remaining tests confirm the gate stays quiet.
func TestFindVJASSKeyword(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		match string
		found bool
	}{
		{"pure_jass", `function Foo takes nothing returns nothing
endfunction`, "", false},
		// library / scope / private / requires NO LONGER block after Phase 2.
		// They're consumed by PreprocessLibScope before the keyword scan runs.
		{"library_no_longer_blocks", `library Foo
    function bar takes nothing returns nothing
    endfunction
endlibrary`, "", false},
		{"scope_no_longer_blocks", `scope MyScope
endscope`, "", false},
		{"private_keyword_no_longer_blocks", `private function helper takes nothing returns nothing
endfunction`, "", false},
		{"requires_no_longer_blocks", `library Foo requires Bar
endlibrary`, "", false},
		// module / interface / define no longer block after Phase 4 — they
		// have their own preprocessors that consume them before the gate.
		{"module_no_longer_blocks", `module M
endmodule`, "", false},
		{"interface_no_longer_blocks", `interface I
endinterface`, "", false},
		{"textmacro", `//! textmacro FOO takes BAR
//! endtextmacro`, "", false}, // textmacro lives in a comment; correct false
		// Quoted "struct" / "module" should NOT trip.
		{"quoted_module", `function Q takes nothing returns nothing
    call BJDebugMsg("module Foo")
endfunction`, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			match, found := FindVJASSKeyword(c.src)
			if found != c.found {
				t.Errorf("found=%v want=%v (match=%q)", found, c.found, match)
				return
			}
			if found && match != c.match {
				t.Errorf("matched %q want %q", match, c.match)
			}
		})
	}
}
