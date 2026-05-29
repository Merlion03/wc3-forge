package jass2lua

import (
	"strings"
	"testing"
)

// TestEmit_Ternary — `cond ? a : b` lowers to Lua's `(cond and a) or b`.
func TestEmit_Ternary(t *testing.T) {
	src := `function F takes nothing returns integer
return x ? 1 : 2
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "(x) and (1)") || !strings.Contains(got, "or (2)") {
		t.Errorf("expected `(x) and (1) or (2)` ternary lowering, got:\n%s", got)
	}
}

// TestEmit_TernaryFalseCaveat — if the then-arm looks boolean, the emitter
// appends an inline caveat comment about the false/nil unreliability.
func TestEmit_TernaryFalseCaveat(t *testing.T) {
	// `x > 0 ? a > 0 : a < 0` — then-arm is a comparison ⇒ caveat expected.
	src := `function F takes nothing returns boolean
return x > 0 ? a > 0 : a < 0
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "ternary:") {
		t.Errorf("expected ternary caveat comment for boolean-then, got:\n%s", got)
	}
}

// TestEmit_BitwiseOr — `a | b` emits as Lua 5.3+ `a | b` (pass-through).
func TestEmit_BitwiseOr(t *testing.T) {
	src := `function F takes nothing returns integer
return a | b
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, " | ") {
		t.Errorf("expected `|` operator, got:\n%s", got)
	}
}

// TestEmit_BitwiseAnd — `a & b` emits as Lua 5.3+ `a & b`.
func TestEmit_BitwiseAnd(t *testing.T) {
	src := `function F takes nothing returns integer
return a & b
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, " & ") {
		t.Errorf("expected `&` operator, got:\n%s", got)
	}
}

// TestEmit_BitwiseXor — JASS `^` (vJASS extension) → Lua `~` for binary
// position (Lua uses `^` for exponentiation, not XOR).
func TestEmit_BitwiseXor(t *testing.T) {
	src := `function F takes nothing returns integer
return a ^ b
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, " ~ ") {
		t.Errorf("expected `~` (Lua bitwise xor), got:\n%s", got)
	}
}

// TestEmit_BitwiseShift — `<<` / `>>` emit as Lua 5.3+ pass-through.
func TestEmit_BitwiseShift(t *testing.T) {
	src := `function F takes nothing returns integer
return (x << 2) + (y >> 1)
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "<<") || !strings.Contains(got, ">>") {
		t.Errorf("expected shift operators, got:\n%s", got)
	}
}

// TestEmit_UnaryBitNot — `~x` emits as Lua `~x`.
func TestEmit_UnaryBitNot(t *testing.T) {
	src := `function F takes nothing returns integer
return ~x
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "~x") {
		t.Errorf("expected unary `~x`, got:\n%s", got)
	}
}

// TestEmit_NULSourceStillTranspiles — confirms that a NUL-bearing source
// flows through the transpile pipeline without erroring.
func TestEmit_NULSourceStillTranspiles(t *testing.T) {
	src := "function Foo \x00 takes nothing returns nothing\n\x00\nendfunction\n"
	lua, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile must not error on NUL source: %v", err)
	}
	if !strings.Contains(lua, "function Foo()") {
		t.Errorf("expected Foo() in output, got:\n%s", lua)
	}
}


// ---------------------------------------------------------------------------
// P1#3 — integer division: JASS `/` between two integers truncates toward
// zero. Emitted as R2I(a / b) (R2I truncates toward zero exactly like JASS,
// unlike Lua `//` which floors toward -inf). Mixed int/real stays float `/`.
// ---------------------------------------------------------------------------

// TestEmit_IntDiv_FearModulo is the exact failing function from the analysis:
//
//	function FearSystem__modulo takes integer a, integer b returns integer
//	    return a - ( a / b ) * b
//
// Before the fix this emitted `a - ((a / b) * b)` which returns 0.0 for ALL
// inputs (float division). After the fix the inner integer `a / b` becomes
// `R2I(a / b)`, restoring the remainder semantics.
func TestEmit_IntDiv_FearModulo(t *testing.T) {
	src := `function FearSystem__modulo takes integer a,integer b returns integer
return a - ( a / b ) * b
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "R2I(a / b)") {
		t.Errorf("expected integer division wrapped as R2I(a / b), got:\n%s", got)
	}
	// Document/verify the runtime semantics the lowering targets: R2I truncates
	// toward zero, so `a - R2I(a/b)*b` is the true JASS remainder.
	luaModulo := func(a, b int) int {
		q := truncTowardZero(a, b) // == R2I(a / b)
		return a - q*b
	}
	if v := luaModulo(7, 2); v != 1 {
		t.Errorf("FearSystem__modulo(7,2): want 1, got %d", v)
	}
	if v := luaModulo(-7, 2); v != -1 {
		t.Errorf("FearSystem__modulo(-7,2): want -1 (trunc toward zero), got %d", v)
	}
}

// truncTowardZero mirrors WC3-Lua R2I(a / b): float-divide then truncate toward
// zero. Go's integer `/` already truncates toward zero, matching R2I exactly.
func truncTowardZero(a, b int) int { return a / b }

// TestEmit_IntDiv_RealStaysFloat — a division with a real operand must NOT be
// wrapped in R2I; it stays Lua float `/`.
func TestEmit_IntDiv_RealStaysFloat(t *testing.T) {
	src := `function F takes real x, integer n returns real
return x / n
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if strings.Contains(got, "R2I(") {
		t.Errorf("real-typed division must stay float (no R2I), got:\n%s", got)
	}
	if !strings.Contains(got, "x / n") {
		t.Errorf("expected float `x / n`, got:\n%s", got)
	}
}

// TestEmit_IntDiv_RealLiteralStaysFloat — two real literals divide as float.
func TestEmit_IntDiv_RealLiteralStaysFloat(t *testing.T) {
	src := `function F takes nothing returns real
return 7.0 / 2.0
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if strings.Contains(got, "R2I(") {
		t.Errorf("real-literal division must stay float, got:\n%s", got)
	}
}

// TestEmit_IntDiv_IntLiterals — two integer literals divide with truncation.
func TestEmit_IntDiv_IntLiterals(t *testing.T) {
	src := `function F takes nothing returns integer
return 7 / 2
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "R2I(7 / 2)") {
		t.Errorf("expected R2I(7 / 2), got:\n%s", got)
	}
}

// TestEmit_IntDiv_CounterLocal — an integer counter `i / 10` (local declared
// `integer`) wraps in R2I.
func TestEmit_IntDiv_CounterLocal(t *testing.T) {
	src := `function F takes nothing returns nothing
local integer i = 0
set i = i / 10
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "R2I(i / 10)") {
		t.Errorf("expected integer counter `i / 10` wrapped as R2I(i / 10), got:\n%s", got)
	}
}

// TestEmit_IntDiv_GlobalInteger — division of an integer global wraps too.
func TestEmit_IntDiv_GlobalInteger(t *testing.T) {
	src := `globals
integer udg_total
endglobals
function F takes nothing returns integer
return udg_total / 4
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "R2I(udg_total / 4)") {
		t.Errorf("expected R2I(udg_total / 4), got:\n%s", got)
	}
}

// TestEmit_IntDiv_UnknownStaysFloat — when an operand's type can't be proven
// integer (a bare ident with no decl in scope), the division stays float.
// Conservative: never guess.
func TestEmit_IntDiv_UnknownStaysFloat(t *testing.T) {
	src := `function F takes nothing returns nothing
set x = y / z
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if strings.Contains(got, "R2I(") {
		t.Errorf("unknown-typed division must stay float (no R2I), got:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// P1#4 — typed array defaults: JASS arrays default unset slots to the element
// type's zero value (0 / 0.0 / false / null). Emitted via __jarray(default)
// for numeric/boolean element types; non-numeric stays `{}` (nil == null).
// ---------------------------------------------------------------------------

// TestEmit_ArrayDefault_IntegerGlobal is the exact FearCounter case: a fresh
// integer-array read-before-write must yield 0, not nil, so `FearCounter[ud]+1`
// is 1 instead of an "arithmetic on nil" crash.
func TestEmit_ArrayDefault_IntegerGlobal(t *testing.T) {
	src := `globals
integer array FearCounter
endglobals
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "FearCounter = __jarray(0)") {
		t.Errorf("expected `FearCounter = __jarray(0)`, got:\n%s", got)
	}
}

// TestEmit_ArrayDefault_BooleanGlobal — boolean array defaults to false.
func TestEmit_ArrayDefault_BooleanGlobal(t *testing.T) {
	src := `globals
boolean array flags
endglobals
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "flags = __jarray(false)") {
		t.Errorf("expected `flags = __jarray(false)`, got:\n%s", got)
	}
}

// TestEmit_ArrayDefault_RealGlobal — real array defaults to 0.0.
func TestEmit_ArrayDefault_RealGlobal(t *testing.T) {
	src := `globals
real array dmg
endglobals
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "dmg = __jarray(0.0)") {
		t.Errorf("expected `dmg = __jarray(0.0)`, got:\n%s", got)
	}
}

// TestEmit_ArrayDefault_HandleStaysEmpty — a handle-typed array keeps `{}`
// because its JASS default is null and Lua's nil-for-unset matches exactly.
func TestEmit_ArrayDefault_HandleStaysEmpty(t *testing.T) {
	src := `globals
unit array units
string array names
endglobals
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "units = {}") {
		t.Errorf("expected handle array `units = {}`, got:\n%s", got)
	}
	if !strings.Contains(got, "names = {}") {
		t.Errorf("expected string array `names = {}`, got:\n%s", got)
	}
}

// TestEmit_ArrayDefault_LocalInteger — local integer arrays get __jarray(0) too.
func TestEmit_ArrayDefault_LocalInteger(t *testing.T) {
	src := `function F takes nothing returns nothing
local integer array buckets
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "local buckets = __jarray(0)") {
		t.Errorf("expected `local buckets = __jarray(0)`, got:\n%s", got)
	}
}

// TestEmit_ArrayDefault_SetThenRead — writing then reading returns the set
// value (the metatable __index only fires for UNSET keys). The emitter writes
// the assignment unchanged; this asserts the write path is untouched.
func TestEmit_ArrayDefault_SetThenRead(t *testing.T) {
	src := `globals
integer array FearCounter
endglobals
function F takes nothing returns integer
set FearCounter[5] = 42
return FearCounter[5]
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "FearCounter = __jarray(0)") {
		t.Errorf("expected typed-array init, got:\n%s", got)
	}
	if !strings.Contains(got, "FearCounter[5] = 42") {
		t.Errorf("expected write `FearCounter[5] = 42` unchanged, got:\n%s", got)
	}
	if !strings.Contains(got, "return FearCounter[5]") {
		t.Errorf("expected read `return FearCounter[5]` unchanged, got:\n%s", got)
	}
}

// TestEmit_ArrayDefault_IndexReadInfersIntForDivision — reading an integer-array
// element is integer-typed, so dividing two such reads wraps in R2I.
func TestEmit_ArrayDefault_IndexReadInfersIntForDivision(t *testing.T) {
	src := `globals
integer array steps
endglobals
function F takes nothing returns integer
return steps[1] / steps[2]
endfunction
`
	got, err := TranspileScript(src)
	if err != nil {
		t.Fatalf("transpile err: %v", err)
	}
	if !strings.Contains(got, "R2I(steps[1] / steps[2])") {
		t.Errorf("expected integer-array element division wrapped, got:\n%s", got)
	}
}
