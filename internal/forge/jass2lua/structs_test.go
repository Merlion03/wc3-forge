package jass2lua

import (
	"strings"
	"testing"
)

// TestPreprocessStructs_PassThrough: source with no struct blocks round-trips
// unchanged + empty results.
func TestPreprocessStructs_PassThrough(t *testing.T) {
	src := `function Foo takes nothing returns nothing
    call BJDebugMsg("hi")
endfunction
`
	res := PreprocessStructs(src)
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %+v", res.Errors)
	}
	if len(res.Structs) != 0 {
		t.Errorf("unexpected structs: %+v", res.Structs)
	}
	if res.Expanded != src {
		t.Errorf("expected pass-through, got:\n%s", res.Expanded)
	}
}

// TestPreprocessStructs_MinimalField: one field + one method.
func TestPreprocessStructs_MinimalField(t *testing.T) {
	src := `struct Foo
    integer x = 0
    method greet takes nothing returns nothing
        call BJDebugMsg("hi")
    endmethod
endstruct
`
	res := PreprocessStructs(src)
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %+v", res.Errors)
	}
	if len(res.Structs) != 1 {
		t.Fatalf("expected 1 struct, got %d", len(res.Structs))
	}
	s, ok := res.Structs["Foo"]
	if !ok {
		t.Fatalf("expected struct Foo, got %v", StructNamesSorted(res))
	}
	if len(s.Fields) != 1 || s.Fields[0].Name != "x" || s.Fields[0].Type != "integer" || s.Fields[0].Default != "0" {
		t.Errorf("unexpected fields: %+v", s.Fields)
	}
	if len(s.Methods) != 1 || s.Methods[0].Name != "greet" || s.Methods[0].Static {
		t.Errorf("unexpected methods: %+v", s.Methods)
	}
	// Marker emitted.
	if !strings.Contains(res.Expanded, "__VJASS_STRUCT__ Foo") {
		t.Errorf("expected marker comment in expanded, got:\n%s", res.Expanded)
	}
	// struct/endstruct keywords stripped.
	for _, line := range strings.Split(res.Expanded, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "endstruct" || strings.HasPrefix(trim, "struct ") {
			t.Errorf("expected struct keywords stripped, found: %q", line)
		}
	}
}

// TestPreprocessStructs_Inheritance: `extends Bar` → emitted Lua uses
// `setmetatable({}, {__index = Bar})`.
func TestPreprocessStructs_Inheritance(t *testing.T) {
	src := `struct Foo extends Bar
    integer y = 1
endstruct
`
	res := PreprocessStructs(src)
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %+v", res.Errors)
	}
	s := res.Structs["Foo"]
	if s.Extends != "Bar" {
		t.Errorf("expected Extends=Bar, got %q", s.Extends)
	}
	lua := EmitStructLua(s)
	if !strings.Contains(lua, "setmetatable({}, {__index = Bar})") {
		t.Errorf("expected metatable inheritance, got:\n%s", lua)
	}
}

// TestPreprocessStructs_StaticFields: static field becomes class-level Lua.
func TestPreprocessStructs_StaticFields(t *testing.T) {
	src := `struct Foo
    static integer count = 0
    integer instance_x = 0
endstruct
`
	res := PreprocessStructs(src)
	s := res.Structs["Foo"]
	if len(s.Statics) != 1 || s.Statics[0].Name != "count" {
		t.Errorf("expected one static `count`, got: %+v", s.Statics)
	}
	if len(s.Fields) != 1 || s.Fields[0].Name != "instance_x" {
		t.Errorf("expected one instance `instance_x`, got: %+v", s.Fields)
	}
	lua := EmitStructLua(s)
	if !strings.Contains(lua, "Foo.count = 0") {
		t.Errorf("expected static field assignment, got:\n%s", lua)
	}
	// Instance field appears in allocate() table.
	if !strings.Contains(lua, "instance_x = 0") {
		t.Errorf("expected instance field default in allocate(), got:\n%s", lua)
	}
}

// TestPreprocessStructs_StaticMethod: dot-call syntax for static methods.
func TestPreprocessStructs_StaticMethod(t *testing.T) {
	src := `struct Foo
    static method staticOp takes integer x returns integer
        return x + 1
    endmethod
endstruct
`
	res := PreprocessStructs(src)
	s := res.Structs["Foo"]
	if len(s.Methods) != 1 || !s.Methods[0].Static {
		t.Fatalf("expected one static method, got: %+v", s.Methods)
	}
	lua := EmitStructLua(s)
	if !strings.Contains(lua, "function Foo.staticOp(x)") {
		t.Errorf("expected dot-call static method signature, got:\n%s", lua)
	}
}

// TestPreprocessStructs_UserCreate: user-defined `create` is preserved; no
// auto-synthesis.
func TestPreprocessStructs_UserCreate(t *testing.T) {
	src := `struct Foo
    integer x = 0
    static method create takes integer initial returns Foo
        local Foo self = Foo.allocate()
        set self.x = initial
        return self
    endmethod
endstruct
`
	res := PreprocessStructs(src)
	s := res.Structs["Foo"]
	if !s.HasCreate {
		t.Errorf("expected HasCreate=true")
	}
	lua := EmitStructLua(s)
	// User's create body should appear; the auto-synthesized one-liner
	// should NOT.
	if !strings.Contains(lua, "function Foo.create(initial)") {
		t.Errorf("expected user create signature, got:\n%s", lua)
	}
	// Count occurrences of `function Foo.create` — should be exactly 1.
	if c := strings.Count(lua, "function Foo.create"); c != 1 {
		t.Errorf("expected exactly 1 create definition, got %d:\n%s", c, lua)
	}
}

// TestPreprocessStructs_AutoCreate: without a user `create`, one is
// synthesized.
func TestPreprocessStructs_AutoCreate(t *testing.T) {
	src := `struct Foo
    integer x = 0
endstruct
`
	res := PreprocessStructs(src)
	s := res.Structs["Foo"]
	if s.HasCreate {
		t.Errorf("expected HasCreate=false for no-user-create struct")
	}
	lua := EmitStructLua(s)
	if !strings.Contains(lua, "function Foo.create()") {
		t.Errorf("expected auto-synthesized Foo.create(), got:\n%s", lua)
	}
}

// TestPreprocessStructs_OnInit: `static method onInit` populates Inits.
func TestPreprocessStructs_OnInit(t *testing.T) {
	src := `struct Foo
    static method onInit takes nothing returns nothing
        call BJDebugMsg("Foo loaded")
    endmethod
endstruct
`
	res := PreprocessStructs(src)
	if len(res.Inits) != 1 {
		t.Fatalf("expected 1 onInit, got %d: %+v", len(res.Inits), res.Inits)
	}
	if res.Inits[0].StructName != "Foo" || res.Inits[0].InitMethod != "onInit" {
		t.Errorf("expected (Foo, onInit), got %+v", res.Inits[0])
	}
}

// TestPreprocessStructs_ThistypeSubstitution: `thistype` inside a method body
// becomes the struct name.
func TestPreprocessStructs_ThistypeSubstitution(t *testing.T) {
	src := `struct Foo
    static method make takes nothing returns Foo
        return thistype.allocate()
    endmethod
endstruct
`
	res := PreprocessStructs(src)
	s := res.Structs["Foo"]
	lua := EmitStructLua(s)
	if strings.Contains(lua, "thistype") {
		t.Errorf("expected thistype substituted, got:\n%s", lua)
	}
	if !strings.Contains(lua, "Foo.allocate()") {
		t.Errorf("expected Foo.allocate() in emitted method, got:\n%s", lua)
	}
}

// TestPreprocessStructs_ThisToSelf: `this.x` inside a method body becomes
// `self.x`.
func TestPreprocessStructs_ThisToSelf(t *testing.T) {
	src := `struct Foo
    integer x = 0
    method bump takes nothing returns nothing
        set this.x = this.x + 1
    endmethod
endstruct
`
	res := PreprocessStructs(src)
	s := res.Structs["Foo"]
	lua := EmitStructLua(s)
	if strings.Contains(lua, "this.x") {
		t.Errorf("expected `this.x` rewritten to `self.x`, got:\n%s", lua)
	}
	if !strings.Contains(lua, "self.x") {
		t.Errorf("expected `self.x` in emitted method, got:\n%s", lua)
	}
}

// TestPreprocessStructs_MultipleMethods: instance + static methods coexist.
func TestPreprocessStructs_MultipleMethods(t *testing.T) {
	src := `struct Foo
    method instanceM takes nothing returns nothing
    endmethod
    static method staticM takes nothing returns nothing
    endmethod
endstruct
`
	res := PreprocessStructs(src)
	s := res.Structs["Foo"]
	if len(s.Methods) != 2 {
		t.Fatalf("expected 2 methods, got %d", len(s.Methods))
	}
	lua := EmitStructLua(s)
	if !strings.Contains(lua, "function Foo:instanceM(") {
		t.Errorf("expected instance method colon syntax, got:\n%s", lua)
	}
	if !strings.Contains(lua, "function Foo.staticM(") {
		t.Errorf("expected static method dot syntax, got:\n%s", lua)
	}
}

// TestPreprocessStructs_EmptyStruct: struct with no fields/methods still
// produces a valid Lua snippet (with synthesized create + destroy).
func TestPreprocessStructs_EmptyStruct(t *testing.T) {
	src := `struct Foo
endstruct
`
	res := PreprocessStructs(src)
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors for empty struct: %+v", res.Errors)
	}
	s := res.Structs["Foo"]
	lua := EmitStructLua(s)
	if !strings.Contains(lua, "function Foo.allocate()") {
		t.Errorf("expected synthesized allocate, got:\n%s", lua)
	}
	if !strings.Contains(lua, "function Foo.create()") {
		t.Errorf("expected synthesized create, got:\n%s", lua)
	}
	if !strings.Contains(lua, "function Foo:destroy()") {
		t.Errorf("expected synthesized destroy, got:\n%s", lua)
	}
}

// TestPreprocessStructs_ExtendsArrayWarning: `extends array` produces a
// warning + treated as plain struct.
func TestPreprocessStructs_ExtendsArrayWarning(t *testing.T) {
	src := `struct Foo extends array
    integer x
endstruct
`
	res := PreprocessStructs(src)
	if len(res.Errors) == 0 {
		t.Errorf("expected a warning for extends array")
	}
	gotWarn := false
	for _, e := range res.Errors {
		if strings.Contains(e.Message, "extends array") {
			gotWarn = true
			break
		}
	}
	if !gotWarn {
		t.Errorf("expected an `extends array` warning in: %+v", res.Errors)
	}
	s := res.Structs["Foo"]
	if s.Extends != "" {
		t.Errorf("expected Extends cleared for `extends array`, got %q", s.Extends)
	}
}

// TestPreprocessStructs_DelegateWarning: `delegate` member produces a warning.
func TestPreprocessStructs_DelegateWarning(t *testing.T) {
	src := `struct Foo
    delegate Bar parent
endstruct
`
	res := PreprocessStructs(src)
	gotWarn := false
	for _, e := range res.Errors {
		if strings.Contains(e.Message, "delegate") {
			gotWarn = true
			break
		}
	}
	if !gotWarn {
		t.Errorf("expected a `delegate` warning in: %+v", res.Errors)
	}
	s := res.Structs["Foo"]
	if len(s.Fields) != 1 || !s.Fields[0].Delegate {
		t.Errorf("expected one delegate field, got: %+v", s.Fields)
	}
}

// TestPreprocessStructs_ReadonlyAllowed: `readonly` member is parsed
// without warning and behaves like a normal field.
func TestPreprocessStructs_ReadonlyAllowed(t *testing.T) {
	src := `struct Foo
    readonly integer id
endstruct
`
	res := PreprocessStructs(src)
	if len(res.Errors) != 0 {
		t.Errorf("expected no errors for readonly, got: %+v", res.Errors)
	}
	s := res.Structs["Foo"]
	if len(s.Fields) != 1 || !s.Fields[0].Readonly {
		t.Errorf("expected one readonly field, got: %+v", s.Fields)
	}
}

// TestPreprocessStructs_DotVsColonDisambiguation: outside-struct method-call
// rewriting. `Foo.method()` stays dot; `instance.method()` becomes colon.
func TestPreprocessStructs_DotVsColonDisambiguation(t *testing.T) {
	src := `struct Foo
    method greet takes nothing returns nothing
    endmethod
endstruct

function caller takes nothing returns nothing
    local Foo inst = Foo.create()
    call Foo.create()
    call inst.greet()
endfunction
`
	res := PreprocessStructs(src)
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %+v", res.Errors)
	}
	// Foo.create stays dot (Foo is a known struct).
	if !strings.Contains(res.Expanded, "Foo.create()") {
		t.Errorf("expected `Foo.create()` dot preserved, got:\n%s", res.Expanded)
	}
	// inst.greet becomes inst:greet (instance receiver).
	if !strings.Contains(res.Expanded, "inst:greet()") {
		t.Errorf("expected `inst.greet()` rewritten to `inst:greet()`, got:\n%s", res.Expanded)
	}
}

// TestSpliceStructLua: marker comments in the transpiled Lua get replaced
// with the actual struct Lua snippet.
func TestSpliceStructLua(t *testing.T) {
	src := `struct Foo
    integer x = 0
endstruct
`
	res := PreprocessStructs(src)
	// The marker would have been transpiled to `-- __VJASS_STRUCT__ Foo`.
	lua := "before\n" + StructLuaMarker + "Foo\nafter\n"
	out := SpliceStructLua(lua, res)
	if strings.Contains(out, StructLuaMarker+"Foo") {
		t.Errorf("expected marker replaced, still present:\n%s", out)
	}
	if !strings.Contains(out, "function Foo.allocate()") {
		t.Errorf("expected splice to include allocate(), got:\n%s", out)
	}
}

// TestPreprocessStructs_TwoStructsOrder: source order is preserved in
// StructOrder.
func TestPreprocessStructs_TwoStructsOrder(t *testing.T) {
	src := `struct A
endstruct

struct B
endstruct
`
	res := PreprocessStructs(src)
	if len(res.StructOrder) != 2 {
		t.Fatalf("expected 2 structs, got %d", len(res.StructOrder))
	}
	if res.StructOrder[0] != "A" || res.StructOrder[1] != "B" {
		t.Errorf("expected source order [A, B], got %v", res.StructOrder)
	}
}

// TestPreprocessStructs_MissingEndstruct: warning emitted, body still
// captured for what we got.
func TestPreprocessStructs_MissingEndstruct(t *testing.T) {
	src := `struct Foo
    integer x = 0
`
	res := PreprocessStructs(src)
	if len(res.Errors) == 0 {
		t.Errorf("expected missing-endstruct warning")
	}
	if _, ok := res.Structs["Foo"]; !ok {
		t.Errorf("expected partial struct still registered")
	}
}

// TestPreprocessStructs_NoFalseTripOnStringStruct: a function body that has
// the word "struct" inside a string literal must NOT be parsed as a struct
// block.
func TestPreprocessStructs_NoFalseTripOnStringStruct(t *testing.T) {
	src := `function F takes nothing returns nothing
    call BJDebugMsg("struct Foo")
endfunction
`
	res := PreprocessStructs(src)
	if len(res.Structs) != 0 {
		t.Errorf("expected no structs parsed (string literal), got: %+v", res.Structs)
	}
	if res.Expanded != src {
		t.Errorf("expected pass-through, got:\n%s", res.Expanded)
	}
}
