package jass2lua

import (
	"strings"
	"testing"
)

func TestPreprocess_NoMacros(t *testing.T) {
	src := `function Foo takes nothing returns nothing
    call BJDebugMsg("hi")
endfunction
`
	res := Preprocess(src)
	if res.Expanded != src {
		t.Errorf("expected passthrough; got:\n%s", res.Expanded)
	}
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %+v", res.Errors)
	}
	if len(res.Macros) != 0 {
		t.Errorf("unexpected macros: %+v", res.Macros)
	}
}

func TestPreprocess_NoParamMacro(t *testing.T) {
	src := `//! textmacro Hello
    call BJDebugMsg("hi")
//! endtextmacro

function Init takes nothing returns nothing
//! runtextmacro Hello()
endfunction
`
	res := Preprocess(src)
	if !strings.Contains(res.Expanded, `call BJDebugMsg("hi")`) {
		t.Errorf("expected expansion body, got:\n%s", res.Expanded)
	}
	if strings.Contains(res.Expanded, "//! textmacro") {
		t.Errorf("definition should be stripped, got:\n%s", res.Expanded)
	}
	if strings.Contains(res.Expanded, "//! runtextmacro") {
		t.Errorf("call site should be expanded, got:\n%s", res.Expanded)
	}
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %+v", res.Errors)
	}
}

func TestPreprocess_OneParam(t *testing.T) {
	src := `//! textmacro Greet takes WHO
    call BJDebugMsg("hello $WHO$")
//! endtextmacro

//! runtextmacro Greet("world")
`
	res := Preprocess(src)
	if !strings.Contains(res.Expanded, `call BJDebugMsg("hello world")`) {
		t.Errorf("expected param substitution, got:\n%s", res.Expanded)
	}
}

func TestPreprocess_MultipleParams(t *testing.T) {
	src := `//! textmacro Assign takes NAME, VAL
    local integer $NAME$ = $VAL$
//! endtextmacro

//! runtextmacro Assign("counter", "42")
`
	res := Preprocess(src)
	if !strings.Contains(res.Expanded, `local integer counter = 42`) {
		t.Errorf("expected `local integer counter = 42`, got:\n%s", res.Expanded)
	}
}

func TestPreprocess_RunOnce_SuppressesDuplicates(t *testing.T) {
	src := `//! textmacro Decl takes X
    local integer $X$ = 0
//! endtextmacro

//! runtextmacroonce Decl("a")
//! runtextmacroonce Decl("a")
`
	res := Preprocess(src)
	// First expands; second is suppressed (becomes a comment).
	count := strings.Count(res.Expanded, "local integer a = 0")
	if count != 1 {
		t.Errorf("expected exactly 1 expansion (got %d):\n%s", count, res.Expanded)
	}
	if !strings.Contains(res.Expanded, "runtextmacroonce suppressed") {
		t.Errorf("expected suppression comment, got:\n%s", res.Expanded)
	}
}

func TestPreprocess_RunOnce_DifferentArgsExpandBoth(t *testing.T) {
	src := `//! textmacro Decl takes X
    local integer $X$ = 0
//! endtextmacro

//! runtextmacroonce Decl("a")
//! runtextmacroonce Decl("b")
`
	res := Preprocess(src)
	if !strings.Contains(res.Expanded, "local integer a = 0") {
		t.Errorf("expected `a` expansion, got:\n%s", res.Expanded)
	}
	if !strings.Contains(res.Expanded, "local integer b = 0") {
		t.Errorf("expected `b` expansion, got:\n%s", res.Expanded)
	}
	if strings.Contains(res.Expanded, "suppressed") {
		t.Errorf("did not expect suppression, got:\n%s", res.Expanded)
	}
}

func TestPreprocess_NestedExpansion(t *testing.T) {
	src := `//! textmacro Inner takes WHO
    call BJDebugMsg("hi $WHO$")
//! endtextmacro

//! textmacro Outer takes NAME
//! runtextmacro Inner($NAME$)
//! endtextmacro

//! runtextmacro Outer("world")
`
	res := Preprocess(src)
	if !strings.Contains(res.Expanded, `call BJDebugMsg("hi world")`) {
		t.Errorf("expected nested expansion, got:\n%s", res.Expanded)
	}
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %+v", res.Errors)
	}
}

func TestPreprocess_InfiniteRecursionDetected(t *testing.T) {
	src := `//! textmacro Loopy
//! runtextmacro Loopy()
//! endtextmacro

//! runtextmacro Loopy()
`
	res := Preprocess(src)
	if len(res.Errors) == 0 {
		t.Fatalf("expected at least one error for infinite recursion")
	}
	foundDepth := false
	for _, e := range res.Errors {
		if strings.Contains(e.Message, "recursion") || strings.Contains(e.Message, "depth") {
			foundDepth = true
			break
		}
	}
	if !foundDepth {
		t.Errorf("expected recursion/depth error, got: %+v", res.Errors)
	}
}

func TestPreprocess_UnknownMacroIsNonFatal(t *testing.T) {
	src := `//! runtextmacro DoesNotExist(x)
function Foo takes nothing returns nothing
endfunction
`
	res := Preprocess(src)
	if len(res.Errors) == 0 {
		t.Fatalf("expected an error for unknown macro")
	}
	if !strings.Contains(res.Expanded, "unknown textmacro") {
		t.Errorf("expected unknown-textmacro placeholder, got:\n%s", res.Expanded)
	}
	// Subsequent code must still be present.
	if !strings.Contains(res.Expanded, "function Foo") {
		t.Errorf("subsequent source missing, got:\n%s", res.Expanded)
	}
}

func TestPreprocess_UnknownDirective_PassThrough(t *testing.T) {
	src := `//! i //some literal injection
function Foo takes nothing returns nothing
endfunction
`
	res := Preprocess(src)
	// `//!` rewritten to plain `//` comment — must not contain bare `//!`.
	if strings.Contains(res.Expanded, "//!") {
		t.Errorf("expected //! to be rewritten, got:\n%s", res.Expanded)
	}
	if !strings.Contains(res.Expanded, "function Foo") {
		t.Errorf("subsequent source missing, got:\n%s", res.Expanded)
	}
	// Should record a non-fatal diagnostic for the directive.
	if len(res.Errors) == 0 {
		t.Errorf("expected at least one non-fatal directive error")
	}
}

func TestPreprocess_LiteralDollarPreservedWhenNotParam(t *testing.T) {
	src := `//! textmacro Greet takes WHO
    call BJDebugMsg("hi $WHO$ — call $999 now")
//! endtextmacro

//! runtextmacro Greet("world")
`
	res := Preprocess(src)
	if !strings.Contains(res.Expanded, `call BJDebugMsg("hi world — call $999 now")`) {
		t.Errorf("expected $999 literal preserved, got:\n%s", res.Expanded)
	}
	// Also: `$undef$` shape (non-param dollar-delimited) — preserved verbatim.
	src2 := `//! textmacro X takes A
local integer $A$ = 0
local string s = "$NOTAPARAM$"
//! endtextmacro

//! runtextmacro X("foo")
`
	res2 := Preprocess(src2)
	if !strings.Contains(res2.Expanded, "$NOTAPARAM$") {
		t.Errorf("expected unmatched $NOTAPARAM$ preserved, got:\n%s", res2.Expanded)
	}
	if !strings.Contains(res2.Expanded, "local integer foo = 0") {
		t.Errorf("expected matched param substituted, got:\n%s", res2.Expanded)
	}
}

func TestPreprocess_LeadingWhitespaceAllowedOnDirective(t *testing.T) {
	src := `    //! textmacro Foo
        call BJDebugMsg("hi")
    //! endtextmacro

        //! runtextmacro Foo()
`
	res := Preprocess(src)
	if !strings.Contains(res.Expanded, `call BJDebugMsg("hi")`) {
		t.Errorf("expected expansion despite leading whitespace, got:\n%s", res.Expanded)
	}
	if strings.Contains(res.Expanded, "//! textmacro") {
		t.Errorf("definition should be stripped despite leading whitespace, got:\n%s", res.Expanded)
	}
}

func TestPreprocess_ArgCountMismatch_NonFatal(t *testing.T) {
	src := `//! textmacro Decl takes NAME, VALUE
    local integer $NAME$ = $VALUE$
//! endtextmacro

//! runtextmacro Decl("a")
`
	res := Preprocess(src)
	if len(res.Errors) == 0 {
		t.Fatalf("expected arg-count error")
	}
	if !strings.Contains(res.Expanded, "arg count mismatch") {
		t.Errorf("expected arg-mismatch placeholder, got:\n%s", res.Expanded)
	}
}

// Make sure expansion plays nicely with the downstream TranspileScript: a
// textmacro-only file should produce valid Lua, end-to-end.
func TestPreprocess_ThenTranspile_EndToEnd(t *testing.T) {
	src := `//! textmacro DeclInt takes NAME, VAL
    local integer $NAME$ = $VAL$
//! endtextmacro

function Init takes nothing returns nothing
//! runtextmacro DeclInt("a", "1")
//! runtextmacro DeclInt("b", "2")
    call BJDebugMsg("ready")
endfunction
`
	pp := Preprocess(src)
	if len(pp.Errors) != 0 {
		t.Fatalf("unexpected preprocess errors: %+v", pp.Errors)
	}
	lua, err := TranspileScript(pp.Expanded)
	if err != nil {
		t.Fatalf("transpile after preprocess failed: %v\nexpanded:\n%s", err, pp.Expanded)
	}
	if !strings.Contains(lua, "local a = 1") {
		t.Errorf("expected `local a = 1` in lua, got:\n%s", lua)
	}
	if !strings.Contains(lua, "local b = 2") {
		t.Errorf("expected `local b = 2` in lua, got:\n%s", lua)
	}
}

// FindVJASSKeyword should NOT trip on textmacro-family keywords after Phase 1
// (the preprocessor consumes them). Other vJASS keywords should still trip.
func TestFindVJASSKeyword_TextmacroFamilyIgnored(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		found bool
	}{
		{"runtextmacro_in_source", `function F takes nothing returns nothing
//! runtextmacro Foo()
endfunction`, false},
		{"library_still_blocks", `library X
endlibrary`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, found := FindVJASSKeyword(c.src)
			if found != c.found {
				t.Errorf("found=%v want=%v", found, c.found)
			}
		})
	}
}
