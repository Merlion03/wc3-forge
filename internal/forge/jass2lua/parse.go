package jass2lua

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// AST.
// ---------------------------------------------------------------------------

// Node is the AST root marker. Every concrete AST type implements node() so a
// downstream pass can sanity-check shape.
type Node interface{ node() }

// Stmt is a marker for statement nodes.
type Stmt interface{ Node; stmt() }

// Expr is a marker for expression nodes.
type Expr interface{ Node; expr() }

// Lhs is a marker for assignment-target nodes (Ident or IndexExpr).
type Lhs interface{ Node; lhs() }

// File is the top-level parse output. Mix of GlobalsBlock + FuncDecl, plus
// any leading comments. Includes Errors so callers can surface partial
// failures in the diff view.
type File struct {
	Items  []Node
	Errors []string
}

// FuncDecl is `function name takes ... returns ... <body> endfunction`.
type FuncDecl struct {
	Name      string
	Params    []Param
	Returns   string  // "" for `returns nothing`
	Body      []Stmt
	Native    bool    // `native foo takes ... returns ...`
	Constant  bool    // `constant function ...` or `constant native ...`
	LeadingCo []string // leading comments (// only)
}

// Param is one entry of a function's `takes` list.
type Param struct {
	Type string
	Name string
}

// GlobalsBlock is `globals ... endglobals`.
type GlobalsBlock struct {
	Decls []GlobalDecl
}

// GlobalDecl is one line inside a `globals` block. Constant=true for
// `constant integer X = 5`. Array=true for `integer arr array` (no init).
type GlobalDecl struct {
	Name     string
	Type     string
	Constant bool
	Array    bool
	Init     Expr // nil if no init
}

// LocalDecl is `local <type> name [= expr]` or `local <type> array name`.
type LocalDecl struct {
	Name  string
	Type  string
	Array bool
	Init  Expr
}

// TypeDecl is `type Foo extends handle`. We emit nothing for it (Lua has no
// type system), but parse it so we don't error.
type TypeDecl struct {
	Name    string
	Extends string
}

// CommentStmt holds a `// comment` line we want preserved in the output.
type CommentStmt struct{ Text string }

// IfStmt is `if cond then ... elseif cond then ... else ... endif`.
type IfStmt struct {
	Cond  Expr
	Then  []Stmt
	Elifs []Elif
	Else  []Stmt // nil if no else branch
}

// Elif is one `elseif cond then ... ` arm.
type Elif struct {
	Cond Expr
	Body []Stmt
}

// LoopStmt is `loop ... endloop`. ExitWhen lives inside Body as ExitWhenStmt.
type LoopStmt struct {
	Body []Stmt
}

// ExitWhenStmt is `exitwhen <cond>`.
type ExitWhenStmt struct {
	Cond Expr
}

// SetStmt is `set lhs = expr`.
type SetStmt struct {
	Target Lhs
	Value  Expr
}

// CallStmt is `call foo(a, b)` — top-level call statement (return value
// discarded).
type CallStmt struct {
	Func string
	Args []Expr
}

// ReturnStmt is `return [expr]`.
type ReturnStmt struct {
	Value Expr // nil for bare return
}

// LocalStmt wraps a LocalDecl so it can appear as a Stmt.
type LocalStmt struct {
	Decl LocalDecl
}

// RawStmt is a placeholder we emit when we can't parse something — surfaces
// as `error("jass2lua: ...")` in the output.
type RawStmt struct {
	Reason  string
	Snippet string // the source bytes we skipped
}

// BinaryExpr is `L op R`.
type BinaryExpr struct {
	Op string
	L  Expr
	R  Expr
}

// UnaryExpr is `op X` (op ∈ {-, not}).
type UnaryExpr struct {
	Op string
	X  Expr
}

// CallExpr is `func(args...)`.
type CallExpr struct {
	Func string
	Args []Expr
}

// Ident is a bare variable reference.
type Ident struct {
	Name string
}

// IntLit / RealLit / StringLit / BoolLit / NullLit / RawLit are leaf
// expressions. RawLit holds the rawcode characters (4 chars typically;
// always emitted as FourCC("...") on the Lua side).
type IntLit struct{ Value string }   // raw source token (0x..., decimal, or $... already rewritten to 0x...)
type RealLit struct{ Value string }  // raw source token
type StringLit struct{ Value string } // post-escape bytes
type BoolLit struct{ Value bool }
type NullLit struct{}
type RawLit struct{ Value string }

// IndexExpr is `base[index]`.
type IndexExpr struct {
	Base  Expr
	Index Expr
}

// Method markers.
func (*FuncDecl) node()     {}
func (*GlobalsBlock) node() {}
func (*GlobalDecl) node()   {}
func (*LocalDecl) node()    {}
func (*TypeDecl) node()     {}
func (*CommentStmt) node()  {}
func (*IfStmt) node()       {}
func (*LoopStmt) node()     {}
func (*ExitWhenStmt) node() {}
func (*SetStmt) node()      {}
func (*CallStmt) node()     {}
func (*ReturnStmt) node()   {}
func (*LocalStmt) node()    {}
func (*RawStmt) node()      {}
func (*BinaryExpr) node()   {}
func (*UnaryExpr) node()    {}
func (*CallExpr) node()     {}
func (*Ident) node()        {}
func (*IntLit) node()       {}
func (*RealLit) node()      {}
func (*StringLit) node()    {}
func (*BoolLit) node()      {}
func (*NullLit) node()      {}
func (*RawLit) node()       {}
func (*IndexExpr) node()    {}

func (*CommentStmt) stmt()  {}
func (*IfStmt) stmt()       {}
func (*LoopStmt) stmt()     {}
func (*ExitWhenStmt) stmt() {}
func (*SetStmt) stmt()      {}
func (*CallStmt) stmt()     {}
func (*ReturnStmt) stmt()   {}
func (*LocalStmt) stmt()    {}
func (*RawStmt) stmt()      {}

func (*BinaryExpr) expr() {}
func (*UnaryExpr) expr()  {}
func (*CallExpr) expr()   {}
func (*Ident) expr()      {}
func (*IntLit) expr()     {}
func (*RealLit) expr()    {}
func (*StringLit) expr()  {}
func (*BoolLit) expr()    {}
func (*NullLit) expr()    {}
func (*RawLit) expr()     {}
func (*IndexExpr) expr()  {}

func (*Ident) lhs()     {}
func (*IndexExpr) lhs() {}

// ---------------------------------------------------------------------------
// Parser. Hand-rolled recursive descent + Pratt for expressions.
// ---------------------------------------------------------------------------

type parser struct {
	toks []Token
	pos  int
	errs []string
}

// Parse turns a token stream into a File AST. Soft errors are collected on
// File.Errors; hard panics are caught and surfaced as a RawStmt at the
// top level so the rest of the file still translates.
func Parse(toks []Token) *File {
	p := &parser{toks: toks}
	f := &File{}
	// Skip leading EOL/comments at file scope.
	for p.peek().Kind != TokEOF {
		// Skip blank lines.
		if p.peek().Kind == TokEOL {
			p.advance()
			continue
		}
		// Top-level comments become CommentStmt-like nodes (we wrap them as
		// CommentStmt; the emitter writes them at file scope).
		if p.peek().Kind == TokComment {
			f.Items = append(f.Items, &CommentStmt{Text: p.advance().Value})
			continue
		}
		startPos := p.pos
		item, err := p.parseTopLevel()
		if err != nil {
			f.Errors = append(f.Errors, err.Error())
			// Skip to next EOL to recover and avoid infinite loops.
			snippet := p.snippetFrom(startPos)
			f.Items = append(f.Items, &RawStmt{Reason: err.Error(), Snippet: snippet})
			p.skipToEOL()
			continue
		}
		f.Items = append(f.Items, item)
	}
	if len(p.errs) > 0 {
		f.Errors = append(f.Errors, p.errs...)
	}
	return f
}

func (p *parser) peek() Token {
	if p.pos >= len(p.toks) {
		return Token{Kind: TokEOF}
	}
	return p.toks[p.pos]
}

func (p *parser) peekAt(n int) Token {
	if p.pos+n >= len(p.toks) {
		return Token{Kind: TokEOF}
	}
	return p.toks[p.pos+n]
}

func (p *parser) advance() Token {
	t := p.peek()
	p.pos++
	return t
}

func (p *parser) expect(kind TokenKind, value string) (Token, error) {
	t := p.peek()
	if t.Kind != kind || (value != "" && t.Value != value) {
		return t, fmt.Errorf("expected %s %q, got %s %q at line %d:%d", kindName(kind), value, kindName(t.Kind), t.Value, t.Line, t.Col)
	}
	return p.advance(), nil
}

func (p *parser) skipEOLs() {
	for p.peek().Kind == TokEOL {
		p.advance()
	}
}

func (p *parser) skipToEOL() {
	for p.peek().Kind != TokEOL && p.peek().Kind != TokEOF {
		p.advance()
	}
	if p.peek().Kind == TokEOL {
		p.advance()
	}
}

func (p *parser) snippetFrom(startPos int) string {
	var b strings.Builder
	for i := startPos; i < p.pos && i < len(p.toks); i++ {
		if i > startPos {
			b.WriteByte(' ')
		}
		b.WriteString(p.toks[i].Value)
	}
	return b.String()
}

// parseTopLevel dispatches between globals / function / native / type / constant
// (and tolerates straggling EOLs already eaten by the caller).
func (p *parser) parseTopLevel() (Node, error) {
	t := p.peek()
	if t.Kind == TokKeyword {
		switch t.Value {
		case "globals":
			return p.parseGlobals()
		case "function":
			return p.parseFunction(false, false)
		case "constant":
			// `constant native foo ...` or `constant function foo ...`
			p.advance()
			next := p.peek()
			if next.Kind == TokKeyword && next.Value == "native" {
				p.advance()
				return p.parseNative(true)
			}
			if next.Kind == TokKeyword && next.Value == "function" {
				return p.parseFunction(false, true)
			}
			return nil, fmt.Errorf("constant must precede function/native at line %d:%d", t.Line, t.Col)
		case "native":
			p.advance()
			return p.parseNative(false)
		case "type":
			return p.parseTypeDecl()
		}
	}
	return nil, fmt.Errorf("unexpected token %q at line %d:%d", t.Value, t.Line, t.Col)
}

func (p *parser) parseGlobals() (*GlobalsBlock, error) {
	if _, err := p.expect(TokKeyword, "globals"); err != nil {
		return nil, err
	}
	p.skipToEOL()
	gb := &GlobalsBlock{}
	for {
		// Tolerate blank lines and comments inside globals.
		if p.peek().Kind == TokEOL {
			p.advance()
			continue
		}
		if p.peek().Kind == TokComment {
			p.advance() // dropped — globals comments rarely round-trip cleanly
			p.skipToEOL()
			continue
		}
		if p.peek().Kind == TokKeyword && p.peek().Value == "endglobals" {
			p.advance()
			p.skipToEOL()
			return gb, nil
		}
		if p.peek().Kind == TokEOF {
			return nil, fmt.Errorf("unexpected EOF in globals block")
		}
		gd, err := p.parseGlobalDecl()
		if err != nil {
			// Recover: skip to next line and continue.
			p.errs = append(p.errs, err.Error())
			p.skipToEOL()
			continue
		}
		gb.Decls = append(gb.Decls, gd)
	}
}

// parseGlobalDecl handles: [constant] <type> [array] name [= expr]
func (p *parser) parseGlobalDecl() (GlobalDecl, error) {
	gd := GlobalDecl{}
	if p.peek().Kind == TokKeyword && p.peek().Value == "constant" {
		gd.Constant = true
		p.advance()
	}
	// Type can be TokType or TokIdent (for user types we don't know about).
	if p.peek().Kind != TokType && p.peek().Kind != TokIdent {
		return gd, fmt.Errorf("expected type at line %d:%d", p.peek().Line, p.peek().Col)
	}
	gd.Type = p.advance().Value
	if p.peek().Kind == TokKeyword && p.peek().Value == "array" {
		gd.Array = true
		p.advance()
	}
	if p.peek().Kind != TokIdent {
		return gd, fmt.Errorf("expected name at line %d:%d", p.peek().Line, p.peek().Col)
	}
	gd.Name = p.advance().Value
	if p.peek().Kind == TokOp && p.peek().Value == "=" {
		p.advance()
		e, err := p.parseExpr()
		if err != nil {
			return gd, err
		}
		gd.Init = e
	}
	p.skipToEOL()
	return gd, nil
}

func (p *parser) parseFunction(native bool, constant bool) (*FuncDecl, error) {
	if _, err := p.expect(TokKeyword, "function"); err != nil {
		return nil, err
	}
	fd := &FuncDecl{Native: native, Constant: constant}
	if p.peek().Kind != TokIdent {
		return nil, fmt.Errorf("expected function name at line %d:%d", p.peek().Line, p.peek().Col)
	}
	fd.Name = p.advance().Value
	if err := p.parseSignature(fd); err != nil {
		return nil, err
	}
	p.skipToEOL()
	// Body until endfunction.
	for {
		t := p.peek()
		if t.Kind == TokEOF {
			return nil, fmt.Errorf("unterminated function %q", fd.Name)
		}
		if t.Kind == TokEOL {
			p.advance()
			continue
		}
		if t.Kind == TokKeyword && t.Value == "endfunction" {
			p.advance()
			p.skipToEOL()
			return fd, nil
		}
		s, err := p.parseStmt()
		if err != nil {
			startPos := p.pos
			p.errs = append(p.errs, err.Error())
			snippet := p.snippetFrom(startPos)
			fd.Body = append(fd.Body, &RawStmt{Reason: err.Error(), Snippet: snippet})
			p.skipToEOL()
			continue
		}
		fd.Body = append(fd.Body, s)
	}
}

func (p *parser) parseNative(constant bool) (*FuncDecl, error) {
	// We've already consumed `native`. Native looks like: `native Foo takes ... returns ...`.
	fd := &FuncDecl{Native: true, Constant: constant}
	if p.peek().Kind != TokIdent {
		return nil, fmt.Errorf("expected native function name at line %d:%d", p.peek().Line, p.peek().Col)
	}
	fd.Name = p.advance().Value
	if err := p.parseSignature(fd); err != nil {
		return nil, err
	}
	p.skipToEOL()
	return fd, nil
}

func (p *parser) parseTypeDecl() (*TypeDecl, error) {
	if _, err := p.expect(TokKeyword, "type"); err != nil {
		return nil, err
	}
	td := &TypeDecl{}
	if p.peek().Kind != TokIdent && p.peek().Kind != TokType {
		return nil, fmt.Errorf("expected type name at line %d:%d", p.peek().Line, p.peek().Col)
	}
	td.Name = p.advance().Value
	if _, err := p.expect(TokKeyword, "extends"); err != nil {
		return nil, err
	}
	if p.peek().Kind != TokType && p.peek().Kind != TokIdent {
		return nil, fmt.Errorf("expected extends-type at line %d:%d", p.peek().Line, p.peek().Col)
	}
	td.Extends = p.advance().Value
	p.skipToEOL()
	return td, nil
}

// parseSignature handles `takes (nothing | <type> name, ...) returns (nothing | <type>)`.
func (p *parser) parseSignature(fd *FuncDecl) error {
	if _, err := p.expect(TokKeyword, "takes"); err != nil {
		return err
	}
	if p.peek().Kind == TokKeyword && p.peek().Value == "nothing" {
		p.advance()
	} else {
		// One or more `type name` separated by commas.
		for {
			if p.peek().Kind != TokType && p.peek().Kind != TokIdent {
				return fmt.Errorf("expected param type at line %d:%d", p.peek().Line, p.peek().Col)
			}
			ptype := p.advance().Value
			if p.peek().Kind != TokIdent {
				return fmt.Errorf("expected param name at line %d:%d", p.peek().Line, p.peek().Col)
			}
			pname := p.advance().Value
			fd.Params = append(fd.Params, Param{Type: ptype, Name: pname})
			if p.peek().Kind == TokOp && p.peek().Value == "," {
				p.advance()
				continue
			}
			break
		}
	}
	if _, err := p.expect(TokKeyword, "returns"); err != nil {
		return err
	}
	if p.peek().Kind == TokKeyword && p.peek().Value == "nothing" {
		p.advance()
		fd.Returns = ""
	} else if p.peek().Kind == TokType || p.peek().Kind == TokIdent {
		fd.Returns = p.advance().Value
	} else {
		return fmt.Errorf("expected return type at line %d:%d", p.peek().Line, p.peek().Col)
	}
	return nil
}

func (p *parser) parseStmt() (Stmt, error) {
	t := p.peek()
	if t.Kind == TokComment {
		p.advance()
		return &CommentStmt{Text: t.Value}, nil
	}
	if t.Kind == TokKeyword {
		switch t.Value {
		case "local":
			return p.parseLocal()
		case "set":
			return p.parseSet()
		case "call":
			return p.parseCall()
		case "if":
			return p.parseIf()
		case "loop":
			return p.parseLoop()
		case "exitwhen":
			return p.parseExitWhen()
		case "return":
			return p.parseReturn()
		case "debug":
			// `debug call X(...)` / `debug set ...` / etc. Strip debug, parse the rest.
			p.advance()
			return p.parseStmt()
		}
	}
	return nil, fmt.Errorf("unexpected token %q at line %d:%d", t.Value, t.Line, t.Col)
}

func (p *parser) parseLocal() (*LocalStmt, error) {
	if _, err := p.expect(TokKeyword, "local"); err != nil {
		return nil, err
	}
	ld := LocalDecl{}
	if p.peek().Kind != TokType && p.peek().Kind != TokIdent {
		return nil, fmt.Errorf("expected local type at line %d:%d", p.peek().Line, p.peek().Col)
	}
	ld.Type = p.advance().Value
	if p.peek().Kind == TokKeyword && p.peek().Value == "array" {
		ld.Array = true
		p.advance()
	}
	if p.peek().Kind != TokIdent {
		return nil, fmt.Errorf("expected local name at line %d:%d", p.peek().Line, p.peek().Col)
	}
	ld.Name = p.advance().Value
	if p.peek().Kind == TokOp && p.peek().Value == "=" {
		p.advance()
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		ld.Init = e
	}
	p.skipToEOL()
	return &LocalStmt{Decl: ld}, nil
}

func (p *parser) parseSet() (*SetStmt, error) {
	if _, err := p.expect(TokKeyword, "set"); err != nil {
		return nil, err
	}
	if p.peek().Kind != TokIdent {
		return nil, fmt.Errorf("expected lhs at line %d:%d", p.peek().Line, p.peek().Col)
	}
	name := p.advance().Value
	var lhs Lhs = &Ident{Name: name}
	// Optional array index: name[expr]
	if p.peek().Kind == TokOp && p.peek().Value == "[" {
		p.advance()
		idx, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(TokOp, "]"); err != nil {
			return nil, err
		}
		lhs = &IndexExpr{Base: &Ident{Name: name}, Index: idx}
	}
	if _, err := p.expect(TokOp, "="); err != nil {
		return nil, err
	}
	val, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	p.skipToEOL()
	return &SetStmt{Target: lhs, Value: val}, nil
}

func (p *parser) parseCall() (*CallStmt, error) {
	if _, err := p.expect(TokKeyword, "call"); err != nil {
		return nil, err
	}
	if p.peek().Kind != TokIdent {
		return nil, fmt.Errorf("expected function name at line %d:%d", p.peek().Line, p.peek().Col)
	}
	name := p.advance().Value
	if _, err := p.expect(TokOp, "("); err != nil {
		return nil, err
	}
	args, err := p.parseArgList()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(TokOp, ")"); err != nil {
		return nil, err
	}
	p.skipToEOL()
	return &CallStmt{Func: name, Args: args}, nil
}

func (p *parser) parseIf() (*IfStmt, error) {
	if _, err := p.expect(TokKeyword, "if"); err != nil {
		return nil, err
	}
	cond, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(TokKeyword, "then"); err != nil {
		return nil, err
	}
	p.skipToEOL()
	is := &IfStmt{Cond: cond}
	// Parse body up to elseif / else / endif.
	body, term, err := p.parseStmtsUntil("elseif", "else", "endif")
	if err != nil {
		return nil, err
	}
	is.Then = body
	for term == "elseif" {
		p.advance() // elseif
		ec, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(TokKeyword, "then"); err != nil {
			return nil, err
		}
		p.skipToEOL()
		ebody, eterm, err := p.parseStmtsUntil("elseif", "else", "endif")
		if err != nil {
			return nil, err
		}
		is.Elifs = append(is.Elifs, Elif{Cond: ec, Body: ebody})
		term = eterm
	}
	if term == "else" {
		p.advance() // else
		p.skipToEOL()
		ebody, eterm, err := p.parseStmtsUntil("endif")
		if err != nil {
			return nil, err
		}
		if eterm != "endif" {
			return nil, fmt.Errorf("missing endif")
		}
		is.Else = ebody
	}
	if _, err := p.expect(TokKeyword, "endif"); err != nil {
		return nil, err
	}
	p.skipToEOL()
	return is, nil
}

func (p *parser) parseLoop() (*LoopStmt, error) {
	if _, err := p.expect(TokKeyword, "loop"); err != nil {
		return nil, err
	}
	p.skipToEOL()
	ls := &LoopStmt{}
	body, term, err := p.parseStmtsUntil("endloop")
	if err != nil {
		return nil, err
	}
	if term != "endloop" {
		return nil, fmt.Errorf("missing endloop")
	}
	ls.Body = body
	p.advance() // endloop
	p.skipToEOL()
	return ls, nil
}

func (p *parser) parseExitWhen() (*ExitWhenStmt, error) {
	if _, err := p.expect(TokKeyword, "exitwhen"); err != nil {
		return nil, err
	}
	e, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	p.skipToEOL()
	return &ExitWhenStmt{Cond: e}, nil
}

func (p *parser) parseReturn() (*ReturnStmt, error) {
	if _, err := p.expect(TokKeyword, "return"); err != nil {
		return nil, err
	}
	// Bare return → no expression on this line.
	if p.peek().Kind == TokEOL || p.peek().Kind == TokEOF {
		p.skipToEOL()
		return &ReturnStmt{}, nil
	}
	e, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	p.skipToEOL()
	return &ReturnStmt{Value: e}, nil
}

// parseStmtsUntil parses statements until one of the given keywords appears at
// statement start. Returns the body, the matched terminator keyword, or an
// error. The terminator is NOT consumed.
func (p *parser) parseStmtsUntil(terminators ...string) ([]Stmt, string, error) {
	var out []Stmt
	for {
		// Skip blank lines / comments-at-EOL.
		if p.peek().Kind == TokEOL {
			p.advance()
			continue
		}
		t := p.peek()
		if t.Kind == TokEOF {
			return out, "", fmt.Errorf("unexpected EOF (looking for %v)", terminators)
		}
		if t.Kind == TokKeyword {
			for _, tk := range terminators {
				if t.Value == tk {
					return out, tk, nil
				}
			}
		}
		startPos := p.pos
		s, err := p.parseStmt()
		if err != nil {
			p.errs = append(p.errs, err.Error())
			snippet := p.snippetFrom(startPos)
			out = append(out, &RawStmt{Reason: err.Error(), Snippet: snippet})
			p.skipToEOL()
			continue
		}
		out = append(out, s)
	}
}

// ---------------------------------------------------------------------------
// Expression parser — Pratt-style precedence climbing.
// ---------------------------------------------------------------------------

func (p *parser) parseExpr() (Expr, error) {
	return p.parseBinary(0)
}

// Precedence table — matches JASS/standard order:
//
//	1 — or
//	2 — and
//	3 — not (unary)
//	4 — == != < <= > >=
//	5 — + -
//	6 — * / %
//	7 — unary -
func opPrec(t Token) int {
	if t.Kind == TokKeyword {
		switch t.Value {
		case "or":
			return 1
		case "and":
			return 2
		}
	}
	if t.Kind == TokOp {
		switch t.Value {
		case "==", "!=", "<", "<=", ">", ">=":
			return 4
		case "+", "-":
			return 5
		case "*", "/", "%":
			return 6
		}
	}
	return 0
}

func (p *parser) parseBinary(minPrec int) (Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		prec := opPrec(t)
		if prec == 0 || prec < minPrec {
			return left, nil
		}
		op := t.Value
		p.advance()
		right, err := p.parseBinary(prec + 1)
		if err != nil {
			return nil, err
		}
		left = &BinaryExpr{Op: op, L: left, R: right}
	}
}

func (p *parser) parseUnary() (Expr, error) {
	t := p.peek()
	if t.Kind == TokKeyword && t.Value == "not" {
		p.advance()
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &UnaryExpr{Op: "not", X: x}, nil
	}
	if t.Kind == TokOp && t.Value == "-" {
		p.advance()
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &UnaryExpr{Op: "-", X: x}, nil
	}
	if t.Kind == TokOp && t.Value == "+" {
		// Unary plus — drop.
		p.advance()
		return p.parseUnary()
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Expr, error) {
	t := p.peek()
	switch t.Kind {
	case TokInt:
		p.advance()
		return &IntLit{Value: t.Value}, nil
	case TokReal:
		p.advance()
		return &RealLit{Value: t.Value}, nil
	case TokString:
		p.advance()
		return &StringLit{Value: t.Value}, nil
	case TokRaw:
		p.advance()
		return &RawLit{Value: t.Value}, nil
	case TokKeyword:
		switch t.Value {
		case "true":
			p.advance()
			return &BoolLit{Value: true}, nil
		case "false":
			p.advance()
			return &BoolLit{Value: false}, nil
		case "null":
			p.advance()
			return &NullLit{}, nil
		case "function":
			// `function FuncName` — JASS code literal. Lua: just the bare name (Lua functions are first-class).
			p.advance()
			if p.peek().Kind != TokIdent {
				return nil, fmt.Errorf("expected function name after `function` at line %d:%d", p.peek().Line, p.peek().Col)
			}
			name := p.advance().Value
			return &Ident{Name: name}, nil
		}
	case TokOp:
		if t.Value == "(" {
			p.advance()
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(TokOp, ")"); err != nil {
				return nil, err
			}
			return e, nil
		}
	case TokIdent:
		// Identifier, optionally followed by ( for call, or [ for index.
		p.advance()
		if p.peek().Kind == TokOp && p.peek().Value == "(" {
			p.advance()
			args, err := p.parseArgList()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(TokOp, ")"); err != nil {
				return nil, err
			}
			return &CallExpr{Func: t.Value, Args: args}, nil
		}
		if p.peek().Kind == TokOp && p.peek().Value == "[" {
			p.advance()
			idx, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(TokOp, "]"); err != nil {
				return nil, err
			}
			return &IndexExpr{Base: &Ident{Name: t.Value}, Index: idx}, nil
		}
		return &Ident{Name: t.Value}, nil
	}
	return nil, fmt.Errorf("unexpected token in expression: %q at line %d:%d", t.Value, t.Line, t.Col)
}

func (p *parser) parseArgList() ([]Expr, error) {
	var args []Expr
	if p.peek().Kind == TokOp && p.peek().Value == ")" {
		return args, nil
	}
	for {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		args = append(args, e)
		if p.peek().Kind == TokOp && p.peek().Value == "," {
			p.advance()
			continue
		}
		break
	}
	return args, nil
}

func kindName(k TokenKind) string {
	switch k {
	case TokIdent:
		return "ident"
	case TokKeyword:
		return "keyword"
	case TokType:
		return "type"
	case TokInt:
		return "int"
	case TokReal:
		return "real"
	case TokString:
		return "string"
	case TokRaw:
		return "rawcode"
	case TokOp:
		return "op"
	case TokComment:
		return "comment"
	case TokEOL:
		return "eol"
	case TokEOF:
		return "eof"
	}
	return "?"
}
