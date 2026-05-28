package jass2lua

import (
	"fmt"
	"strings"
)

// TranspileScript translates a JASS source string to Lua.
//
// Conservative on errors: any unparseable construct produces a
// `--[[ jass2lua: <reason> ]] error("jass2lua failed")` placeholder so the
// rest of the file still translates and the user sees something actionable
// in the diff.
func TranspileScript(jass string) (lua string, err error) {
	toks, err := Tokenize(jass)
	if err != nil {
		// Lex errors can't be recovered (we have no token stream to walk).
		// Surface the whole input as a placeholder so the diff still renders.
		return fmt.Sprintf("--[[ jass2lua lex error: %s ]]\nerror(%q)\n", err.Error(), "jass2lua failed: "+err.Error()), err
	}
	file := Parse(toks)
	em := &emitter{}
	em.emitFile(file)
	return em.b.String(), nil
}

// TranspileFunction translates a JASS fragment for a single trigger's custom_text.
// Many maps' custom_text blocks contain bare statements (no surrounding
// function/endfunction), so this auto-detects the shape:
//
//   - If the input contains a top-level `function` keyword, treat as full
//     script and delegate to TranspileScript.
//   - Otherwise wrap as a body (`function Name() ... end`) named after `name`.
//
// Returns conservative output on errors (same contract as TranspileScript).
func TranspileFunction(name, jass string) (lua string, err error) {
	if hasTopLevelFunction(jass) {
		return TranspileScript(jass)
	}
	// Auto-wrap as function body.
	toks, err := Tokenize(jass)
	if err != nil {
		return fmt.Sprintf("--[[ jass2lua lex error: %s ]]\nfunction %s()\n\terror(%q)\nend\n", err.Error(), sanitizeName(name), "jass2lua failed: "+err.Error()), err
	}
	// Synthesize a wrapping function decl by manual statement-list parse.
	p := &parser{toks: toks}
	stmts, _, parseErr := p.parseStmtsUntilEOF()
	em := &emitter{}
	em.writeLine(fmt.Sprintf("function %s()", sanitizeName(name)))
	em.indent++
	for _, s := range stmts {
		em.emitStmt(s)
	}
	em.indent--
	em.writeLine("end")
	if parseErr != nil {
		// Best-effort: still emit what we got.
		return em.b.String(), parseErr
	}
	if len(p.errs) > 0 {
		return em.b.String(), fmt.Errorf("%s", strings.Join(p.errs, "; "))
	}
	return em.b.String(), nil
}

// hasTopLevelFunction is a cheap pre-check used by TranspileFunction to decide
// whether the input is a full script or a body-only fragment. False positive
// at the line-start level is acceptable (worst case: we treat a body fragment
// as a script and the parser falls back to RawStmt for any non-decl).
func hasTopLevelFunction(jass string) bool {
	for _, line := range strings.Split(jass, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "function ") || strings.HasPrefix(trim, "globals") ||
			strings.HasPrefix(trim, "constant function ") || strings.HasPrefix(trim, "native ") ||
			strings.HasPrefix(trim, "constant native ") {
			return true
		}
	}
	return false
}

// parseStmtsUntilEOF is the body-fragment entry — keeps parsing until TokEOF.
// Returns the collected statements, "" (no terminator), and any hard error.
func (p *parser) parseStmtsUntilEOF() ([]Stmt, string, error) {
	var out []Stmt
	for {
		if p.peek().Kind == TokEOL {
			p.advance()
			continue
		}
		if p.peek().Kind == TokEOF {
			return out, "", nil
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

// sanitizeName strips characters that can't appear in a Lua identifier so the
// auto-wrap shim doesn't produce malformed Lua.
func sanitizeName(s string) string {
	var b strings.Builder
	for i, r := range s {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if i > 0 && r >= '0' && r <= '9' {
			ok = true
		}
		if ok {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || (out[0] >= '0' && out[0] <= '9') {
		out = "Trig_" + out
	}
	return out
}

// ---------------------------------------------------------------------------
// Emitter.
// ---------------------------------------------------------------------------

type emitter struct {
	b      strings.Builder
	indent int
}

func (e *emitter) writeIndent() {
	for i := 0; i < e.indent; i++ {
		e.b.WriteByte('\t')
	}
}

func (e *emitter) writeLine(s string) {
	e.writeIndent()
	e.b.WriteString(s)
	e.b.WriteByte('\n')
}

// defaultInitForType returns the Lua literal a global of the given JASS type
// should initialize to when there's no explicit initializer.
func defaultInitForType(t string) string {
	switch t {
	case "integer":
		return "0"
	case "real":
		return "0.0"
	case "string":
		return `""`
	case "boolean":
		return "false"
	case "code":
		return "nil"
	}
	// All handle subtypes default to nil.
	return "nil"
}

func (e *emitter) emitFile(f *File) {
	for _, item := range f.Items {
		switch n := item.(type) {
		case *CommentStmt:
			e.writeLine("--" + n.Text)
		case *GlobalsBlock:
			e.emitGlobalsBlock(n)
		case *FuncDecl:
			e.emitFuncDecl(n)
		case *TypeDecl:
			e.writeLine(fmt.Sprintf("-- type %s extends %s (no Lua equivalent; declared for parity)", n.Name, n.Extends))
		case *RawStmt:
			e.emitRaw(n)
		default:
			e.writeLine(fmt.Sprintf("-- jass2lua: unhandled top-level node %T", item))
		}
	}
}

func (e *emitter) emitGlobalsBlock(g *GlobalsBlock) {
	for _, d := range g.Decls {
		// Constant: emit a comment marker since WC3's Lua is 5.3 (no <const>).
		prefix := ""
		if d.Constant {
			prefix = "-- constant\n"
		}
		var init string
		if d.Array {
			// Array decl — empty table; Lua tables are dynamic.
			init = "{}"
		} else if d.Init != nil {
			init = e.emitExpr(d.Init)
		} else {
			init = defaultInitForType(d.Type)
		}
		if prefix != "" {
			e.writeIndent()
			e.b.WriteString(prefix)
		}
		e.writeLine(fmt.Sprintf("%s = %s", d.Name, init))
	}
}

func (e *emitter) emitFuncDecl(f *FuncDecl) {
	if f.Native {
		// Natives can't be redeclared in Lua-only WC3 maps (they're bound at
		// runtime by the engine). Emit as a comment so the diff is still
		// informative.
		var params []string
		for _, p := range f.Params {
			params = append(params, p.Name)
		}
		e.writeLine(fmt.Sprintf("-- native %s(%s) returns %s", f.Name, strings.Join(params, ", "), retName(f.Returns)))
		return
	}
	var params []string
	for _, p := range f.Params {
		params = append(params, p.Name)
	}
	e.writeLine(fmt.Sprintf("function %s(%s)", f.Name, strings.Join(params, ", ")))
	e.indent++
	for _, s := range f.Body {
		e.emitStmt(s)
	}
	e.indent--
	e.writeLine("end")
	e.b.WriteByte('\n')
}

func retName(r string) string {
	if r == "" {
		return "nothing"
	}
	return r
}

func (e *emitter) emitStmt(s Stmt) {
	switch n := s.(type) {
	case *CommentStmt:
		e.writeLine("--" + n.Text)
	case *LocalStmt:
		e.emitLocal(n.Decl)
	case *SetStmt:
		e.writeLine(fmt.Sprintf("%s = %s", e.emitLhs(n.Target), e.emitExpr(n.Value)))
	case *CallStmt:
		e.writeLine(fmt.Sprintf("%s(%s)", n.Func, e.emitArgList(n.Args)))
	case *MemberCallStmt:
		// recv.method(args) or recv:method(args) depending on Colon flag.
		sep := "."
		if n.Call.Colon {
			sep = ":"
		}
		e.writeLine(fmt.Sprintf("%s%s%s(%s)", e.emitExpr(n.Call.Recv), sep, n.Call.Method, e.emitArgList(n.Call.Args)))
	case *IfStmt:
		e.emitIf(n)
	case *LoopStmt:
		e.emitLoop(n)
	case *ExitWhenStmt:
		// `exitwhen cond` → `if cond then break end`.
		e.writeLine(fmt.Sprintf("if %s then break end", e.emitExpr(n.Cond)))
	case *ReturnStmt:
		if n.Value == nil {
			e.writeLine("return")
		} else {
			e.writeLine("return " + e.emitExpr(n.Value))
		}
	case *RawStmt:
		e.emitRaw(n)
	default:
		e.writeLine(fmt.Sprintf("-- jass2lua: unhandled stmt %T", s))
	}
}

func (e *emitter) emitLocal(d LocalDecl) {
	if d.Array {
		// Lua tables are dynamic — no size; just initialize to {}.
		e.writeLine(fmt.Sprintf("local %s = {} -- jass: %s array", d.Name, d.Type))
		return
	}
	if d.Init == nil {
		e.writeLine(fmt.Sprintf("local %s = %s", d.Name, defaultInitForType(d.Type)))
		return
	}
	e.writeLine(fmt.Sprintf("local %s = %s", d.Name, e.emitExpr(d.Init)))
}

func (e *emitter) emitIf(s *IfStmt) {
	e.writeLine(fmt.Sprintf("if %s then", e.emitExpr(s.Cond)))
	e.indent++
	for _, st := range s.Then {
		e.emitStmt(st)
	}
	e.indent--
	for _, ef := range s.Elifs {
		e.writeLine(fmt.Sprintf("elseif %s then", e.emitExpr(ef.Cond)))
		e.indent++
		for _, st := range ef.Body {
			e.emitStmt(st)
		}
		e.indent--
	}
	if s.Else != nil {
		e.writeLine("else")
		e.indent++
		for _, st := range s.Else {
			e.emitStmt(st)
		}
		e.indent--
	}
	e.writeLine("end")
}

func (e *emitter) emitLoop(s *LoopStmt) {
	e.writeLine("while true do")
	e.indent++
	for _, st := range s.Body {
		e.emitStmt(st)
	}
	e.indent--
	e.writeLine("end")
}

func (e *emitter) emitRaw(r *RawStmt) {
	// Multi-line snippet → single-line comment. Replace ]] to avoid breaking
	// the comment block.
	snippet := strings.ReplaceAll(r.Snippet, "]]", "] ]")
	e.writeLine(fmt.Sprintf("--[[ jass2lua: %s | source: %s ]]", r.Reason, snippet))
	e.writeLine(fmt.Sprintf("error(%q)", "jass2lua failed: "+r.Reason))
}

func (e *emitter) emitLhs(l Lhs) string {
	switch n := l.(type) {
	case *Ident:
		return n.Name
	case *IndexExpr:
		return e.emitExpr(n.Base) + "[" + e.emitExpr(n.Index) + "]"
	case *MemberExpr:
		// LHS member access — always dot in Lua (colon is call-only).
		return e.emitExpr(n.Base) + "." + n.Member
	}
	return "--[[lhs?]]"
}

func (e *emitter) emitArgList(args []Expr) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		parts = append(parts, e.emitExpr(a))
	}
	return strings.Join(parts, ", ")
}

// emitExpr translates an expression to its Lua text form.
func (e *emitter) emitExpr(x Expr) string {
	switch n := x.(type) {
	case *IntLit:
		return n.Value
	case *RealLit:
		return n.Value
	case *StringLit:
		return luaStringLit(n.Value)
	case *BoolLit:
		if n.Value {
			return "true"
		}
		return "false"
	case *NullLit:
		return "nil"
	case *RawLit:
		// FourCC literal — WC3-Lua standard shim.
		return fmt.Sprintf(`FourCC(%q)`, n.Value)
	case *Ident:
		return n.Name
	case *IndexExpr:
		return e.emitExpr(n.Base) + "[" + e.emitExpr(n.Index) + "]"
	case *CallExpr:
		return fmt.Sprintf("%s(%s)", n.Func, e.emitArgList(n.Args))
	case *MemberExpr:
		// Field access — Lua dot syntax (Colon is call-only and ignored
		// here; the parser sets Colon=false for bare field access).
		return e.emitExpr(n.Base) + "." + n.Member
	case *MemberCallExpr:
		sep := "."
		if n.Colon {
			sep = ":"
		}
		return fmt.Sprintf("%s%s%s(%s)", e.emitExpr(n.Recv), sep, n.Method, e.emitArgList(n.Args))
	case *UnaryExpr:
		op := n.Op
		if op == "not" {
			return "not " + e.emitExprWithParens(n.X)
		}
		return op + e.emitExprWithParens(n.X)
	case *BinaryExpr:
		// String concat heuristic: `+` on string literals becomes `..`. We
		// only have a literal-side hint to go on (no type system), so:
		// if either side is a StringLit, emit `..`. Otherwise, emit `+`
		// and let Lua surface the mismatch at runtime if needed.
		op := n.Op
		if op == "+" && (isStringExpr(n.L) || isStringExpr(n.R)) {
			op = ".."
		} else {
			op = mapBinaryOp(op)
		}
		return e.emitExprWithParens(n.L) + " " + op + " " + e.emitExprWithParens(n.R)
	}
	return "--[[expr?]]"
}

// emitExprWithParens conservatively parenthesizes nested binary expressions
// so the emitted Lua keeps source precedence. Leaf nodes don't need them.
func (e *emitter) emitExprWithParens(x Expr) string {
	switch x.(type) {
	case *BinaryExpr:
		return "(" + e.emitExpr(x) + ")"
	}
	return e.emitExpr(x)
}

// isStringExpr is the heuristic for the `+` → `..` concat rewrite. Recursive
// into Binary so chains of string concats route through `..`.
func isStringExpr(x Expr) bool {
	switch n := x.(type) {
	case *StringLit:
		return true
	case *BinaryExpr:
		return n.Op == "+" && (isStringExpr(n.L) || isStringExpr(n.R))
	}
	return false
}

// mapBinaryOp translates JASS comparison operators to Lua. JASS uses `!=`;
// Lua uses `~=`. Everything else is identical.
func mapBinaryOp(op string) string {
	if op == "!=" {
		return "~="
	}
	return op
}

// luaStringLit double-quotes a Lua string literal, escaping the bytes Lua
// requires (\, ", \n, \r, \t).
func luaStringLit(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// ---------------------------------------------------------------------------
// vJASS detection — exported helpers used by triggers_convert.go's refined
// blocker check.
// ---------------------------------------------------------------------------

// vJASSKeywords is the set of vJASS-only keywords whose presence in a script
// indicates the source needs the JassHelper preprocessor before it can be
// transpiled. Maps detected keyword → human-readable label for the blocker
// reason.
//
// NOTE: textmacro / endtextmacro / runtextmacro / runtextmacroonce live in
// `//!` comment lines and are consumed by the Preprocess pass (preprocess.go)
// BEFORE this scan runs. They're intentionally absent from this list so a
// textmacro-only map (Phase 1 of vJASS support) flows through cleanly.
//
// Phase 2 added the library/scope preprocessor (PreprocessLibScope) which
// runs AFTER textmacro expansion. It consumes library / endlibrary / scope /
// endscope plus the requires/needs/uses/initializer/optional keywords AND
// strips private/public visibility prefixes from inner decls.
//
// Phase 3 added the struct preprocessor (PreprocessStructs) which strips
// struct/endstruct blocks (with method/endmethod, static, delegate, readonly,
// thistype, onInit inside them) and replaces each with a marker comment that
// the codegen splices the emitted Lua at.
//
// Phase 4 added module / interface / define handling. The keyword scan now
// has no remaining blockers — every known vJASS construct is handled by a
// preprocessor pass. The list is kept (currently empty) as a safety net: if
// a future map uses a JassHelper construct we don't yet know about, adding
// it here is the gate.
var vJASSKeywords = []string{}

// FindVJASSKeyword scans the source for the first vJASS-only keyword. Returns
// the matched keyword and true if found; "" / false otherwise. Word-boundary
// match using the lexer so quoted strings and comments don't trip it.
//
// As of Phase 4 the keyword list is empty — every known vJASS construct has
// a preprocessor pass. The function still exists for a future safety-net
// keyword to be added without rippling through callers.
func FindVJASSKeyword(src string) (string, bool) {
	if len(vJASSKeywords) == 0 {
		return "", false
	}
	toks, err := Tokenize(src)
	if err != nil {
		// Lex failed — be conservative; report no keyword (the convert flow
		// can fall back to "unparseable as JASS" via the transpiler).
		return "", false
	}
	set := make(map[string]bool, len(vJASSKeywords))
	for _, k := range vJASSKeywords {
		set[k] = true
	}
	for _, t := range toks {
		// Treat both ident-shaped tokens (since these are NOT in pureKeywords)
		// and explicit keyword tokens uniformly — any of those would have
		// classified as TokIdent in our lexer (vJASS keywords aren't in the
		// pureKeywords map).
		if (t.Kind == TokIdent || t.Kind == TokKeyword) && set[t.Value] {
			return t.Value, true
		}
	}
	return "", false
}
