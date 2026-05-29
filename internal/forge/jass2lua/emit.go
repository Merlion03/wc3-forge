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
//
// NOTE: this returns a non-nil `err` ONLY for unrecoverable lex errors. Per-
// statement parse failures are recovered into RawStmt placeholders and do NOT
// surface here — callers that need to know how many statements failed must use
// TranspileScriptWithErrors, which returns the structured File.Errors list.
// This signature is preserved so the many existing callers/tests that treat a
// nil err as "clean enough to translate" keep their behavior.
func TranspileScript(jass string) (lua string, err error) {
	lua, _, err = TranspileScriptWithErrors(jass)
	return lua, err
}

// TranspileScriptWithErrors is the structured-diagnostics variant of
// TranspileScript. It returns the emitted Lua, the list of recovered parse
// errors (File.Errors — one entry per statement/top-level node the parser
// could not parse and turned into a RawStmt/error() placeholder), and a
// separate hard lex error.
//
// This is the source of truth for surfacing the REAL failure count to the
// convert dialog: each parse error here corresponds 1:1 to an inline
// `error("jass2lua failed: ...")` marker the emitter writes, so the section's
// reported error count finally matches reality instead of under-reporting to 0.
func TranspileScriptWithErrors(jass string) (lua string, parseErrors []string, lexErr error) {
	toks, err := Tokenize(jass)
	if err != nil {
		// Lex errors can't be recovered (we have no token stream to walk).
		// Surface the whole input as a placeholder so the diff still renders.
		return fmt.Sprintf("--[[ jass2lua lex error: %s ]]\nerror(%q)\n", err.Error(), "jass2lua failed: "+err.Error()), nil, err
	}
	file := Parse(toks)
	em := &emitter{}
	em.emitFile(file)
	return em.b.String(), file.Errors, nil
}

// TranspileFunction translates a JASS fragment for a single trigger's custom_text.
// Many maps' custom_text blocks contain bare statements (no surrounding
// function/endfunction), so this auto-detects the shape:
//
//   - If the input contains a top-level `function` keyword, treat as full
//     script and delegate to TranspileScript.
//   - Otherwise wrap as a body (`function Name() ... end`) named after `name`.
//
// Returns conservative output on errors (same contract as TranspileScript):
// a non-nil err when lexing failed OR when statement-list parsing produced
// recovered soft errors (joined with "; ").
func TranspileFunction(name, jass string) (lua string, err error) {
	lua, parseErrors, lexErr := TranspileFunctionWithErrors(name, jass)
	if lexErr != nil {
		return lua, lexErr
	}
	if len(parseErrors) > 0 {
		return lua, fmt.Errorf("%s", strings.Join(parseErrors, "; "))
	}
	return lua, nil
}

// TranspileFunctionWithErrors is the structured-diagnostics variant of
// TranspileFunction. It returns the emitted Lua, the per-statement parse-error
// list (one entry per RawStmt/error() marker emitted), and a separate hard lex
// error.
//
//   - Full-script fragment (top-level function/globals present) → delegate to
//     TranspileScriptWithErrors so its File.Errors surface too.
//   - Bare-statement fragment → auto-wrap as `function Name() ... end` and
//     collect the statement-list parser's soft errors.
//
// The convert orchestrator uses this so bare-statement custom_text / script
// sections report their REAL failure count (previously these recovered errors
// were collapsed into a single joined string or dropped entirely).
func TranspileFunctionWithErrors(name, jass string) (lua string, parseErrors []string, lexErr error) {
	if hasTopLevelFunction(jass) {
		return TranspileScriptWithErrors(jass)
	}
	// Auto-wrap as function body.
	toks, err := Tokenize(jass)
	if err != nil {
		return fmt.Sprintf("--[[ jass2lua lex error: %s ]]\nfunction %s()\n\terror(%q)\nend\n", err.Error(), sanitizeName(name), "jass2lua failed: "+err.Error()), nil, err
	}
	// Synthesize a wrapping function decl by manual statement-list parse.
	p := &parser{toks: toks}
	stmts, _, _ := p.parseStmtsUntilEOF()
	em := &emitter{}
	em.writeLine(fmt.Sprintf("function %s()", sanitizeName(name)))
	em.indent++
	for _, s := range stmts {
		em.emitStmt(s)
	}
	em.indent--
	em.writeLine("end")
	// Filter empty / whitespace-only entries from the soft-error list —
	// otherwise an upstream bug pushing a blank entry would leak into the
	// transpile-preview UI as a blank diagnostic bullet. Phase 5 fix.
	cleaned := make([]string, 0, len(p.errs))
	for _, msg := range p.errs {
		if strings.TrimSpace(msg) == "" {
			continue
		}
		cleaned = append(cleaned, msg)
	}
	if len(cleaned) > 0 {
		return em.b.String(), cleaned, nil
	}
	return em.b.String(), nil, nil
}

// HasTopLevelDecl reports whether the source contains a top-level JASS
// declaration (function / globals / native). It is the exported form of
// hasTopLevelFunction, used by the convert orchestrator to decide whether a
// "script"-classified trigger body is a real full script (→ TranspileScript)
// or a bare statement list (→ TranspileFunction body-fragment parser). See
// triggers_convert.go's transpileSectionScript (P0#2).
func HasTopLevelDecl(jass string) bool { return hasTopLevelFunction(jass) }

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
		// Bitwise not (`~`) and unary minus emit straight through — Lua 5.3+
		// supports both with identical spelling.
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
	case *TernaryExpr:
		// Lua has no native ternary; lower to `(cond and then) or else`.
		// Documented caveat: if `then` evaluates to `false` or `nil` the
		// idiom returns `else` instead of `then`. JASS has no nil but a
		// boolean `then` is the risky case — append an inline comment when
		// `then` looks like a boolean expression.
		out := "((" + e.emitExpr(n.Cond) + ") and (" + e.emitExpr(n.Then) + ") or (" + e.emitExpr(n.Else) + "))"
		if isLikelyBoolExpr(n.Then) {
			out += " --[[ ternary: `else` fallback unreliable if `then` is false ]]"
		}
		return out
	}
	return "--[[expr?]]"
}

// emitExprWithParens conservatively parenthesizes nested binary / ternary
// expressions so the emitted Lua keeps source precedence. Leaf nodes don't
// need them.
func (e *emitter) emitExprWithParens(x Expr) string {
	switch x.(type) {
	case *BinaryExpr, *TernaryExpr:
		return "(" + e.emitExpr(x) + ")"
	}
	return e.emitExpr(x)
}

// isLikelyBoolExpr is a cheap heuristic the ternary lowering uses to decide
// whether to append the "false-then" caveat comment. Conservative: only known-
// boolean-shaped subtrees flag it.
func isLikelyBoolExpr(x Expr) bool {
	switch n := x.(type) {
	case *BoolLit:
		return true
	case *UnaryExpr:
		return n.Op == "not"
	case *BinaryExpr:
		switch n.Op {
		case "==", "!=", "<", "<=", ">", ">=", "and", "or":
			return true
		}
	}
	return false
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
// Lua uses `~=`. Bitwise operators (Phase 5) pass through mostly identical
// for Lua 5.3+ EXCEPT bitwise XOR: Lua uses `~` (which is also Lua's bitwise
// not unary), whereas vJASS / C-style bitwise XOR is `^`. We translate `^` →
// `~` for binary positions; the parser distinguishes unary-`~` from binary-
// `~` by context so the round-trip stays consistent.
//
// `|` (bitwise or), `&` (bitwise and), `<<` / `>>` (shifts) pass through as-is
// — Lua 5.3+ uses the same spelling.
func mapBinaryOp(op string) string {
	switch op {
	case "!=":
		return "~="
	case "^":
		// JASS `^` (bitwise xor, vJASS extension) → Lua `~` (binary form).
		return "~"
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
