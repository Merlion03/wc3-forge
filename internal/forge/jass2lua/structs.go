package jass2lua

// structs.go — vJASS struct preprocessor (Phase 3 of vJASS support).
//
// JassHelper structs are the headliner vJASS feature: an integer-handle-based
// OOP layer over JASS. Each struct gets a hidden allocator that hands out
// unique integer indices; instance "members" are really global arrays indexed
// by those handles; instance methods are functions whose first parameter is
// the handle (implicitly named `this`); inheritance is a single-parent chain.
//
// Source shape:
//
//	struct Foo extends Bar
//	    integer x = 0                    -- instance field (with default)
//	    static integer count = 0         -- static (class-level) field
//	    readonly integer id              -- readonly (ignored — same as field for our purposes)
//	    delegate Bar parent              -- delegate (warning; treated as plain field)
//
//	    method greet takes nothing returns nothing
//	        call BJDebugMsg(I2S(this.x))
//	    endmethod
//
//	    static method foo takes integer y returns integer
//	        return y + 1
//	    endmethod
//
//	    static method create takes integer initial returns Foo
//	        local Foo self = Foo.allocate()
//	        set self.x = initial
//	        return self
//	    endmethod
//
//	    method onDestroy takes nothing returns nothing
//	        call BJDebugMsg("bye")
//	    endmethod
//
//	    static method onInit takes nothing returns nothing
//	        call BJDebugMsg("Foo initialized")
//	    endmethod
//	endstruct
//
// Target Lua mapping (metatable-based OOP):
//
//	-- struct Foo extends Bar
//	Foo = (Bar and setmetatable({}, {__index = Bar})) or {}
//	Foo.__index = Foo
//	Foo.count = 0
//	function Foo.allocate()
//	    return setmetatable({x = 0}, Foo)
//	end
//	function Foo:greet()
//	    BJDebugMsg(I2S(self.x))
//	end
//	function Foo.foo(y)
//	    return y + 1
//	end
//	function Foo.create(initial)
//	    local self = Foo.allocate()
//	    self.x = initial
//	    return self
//	end
//	function Foo:onDestroy()
//	    BJDebugMsg("bye")
//	end
//	function Foo:destroy()
//	    if self.onDestroy then self:onDestroy() end
//	    setmetatable(self, nil)
//	end
//	function Foo.onInit()
//	    BJDebugMsg("Foo initialized")
//	end
//
// Pipeline placement:
//
//	raw JASS
//	    → Preprocess           (textmacro expansion — Phase 1)
//	    → PreprocessLibScope   (library/scope — Phase 2)
//	    → PreprocessStructs    (this file — Phase 3)
//	    → scanVJASS            (module/interface/define gate)
//	    → blocker OR transpile + struct-Lua splice
//
// Strategy: structs DON'T translate to JASS — they translate directly to Lua.
// So this pass:
//  1. Parses every `struct ... endstruct` block, capturing fields/methods.
//  2. STRIPS the entire block from the source and replaces it with a single
//     marker comment line:  `// __VJASS_STRUCT__ Foo`
//  3. Outside struct bodies, rewrites:
//     - `Foo.method(args)` (Foo is a known struct name) — leave alone (Lua
//       handles dot-call for static methods).
//     - `instance.method(args)` (receiver not a struct name) — rewrite to
//       `instance:method(args)` so Lua dispatches via __index.
//     - `instance.field` access on a non-struct-name receiver stays as-is.
//     - `set instance.field = value` — `set` is dropped by the transpiler
//       already; the dot survives. Lua semantics match.
//  4. Inside struct method bodies (which got STRIPPED out of the JASS source),
//     a separate rewrite happens during emit:
//     - `this` → `self`  (Lua self)
//     - `thistype` → containing struct name
//     - `set` prefix dropped (same as top-level rewriting)
//
// The caller (triggers_convert.go) takes the StructDef list from this result,
// asks emitStructDefsLua for the Lua snippets, then splices them at the
// marker positions in the transpiled output. Marker text passes through the
// transpiler verbatim — `// __VJASS_STRUCT__ Foo` becomes `-- __VJASS_STRUCT__
// Foo` in the Lua output, which gives us a deterministic splice anchor.
//
// What's intentionally not supported in v1 (Phase 4 / post-v1):
//   - `extends array` is treated identically to plain struct (auto-allocate).
//     The "array" marker would require user-managed indices and is rare.
//   - `delegate` is treated as a regular member; method-fallthrough is NOT
//     synthesized. A warning is emitted.
//   - `interface` / `module` / `define` — Phase 4.
//   - Struct member access type inference (we can't disambiguate `foo.bar`
//     as static vs instance with perfect accuracy; the heuristic is
//     receiver-name capitalization + matches-known-struct).

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// StructResult is the output of PreprocessStructs.
type StructResult struct {
	// Expanded is the source with all struct blocks stripped, replaced by
	// `// __VJASS_STRUCT__ <Name>` marker comments. References to struct
	// methods outside the bodies are rewritten to colon-syntax where the
	// heuristic identifies an instance receiver.
	Expanded string

	// Structs is the parsed struct definitions, keyed by name. The caller
	// uses these to emit Lua snippets at the marker positions.
	Structs map[string]StructDef

	// StructOrder preserves source-order so the emitted Lua has a stable
	// layout independent of map iteration.
	StructOrder []string

	// Inits is the list of structs with a `static method onInit` — the
	// codegen threads these into _vjass_init_libs() so each Foo.onInit()
	// fires at map load, AFTER library initializers.
	Inits []StructInit

	// Errors is the non-fatal diagnostics bucket (parse errors, unsupported
	// extends-array warning, delegate warning, etc.).
	Errors []StructError
}

// StructDef captures one parsed struct.
type StructDef struct {
	Name     string
	Extends  string // "" for no inheritance; "array" treated as "" + warning
	Fields   []StructField
	Statics  []StructField // static fields
	Methods  []StructMethod
	HasCreate    bool // user defined `static method create` — skip auto-synthesis
	HasOnDestroy bool // user defined `method onDestroy`
	HasDestroy   bool // user defined `method destroy` — skip auto-synthesis
	HasAllocate  bool // user defined `static method allocate` — skip auto-synthesis
	OnInit       string // method name (usually "onInit"); "" if none
	StartLine    int
	EndLine      int
}

// StructField is one instance or static member.
type StructField struct {
	Name     string
	Type     string
	Default  string // raw JASS source for the initializer, "" if none
	Array    bool
	Static   bool
	Delegate bool
	Readonly bool
}

// StructMethod is one instance or static method.
type StructMethod struct {
	Name    string
	Static  bool
	Params  []StructParam
	Returns string // "" for `returns nothing`
	Body    string // raw JASS body bytes (the contents between method/endmethod)
}

// StructParam is one entry of a method's `takes` list.
type StructParam struct {
	Type string
	Name string
}

// StructInit names a struct + its onInit method for codegen threading.
type StructInit struct {
	StructName string
	InitMethod string
}

// StructError is a non-fatal diagnostic surfaced to the UI.
type StructError struct {
	Line    int
	Message string
}

// markerPrefix is the comment marker we splice into the source for each
// struct. The transpiler turns `// ...` into `-- ...` in the Lua output, so
// the splice anchor we look for downstream is `-- __VJASS_STRUCT__ <Name>`.
const structMarkerPrefix = "// __VJASS_STRUCT__ "

// StructLuaMarker is the splice anchor in the post-transpile Lua output.
const StructLuaMarker = "-- __VJASS_STRUCT__ "

// ---------------------------------------------------------------------------
// Regex helpers.
// ---------------------------------------------------------------------------

var (
	structOpenerRe = regexp.MustCompile(`^\s*struct\s+([A-Za-z_][A-Za-z0-9_]*)(?:\s+extends\s+([A-Za-z_][A-Za-z0-9_]*))?\s*$`)
	structCloserRe = regexp.MustCompile(`^\s*endstruct\s*$`)

	// methodOpenerRe matches both instance + static methods.
	// Groups: 1=optional `static`, 2=name, 3=takes-list (or "nothing"),
	//         4=return type (or "nothing").
	methodOpenerRe = regexp.MustCompile(`^\s*(static\s+)?method\s+(?:operator\s+)?([A-Za-z_][A-Za-z0-9_=]*)\s+takes\s+(.+?)\s+returns\s+([A-Za-z_][A-Za-z0-9_]*)\s*$`)
	methodCloserRe = regexp.MustCompile(`^\s*endmethod\s*$`)

	// fieldRe matches a struct field declaration. Groups:
	//   1=optional `static`/`readonly`/`delegate` modifier(s), 2=type, 3=name, 4=optional default.
	// We handle modifiers in the body of the parser since regex can't capture
	// all orderings of an arbitrary multi-modifier prefix cleanly.
	fieldRe = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s+(?:array\s+)?([A-Za-z_][A-Za-z0-9_]*)(?:\s*=\s*(.+))?\s*$`)
)

// ---------------------------------------------------------------------------
// Entry point.
// ---------------------------------------------------------------------------

// PreprocessStructs parses + strips vJASS struct blocks. Call AFTER
// PreprocessLibScope and BEFORE the vJASS keyword gate / transpiler.
//
// Conservative on errors: malformed struct opener, missing endstruct, unknown
// field modifier — every one produces a StructError + a comment marker in
// the output, but processing continues.
func PreprocessStructs(src string) StructResult {
	res := StructResult{
		Structs: map[string]StructDef{},
	}
	lines := splitLinesKeepEnd(src)

	// Pass 1 — extract blocks; replace each block with a marker comment.
	stripped, blocks := extractStructBlocks(lines, &res)

	// Pass 2 — register parsed defs.
	for _, b := range blocks {
		if _, dup := res.Structs[b.Name]; dup {
			res.Errors = append(res.Errors, StructError{
				Line:    b.StartLine,
				Message: fmt.Sprintf("duplicate struct %q; second definition ignored", b.Name),
			})
			continue
		}
		res.Structs[b.Name] = b
		res.StructOrder = append(res.StructOrder, b.Name)
		if b.OnInit != "" {
			res.Inits = append(res.Inits, StructInit{
				StructName: b.Name,
				InitMethod: b.OnInit,
			})
		}
	}

	// Pass 3 — rewrite struct-method-call sites OUTSIDE struct bodies. We
	// tokenize the stripped source and emit dot/colon based on receiver
	// identity. Lex errors degrade to leaving the source alone (the downstream
	// transpiler will surface its own error).
	rewritten, err := rewriteStructRefs(stripped, res.Structs)
	if err != nil {
		res.Errors = append(res.Errors, StructError{
			Line:    0,
			Message: fmt.Sprintf("struct ref-rewrite lex error: %v; leaving source unchanged", err),
		})
		res.Expanded = stripped
		return res
	}
	res.Expanded = rewritten
	return res
}

// EmitStructLua produces the Lua block for one struct. Used by the caller
// (triggers_convert.go) to splice into the transpiled output at each
// `-- __VJASS_STRUCT__ Foo` marker.
//
// The output is a self-contained Lua snippet (no surrounding indentation,
// trailing newline included).
func EmitStructLua(s StructDef) string {
	var b strings.Builder
	emitStructLua(&b, s)
	return b.String()
}

// EmitAllStructLua emits every struct's Lua snippet in StructOrder order.
// Useful for callers that want one big string rather than per-struct splices.
func EmitAllStructLua(res StructResult) string {
	var b strings.Builder
	for _, name := range res.StructOrder {
		s, ok := res.Structs[name]
		if !ok {
			continue
		}
		emitStructLua(&b, s)
		b.WriteByte('\n')
	}
	return b.String()
}

// SpliceStructLua replaces every `-- __VJASS_STRUCT__ <Name>` marker line in
// `lua` with the emitted Lua snippet for that struct. Markers for unknown
// names round-trip unchanged (defensive — shouldn't happen in practice).
//
// The marker line is replaced WHOLE; surrounding lines are preserved
// verbatim. This is the splice anchor the marker-comment strategy relies on.
func SpliceStructLua(lua string, res StructResult) string {
	if len(res.Structs) == 0 {
		return lua
	}
	var out strings.Builder
	out.Grow(len(lua) + 1024)
	lines := splitLinesKeepEnd(lua)
	for _, line := range lines {
		trim := strings.TrimSpace(strings.TrimRight(line, "\r\n"))
		if strings.HasPrefix(trim, StructLuaMarker) {
			name := strings.TrimSpace(strings.TrimPrefix(trim, StructLuaMarker))
			s, ok := res.Structs[name]
			if !ok {
				out.WriteString(line)
				continue
			}
			emitStructLua(&out, s)
			continue
		}
		out.WriteString(line)
	}
	return out.String()
}

// ---------------------------------------------------------------------------
// Pass 1 — block extraction.
// ---------------------------------------------------------------------------

// extractStructBlocks walks `lines`, pulls out every `struct ... endstruct`
// block, and returns (a) the source with each block replaced by a single
// marker comment, and (b) the parsed defs in source order.
//
// Nested structs are NOT supported (vJASS forbids them); an inner `struct`
// inside a struct body falls through as body text and the parser will surface
// a diagnostic.
func extractStructBlocks(lines []string, res *StructResult) (string, []StructDef) {
	var out strings.Builder
	var blocks []StructDef
	i := 0
	for i < len(lines) {
		line := lines[i]
		trim := strings.TrimRight(line, "\r\n")
		m := structOpenerRe.FindStringSubmatch(trim)
		if m == nil {
			out.WriteString(line)
			i++
			continue
		}
		// Found a struct opener. Collect body until endstruct.
		startLine := i + 1
		def := StructDef{
			Name:      m[1],
			Extends:   m[2],
			StartLine: startLine,
		}
		if def.Extends == "array" {
			// `extends array` — treat like plain struct (auto-allocate) +
			// warning. Real-world usage is rare; full semantics (user-managed
			// indices) is Phase 4.
			res.Errors = append(res.Errors, StructError{
				Line:    startLine,
				Message: fmt.Sprintf("struct %q: `extends array` is treated as a normal struct (auto-allocated); user-managed indices not yet supported", def.Name),
			})
			def.Extends = ""
		}
		// Collect body lines + parse them.
		var bodyLines []string
		i++ // past opener
		closed := false
		for i < len(lines) {
			inner := lines[i]
			innerTrim := strings.TrimRight(inner, "\r\n")
			if structCloserRe.MatchString(innerTrim) {
				closed = true
				i++
				break
			}
			bodyLines = append(bodyLines, inner)
			i++
		}
		def.EndLine = i
		if !closed {
			res.Errors = append(res.Errors, StructError{
				Line:    startLine,
				Message: fmt.Sprintf("struct %q: missing `endstruct` before EOF", def.Name),
			})
		}
		parseStructBody(&def, bodyLines, res)
		// Emit the marker comment in place. Use a leading blank line so it's
		// easy to spot in the converted Lua.
		out.WriteString(structMarkerPrefix + def.Name + "\n")
		blocks = append(blocks, def)
	}
	return out.String(), blocks
}

// parseStructBody walks the lines between `struct ... endstruct` and
// populates def.Fields / def.Statics / def.Methods / def.OnInit. Unrecognized
// lines produce a diagnostic but processing continues.
func parseStructBody(def *StructDef, lines []string, res *StructResult) {
	i := 0
	for i < len(lines) {
		line := lines[i]
		trim := strings.TrimRight(line, "\r\n")
		bare := strings.TrimSpace(trim)
		if bare == "" || strings.HasPrefix(bare, "//") {
			i++
			continue
		}
		// Method opener?
		if m := methodOpenerRe.FindStringSubmatch(trim); m != nil {
			method := StructMethod{
				Static: strings.TrimSpace(m[1]) == "static",
				Name:   strings.TrimSpace(m[2]),
			}
			method.Params = parseMethodParams(m[3])
			if m[4] != "nothing" {
				method.Returns = m[4]
			}
			// Collect body until endmethod.
			var body strings.Builder
			i++
			closed := false
			for i < len(lines) {
				inner := lines[i]
				if methodCloserRe.MatchString(strings.TrimRight(inner, "\r\n")) {
					closed = true
					i++
					break
				}
				body.WriteString(inner)
				i++
			}
			if !closed {
				res.Errors = append(res.Errors, StructError{
					Line:    def.StartLine,
					Message: fmt.Sprintf("struct %q method %q: missing `endmethod`", def.Name, method.Name),
				})
			}
			method.Body = body.String()
			def.Methods = append(def.Methods, method)
			switch {
			case method.Static && method.Name == "create":
				def.HasCreate = true
			case method.Static && method.Name == "allocate":
				def.HasAllocate = true
			case !method.Static && method.Name == "onDestroy":
				def.HasOnDestroy = true
			case !method.Static && method.Name == "destroy":
				def.HasDestroy = true
			case method.Static && method.Name == "onInit":
				def.OnInit = "onInit"
			}
			continue
		}
		// Field declaration. Parse leading modifiers manually.
		modStatic := false
		modReadonly := false
		modDelegate := false
		rest := trim
		for {
			b := strings.TrimSpace(rest)
			if strings.HasPrefix(b, "static ") {
				modStatic = true
				rest = strings.TrimPrefix(b, "static ")
				continue
			}
			if strings.HasPrefix(b, "readonly ") {
				modReadonly = true
				rest = strings.TrimPrefix(b, "readonly ")
				continue
			}
			if strings.HasPrefix(b, "delegate ") {
				modDelegate = true
				rest = strings.TrimPrefix(b, "delegate ")
				continue
			}
			rest = b
			break
		}
		fm := fieldRe.FindStringSubmatch(rest)
		if fm == nil {
			res.Errors = append(res.Errors, StructError{
				Line:    def.StartLine + i,
				Message: fmt.Sprintf("struct %q: unrecognized body line %q", def.Name, bare),
			})
			i++
			continue
		}
		field := StructField{
			Type:     fm[1],
			Name:     fm[2],
			Default:  strings.TrimSpace(fm[3]),
			Array:    strings.Contains(rest, "array "),
			Static:   modStatic,
			Readonly: modReadonly,
			Delegate: modDelegate,
		}
		if modDelegate {
			res.Errors = append(res.Errors, StructError{
				Line:    def.StartLine + i,
				Message: fmt.Sprintf("struct %q field %q: `delegate` is treated as a plain field; method-fallthrough not synthesized (Phase 4)", def.Name, field.Name),
			})
		}
		if modStatic {
			def.Statics = append(def.Statics, field)
		} else {
			def.Fields = append(def.Fields, field)
		}
		i++
	}
}

// parseMethodParams parses a method's `takes` clause. JASS supports
// `nothing` (no params) and a comma-separated list of `<type> <name>` pairs.
func parseMethodParams(raw string) []StructParam {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "nothing" {
		return nil
	}
	var out []StructParam
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		fields := strings.Fields(p)
		if len(fields) < 2 {
			continue
		}
		// Last field is the param name; everything before is the type
		// (JASS doesn't allow multi-word types, but `code` etc. are single
		// tokens; this stays simple).
		name := fields[len(fields)-1]
		typ := strings.Join(fields[:len(fields)-1], " ")
		out = append(out, StructParam{Type: typ, Name: name})
	}
	return out
}

// ---------------------------------------------------------------------------
// Pass 3 — outside-struct reference rewrite (dot vs colon disambiguation).
// ---------------------------------------------------------------------------

// rewriteStructRefs tokenizes the source and rewrites method calls based on
// receiver identity:
//
//   - `Foo.method(args)` where `Foo` is a known struct name → leave as
//     `Foo.method(args)` (Lua static call).
//   - `something.method(args)` where `something` is NOT a known struct name →
//     rewrite the `.` to `:` so Lua dispatches via __index (instance call).
//
// Field access (`something.field` not followed by `(`) is NOT rewritten —
// Lua's dot works for both static and instance fields.
//
// We use the tokenizer so strings + comments + rawcodes don't get mangled.
// The token stream is walked window-by-window: detect IDENT, DOT, IDENT,
// optional whitespace, LPAREN. For non-struct receivers, swap the DOT for a
// COLON in the output.
func rewriteStructRefs(src string, structs map[string]StructDef) (string, error) {
	if len(structs) == 0 || src == "" {
		return src, nil
	}
	toks, err := Tokenize(src)
	if err != nil {
		return "", err
	}
	// Build a quick lookup of known struct names.
	known := make(map[string]bool, len(structs))
	for name := range structs {
		known[name] = true
	}
	// Find DOTs to swap. We work in a positions list keyed by (line, col)
	// since the tokenizer doesn't emit byte offsets. To avoid an O(n²) walk
	// while applying swaps, we collect the dot positions in source order,
	// then walk the source byte-by-byte applying swaps when we hit the next
	// queued position.
	type swap struct {
		line, col int
	}
	var swaps []swap
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.Kind != TokIdent {
			continue
		}
		// Look ahead for the pattern: IDENT . IDENT (whitespace/EOL between
		// dot and second ident is unusual in JASS but we tolerate it).
		j := i + 1
		if j >= len(toks) {
			break
		}
		if !(toks[j].Kind == TokOp && toks[j].Value == ".") {
			continue
		}
		k := j + 1
		for k < len(toks) && (toks[k].Kind == TokEOL || toks[k].Kind == TokComment) {
			k++
		}
		if k >= len(toks) || toks[k].Kind != TokIdent {
			continue
		}
		// Look ahead for `(` to confirm this is a method call (vs bare field
		// access). Allow whitespace tokens (the lexer skips horizontal ws so
		// there's no whitespace token to step over — just check EOL).
		l := k + 1
		for l < len(toks) && (toks[l].Kind == TokEOL || toks[l].Kind == TokComment) {
			l++
		}
		isMethodCall := l < len(toks) && toks[l].Kind == TokOp && toks[l].Value == "("
		if !isMethodCall {
			continue
		}
		// Receiver name (t.Value): if it's a known struct, leave the dot. If
		// it's `thistype` (rare outside method bodies) leave alone (it'd be
		// rewritten by the struct method body emit path anyway). Otherwise
		// swap the dot for a colon.
		recv := t.Value
		if known[recv] {
			continue
		}
		// Skip rewrites on common false-positives: keywords-like identifiers
		// such as a struct's static name appearing on the LHS (`Foo` filtered
		// above already), or built-in identifiers we know aren't structs.
		// We're permissive here; the receiver heuristic is "anything that
		// looks like an instance variable".
		swaps = append(swaps, swap{line: toks[j].Line, col: toks[j].Col})
		// Advance past the consumed window minus 1 (the outer loop's i++
		// steps to t+1, but the next ident could itself participate as
		// receiver, so we don't skip ahead).
	}
	if len(swaps) == 0 {
		return src, nil
	}
	// Apply swaps by walking the source byte-by-byte and tracking line/col.
	var out strings.Builder
	out.Grow(len(src))
	swapIdx := 0
	line, col := 1, 1
	for pos := 0; pos < len(src); pos++ {
		c := src[pos]
		if swapIdx < len(swaps) && swaps[swapIdx].line == line && swaps[swapIdx].col == col {
			if c == '.' {
				out.WriteByte(':')
				swapIdx++
				// Advance position counter.
				col++
				continue
			}
			// Mis-located swap (e.g. token-position drift). Skip it.
			swapIdx++
		}
		out.WriteByte(c)
		if c == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return out.String(), nil
}

// ---------------------------------------------------------------------------
// Lua emission.
// ---------------------------------------------------------------------------

// emitStructLua writes the Lua snippet for one struct into b.
func emitStructLua(b *strings.Builder, s StructDef) {
	// Header comment + class table init.
	if s.Extends != "" {
		fmt.Fprintf(b, "-- struct %s extends %s\n", s.Name, s.Extends)
		fmt.Fprintf(b, "%s = (%s and setmetatable({}, {__index = %s})) or {}\n", s.Name, s.Extends, s.Extends)
	} else {
		fmt.Fprintf(b, "-- struct %s\n", s.Name)
		fmt.Fprintf(b, "%s = %s or {}\n", s.Name, s.Name)
	}
	fmt.Fprintf(b, "%s.__index = %s\n", s.Name, s.Name)

	// Static fields (class-level).
	for _, f := range s.Statics {
		init := defaultForStructField(f)
		fmt.Fprintf(b, "%s.%s = %s\n", s.Name, f.Name, init)
	}

	// allocate(): always synthesized unless the user defined one.
	if !s.HasAllocate {
		b.WriteString(fmt.Sprintf("function %s.allocate()\n", s.Name))
		b.WriteString("\treturn setmetatable({")
		first := true
		for _, f := range s.Fields {
			if !first {
				b.WriteString(", ")
			}
			first = false
			fmt.Fprintf(b, "%s = %s", f.Name, defaultForStructField(f))
		}
		fmt.Fprintf(b, "}, %s)\n", s.Name)
		b.WriteString("end\n")
	}

	// User-defined methods.
	for _, m := range s.Methods {
		emitStructMethod(b, s, m)
	}

	// Auto-synthesize `create` if user didn't define one. The synthesized
	// create takes no args and just calls allocate.
	if !s.HasCreate {
		fmt.Fprintf(b, "function %s.create()\n", s.Name)
		fmt.Fprintf(b, "\treturn %s.allocate()\n", s.Name)
		b.WriteString("end\n")
	}

	// Auto-synthesize `destroy` if user didn't define one. The synthesized
	// destroy calls onDestroy (if defined on the instance) then clears the
	// metatable to release references.
	if !s.HasDestroy {
		fmt.Fprintf(b, "function %s:destroy()\n", s.Name)
		b.WriteString("\tif self.onDestroy then self:onDestroy() end\n")
		b.WriteString("\tsetmetatable(self, nil)\n")
		b.WriteString("end\n")
	}
}

// emitStructMethod writes one method as a Lua function. Instance methods use
// colon syntax (`function Foo:bar(...)`) so `self` is the implicit first arg;
// static methods use dot syntax (`function Foo.bar(...)`).
//
// The body is the raw JASS method body. We transpile it through the same
// jass2lua pipeline (re-wrapping as a function so the parser is happy), then
// extract the body lines. `this` / `thistype` rewrites are applied to the
// raw body BEFORE transpiling so the body looks like plain JASS to the
// transpiler.
func emitStructMethod(b *strings.Builder, s StructDef, m StructMethod) {
	body := rewriteMethodBody(s.Name, m)
	// Build a JASS function wrapper around the body so the transpiler treats
	// it as a function decl. Instance methods get `self` as the first param
	// (we'll strip it from the signature when emitting Lua); static methods
	// just use the params verbatim.
	var params []string
	if !m.Static {
		// For instance methods we prepend a `self` synthetic param so the
		// transpiler sees it in scope. We'll emit `function Foo:method(...)`
		// without the self in the signature.
		params = append(params, "self")
	}
	for _, p := range m.Params {
		params = append(params, p.Name)
	}
	// Emit signature.
	if m.Static {
		fmt.Fprintf(b, "function %s.%s(%s)\n", s.Name, m.Name, strings.Join(params, ", "))
	} else {
		var sigParams []string
		for _, p := range m.Params {
			sigParams = append(sigParams, p.Name)
		}
		fmt.Fprintf(b, "function %s:%s(%s)\n", s.Name, m.Name, strings.Join(sigParams, ", "))
	}
	// Transpile the body. We wrap it as a function so the JASS parser
	// accepts the statement list, then extract the body lines.
	lua := transpileMethodBody(body)
	// Indent each non-empty line by one tab.
	for _, line := range splitLinesKeepEnd(lua) {
		if strings.TrimSpace(line) == "" {
			b.WriteString(line)
			continue
		}
		b.WriteByte('\t')
		b.WriteString(line)
	}
	if !strings.HasSuffix(lua, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("end\n")
}

// rewriteMethodBody applies struct-body-specific token rewrites to the raw
// JASS method body before it's handed to the transpiler:
//
//   - `thistype` → struct name (the containing struct)
//   - `this`     → `self` (so the transpiled Lua matches the colon-syntax
//     implicit `self` arg)
//
// We use the tokenizer so strings/comments aren't touched. On lex error we
// fall back to the original body (the downstream transpiler will surface
// its own error).
func rewriteMethodBody(structName string, m StructMethod) string {
	if m.Body == "" {
		return ""
	}
	toks, err := Tokenize(m.Body)
	if err != nil {
		return m.Body
	}
	var out strings.Builder
	out.Grow(len(m.Body))
	pos := 0
	line, col := 1, 1
	for _, t := range toks {
		if t.Kind == TokEOF {
			if pos < len(m.Body) {
				out.WriteString(m.Body[pos:])
			}
			break
		}
		// Advance to token start.
		for pos < len(m.Body) && (line < t.Line || (line == t.Line && col < t.Col)) {
			c := m.Body[pos]
			out.WriteByte(c)
			pos++
			if c == '\n' {
				line++
				col = 1
			} else {
				col++
			}
		}
		srcLen := tokenSourceLen(m.Body, pos, t)
		if srcLen <= 0 {
			srcLen = len(t.Value)
			if srcLen == 0 {
				srcLen = 1
			}
		}
		// Apply rewrites at TokIdent positions.
		if t.Kind == TokIdent {
			switch t.Value {
			case "thistype":
				out.WriteString(structName)
			case "this":
				out.WriteString("self")
			default:
				out.WriteString(m.Body[pos : pos+srcLen])
			}
		} else {
			out.WriteString(m.Body[pos : pos+srcLen])
		}
		for k := 0; k < srcLen && pos < len(m.Body); k++ {
			c := m.Body[pos]
			pos++
			if c == '\n' {
				line++
				col = 1
			} else {
				col++
			}
		}
	}
	if pos < len(m.Body) {
		out.WriteString(m.Body[pos:])
	}
	return out.String()
}

// transpileMethodBody runs the raw (already-rewritten) JASS body through the
// transpiler. We wrap the body as a function decl so the parser is happy,
// then strip the wrapping `function Wrapped()` and `end` lines off the
// result.
func transpileMethodBody(body string) string {
	wrapped := "function __vjass_struct_method takes nothing returns nothing\n" + body + "\nendfunction\n"
	lua, _ := TranspileScript(wrapped)
	// Strip the `function __vjass_struct_method()` opener and matching
	// `end` closer + the trailing blank line.
	lines := strings.Split(lua, "\n")
	var inner []string
	depth := 0
	started := false
	for _, ln := range lines {
		trim := strings.TrimSpace(ln)
		if !started {
			if strings.HasPrefix(trim, "function __vjass_struct_method") {
				started = true
				depth = 1
			}
			continue
		}
		if depth == 1 && trim == "end" {
			depth = 0
			break
		}
		inner = append(inner, ln)
	}
	// Trim leading tab indent the emitter added.
	for i, ln := range inner {
		inner[i] = strings.TrimPrefix(ln, "\t")
	}
	return strings.Join(inner, "\n") + "\n"
}

// defaultForStructField returns the Lua literal to initialize a field. If the
// user wrote `integer x = 5` we use `5`; otherwise we fall back to the JASS
// type's default (0 / 0.0 / "" / false / nil).
func defaultForStructField(f StructField) string {
	if f.Array {
		return "{}"
	}
	if f.Default != "" {
		// Best-effort transpile of the initializer expression. Wrap as
		// `local x = <expr>` to get an expression context, then strip.
		wrapped := "function __vjass_init takes nothing returns nothing\nlocal " + f.Type + " x = " + f.Default + "\nendfunction\n"
		lua, err := TranspileScript(wrapped)
		if err == nil {
			// Pull out the `local x = <expr>` line.
			for _, line := range strings.Split(lua, "\n") {
				trim := strings.TrimSpace(line)
				if strings.HasPrefix(trim, "local x = ") {
					return strings.TrimPrefix(trim, "local x = ")
				}
			}
		}
		// Fallback: emit the raw default. Wrong for some types but visible.
		return f.Default
	}
	return defaultInitForType(f.Type)
}

// ---------------------------------------------------------------------------
// Sorted name iteration for deterministic output (used by tests).
// ---------------------------------------------------------------------------

// StructNamesSorted returns the names in res.Structs sorted alphabetically.
func StructNamesSorted(res StructResult) []string {
	out := make([]string, 0, len(res.Structs))
	for name := range res.Structs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
