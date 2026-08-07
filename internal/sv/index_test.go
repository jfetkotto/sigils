package sv

import "testing"

func TestIndexLookup(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top;\nendmodule\n")

	locs, ok := ix.Lookup("top")
	if !ok || len(locs) != 1 {
		t.Fatalf("Lookup(top) = %+v, %v", locs, ok)
	}
	if locs[0].URI != "file:///a.sv" || locs[0].Line != 0 || locs[0].Character != 7 {
		t.Fatalf("unexpected location: %+v", locs[0])
	}
	if ix.FileCount() != 1 {
		t.Fatalf("FileCount() = %d, want 1", ix.FileCount())
	}
}

func TestIndexLookupMissing(t *testing.T) {
	ix := NewIndex()
	if _, ok := ix.Lookup("nope"); ok {
		t.Fatalf("expected no result for an unindexed name")
	}
}

func TestIndexSetFileReplacesPriorEntries(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module old_name;\nendmodule\n")
	ix.SetFile("file:///a.sv", "module new_name;\nendmodule\n")

	if _, ok := ix.Lookup("old_name"); ok {
		t.Fatalf("expected old_name to be gone after rescanning the same URI")
	}
	if locs, ok := ix.Lookup("new_name"); !ok || len(locs) != 1 {
		t.Fatalf("Lookup(new_name) = %+v, %v", locs, ok)
	}
}

func TestIndexMultipleFilesSameName(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module dup;\nendmodule\n")
	ix.SetFile("file:///b.sv", "module dup;\nendmodule\n")

	locs, ok := ix.Lookup("dup")
	if !ok || len(locs) != 2 {
		t.Fatalf("Lookup(dup) = %+v, %v; want 2 locations", locs, ok)
	}
}

func TestIndexRemoveFile(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top;\nendmodule\n")
	ix.RemoveFile("file:///a.sv")

	if _, ok := ix.Lookup("top"); ok {
		t.Fatalf("expected top to be gone after RemoveFile")
	}
	if ix.FileCount() != 0 {
		t.Fatalf("FileCount() = %d, want 0", ix.FileCount())
	}
}

func TestIndexRemoveFileOnlyAffectsThatFile(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module shared;\nendmodule\n")
	ix.SetFile("file:///b.sv", "module shared;\nendmodule\n")
	ix.RemoveFile("file:///a.sv")

	locs, ok := ix.Lookup("shared")
	if !ok || len(locs) != 1 || locs[0].URI != "file:///b.sv" {
		t.Fatalf("Lookup(shared) = %+v, %v; want only file:///b.sv", locs, ok)
	}
}

func TestIndexLookupReturnsIndependentCopy(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top;\nendmodule\n")

	locs, _ := ix.Lookup("top")
	locs[0].URI = "mutated"

	fresh, _ := ix.Lookup("top")
	if fresh[0].URI != "file:///a.sv" {
		t.Fatalf("Lookup should return an independent copy, got mutated state: %+v", fresh[0])
	}
}

func TestParamsExcludesLocalParams(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///leaf.sv", "module leaf #(parameter int WIDTH = 8, localparam int FIXED = 1) ();\nendmodule\n")

	params, ok := ix.Params("leaf")
	if !ok || len(params) != 1 || params[0].Name != "WIDTH" {
		t.Fatalf("Params(leaf) = %+v, %v", params, ok)
	}
}

func TestFindInstantiationPortAlsoResolvesParameterOverrides(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///leaf.sv", "module leaf #(parameter int WIDTH = 8) ();\nendmodule\n")

	locs, ok := ix.FindInstantiationPort("leaf", "WIDTH")
	if !ok || len(locs) != 1 || locs[0].Kind != KindParameter {
		t.Fatalf("FindInstantiationPort(leaf, WIDTH) = %+v, %v", locs, ok)
	}

	decl, ok := ix.InstantiationPortInfo("leaf", "WIDTH")
	if !ok || decl.Kind != KindParameter || decl.Detail != "int" || decl.Default != "8" {
		t.Fatalf("InstantiationPortInfo(leaf, WIDTH) = %+v, %v", decl, ok)
	}
}

func TestScopedOccurrencesForInstantiationConnectionExcludesUnrelatedModule(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///leaf.sv", "module leaf(input logic clk);\nendmodule\n")
	ix.SetFile("file:///leaf2.sv", "module leaf2(input logic clk);\nendmodule\n")
	ix.SetFile("file:///top.sv", "module top;\n  leaf u_leaf(.clk(a));\n  leaf2 u_leaf2(.clk(b));\nendmodule\n")

	locs := ix.ScopedOccurrencesForInstantiationConnection("leaf", "clk")

	foundLeafDecl, foundLeafConn, foundLeaf2Conn := false, false, false
	for _, l := range locs {
		switch {
		case l.URI == "file:///leaf.sv":
			foundLeafDecl = true
		case l.URI == "file:///top.sv" && l.Line == 1:
			foundLeafConn = true
		case l.URI == "file:///top.sv" && l.Line == 2:
			foundLeaf2Conn = true
		}
	}
	if !foundLeafDecl {
		t.Fatalf("expected leaf's own clk declaration in results, got %+v", locs)
	}
	if !foundLeafConn {
		t.Fatalf("expected u_leaf's .clk( connection in results, got %+v", locs)
	}
	if foundLeaf2Conn {
		t.Fatalf("expected u_leaf2's .clk( connection (a different, unrelated module) to be excluded, got %+v", locs)
	}
}

func TestScopedOccurrencesForInstantiationConnectionFindsAllInstantiationsOfSameModule(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///leaf.sv", "module leaf(input logic clk);\nendmodule\n")
	ix.SetFile("file:///top.sv", "module top;\n  leaf u_a(.clk(a));\n  leaf u_b(.clk(b));\nendmodule\n")

	locs := ix.ScopedOccurrencesForInstantiationConnection("leaf", "clk")

	foundA, foundB := false, false
	for _, l := range locs {
		if l.URI != "file:///top.sv" {
			continue
		}
		if l.Line == 1 {
			foundA = true
		}
		if l.Line == 2 {
			foundB = true
		}
	}
	if !foundA || !foundB {
		t.Fatalf("expected both u_a's and u_b's .clk( connections in results, got %+v", locs)
	}
}

func TestScopedOccurrencesForInstantiationConnectionParamOverride(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///leaf.sv", "module leaf #(parameter int WIDTH = 8) ();\nendmodule\n")
	ix.SetFile("file:///leaf2.sv", "module leaf2 #(parameter int WIDTH = 4) ();\nendmodule\n")
	ix.SetFile("file:///top.sv", "module top;\n  leaf #(.WIDTH(16)) u_a();\n  leaf2 #(.WIDTH(2)) u_b();\nendmodule\n")

	locs := ix.ScopedOccurrencesForInstantiationConnection("leaf", "WIDTH")

	foundLeafOverride, foundLeaf2Override := false, false
	for _, l := range locs {
		if l.URI != "file:///top.sv" {
			continue
		}
		if l.Line == 1 {
			foundLeafOverride = true
		}
		if l.Line == 2 {
			foundLeaf2Override = true
		}
	}
	if !foundLeafOverride {
		t.Fatalf("expected leaf's .WIDTH( override in results, got %+v", locs)
	}
	if foundLeaf2Override {
		t.Fatalf("expected leaf2's unrelated .WIDTH( override to be excluded, got %+v", locs)
	}
}

func TestScopedOccurrencesForInstantiationConnectionIncludesLocalOccurrences(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///leaf.sv", "module leaf(input logic clk);\n  always_ff @(posedge clk) begin end\nendmodule\n")
	ix.SetFile("file:///top.sv", "module top;\n  leaf u_leaf(.clk(a));\nendmodule\n")

	locs := ix.ScopedOccurrencesForInstantiationConnection("leaf", "clk")

	foundDecl, foundBodyRef := false, false
	for _, l := range locs {
		if l.URI != "file:///leaf.sv" {
			continue
		}
		if l.Line == 0 {
			foundDecl = true
		}
		if l.Line == 1 {
			foundBodyRef = true
		}
	}
	if !foundDecl || !foundBodyRef {
		t.Fatalf("expected both clk's own declaration and its in-body always_ff reference, got %+v", locs)
	}
}

func TestFindInstantiationPortResolvesToTargetModulePort(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///leaf.sv", "module leaf(input logic clk, output logic [7:0] data);\nendmodule\n")
	ix.SetFile("file:///top.sv", "module top;\n  leaf u_leaf(.clk(sig));\nendmodule\n")

	locs, ok := ix.FindInstantiationPort("leaf", "clk")
	if !ok || len(locs) != 1 {
		t.Fatalf("FindInstantiationPort(leaf, clk) = %+v, %v", locs, ok)
	}
	if locs[0].URI != "file:///leaf.sv" || locs[0].Kind != KindPort {
		t.Fatalf("unexpected location: %+v", locs[0])
	}
}

func TestFindInstantiationPortNoMatchForUnknownPort(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///leaf.sv", "module leaf(input logic clk);\nendmodule\n")

	if _, ok := ix.FindInstantiationPort("leaf", "not_a_port"); ok {
		t.Fatalf("expected no match for a port leaf doesn't have")
	}
}

func TestInstantiationPortInfoReturnsDetail(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///leaf.sv", "module leaf(input logic clk, output logic [7:0] data);\nendmodule\n")

	decl, ok := ix.InstantiationPortInfo("leaf", "data")
	if !ok || decl.Kind != KindPort || decl.Detail != "output logic [7:0]" {
		t.Fatalf("InstantiationPortInfo(leaf, data) = %+v, %v", decl, ok)
	}
}

func TestFindDefinitionQualifiedLookup(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///pkg.sv", `package pkg;
  function void helper();
  endfunction
endpackage
`)

	// Position doesn't matter for a qualified lookup -- resolved purely by
	// qualifier + name, searched globally.
	locs, ok := ix.FindDefinition("file:///anywhere.sv", 0, 0, "helper", "pkg", true)
	if !ok || len(locs) != 1 {
		t.Fatalf("FindDefinition(pkg::helper) = %+v, %v", locs, ok)
	}
	if locs[0].URI != "file:///pkg.sv" || locs[0].Line != 1 {
		t.Fatalf("unexpected location: %+v", locs[0])
	}
}

func TestFindDefinitionQualifiedLookupThroughEnumTypedef(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///pkg.sv", `package pa_pkg;
  typedef enum int {
    S_FOO,
    S_BAR,
    S_BAZ
  } en_States;
endpackage
`)

	// "pa_pkg::en_States::S_BAZ" -- QualifierAt only ever returns the
	// identifier immediately before the final "::" ("en_States"), so the
	// leading "pa_pkg::" never reaches this lookup at all; this is exactly
	// what the LSP layer passes through for that reference (position is
	// irrelevant to a qualified lookup, so a dummy one is used here, same
	// as TestFindDefinitionQualifiedLookup above).
	locs, ok := ix.FindDefinition("file:///anywhere.sv", 0, 0, "S_BAZ", "en_States", true)
	if !ok || len(locs) != 1 {
		t.Fatalf("FindDefinition(en_States::S_BAZ) = %+v, %v", locs, ok)
	}
	if locs[0].URI != "file:///pkg.sv" {
		t.Fatalf("unexpected location: %+v", locs[0])
	}
}

func TestFindDefinitionQualifiedLookupThroughEnumTypedefOnlyMatchesEnumMembers(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///pkg.sv", `package pa_pkg;
  localparam int S_BAZ = 5;
  typedef enum int { S_FOO } en_States;
endpackage
`)

	// en_States doesn't actually declare S_BAZ -- it's an unrelated
	// sibling localparam in the same package -- so this must not resolve,
	// even though a naive "search the typedef's enclosing scope for any
	// same-named sibling" would otherwise find it.
	if _, ok := ix.FindDefinition("file:///anywhere.sv", 0, 0, "S_BAZ", "en_States", true); ok {
		t.Fatalf("expected no match: S_BAZ is not a member of en_States")
	}
}

func TestFindDefinitionQualifiedLookupIgnoresNonContainerQualifier(t *testing.T) {
	ix := NewIndex()
	// "helper" exists, but "not_a_pkg" isn't a class or package, so
	// "not_a_pkg::helper" must not resolve through it.
	ix.SetFile("file:///a.sv", `module not_a_pkg;
  function void helper();
  endfunction
endmodule
`)
	if _, ok := ix.FindDefinition("file:///a.sv", 0, 0, "helper", "not_a_pkg", true); ok {
		t.Fatalf("expected no match: a module is not a valid :: qualifier container")
	}
}

func TestFindDefinitionScopeChainPrefersLocalOverGlobal(t *testing.T) {
	ix := NewIndex()
	classA := `class A;
  function void helper();
  endfunction
endclass
`
	classB := `class B;
  function void helper();
  endfunction
endclass
`
	ix.SetFile("file:///a.sv", classA)
	ix.SetFile("file:///b.sv", classB)

	// From inside class A's body (line 1, inside the function's own
	// header even -- position 0,0 is inside "class A" itself, so use a
	// position on line 1 which is inside class A's span), an unqualified
	// "helper" should resolve to A's helper, not B's, and not both.
	locs, ok := ix.FindDefinition("file:///a.sv", 1, 2, "helper", "", false)
	if !ok || len(locs) != 1 {
		t.Fatalf("FindDefinition(helper) = %+v, %v; want exactly one local match", locs, ok)
	}
	if locs[0].URI != "file:///a.sv" {
		t.Fatalf("expected the local (class A) helper, got %+v", locs[0])
	}
}

func TestFindDefinitionUnqualifiedFunctionDoesNotFallBackAcrossFiles(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///util.sv", "function void util();\nendfunction\n")
	ix.SetFile("file:///main.sv", "module top;\nendmodule\n")

	// "util" is a bare, file-scope function in a different file -- with no
	// qualifier and no local declaration in scope, it must not resolve
	// via the global fallback (functions aren't globally referenceable by
	// bare name without a package/class qualifier or module-local
	// visibility).
	if _, ok := ix.FindDefinition("file:///main.sv", 0, 0, "util", "", false); ok {
		t.Fatalf("expected no cross-file fallback match for a bare function reference")
	}
}

func TestFindDefinitionUnqualifiedModuleFallsBackAcrossFiles(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///leaf.sv", "module leaf;\nendmodule\n")
	ix.SetFile("file:///top.sv", "module top;\n  leaf u_leaf();\nendmodule\n")

	locs, ok := ix.FindDefinition("file:///top.sv", 1, 3, "leaf", "", false)
	if !ok || len(locs) != 1 || locs[0].URI != "file:///leaf.sv" {
		t.Fatalf("FindDefinition(leaf) = %+v, %v", locs, ok)
	}
}

func TestFindDefinitionWalksUpMultipleScopeLevels(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///pkg.sv", `package pkg;
  typedef logic [7:0] byte_t;

  class inner;
    function byte_t get();
    endfunction
  endclass
endpackage
`)

	// Position inside "function byte_t get();" (line 4) -- byte_t isn't
	// declared in the function or the class, only up at the package
	// level, so this only resolves if the scope chain actually walks
	// past the class to the package.
	locs, ok := ix.FindDefinition("file:///pkg.sv", 4, 15, "byte_t", "", false)
	if !ok || len(locs) != 1 {
		t.Fatalf("FindDefinition(byte_t) = %+v, %v", locs, ok)
	}
	if locs[0].Line != 1 {
		t.Fatalf("expected byte_t's declaration at line 1, got %+v", locs[0])
	}
}

func TestFindDeclarationPrefersPrototypeOverBody(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", `class foo;
  extern function void bar();
endclass
function foo::bar();
endfunction
`)

	decl, ok := ix.FindDeclaration("file:///a.sv", 1, 2, "bar", "", false)
	if !ok || len(decl) != 1 {
		t.Fatalf("FindDeclaration = %+v, %v", decl, ok)
	}
	if decl[0].Line != 1 || !decl[0].Prototype {
		t.Fatalf("expected the prototype at line 1, got %+v", decl[0])
	}
}

func TestFindDefinitionPrefersBodyOverPrototype(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", `class foo;
  extern function void bar();
endclass
function foo::bar();
endfunction
`)

	def, ok := ix.FindDefinition("file:///a.sv", 1, 2, "bar", "", false)
	if !ok || len(def) != 1 {
		t.Fatalf("FindDefinition = %+v, %v", def, ok)
	}
	if def[0].Line != 3 || def[0].Prototype {
		t.Fatalf("expected the out-of-line body at line 3, got %+v", def[0])
	}
}

func TestFindDeclarationSameAsDefinitionWhenNoPrototypeExists(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top;\n  function void helper();\n  endfunction\nendmodule\n")

	decl, ok1 := ix.FindDeclaration("file:///a.sv", 1, 3, "helper", "", false)
	def, ok2 := ix.FindDefinition("file:///a.sv", 1, 3, "helper", "", false)
	if !ok1 || !ok2 {
		t.Fatalf("expected both to resolve: decl ok=%v def ok=%v", ok1, ok2)
	}
	if len(decl) != 1 || len(def) != 1 || decl[0] != def[0] {
		t.Fatalf("expected identical results when there's no prototype anywhere, got decl=%+v def=%+v", decl, def)
	}
}

func TestFindDeclarationModuleSameAsDefinition(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///leaf.sv", "module leaf;\nendmodule\n")
	ix.SetFile("file:///top.sv", "module top;\n  leaf u_leaf();\nendmodule\n")

	decl, ok1 := ix.FindDeclaration("file:///top.sv", 1, 3, "leaf", "", false)
	def, ok2 := ix.FindDefinition("file:///top.sv", 1, 3, "leaf", "", false)
	if !ok1 || !ok2 || len(decl) != 1 || len(def) != 1 || decl[0] != def[0] {
		t.Fatalf("expected identical module results, got decl=%+v (%v) def=%+v (%v)", decl, ok1, def, ok2)
	}
}

func TestIndexPorts(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///leaf.sv", "module leaf(input logic clk, input logic rst_n);\nendmodule\n")

	ports, ok := ix.Ports("leaf")
	if !ok || len(ports) != 2 || ports[0].Name != "clk" || ports[1].Name != "rst_n" {
		t.Fatalf("Ports(leaf) = %+v, %v", ports, ok)
	}
}

func TestIndexPortsMissing(t *testing.T) {
	ix := NewIndex()
	if _, ok := ix.Ports("nope"); ok {
		t.Fatalf("expected no ports for an unknown name")
	}
}

func TestIndexPortsIgnoresNonContainerKind(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "typedef logic [7:0] leaf;\n")
	if _, ok := ix.Ports("leaf"); ok {
		t.Fatalf("expected Ports to ignore a typedef sharing a module's name")
	}
}

func TestIndexStructFields(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "typedef struct packed { logic [7:0] a; logic b; } bus_t;\n")

	fields, ok := ix.StructFields("bus_t")
	if !ok || len(fields) != 2 || fields[0].Name != "a" || fields[1].Name != "b" {
		t.Fatalf("StructFields(bus_t) = %+v, %v", fields, ok)
	}
}

func TestIndexStructFieldsMissing(t *testing.T) {
	ix := NewIndex()
	if _, ok := ix.StructFields("nope"); ok {
		t.Fatalf("expected no fields for an unknown name")
	}
}

func TestIndexStructFieldsIgnoresNonStructTypedef(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "typedef logic [7:0] bus_t;\n")
	if _, ok := ix.StructFields("bus_t"); ok {
		t.Fatalf("expected StructFields to ignore a plain alias typedef")
	}
}

func TestIndexStructFieldsAcceptsUnion(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "typedef union packed { logic [7:0] a; logic [7:0] b; } u_t;\n")
	if _, ok := ix.StructFields("u_t"); !ok {
		t.Fatalf("expected StructFields to accept a union typedef")
	}
}

func TestIndexTypedefReturnsFullDeclaration(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "typedef struct packed { logic [7:0] a; } bus_t;\n")

	d, ok := ix.Typedef("bus_t")
	if !ok || d.Kind != KindTypedef || d.TypedefKind != "struct" || len(d.Fields) != 1 {
		t.Fatalf("Typedef(bus_t) = %+v, %v", d, ok)
	}
}

func TestIndexTypedefMissing(t *testing.T) {
	ix := NewIndex()
	if _, ok := ix.Typedef("nope"); ok {
		t.Fatalf("expected no typedef for an unknown name")
	}
}

func TestIndexTypedefAcceptsAliasAndEnum(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "typedef logic [7:0] byte_t;\ntypedef enum {IDLE} state_t;\n")

	if _, ok := ix.Typedef("byte_t"); !ok {
		t.Fatalf("expected Typedef to accept a plain alias typedef")
	}
	if _, ok := ix.Typedef("state_t"); !ok {
		t.Fatalf("expected Typedef to accept an enum typedef")
	}
}

func TestIndexTypedefIgnoresNonTypedefKind(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module bus_t;\nendmodule\n")
	if _, ok := ix.Typedef("bus_t"); ok {
		t.Fatalf("expected Typedef to ignore a module sharing a typedef's name")
	}
}

func TestFindDefinitionNoMatchAnywhere(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top;\nendmodule\n")
	if _, ok := ix.FindDefinition("file:///a.sv", 0, 0, "nope", "", false); ok {
		t.Fatalf("expected no match for an unknown name")
	}
}

func TestCompleteSymbolsFiltersByPrefix(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", `module leaf;
endmodule
class leaf_driver;
endclass
typedef enum {IDLE, RUNNING} state_t;
`)

	got := ix.CompleteSymbols("le")
	if len(got) != 2 || got[0].Name != "leaf" || got[1].Name != "leaf_driver" {
		t.Fatalf("CompleteSymbols(le) = %+v, want [leaf leaf_driver] (sorted)", got)
	}
	if got[0].Kind != KindModule || got[1].Kind != KindClass {
		t.Fatalf("unexpected kinds: %+v", got)
	}
}

func TestCompleteSymbolsEmptyPrefixReturnsEverything(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module a;\nendmodule\nmodule b;\nendmodule\n")
	if got := ix.CompleteSymbols(""); len(got) != 2 {
		t.Fatalf("CompleteSymbols(\"\") = %+v, want 2 symbols", got)
	}
}

func TestCompleteSymbolsNoMatch(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module leaf;\nendmodule\n")
	if got := ix.CompleteSymbols("zzz"); len(got) != 0 {
		t.Fatalf("CompleteSymbols(zzz) = %+v, want none", got)
	}
}

func TestCompleteSymbolsDeduplicatesRepeatedNames(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", `class foo;
  extern function void bar();
endclass
function foo::bar();
endfunction
`)
	got := ix.CompleteSymbols("bar")
	if len(got) != 1 || got[0].Name != "bar" {
		t.Fatalf("CompleteSymbols(bar) = %+v, want exactly one \"bar\" despite the prototype+body", got)
	}
}

func TestCompleteSymbolsIncludesEnumMembers(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "typedef enum {IDLE, RUNNING} state_t;\n")
	got := ix.CompleteSymbols("RUN")
	if len(got) != 1 || got[0].Name != "RUNNING" || got[0].Kind != KindEnumMember {
		t.Fatalf("CompleteSymbols(RUN) = %+v, want [RUNNING (enum_member)]", got)
	}
}

func TestFileDeclarationsReturnsIndependentCopy(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top;\nendmodule\n")

	decls := ix.FileDeclarations("file:///a.sv")
	if len(decls) != 1 || decls[0].Name != "top" {
		t.Fatalf("FileDeclarations = %+v, want [top]", decls)
	}
	decls[0].Name = "mutated"

	fresh := ix.FileDeclarations("file:///a.sv")
	if fresh[0].Name != "top" {
		t.Fatalf("FileDeclarations should return an independent copy, got mutated state: %+v", fresh[0])
	}
}

func TestFileDeclarationsUnknownURI(t *testing.T) {
	ix := NewIndex()
	if decls := ix.FileDeclarations("file:///nope.sv"); len(decls) != 0 {
		t.Fatalf("expected no declarations for an unknown URI, got %+v", decls)
	}
}

func TestIsContainer(t *testing.T) {
	for _, kind := range []Kind{KindModule, KindInterface, KindProgram, KindClass, KindPackage, KindFunction, KindTask} {
		if !IsContainer(kind) {
			t.Fatalf("expected %q to be a container kind", kind)
		}
	}
	for _, kind := range []Kind{KindTypedef, KindEnumMember} {
		if IsContainer(kind) {
			t.Fatalf("expected %q to not be a container kind", kind)
		}
	}
}

func TestWorkspaceSymbolsSubstringMatch(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module leaf_driver;\nendmodule\n")
	ix.SetFile("file:///b.sv", "class other;\nendclass\n")

	got := ix.WorkspaceSymbols("driver")
	if len(got) != 1 || got[0].Name != "leaf_driver" || got[0].URI != "file:///a.sv" {
		t.Fatalf("WorkspaceSymbols(driver) = %+v", got)
	}
}

func TestWorkspaceSymbolsReturnsEveryDeclarationSite(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module dup;\nendmodule\n")
	ix.SetFile("file:///b.sv", "module dup;\nendmodule\n")

	got := ix.WorkspaceSymbols("dup")
	if len(got) != 2 {
		t.Fatalf("WorkspaceSymbols(dup) = %+v, want 2 (not deduplicated, unlike CompleteSymbols)", got)
	}
}

func TestWorkspaceSymbolsCaseInsensitive(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module AXI_master;\nendmodule\n")

	if got := ix.WorkspaceSymbols("axi"); len(got) != 1 || got[0].Name != "AXI_master" {
		t.Fatalf("WorkspaceSymbols(axi) = %+v, want AXI_master", got)
	}
	if got := ix.WorkspaceSymbols("MASTER"); len(got) != 1 {
		t.Fatalf("WorkspaceSymbols(MASTER) = %+v, want AXI_master", got)
	}
}

func TestWorkspaceSymbolsEmptyQueryReturnsEverything(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module a;\nendmodule\nmodule b;\nendmodule\n")
	if got := ix.WorkspaceSymbols(""); len(got) != 2 {
		t.Fatalf("WorkspaceSymbols(\"\") = %+v, want 2", got)
	}
}

func TestFindOccurrences(t *testing.T) {
	text := "module top;\n  leaf u_leaf ();\n  leaf u_leaf2 ();\nendmodule\n"
	occ := FindOccurrences(text, "leaf")
	if len(occ) != 2 {
		t.Fatalf("FindOccurrences(leaf) = %+v, want 2 (the type name, not u_leaf/u_leaf2)", occ)
	}
	if occ[0].Line != 1 || occ[1].Line != 2 {
		t.Fatalf("unexpected occurrence lines: %+v", occ)
	}
}

func TestFindOccurrencesSkipsCommentsAndStrings(t *testing.T) {
	text := "// leaf\nmodule top;\n  initial $display(\"leaf\");\n  leaf u_leaf ();\nendmodule\n"
	occ := FindOccurrences(text, "leaf")
	if len(occ) != 1 || occ[0].Line != 3 {
		t.Fatalf("FindOccurrences(leaf) = %+v, want exactly one match on line 3", occ)
	}
}

func TestFindOccurrencesEmptyName(t *testing.T) {
	if occ := FindOccurrences("module top; endmodule", ""); occ != nil {
		t.Fatalf("expected no occurrences for an empty name, got %+v", occ)
	}
}

func TestHoverInfoResolvesScopeChain(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///leaf.sv", "module leaf(input logic clk);\nendmodule\n")
	ix.SetFile("file:///top.sv", "module top;\n  leaf u_leaf();\nendmodule\n")

	d, ok := ix.HoverInfo("file:///top.sv", 1, 3, "leaf", "", false)
	if !ok || d.Kind != KindModule || len(d.Ports) != 1 || d.Ports[0].Name != "clk" {
		t.Fatalf("HoverInfo(leaf) = %+v, %v", d, ok)
	}
}

func TestHoverInfoNoMatch(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top;\nendmodule\n")
	if _, ok := ix.HoverInfo("file:///a.sv", 0, 0, "nope", "", false); ok {
		t.Fatalf("expected no match for an unknown name")
	}
}

func TestHoverInfoResolvesFileScopeDeclarationWithNoEnclosingContainer(t *testing.T) {
	// A header whose entire content is one file-scope typedef, no
	// enclosing module/package -- innermostContaining never finds a
	// container to walk from, so this only resolves via the direct
	// self-reference check (lookupSelfRefLocked), not the scope-chain
	// walk. Regression guard: this used to return no match at all.
	ix := NewIndex()
	ix.SetFile("file:///defs.svh", "typedef logic [7:0] bus_t;\n")

	d, ok := ix.HoverInfo("file:///defs.svh", 0, 20, "bus_t", "", false)
	if !ok || d.Kind != KindTypedef || d.Name != "bus_t" {
		t.Fatalf("HoverInfo(bus_t) = %+v, %v", d, ok)
	}
}

func TestFindDefinitionResolvesTypedefFromIncludedFile(t *testing.T) {
	// The scope-chain walk is bucket-local and typedef isn't in
	// GloballyReferenceableKinds, so without the explicit "search this
	// file's own `include d files" step, a typedef pulled in via
	// `include would never resolve from the includer at all -- the
	// entire point of attributing it to its own file in the first place.
	ix := NewIndex()
	ix.SetIncludeResolverFactory(func() IncludeResolver {
		return &stubResolver{files: map[string]string{"defs.svh": "typedef logic [7:0] bus_t;\n"}}
	})
	ix.SetFile("file:///top.sv", "`include \"defs.svh\"\nmodule top;\n  bus_t data;\nendmodule\n")

	locs, ok := ix.FindDefinition("file:///top.sv", 2, 4, "bus_t", "", false)
	if !ok || len(locs) != 1 || locs[0].URI != "file:///defs.svh" {
		t.Fatalf("FindDefinition(bus_t) = %+v, %v", locs, ok)
	}
}

func TestFindDefinitionDoesNotLeakUnrelatedFilesIncludes(t *testing.T) {
	// b.sv doesn't `include defs.svh at all -- it must not resolve
	// bus_t just because some *other* file in the workspace does.
	ix := NewIndex()
	ix.SetIncludeResolverFactory(func() IncludeResolver {
		return &stubResolver{files: map[string]string{"defs.svh": "typedef logic [7:0] bus_t;\n"}}
	})
	ix.SetFile("file:///top.sv", "`include \"defs.svh\"\nmodule top; endmodule\n")
	ix.SetFile("file:///b.sv", "module b;\n  bus_t data;\nendmodule\n")

	if _, ok := ix.FindDefinition("file:///b.sv", 1, 4, "bus_t", "", false); ok {
		t.Fatalf("expected bus_t not to resolve from b.sv, which never includes defs.svh")
	}
}

func TestFindDefinitionResolvesFunctionViaWildcardImportAtFileScope(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///pkg.sv", `package pa_pkg;
  function int foo();
  endfunction
endpackage
`)
	ix.SetFile("file:///main.sv", `import pa_pkg::*;
module top;
  localparam a = foo();
endmodule
`)

	locs, ok := ix.FindDefinition("file:///main.sv", 2, 18, "foo", "", false)
	if !ok || len(locs) != 1 {
		t.Fatalf("FindDefinition(foo) = %+v, %v", locs, ok)
	}
	if locs[0].URI != "file:///pkg.sv" || locs[0].Line != 1 {
		t.Fatalf("unexpected location: %+v", locs[0])
	}
}

func TestFindDefinitionResolvesViaWildcardImportInsideModuleBody(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///pkg.sv", `package pa_pkg;
  function int foo();
  endfunction
endpackage
`)
	ix.SetFile("file:///main.sv", `module top;
  import pa_pkg::*;
  localparam a = foo();
endmodule
`)

	locs, ok := ix.FindDefinition("file:///main.sv", 2, 18, "foo", "", false)
	if !ok || len(locs) != 1 || locs[0].URI != "file:///pkg.sv" {
		t.Fatalf("FindDefinition(foo) = %+v, %v", locs, ok)
	}
}

func TestFindDefinitionResolvesViaSpecificMemberImport(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///pkg.sv", `package pa_pkg;
  function int foo();
  endfunction
endpackage
`)
	ix.SetFile("file:///main.sv", `import pa_pkg::foo;
module top;
  localparam a = foo();
endmodule
`)

	locs, ok := ix.FindDefinition("file:///main.sv", 2, 18, "foo", "", false)
	if !ok || len(locs) != 1 || locs[0].URI != "file:///pkg.sv" {
		t.Fatalf("FindDefinition(foo) = %+v, %v", locs, ok)
	}
}

func TestFindDefinitionSpecificMemberImportDoesNotGrantOtherMembers(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///pkg.sv", `package pa_pkg;
  function int foo();
  endfunction
  function int bar();
  endfunction
endpackage
`)
	ix.SetFile("file:///main.sv", `import pa_pkg::foo;
module top;
  localparam a = bar();
endmodule
`)

	if _, ok := ix.FindDefinition("file:///main.sv", 2, 18, "bar", "", false); ok {
		t.Fatalf("expected bar not to resolve: only foo was specifically imported")
	}
}

func TestFindDefinitionLocalDeclarationShadowsWildcardImport(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///pkg.sv", `package pa_pkg;
  function int foo();
  endfunction
endpackage
`)
	ix.SetFile("file:///main.sv", `import pa_pkg::*;
module top;
  function int foo();
    return 1;
  endfunction
  localparam a = foo();
endmodule
`)

	locs, ok := ix.FindDefinition("file:///main.sv", 5, 18, "foo", "", false)
	if !ok || len(locs) != 1 {
		t.Fatalf("FindDefinition(foo) = %+v, %v; want exactly one local match", locs, ok)
	}
	if locs[0].URI != "file:///main.sv" {
		t.Fatalf("expected the local foo to shadow the wildcard-imported one, got %+v", locs[0])
	}
}

func TestFindDefinitionWildcardImportVisibilityScopedToModule(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///pkg.sv", `package pa_pkg;
  function int foo();
  endfunction
endpackage
`)
	ix.SetFile("file:///main.sv", `module A;
  import pa_pkg::*;
endmodule

module B;
  localparam a = foo();
endmodule
`)

	if _, ok := ix.FindDefinition("file:///main.sv", 5, 18, "foo", "", false); ok {
		t.Fatalf("expected foo not to resolve in module B: the wildcard import is only in module A's body")
	}
}

func TestFindDefinitionMultipleWildcardImportsSameMemberReturnsAllCandidates(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///pkga.sv", "package pa_pkg;\n  function int foo();\n  endfunction\nendpackage\n")
	ix.SetFile("file:///pkgb.sv", "package pb_pkg;\n  function int foo();\n  endfunction\nendpackage\n")
	ix.SetFile("file:///main.sv", `import pa_pkg::*;
import pb_pkg::*;
module top;
  localparam a = foo();
endmodule
`)

	locs, ok := ix.FindDefinition("file:///main.sv", 3, 18, "foo", "", false)
	if !ok || len(locs) != 2 {
		t.Fatalf("FindDefinition(foo) = %+v, %v; want both candidates returned", locs, ok)
	}
}

func TestFindDefinitionResolvesEnumMemberViaWildcardImport(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///pkg.sv", "package pa_pkg;\n  typedef enum { RED, GREEN, BLUE } color_t;\nendpackage\n")
	ix.SetFile("file:///main.sv", `import pa_pkg::*;
module top;
  localparam c = RED;
endmodule
`)

	locs, ok := ix.FindDefinition("file:///main.sv", 2, 18, "RED", "", false)
	if !ok || len(locs) != 1 || locs[0].URI != "file:///pkg.sv" {
		t.Fatalf("FindDefinition(RED) = %+v, %v", locs, ok)
	}
}

func TestFindDeclarationAlsoResolvesViaWildcardImport(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///pkg.sv", `package pa_pkg;
  function int foo();
  endfunction
endpackage
`)
	ix.SetFile("file:///main.sv", `import pa_pkg::*;
module top;
  localparam a = foo();
endmodule
`)

	locs, ok := ix.FindDeclaration("file:///main.sv", 2, 18, "foo", "", false)
	if !ok || len(locs) != 1 || locs[0].URI != "file:///pkg.sv" {
		t.Fatalf("FindDeclaration(foo) = %+v, %v", locs, ok)
	}
}

func TestOccurrences(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top;\n  leaf u_leaf();\nendmodule\n")
	ix.SetFile("file:///b.sv", "module leaf;\nendmodule\n")

	locs := ix.Occurrences("leaf")
	if len(locs) != 2 {
		t.Fatalf("Occurrences(leaf) = %+v, want 2", locs)
	}
}

func TestOccurrencesRemovedOnSetFile(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top;\n  leaf u_leaf();\nendmodule\n")
	ix.SetFile("file:///a.sv", "module top;\nendmodule\n") // rescan without the reference

	if locs := ix.Occurrences("leaf"); len(locs) != 0 {
		t.Fatalf("expected no occurrences of leaf after rescanning without it, got %+v", locs)
	}
}

func TestOccurrencesRemovedOnRemoveFile(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top;\n  leaf u_leaf();\nendmodule\n")
	ix.SetFile("file:///b.sv", "module leaf;\nendmodule\n")
	ix.RemoveFile("file:///b.sv")

	locs := ix.Occurrences("leaf")
	if len(locs) != 1 || locs[0].URI != "file:///a.sv" {
		t.Fatalf("expected only a.sv's occurrence of leaf after removing b.sv, got %+v", locs)
	}
}

func TestOccurrencesOrderedByURI(t *testing.T) {
	ix := NewIndex()
	// Insert out of URI order to make sure results are sorted, not just
	// echoing insertion order.
	ix.SetFile("file:///b.sv", "module leaf;\nendmodule\n")
	ix.SetFile("file:///a.sv", "module top;\n  leaf u_leaf();\nendmodule\n")

	locs := ix.Occurrences("leaf")
	if len(locs) != 2 || locs[0].URI != "file:///a.sv" || locs[1].URI != "file:///b.sv" {
		t.Fatalf("expected occurrences sorted by URI (a.sv before b.sv), got %+v", locs)
	}
}

func TestScopedOccurrencesRestrictsModuleInternalFunction(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", `module mod_a;
  function void helper();
  endfunction
  initial helper();
endmodule
`)
	ix.SetFile("file:///b.sv", `module mod_b;
  function void helper();
  endfunction
  initial helper();
endmodule
`)

	// "helper" on line 3 ("  initial helper();") of mod_a, resolved via
	// the scope chain to mod_a's own helper.
	locs := ix.ScopedOccurrences("file:///a.sv", 3, 12, "helper", "", false)
	if len(locs) != 2 {
		t.Fatalf("expected 2 occurrences (declaration + call) within mod_a only, got %+v", locs)
	}
	for _, l := range locs {
		if l.URI != "file:///a.sv" {
			t.Fatalf("expected only file:///a.sv occurrences, got %+v", locs)
		}
	}
}

func TestScopedOccurrencesModuleNameStaysWorkspaceWide(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///leaf.sv", "module leaf;\nendmodule\n")
	ix.SetFile("file:///top.sv", "module top;\n  leaf u_leaf();\nendmodule\n")

	locs := ix.ScopedOccurrences("file:///top.sv", 1, 3, "leaf", "", false)
	uris := map[string]bool{}
	for _, l := range locs {
		uris[l.URI] = true
	}
	if !uris["file:///leaf.sv"] || !uris["file:///top.sv"] {
		t.Fatalf("expected occurrences in both files (module names aren't scope-restricted), got %+v", locs)
	}
}

func TestScopedOccurrencesClassMemberStaysWorkspaceWide(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", `class foo;
  function void bar();
  endfunction
  function void caller();
    bar();
  endfunction
endclass
initial bar();
`)

	// "bar()" call inside caller (line 4) resolves via the scope chain to
	// foo's own bar -- but foo is a class, not a module/interface/
	// program, so this must stay workspace-wide.
	locs := ix.ScopedOccurrences("file:///a.sv", 4, 5, "bar", "", false)
	foundOutsideClass := false
	for _, l := range locs {
		if l.Line == 7 {
			foundOutsideClass = true
		}
	}
	if !foundOutsideClass {
		t.Fatalf("expected the occurrence outside the class to still be included (class members aren't scope-restricted), got %+v", locs)
	}
}

func TestScopedOccurrencesFileScopeFunctionStaysWorkspaceWide(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "function void util();\nendfunction\ninitial util();\n")
	ix.SetFile("file:///b.sv", "initial util();\n")

	locs := ix.ScopedOccurrences("file:///a.sv", 0, 15, "util", "", false)
	uris := map[string]bool{}
	for _, l := range locs {
		uris[l.URI] = true
	}
	if !uris["file:///a.sv"] || !uris["file:///b.sv"] {
		t.Fatalf("expected occurrences in both files (file-scope declarations aren't restricted), got %+v", locs)
	}
}

func TestScopedOccurrencesUnresolvedFallsBackToUnrestricted(t *testing.T) {
	ix := NewIndex()
	// "clk" appears only inside an "initial" statement -- svparse's
	// declaration-grade parser never parses statement/expression content
	// (a function/task/initial body is recognized and skipped), so this
	// never becomes a real Declaration anywhere, even though the raw
	// lexer-based occurrence scan still records it as a token.
	ix.SetFile("file:///a.sv", "module top;\n  initial clk = 1;\nendmodule\n")
	ix.SetFile("file:///b.sv", "module other;\n  initial clk = 1;\nendmodule\n")

	locs := ix.ScopedOccurrences("file:///a.sv", 1, 10, "clk", "", false)
	uris := map[string]bool{}
	for _, l := range locs {
		uris[l.URI] = true
	}
	if !uris["file:///a.sv"] || !uris["file:///b.sv"] {
		t.Fatalf("expected unrestricted occurrences across both files, got %+v", locs)
	}
}

func TestSetFileWiresResolverFactoryAndRecordsDependencies(t *testing.T) {
	ix := NewIndex()
	ix.SetIncludeResolverFactory(func() IncludeResolver {
		return &stubResolver{files: map[string]string{"defs.svh": "typedef logic [7:0] bus_t;\n"}}
	})
	ix.SetFile("file:///top.sv", "`include \"defs.svh\"\nmodule top;\n  bus_t data;\nendmodule\n")

	if _, ok := ix.Lookup("bus_t"); !ok {
		t.Fatalf("expected bus_t (from the `include) to be indexed")
	}
	deps := ix.Dependents("file:///defs.svh")
	if len(deps) != 1 || deps[0] != "file:///top.sv" {
		t.Fatalf("Dependents(defs.svh) = %v, want [top.sv]", deps)
	}
}

func TestSetFileWithoutResolverFactoryLeavesIncludesUnresolved(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///top.sv", "`include \"defs.svh\"\nmodule top; endmodule\n")

	if _, ok := ix.Lookup("top"); !ok {
		t.Fatalf("expected top to still be indexed despite the unresolved `include")
	}
	if deps := ix.Dependents("file:///defs.svh"); len(deps) != 0 {
		t.Fatalf("Dependents(defs.svh) = %v, want none", deps)
	}
}

func TestSetFileWiresInitialMacros(t *testing.T) {
	ix := NewIndex()
	ix.SetInitialMacros(map[string]string{"SYNTHESIS": ""})
	ix.SetFile("file:///top.sv", "`ifdef SYNTHESIS\nmodule synth_only; endmodule\n`endif\n")

	if _, ok := ix.Lookup("synth_only"); !ok {
		t.Fatalf("expected synth_only to be declared with SYNTHESIS defined via SetInitialMacros")
	}
}

func TestIncludesOfReturnsRecordedDependencies(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top; endmodule")
	ix.recordDependencies("file:///a.sv", []string{"file:///b.svh", "file:///c.svh"})

	got := ix.IncludesOf("file:///a.sv")
	if len(got) != 2 {
		t.Fatalf("IncludesOf(a.sv) = %v, want 2 entries", got)
	}
}

func TestIncludesOfEmptyForFileWithNoIncludes(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top; endmodule")
	if got := ix.IncludesOf("file:///a.sv"); len(got) != 0 {
		t.Fatalf("IncludesOf(a.sv) = %v, want none", got)
	}
}

func TestDependentsFindsDirectIncluder(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top; endmodule")
	ix.recordDependencies("file:///a.sv", []string{"file:///b.svh"})

	deps := ix.Dependents("file:///b.svh")
	if len(deps) != 1 || deps[0] != "file:///a.sv" {
		t.Fatalf("Dependents(b.svh) = %v, want [a.sv]", deps)
	}
}

func TestDependentsEmptyForUnreferencedFile(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top; endmodule")
	if deps := ix.Dependents("file:///nothing-includes-this.svh"); len(deps) != 0 {
		t.Fatalf("Dependents = %v, want none", deps)
	}
}

func TestDependentsFindsMultipleIncluders(t *testing.T) {
	// b.svh included by both a.sv and c.sv -- covers the "one file's
	// header is shared workspace-wide" case a single-includer test
	// wouldn't catch.
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module a; endmodule")
	ix.SetFile("file:///c.sv", "module c; endmodule")
	ix.recordDependencies("file:///a.sv", []string{"file:///b.svh"})
	ix.recordDependencies("file:///c.sv", []string{"file:///b.svh"})

	deps := ix.Dependents("file:///b.svh")
	if len(deps) != 2 {
		t.Fatalf("Dependents(b.svh) = %v, want 2 entries", deps)
	}
}

func TestDependentsUpdatesOnRescan(t *testing.T) {
	// a.sv drops its include of b.svh on rescan -- recordDependencies
	// must replace, not accumulate, uri's prior dependency set.
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top; endmodule")
	ix.recordDependencies("file:///a.sv", []string{"file:///b.svh"})
	ix.recordDependencies("file:///a.sv", nil)

	if deps := ix.Dependents("file:///b.svh"); len(deps) != 0 {
		t.Fatalf("Dependents(b.svh) = %v, want none after a.sv dropped the include", deps)
	}
}

func TestRemoveFileClearsItsDependencies(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top; endmodule")
	ix.recordDependencies("file:///a.sv", []string{"file:///b.svh"})
	ix.RemoveFile("file:///a.sv")

	if deps := ix.Dependents("file:///b.svh"); len(deps) != 0 {
		t.Fatalf("Dependents(b.svh) = %v, want none after a.sv was removed", deps)
	}
}

func TestAllKnownURIsIncludesEveryScannedFile(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module a; endmodule")
	ix.SetFile("file:///b.sv", "module b; endmodule")

	known := ix.AllKnownURIs()
	want := map[string]bool{"file:///a.sv": true, "file:///b.sv": true}
	if len(known) != len(want) {
		t.Fatalf("AllKnownURIs() = %v, want %v", known, want)
	}
	for _, uri := range known {
		if !want[uri] {
			t.Fatalf("unexpected URI %s in AllKnownURIs()", uri)
		}
	}
}

func TestAllKnownURIsShrinksAfterRemoveFile(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module a; endmodule")
	ix.RemoveFile("file:///a.sv")

	if known := ix.AllKnownURIs(); len(known) != 0 {
		t.Fatalf("AllKnownURIs() = %v, want none", known)
	}
}

func TestIndexDiagnosticsReturnsScanErrors(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "42;\nmodule top; endmodule\n")

	if diags := ix.Diagnostics("file:///a.sv"); len(diags) == 0 {
		t.Fatalf("expected at least one diagnostic for the malformed '42;'")
	}
}

func TestIndexDiagnosticsEmptyForCleanFile(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "module top; endmodule")

	if diags := ix.Diagnostics("file:///a.sv"); len(diags) != 0 {
		t.Fatalf("Diagnostics() = %+v, want none", diags)
	}
}

func TestIndexDiagnosticsClearOnRescanWithoutErrors(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "42;\nmodule top; endmodule\n")
	if diags := ix.Diagnostics("file:///a.sv"); len(diags) == 0 {
		t.Fatalf("expected an initial diagnostic")
	}

	ix.SetFile("file:///a.sv", "module top; endmodule\n")
	if diags := ix.Diagnostics("file:///a.sv"); len(diags) != 0 {
		t.Fatalf("expected diagnostics to clear after rescanning well-formed text, got %+v", diags)
	}
}

func TestIndexDiagnosticsClearedOnRemoveFile(t *testing.T) {
	ix := NewIndex()
	ix.SetFile("file:///a.sv", "42;\nmodule top; endmodule\n")
	ix.RemoveFile("file:///a.sv")

	if diags := ix.Diagnostics("file:///a.sv"); len(diags) != 0 {
		t.Fatalf("Diagnostics() = %+v, want none after RemoveFile", diags)
	}
}

func TestIndexDiagnosticsAttributedToIncludedFile(t *testing.T) {
	ix := NewIndex()
	ix.SetIncludeResolverFactory(func() IncludeResolver {
		return &stubResolver{files: map[string]string{"defs.svh": "42;\n"}}
	})
	ix.SetFile("file:///top.sv", "`include \"defs.svh\"\nmodule top; endmodule\n")

	if diags := ix.Diagnostics("file:///defs.svh"); len(diags) == 0 {
		t.Fatalf("expected defs.svh's own malformed content to produce a diagnostic on defs.svh")
	}
	if diags := ix.Diagnostics("file:///top.sv"); len(diags) != 0 {
		t.Fatalf("expected top.sv itself to have no diagnostics, got %+v", diags)
	}
}

func TestSetFileReturnsTouchedURIs(t *testing.T) {
	ix := NewIndex()
	touched := ix.SetFile("file:///a.sv", "module top; endmodule")
	if len(touched) != 1 || touched[0] != "file:///a.sv" {
		t.Fatalf("SetFile touchedURIs = %v, want [file:///a.sv]", touched)
	}
}

func TestSetFileReturnsTouchedURIsAcrossIncludeWithDeclarationsAndErrors(t *testing.T) {
	// defs.svh has both a real declaration and a malformed statement --
	// it should appear in touchedURIs exactly once, from either source.
	ix := NewIndex()
	ix.SetIncludeResolverFactory(func() IncludeResolver {
		return &stubResolver{files: map[string]string{"defs.svh": "typedef logic bus_t;\n42;\n"}}
	})
	touched := ix.SetFile("file:///top.sv", "`include \"defs.svh\"\nmodule top; endmodule\n")

	want := map[string]bool{"file:///top.sv": true, "file:///defs.svh": true}
	if len(touched) != len(want) {
		t.Fatalf("touchedURIs = %v, want %v", touched, want)
	}
	for _, uri := range touched {
		if !want[uri] {
			t.Fatalf("unexpected URI %s in touchedURIs", uri)
		}
	}
}
