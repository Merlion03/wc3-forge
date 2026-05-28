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
