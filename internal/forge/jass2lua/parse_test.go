package jass2lua

import (
	"testing"
)

// TestParseTernary_Basic — `a ? b : c` parses as TernaryExpr.
func TestParseTernary_Basic(t *testing.T) {
	toks, err := Tokenize("function F takes nothing returns integer\nreturn x ? 1 : 2\nendfunction\n")
	if err != nil {
		t.Fatalf("lex err: %v", err)
	}
	f := Parse(toks)
	if len(f.Items) != 1 {
		t.Fatalf("expected 1 top-level item, got %d (errs=%v)", len(f.Items), f.Errors)
	}
	fn, ok := f.Items[0].(*FuncDecl)
	if !ok {
		t.Fatalf("expected FuncDecl, got %T", f.Items[0])
	}
	if len(fn.Body) != 1 {
		t.Fatalf("expected 1 stmt, got %d", len(fn.Body))
	}
	ret, ok := fn.Body[0].(*ReturnStmt)
	if !ok {
		t.Fatalf("expected ReturnStmt, got %T", fn.Body[0])
	}
	if _, ok := ret.Value.(*TernaryExpr); !ok {
		t.Fatalf("expected return value to be TernaryExpr, got %T", ret.Value)
	}
}

// TestParseTernary_RightAssociative — `a ? b : c ? d : e` should bind as
// `a ? b : (c ? d : e)`.
func TestParseTernary_RightAssociative(t *testing.T) {
	toks, err := Tokenize("function F takes nothing returns integer\nreturn a ? b : c ? d : e\nendfunction\n")
	if err != nil {
		t.Fatalf("lex err: %v", err)
	}
	f := Parse(toks)
	if len(f.Errors) != 0 {
		t.Fatalf("unexpected parse errors: %v", f.Errors)
	}
	fn := f.Items[0].(*FuncDecl)
	ret := fn.Body[0].(*ReturnStmt)
	outer, ok := ret.Value.(*TernaryExpr)
	if !ok {
		t.Fatalf("expected outer TernaryExpr, got %T", ret.Value)
	}
	if _, ok := outer.Else.(*TernaryExpr); !ok {
		t.Errorf("expected right-associative: outer.Else should be TernaryExpr, got %T", outer.Else)
	}
}

// TestParseBitwise_Precedence — `a | b & c` should parse as `a | (b & c)`
// (& binds tighter than |, matching C-style).
func TestParseBitwise_Precedence(t *testing.T) {
	toks, err := Tokenize("function F takes nothing returns integer\nreturn a | b & c\nendfunction\n")
	if err != nil {
		t.Fatalf("lex err: %v", err)
	}
	f := Parse(toks)
	if len(f.Errors) != 0 {
		t.Fatalf("unexpected parse errors: %v", f.Errors)
	}
	fn := f.Items[0].(*FuncDecl)
	ret := fn.Body[0].(*ReturnStmt)
	or, ok := ret.Value.(*BinaryExpr)
	if !ok || or.Op != "|" {
		t.Fatalf("expected outer | BinaryExpr, got %T (%v)", ret.Value, ret.Value)
	}
	and, ok := or.R.(*BinaryExpr)
	if !ok || and.Op != "&" {
		t.Errorf("expected inner & BinaryExpr on RHS, got %T", or.R)
	}
}

// TestParseShift_Operators ensures `<<` / `>>` parse cleanly as BinaryExpr.
func TestParseShift_Operators(t *testing.T) {
	toks, err := Tokenize("function F takes nothing returns integer\nreturn x << 2\nendfunction\n")
	if err != nil {
		t.Fatalf("lex err: %v", err)
	}
	f := Parse(toks)
	if len(f.Errors) != 0 {
		t.Fatalf("unexpected parse errors: %v", f.Errors)
	}
	fn := f.Items[0].(*FuncDecl)
	ret := fn.Body[0].(*ReturnStmt)
	be, ok := ret.Value.(*BinaryExpr)
	if !ok || be.Op != "<<" {
		t.Errorf("expected << BinaryExpr, got %T", ret.Value)
	}
}

// TestParseUnaryTilde — `~x` parses as UnaryExpr(~).
func TestParseUnaryTilde(t *testing.T) {
	toks, err := Tokenize("function F takes nothing returns integer\nreturn ~x\nendfunction\n")
	if err != nil {
		t.Fatalf("lex err: %v", err)
	}
	f := Parse(toks)
	if len(f.Errors) != 0 {
		t.Fatalf("unexpected parse errors: %v", f.Errors)
	}
	fn := f.Items[0].(*FuncDecl)
	ret := fn.Body[0].(*ReturnStmt)
	un, ok := ret.Value.(*UnaryExpr)
	if !ok || un.Op != "~" {
		t.Errorf("expected unary ~ UnaryExpr, got %T (%v)", ret.Value, ret.Value)
	}
}
