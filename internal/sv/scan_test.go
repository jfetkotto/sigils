package sv

import (
	"fmt"
	"testing"
	"unsafe"
)

func declNames(decls []Declaration) []string {
	names := make([]string, len(decls))
	for i, d := range decls {
		names[i] = d.Name
	}
	return names
}

func findDecl(t *testing.T, decls []Declaration, name string) Declaration {
	t.Helper()
	for _, d := range decls {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no declaration named %q in %+v", name, decls)
	return Declaration{}
}

func TestScanDeclarationsFindsModuleInterfaceProgram(t *testing.T) {
	src := `module top;
endmodule

interface bus_if;
endinterface

program test_prog;
endprogram
`
	decls := ScanDeclarations("test.sv", src)
	if len(decls) != 3 {
		t.Fatalf("expected 3 declarations, got %d: %+v", len(decls), decls)
	}

	top := findDecl(t, decls, "top")
	if top.Kind != KindModule || top.Line != 0 || top.Character != 7 || top.Parent != -1 {
		t.Fatalf("unexpected top: %+v", top)
	}
	busIf := findDecl(t, decls, "bus_if")
	if busIf.Kind != KindInterface || busIf.Line != 3 || busIf.Character != 10 {
		t.Fatalf("unexpected bus_if: %+v", busIf)
	}
	testProg := findDecl(t, decls, "test_prog")
	if testProg.Kind != KindProgram || testProg.Line != 6 || testProg.Character != 8 {
		t.Fatalf("unexpected test_prog: %+v", testProg)
	}
}

func TestScanDeclarationsIgnoresLineComments(t *testing.T) {
	src := "// module fake_mod;\nmodule real_mod;\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)
	if len(decls) != 1 || decls[0].Name != "real_mod" {
		t.Fatalf("expected only real_mod, got %+v", decls)
	}
}

func TestScanDeclarationsIgnoresBlockComments(t *testing.T) {
	src := "/* module fake_mod;\nendmodule */\nmodule real_mod;\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)
	if len(decls) != 1 || decls[0].Name != "real_mod" {
		t.Fatalf("expected only real_mod, got %+v", decls)
	}
	if decls[0].Line != 2 {
		t.Fatalf("expected real_mod on line 2, got line %d", decls[0].Line)
	}
}

func TestScanDeclarationsIgnoresStringLiterals(t *testing.T) {
	src := `module real_mod;
  initial $display("module fake_mod");
endmodule
`
	decls := ScanDeclarations("test.sv", src)
	if len(decls) != 1 || decls[0].Name != "real_mod" {
		t.Fatalf("expected only real_mod, got %+v", decls)
	}
}

func TestScanDeclarationsEmptyInput(t *testing.T) {
	if decls := ScanDeclarations("test.sv", ""); decls != nil {
		t.Fatalf("expected nil declarations for empty input, got %+v", decls)
	}
}

func TestScanDeclarationsMultipleModules(t *testing.T) {
	src := `module a; endmodule
module b; endmodule
module c; endmodule
`
	decls := ScanDeclarations("test.sv", src)
	if got := declNames(decls); got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("unexpected names: %v", got)
	}
}

func TestScanDeclarationsClassAndPackage(t *testing.T) {
	src := `package my_pkg;
class my_class;
endclass
endpackage
`
	decls := ScanDeclarations("test.sv", src)
	pkg := findDecl(t, decls, "my_pkg")
	cls := findDecl(t, decls, "my_class")
	if pkg.Kind != KindPackage || pkg.Parent != -1 {
		t.Fatalf("unexpected package decl: %+v", pkg)
	}
	if cls.Kind != KindClass {
		t.Fatalf("unexpected class kind: %+v", cls)
	}
	// cls.Parent should point back at pkg's own index.
	if decls[cls.Parent].Name != "my_pkg" {
		t.Fatalf("expected my_class's parent to be my_pkg, got %+v", decls[cls.Parent])
	}
}

func TestScanDeclarationsFunctionAndTaskNesting(t *testing.T) {
	src := `module top;
  function int get_val();
    return 1;
  endfunction

  task automatic do_thing(input logic [7:0] a);
  endtask
endmodule
`
	decls := ScanDeclarations("test.sv", src)
	top := findDecl(t, decls, "top")
	getVal := findDecl(t, decls, "get_val")
	doThing := findDecl(t, decls, "do_thing")

	if getVal.Kind != KindFunction {
		t.Fatalf("unexpected get_val kind: %+v", getVal)
	}
	if doThing.Kind != KindTask {
		t.Fatalf("unexpected do_thing kind: %+v", doThing)
	}
	for _, idx := range []int{getVal.Parent, doThing.Parent} {
		if decls[idx].Name != "top" {
			t.Fatalf("expected parent to be top, got %+v", decls[idx])
		}
	}
	_ = top
}

func TestScanDeclarationsConstructorNamedNew(t *testing.T) {
	src := `class foo;
  function new();
  endfunction
endclass
`
	decls := ScanDeclarations("test.sv", src)
	ctor := findDecl(t, decls, "new")
	if ctor.Kind != KindFunction {
		t.Fatalf("unexpected constructor decl: %+v", ctor)
	}
	if decls[ctor.Parent].Name != "foo" {
		t.Fatalf("expected constructor's parent to be foo, got %+v", decls[ctor.Parent])
	}
}

func TestScanDeclarationsTypedefSimple(t *testing.T) {
	src := "typedef logic [7:0] byte_t;\n"
	decls := ScanDeclarations("test.sv", src)
	if len(decls) != 1 || decls[0].Name != "byte_t" || decls[0].Kind != KindTypedef {
		t.Fatalf("unexpected decls: %+v", decls)
	}
}

func TestScanDeclarationsTypedefAliasCapturesUnderlyingType(t *testing.T) {
	src := "typedef logic [7:0] byte_t;\n"
	decls := ScanDeclarations("test.sv", src)
	byteT := findDecl(t, decls, "byte_t")
	if byteT.TypedefKind != "alias" || byteT.AliasType != "logic [7:0]" {
		t.Fatalf("unexpected byte_t: %+v", byteT)
	}
}

func TestScanDeclarationsTypedefStructWithInnerSemicolons(t *testing.T) {
	src := `typedef struct packed {
  logic a;
  logic b;
} my_struct_t;
`
	decls := ScanDeclarations("test.sv", src)
	if len(decls) != 1 || decls[0].Name != "my_struct_t" {
		t.Fatalf("expected only my_struct_t (inner field ';'s should not terminate the scan early), got %+v", decls)
	}
}

func TestScanDeclarationsTypedefStructCapturesFields(t *testing.T) {
	src := `typedef struct packed {
  logic [7:0] data;
  logic valid;
} pkt_t;
`
	decls := ScanDeclarations("test.sv", src)
	pktT := findDecl(t, decls, "pkt_t")
	if pktT.TypedefKind != "struct" || !pktT.Packed {
		t.Fatalf("unexpected pkt_t: %+v", pktT)
	}
	if len(pktT.Fields) != 2 || pktT.Fields[0].Name != "data" || pktT.Fields[0].Detail != "logic [7:0]" ||
		pktT.Fields[1].Name != "valid" || pktT.Fields[1].Detail != "logic" {
		t.Fatalf("unexpected fields: %+v", pktT.Fields)
	}
}

func TestScanDeclarationsTypedefUnionCapturesFields(t *testing.T) {
	src := `typedef union packed {
  logic [31:0] word;
  logic [3:0][7:0] bytes;
} raw_t;
`
	decls := ScanDeclarations("test.sv", src)
	rawT := findDecl(t, decls, "raw_t")
	if rawT.TypedefKind != "union" || !rawT.Packed {
		t.Fatalf("unexpected raw_t: %+v", rawT)
	}
	if len(rawT.Fields) != 2 || rawT.Fields[0].Name != "word" || rawT.Fields[1].Name != "bytes" {
		t.Fatalf("unexpected fields: %+v", rawT.Fields)
	}
}

func TestScanDeclarationsTypedefForwardDeclaration(t *testing.T) {
	src := "typedef class my_future_class;\n"
	decls := ScanDeclarations("test.sv", src)
	if len(decls) != 1 || decls[0].Name != "my_future_class" {
		t.Fatalf("unexpected decls: %+v", decls)
	}
}

func TestScanDeclarationsTypedefForwardDeclarationHasNoTypedefKind(t *testing.T) {
	src := "typedef class my_future_class;\n"
	decls := ScanDeclarations("test.sv", src)
	d := findDecl(t, decls, "my_future_class")
	if d.TypedefKind != "" {
		t.Fatalf("expected no TypedefKind for a forward declaration, got %+v", d)
	}
}

func TestScanDeclarationsExternFunctionHasNoBody(t *testing.T) {
	src := `class foo;
  extern function void bar();
endclass
function void foo::bar();
endfunction
`
	decls := ScanDeclarations("test.sv", src)
	bars := 0
	for _, d := range decls {
		if d.Name == "bar" {
			bars++
		}
	}
	if bars != 2 {
		t.Fatalf("expected both the extern prototype and the out-of-body definition to be found, got %d: %+v", bars, decls)
	}
	// The class should still close normally -- the extern prototype must
	// not have been left open on the container stack.
	foo := findDecl(t, decls, "foo")
	if foo.EndLine != 2 {
		t.Fatalf("expected foo (the class) to close at line 2, got %+v", foo)
	}
}

func TestScanDeclarationsPureVirtualFunctionHasNoBody(t *testing.T) {
	src := `class foo;
  pure virtual function void bar();
endclass
`
	decls := ScanDeclarations("test.sv", src)
	foo := findDecl(t, decls, "foo")
	bar := findDecl(t, decls, "bar")
	if bar.Kind != KindFunction {
		t.Fatalf("unexpected bar: %+v", bar)
	}
	if foo.EndLine != 2 {
		t.Fatalf("expected foo (the class) to close normally at line 2, got %+v", foo)
	}
}

func TestScanDeclarationsDPIImportHasNoBody(t *testing.T) {
	src := `import "DPI-C" function int c_func(int x);
module top;
endmodule
`
	decls := ScanDeclarations("test.sv", src)
	cFunc := findDecl(t, decls, "c_func")
	if cFunc.Kind != KindFunction {
		t.Fatalf("unexpected c_func: %+v", cFunc)
	}
	top := findDecl(t, decls, "top")
	if top.Parent != -1 {
		t.Fatalf("expected top to be at file scope, got parent %d (the DPI import must not have stayed open on the stack)", top.Parent)
	}
}

func TestScanDeclarationsPackageImportDoesNotMarkNextFunctionAsPrototype(t *testing.T) {
	// Unlike this package's prior tokenizer, svparse's prototype detection
	// is purely structural (a function/task's own grammar -- extern/
	// virtual/pure-virtual/DPI-import -- not "what token preceded it"), so
	// the bug class this test originally guarded against (a preceding
	// "import" leaking across its ";") can't recur by construction. Kept
	// as a behavioral pin, not a regression guard for that specific bug
	// anymore.
	src := `import my_pkg::*;
function automatic void foo();
  return;
endfunction
`
	decls := ScanDeclarations("test.sv", src)
	foo := findDecl(t, decls, "foo")
	if foo.Prototype {
		t.Fatalf("foo must not be a prototype: %+v", foo)
	}
	if foo.EndLine != 3 {
		t.Fatalf("expected foo to close at its endfunction on line 3, got %+v", foo)
	}
}

func TestScanDeclarationsFunctionBodyLocalsAreNotTracked(t *testing.T) {
	// svparse's parser is declaration-grade: a function/task body is
	// recognized and skipped, never parsed (see svparse/parser's design
	// doc). A typedef or variable declared inside one is therefore
	// invisible to this package -- a known, deliberate limitation, not a
	// bug, worth pinning explicitly now that the engine behind Scan
	// actually parses bodies at all (sigils's own prior tokenizer never
	// looked inside a body either, so this isn't a regression).
	src := `function automatic void foo();
  typedef int inner_t;
  int local_var;
endfunction
`
	decls := ScanDeclarations("test.sv", src)
	for _, d := range decls {
		if d.Name == "inner_t" || d.Name == "local_var" {
			t.Fatalf("expected function-body-local declarations to stay untracked, found %+v", d)
		}
	}
	findDecl(t, decls, "foo") // fails the test if not found
}

func TestScanDeclarationsMismatchedEndKeywordEndsTheContainerEarly(t *testing.T) {
	// Unlike this package's prior scanner (which tracked a real container
	// stack and skipped any end-keyword not matching the innermost open
	// entry, so a later correct "endmodule" would still close top),
	// svparse's parser stops a container's body at the first *any*
	// recognized end keyword it doesn't expect, leaving it unconsumed for
	// the caller rather than searching further for a matching one -- so
	// top closes early, at the mismatched "endclass" on line 1, not the
	// real "endmodule" on line 2. A deliberate, documented difference in
	// this specific recovery edge case (a stray wrong end-keyword
	// followed by the correct one, most likely to occur transiently
	// mid-edit) -- not a crash, not silently wrong, just a less precise
	// End position than the old scanner gave here.
	src := `module top;
endclass
endmodule
`
	decls := ScanDeclarations("test.sv", src)
	top := findDecl(t, decls, "top")
	if top.EndLine != 1 {
		t.Fatalf("expected top to close early at the mismatched 'endclass' (line 1), got %+v", top)
	}
}

func portNames(ports []Port) []string {
	names := make([]string, len(ports))
	for i, p := range ports {
		names[i] = p.Name
	}
	return names
}

func TestScanDeclarationsExtractsAnsiPortList(t *testing.T) {
	src := "module leaf(input logic clk, input logic rst_n, output logic [7:0] data);\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)
	leaf := findDecl(t, decls, "leaf")

	got := portNames(leaf.Ports)
	want := []string{"clk", "rst_n", "data"}
	if len(got) != len(want) {
		t.Fatalf("ports = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ports = %v, want %v", got, want)
		}
	}
	if leaf.Ports[2].Detail != "output logic [7:0]" {
		t.Fatalf("data port detail = %q, want \"output logic [7:0]\"", leaf.Ports[2].Detail)
	}
}

func TestScanDeclarationsPortDeclarationHasDetail(t *testing.T) {
	src := "module leaf(input logic clk, output logic [7:0] data);\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)

	clk := findDecl(t, decls, "clk")
	if clk.Kind != KindPort || clk.Detail != "input logic" {
		t.Fatalf("unexpected clk: %+v", clk)
	}
	data := findDecl(t, decls, "data")
	if data.Kind != KindPort || data.Detail != "output logic [7:0]" {
		t.Fatalf("unexpected data: %+v", data)
	}
}

func TestScanDeclarationsPortCapturesTypeName(t *testing.T) {
	src := "module leaf(input logic clk, input bus_t b);\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)

	clk := findDecl(t, decls, "clk")
	if clk.TypeName != "logic" {
		t.Fatalf("clk.TypeName = %q, want \"logic\"", clk.TypeName)
	}
	b := findDecl(t, decls, "b")
	if b.TypeName != "bus_t" {
		t.Fatalf("b.TypeName = %q, want \"bus_t\"", b.TypeName)
	}
}

func TestScanDeclarationsContainerParamsExcludesLocalParams(t *testing.T) {
	src := "module leaf #(parameter int WIDTH = 8, localparam int FIXED = 1) ();\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)
	leaf := findDecl(t, decls, "leaf")

	if len(leaf.Params) != 1 || leaf.Params[0].Name != "WIDTH" {
		t.Fatalf("expected only WIDTH in Params (FIXED is a localparam), got %+v", leaf.Params)
	}
	if leaf.Params[0].Detail != "int = 8" {
		t.Fatalf("WIDTH detail = %q, want \"int = 8\"", leaf.Params[0].Detail)
	}
}

func TestScanDeclarationsParamPortEntryCreatesIndividualDeclaration(t *testing.T) {
	src := "module leaf #(parameter int WIDTH = 8, localparam int FIXED = 1) ();\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)

	width := findDecl(t, decls, "WIDTH")
	if width.Kind != KindParameter || width.Detail != "int" || width.Default != "8" {
		t.Fatalf("unexpected WIDTH: %+v", width)
	}
	fixed := findDecl(t, decls, "FIXED")
	if fixed.Kind != KindParameter || fixed.Detail != "int" || fixed.Default != "1" {
		t.Fatalf("unexpected FIXED: %+v", fixed)
	}
	if fixed.Parent != width.Parent {
		t.Fatalf("expected FIXED and WIDTH to share the same parent (leaf), got %+v and %+v", fixed, width)
	}
}

func TestScanDeclarationsBodyLevelParameterHasDetailAndDefault(t *testing.T) {
	src := "module top;\n  parameter int WIDTH = 8;\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)

	width := findDecl(t, decls, "WIDTH")
	if width.Kind != KindParameter || width.Detail != "int" || width.Default != "8" {
		t.Fatalf("unexpected WIDTH: %+v", width)
	}
}

func TestScanDeclarationsPortListWithDefaultValue(t *testing.T) {
	src := "module leaf(input logic rst_n = 1'b0, input logic clk);\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)
	leaf := findDecl(t, decls, "leaf")

	// The based-literal "1'b0" tokenizes its "b0" as an identifier-looking
	// run; scanning must stop at "=" so that doesn't get mistaken for the
	// port name.
	if got := portNames(leaf.Ports); len(got) != 2 || got[0] != "rst_n" || got[1] != "clk" {
		t.Fatalf("ports = %v, want [rst_n clk]", got)
	}
}

func TestScanDeclarationsPortListSkipsParameterList(t *testing.T) {
	src := "module leaf #(parameter int WIDTH = 8, parameter int DEPTH = 16) (input logic [WIDTH-1:0] data, input logic clk);\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)
	leaf := findDecl(t, decls, "leaf")

	if got := portNames(leaf.Ports); len(got) != 2 || got[0] != "data" || got[1] != "clk" {
		t.Fatalf("ports = %v, want [data clk] (the #(...) parameter list must not be mistaken for the port list)", got)
	}
}

func TestScanDeclarationsPortListSharedDirection(t *testing.T) {
	src := "module leaf(input a, b, c);\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)
	leaf := findDecl(t, decls, "leaf")

	if got := portNames(leaf.Ports); len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("ports = %v, want [a b c]", got)
	}
}

func TestScanDeclarationsNoPortListYieldsNilPorts(t *testing.T) {
	src := "module leaf;\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)
	leaf := findDecl(t, decls, "leaf")
	if leaf.Ports != nil {
		t.Fatalf("expected nil ports for a module with no port list, got %+v", leaf.Ports)
	}
}

func TestScanDeclarationsClassIsNotGivenPorts(t *testing.T) {
	src := "class foo;\nendclass\n"
	decls := ScanDeclarations("test.sv", src)
	foo := findDecl(t, decls, "foo")
	if foo.Ports != nil {
		t.Fatalf("expected class declarations to never get a Ports list, got %+v", foo.Ports)
	}
}

func TestScanDeclarationsUnterminatedModuleGetsEndOfFile(t *testing.T) {
	src := "module top;\n  function int f();\n"
	decls := ScanDeclarations("test.sv", src)
	top := findDecl(t, decls, "top")
	if top.EndLine == 0 && top.EndCharacter == 0 {
		t.Fatalf("expected an unterminated module to still get a non-zero End, got %+v", top)
	}
}

func enumMemberNames(decls []Declaration) []string {
	var names []string
	for _, d := range decls {
		if d.Kind == KindEnumMember {
			names = append(names, d.Name)
		}
	}
	return names
}

func TestScanDeclarationsTypedefEnumMembers(t *testing.T) {
	src := "typedef enum {IDLE, RUNNING, DONE} state_t;\n"
	decls := ScanDeclarations("test.sv", src)

	stateT := findDecl(t, decls, "state_t")
	if stateT.Kind != KindTypedef {
		t.Fatalf("unexpected state_t: %+v", stateT)
	}

	names := enumMemberNames(decls)
	want := []string{"IDLE", "RUNNING", "DONE"}
	if len(names) != len(want) {
		t.Fatalf("enum members = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("enum members = %v, want %v", names, want)
		}
	}

	idle := findDecl(t, decls, "IDLE")
	if idle.Kind != KindEnumMember || idle.Parent != -1 {
		t.Fatalf("unexpected IDLE: %+v", idle)
	}
}

func TestScanDeclarationsEnumWithBaseTypeAndExplicitValues(t *testing.T) {
	src := "typedef enum logic [1:0] {IDLE = 0, RUNNING = 1, DONE = 2} state_t;\n"
	decls := ScanDeclarations("test.sv", src)

	names := enumMemberNames(decls)
	want := []string{"IDLE", "RUNNING", "DONE"}
	if len(names) != len(want) {
		t.Fatalf("enum members = %v, want %v (the base type and explicit values must not be mistaken for member names)", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("enum members = %v, want %v", names, want)
		}
	}

	findDecl(t, decls, "state_t") // fails the test if not found
}

func TestScanDeclarationsTypedefEnumComputesImplicitValues(t *testing.T) {
	src := "typedef enum {S_FOO, S_BAR, S_BAZ} en_States;\n"
	decls := ScanDeclarations("test.sv", src)

	enStates := findDecl(t, decls, "en_States")
	want := []string{"S_FOO = 0", "S_BAR = 1", "S_BAZ = 2"}
	if len(enStates.EnumMembers) != len(want) {
		t.Fatalf("EnumMembers = %v, want %v", enStates.EnumMembers, want)
	}
	for i := range want {
		if enStates.EnumMembers[i] != want[i] {
			t.Fatalf("EnumMembers = %v, want %v", enStates.EnumMembers, want)
		}
	}

	sBar := findDecl(t, decls, "S_BAR")
	if sBar.Value != "1" || sBar.EnumTypedef != "en_States" {
		t.Fatalf("unexpected S_BAR: %+v", sBar)
	}
}

func TestScanDeclarationsTypedefEnumRespectsExplicitValueThenContinuesIncrementing(t *testing.T) {
	src := "typedef enum {A, B = 5, C} t;\n"
	decls := ScanDeclarations("test.sv", src)
	tDecl := findDecl(t, decls, "t")
	want := []string{"A = 0", "B = 5", "C = 6"}
	if len(tDecl.EnumMembers) != len(want) {
		t.Fatalf("EnumMembers = %v, want %v", tDecl.EnumMembers, want)
	}
	for i := range want {
		if tDecl.EnumMembers[i] != want[i] {
			t.Fatalf("EnumMembers = %v, want %v", tDecl.EnumMembers, want)
		}
	}
}

func TestScanDeclarationsTypedefEnumHandlesMultipleExplicitValues(t *testing.T) {
	src := "typedef enum {FOO = 0, BAR, BAZ = 10, QUX} t;\n"
	decls := ScanDeclarations("test.sv", src)
	tDecl := findDecl(t, decls, "t")
	want := []string{"FOO = 0", "BAR = 1", "BAZ = 10", "QUX = 11"}
	if len(tDecl.EnumMembers) != len(want) {
		t.Fatalf("EnumMembers = %v, want %v", tDecl.EnumMembers, want)
	}
	for i := range want {
		if tDecl.EnumMembers[i] != want[i] {
			t.Fatalf("EnumMembers = %v, want %v", tDecl.EnumMembers, want)
		}
	}
}

func TestScanDeclarationsTypedefEnumStopsComputingAfterNonLiteralValue(t *testing.T) {
	src := "typedef enum {A = WIDTH-1, B} t;\n"
	decls := ScanDeclarations("test.sv", src)
	tDecl := findDecl(t, decls, "t")
	want := []string{"A = WIDTH-1", "B"}
	if len(tDecl.EnumMembers) != len(want) {
		t.Fatalf("EnumMembers = %v, want %v", tDecl.EnumMembers, want)
	}
	for i := range want {
		if tDecl.EnumMembers[i] != want[i] {
			t.Fatalf("EnumMembers = %v, want %v", tDecl.EnumMembers, want)
		}
	}
	b := findDecl(t, decls, "B")
	if b.Value != "" {
		t.Fatalf("expected B's value to be left uncomputed after a non-literal prior value, got %+v", b)
	}
}

func TestScanDeclarationsStandaloneEnumNoTypedefIsNotRecognized(t *testing.T) {
	// svparse's parser only reaches struct/union/enum grammar through a
	// typedef (see svparse/parser/typedef.go's own doc comment) --
	// declaring a variable of an anonymous, non-typedef'd enum type
	// directly is legal SV but out of scope there. This package's prior
	// tokenizer did support it (a bare "enum" keyword was its own special
	// case), so this is a real, deliberate capability difference -- pinned
	// here as "recovers cleanly and finds what comes after", not "still
	// extracts the members", which is no longer true.
	src := "enum {A, B, C} var;\nmodule top; endmodule\n"
	decls := ScanDeclarations("test.sv", src)

	if len(enumMemberNames(decls)) != 0 {
		t.Fatalf("expected no enum members extracted from an untyped enum, got %+v", decls)
	}
	findDecl(t, decls, "top") // fails the test if recovery didn't reach it
}

func TestScanDeclarationsEnumMembersNestedInModule(t *testing.T) {
	src := "module top;\n  typedef enum {IDLE, DONE} state_t;\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)

	top := findDecl(t, decls, "top")
	idle := findDecl(t, decls, "IDLE")
	if idle.Parent != findDecl(t, decls, "state_t").Parent {
		t.Fatalf("expected IDLE to share state_t's parent")
	}
	if decls[idle.Parent].Name != top.Name {
		t.Fatalf("expected IDLE's parent to be top, got %+v", decls[idle.Parent])
	}
}

func TestScanDeclarationsEnumWithNoBraceDoesNotPanic(t *testing.T) {
	src := "enum foo;\nmodule top;\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)
	if len(enumMemberNames(decls)) != 0 {
		t.Fatalf("expected no enum members for malformed input, got %+v", decls)
	}
	// The scanner must still recover and find the module afterwards.
	findDecl(t, decls, "top")
}

func TestScanReturnsDeclarationsAndOccurrences(t *testing.T) {
	text := "module top;\n  leaf u_leaf ();\nendmodule\n"
	decls, occs, _, _, _ := Scan("test.sv", text, nil, nil)

	wantDecls := ScanDeclarations("test.sv", text)
	got := decls["test.sv"]
	if len(got) != len(wantDecls) || got[0].Name != wantDecls[0].Name {
		t.Fatalf("Scan's declarations = %+v, want %+v", got, wantDecls)
	}

	names := map[string]int{}
	for _, o := range occs {
		names[o.Name]++
	}
	if names["top"] != 1 || names["leaf"] != 1 || names["u_leaf"] != 1 {
		t.Fatalf("unexpected occurrence counts: %+v", names)
	}
}

func TestOccurrencesFromTokensInternsRepeatedNames(t *testing.T) {
	text := "module top;\n  wire clk;\n  wire clk2;\n  assign clk2 = clk;\nendmodule\n"
	_, occs, _, _, _ := Scan("test.sv", text, nil, nil)

	var seen []string
	for _, o := range occs {
		if o.Name == "clk" {
			seen = append(seen, o.Name)
		}
	}
	if len(seen) < 2 {
		t.Fatalf("expected at least two occurrences of \"clk\", got %d", len(seen))
	}
	if unsafe.StringData(seen[0]) != unsafe.StringData(seen[1]) {
		t.Fatalf("expected repeated occurrences of the same name to share one backing string (interning)")
	}
}

func TestOccurrencesTrackSystemTasksWithDollarPrefix(t *testing.T) {
	// svparse's lexer (unlike this package's old ad hoc tokenizer, which
	// didn't accept '$' as an identifier-start char) tokenizes a system
	// task/function as one KindSystemIdent token including the '$' --
	// occurrence tracking should record it whole, not split it.
	text := `module top; initial $display("hi"); endmodule`
	_, occs, _, _, _ := Scan("test.sv", text, nil, nil)
	for _, o := range occs {
		if o.Name == "$display" {
			return
		}
	}
	t.Fatalf("expected a \"$display\" occurrence, got %+v", occs)
}

func TestFindOccurrencesPopulatesName(t *testing.T) {
	occ := FindOccurrences("module leaf; endmodule", "leaf")
	if len(occ) != 1 || occ[0].Name != "leaf" {
		t.Fatalf("FindOccurrences = %+v, want Name \"leaf\"", occ)
	}
}

func TestScanDeclarationsTracksVariables(t *testing.T) {
	src := "module top;\n  logic [7:0] data;\n  bus_t bus;\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)

	data := findDecl(t, decls, "data")
	if data.Kind != KindVariable || decls[data.Parent].Name != "top" {
		t.Fatalf("unexpected data: %+v", data)
	}
	if data.TypeName != "logic" {
		t.Fatalf("data.TypeName = %q, want \"logic\"", data.TypeName)
	}
	if data.Detail != "logic [7:0]" {
		t.Fatalf("data.Detail = %q, want \"logic [7:0]\"", data.Detail)
	}
	bus := findDecl(t, decls, "bus")
	if bus.Kind != KindVariable {
		t.Fatalf("unexpected bus (a user-defined-type variable): %+v", bus)
	}
	if bus.TypeName != "bus_t" {
		t.Fatalf("bus.TypeName = %q, want \"bus_t\"", bus.TypeName)
	}
	if bus.Detail != "bus_t" {
		t.Fatalf("bus.Detail = %q, want \"bus_t\"", bus.Detail)
	}
}

func TestScanDeclarationsTracksParameters(t *testing.T) {
	src := "module top #(parameter int WIDTH = 8) ();\n  localparam int DEPTH = 16;\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)

	depth := findDecl(t, decls, "DEPTH")
	if depth.Kind != KindParameter || decls[depth.Parent].Name != "top" {
		t.Fatalf("unexpected DEPTH: %+v", depth)
	}
}

func TestScanDeclarationsTracksPortsAsIndependentDeclarations(t *testing.T) {
	// Beyond the container's own Ports field (used for named-port-
	// connection completion), each port is now also its own leaf
	// KindPort Declaration, parented to the container -- so referencing a
	// port by name from inside the module body resolves for the first
	// time.
	src := "module leaf(input logic clk);\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)

	clk := findDecl(t, decls, "clk")
	if clk.Kind != KindPort || decls[clk.Parent].Name != "leaf" {
		t.Fatalf("unexpected clk: %+v", clk)
	}
}

func TestScanDeclarationsFunctionArgs(t *testing.T) {
	src := "module top;\n  function automatic int add(int a, int b);\n    return a + b;\n  endfunction\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)

	add := findDecl(t, decls, "add")
	if len(add.Args) != 2 || add.Args[0].Name != "a" || add.Args[1].Name != "b" {
		t.Fatalf("unexpected add.Args: %+v", add.Args)
	}
	if add.Args[0].Detail != "int" {
		t.Fatalf("unexpected arg detail: %q", add.Args[0].Detail)
	}
}

// stubResolver is a minimal, in-memory IncludeResolver for testing
// cross-file `include attribution without touching a filesystem -- the
// concrete, filesystem-backed resolver (incdir search, includer-relative
// resolution) lives in internal/lspserver and is tested there.
type stubResolver struct {
	files    map[string]string // includedPath (as written) -> text
	resolved []string          // populated only by actual Resolve calls, matching the real resolver's contract -- Resolved() must NOT just echo back every configured file regardless of whether it was ever asked for
}

func (r *stubResolver) Resolve(includedPath, fromFile string) (text, resolvedPath string, err error) {
	t, ok := r.files[includedPath]
	if !ok {
		return "", "", fmt.Errorf("stubResolver: %q not found", includedPath)
	}
	uri := "file:///" + includedPath
	r.resolved = append(r.resolved, uri)
	return t, uri, nil
}

func (r *stubResolver) Resolved() []string {
	return append([]string(nil), r.resolved...)
}

func TestScanAttributesIncludedDeclarationsToTheirOwnFile(t *testing.T) {
	resolver := &stubResolver{files: map[string]string{
		"defs.svh": "typedef logic [7:0] bus_t;\n",
	}}
	src := "`include \"defs.svh\"\nmodule top;\n  bus_t data;\nendmodule\n"
	decls, _, _, _, _ := Scan("file:///top.sv", src, resolver, nil)

	if len(decls["file:///defs.svh"]) != 1 || decls["file:///defs.svh"][0].Name != "bus_t" {
		t.Fatalf("expected bus_t attributed to defs.svh, got %+v", decls["file:///defs.svh"])
	}
	if decls["file:///defs.svh"][0].Parent != -1 {
		t.Fatalf("expected bus_t to be file-scoped in its own bucket (no cross-file Parent link), got %+v", decls["file:///defs.svh"][0])
	}

	top := decls["file:///top.sv"]
	if len(top) != 2 {
		t.Fatalf("expected top.sv's own bucket to hold top and data, got %+v", top)
	}
}

func TestScanNilResolverLeavesIncludeUnresolvedWithoutCrashing(t *testing.T) {
	src := "`include \"defs.svh\"\nmodule top; endmodule\n"
	decls, _, _, _, _ := Scan("file:///top.sv", src, nil, nil)
	if len(decls["file:///top.sv"]) != 1 || decls["file:///top.sv"][0].Name != "top" {
		t.Fatalf("expected recovery to still find top, got %+v", decls)
	}
}

func TestScanInitialMacrosGateIfdef(t *testing.T) {
	src := "`ifdef SYNTHESIS\nmodule synth_only; endmodule\n`else\nmodule sim_only; endmodule\n`endif\n"
	decls, _, _, _, _ := Scan("test.sv", src, nil, map[string]string{"SYNTHESIS": ""})
	if _, ok := findDeclMap(decls["test.sv"], "synth_only"); !ok {
		t.Fatalf("expected synth_only to be declared with SYNTHESIS defined, got %+v", decls["test.sv"])
	}
	if _, ok := findDeclMap(decls["test.sv"], "sim_only"); ok {
		t.Fatalf("expected sim_only to be excluded with SYNTHESIS defined, got %+v", decls["test.sv"])
	}
}

func findDeclMap(decls []Declaration, name string) (Declaration, bool) {
	for _, d := range decls {
		if d.Name == name {
			return d, true
		}
	}
	return Declaration{}, false
}

func TestScanCleanInputSeedsAnEmptyDiagnosticsEntry(t *testing.T) {
	// uri must be a key in the returned map even with zero diagnostics,
	// not merely absent -- that's what lets a caller notice "this file
	// used to have errors and now has none" and republish an empty
	// diagnostics list to clear them client-side.
	_, _, diags, _, _ := Scan("test.sv", "module top; endmodule", nil, nil)
	list, ok := diags["test.sv"]
	if !ok {
		t.Fatalf("expected test.sv to be a key in diags even with no errors")
	}
	if len(list) != 0 {
		t.Fatalf("expected no diagnostics for well-formed input, got %+v", list)
	}
}

func TestScanRecordsDiagnosticForUnresolvedInclude(t *testing.T) {
	src := "`include \"defs.svh\"\nmodule top; endmodule\n"
	_, _, diags, _, _ := Scan("test.sv", src, nil, nil)
	list := diags["test.sv"]
	if len(list) != 1 {
		t.Fatalf("expected one diagnostic for the unresolved `include, got %+v", list)
	}
	if list[0].Line != 0 {
		t.Fatalf("expected the diagnostic on line 0, got %+v", list[0])
	}
}

func TestScanRecordsDiagnosticForUnrecognizedDeclaration(t *testing.T) {
	// A bare number can't start any declaration the parser recognizes --
	// a genuine syntax problem, not just a preprocessing one.
	_, _, diags, _, _ := Scan("test.sv", "42;\nmodule top; endmodule\n", nil, nil)
	if len(diags["test.sv"]) == 0 {
		t.Fatalf("expected at least one parser diagnostic for the bare '42;'")
	}
}

func TestScanAttributesDiagnosticsToTheirOwnIncludedFile(t *testing.T) {
	resolver := &stubResolver{files: map[string]string{
		"defs.svh": "42;\n", // malformed on purpose
	}}
	src := "`include \"defs.svh\"\nmodule top; endmodule\n"
	_, _, diags, _, _ := Scan("file:///top.sv", src, resolver, nil)

	if len(diags["file:///defs.svh"]) == 0 {
		t.Fatalf("expected defs.svh's own malformed content to produce a diagnostic attributed to defs.svh, got %+v", diags)
	}
	if len(diags["file:///top.sv"]) != 0 {
		t.Fatalf("expected top.sv itself to have no diagnostics of its own, got %+v", diags["file:///top.sv"])
	}
}

// TestScanConstructsPreviouslyReportedSpuriousDiagnostics is a
// consolidated regression guard for a batch of svparse fixes (extern
// virtual/static function prototypes, generate-for/genvar scaffolding,
// modport declarations, gate primitives, `defparam`, randcase inside a
// procedural block, dangling else, covergroup/property/sequence/
// clocking/specify/checker blocks, and “ token-paste/`" stringify in
// macro bodies) that all used to produce parser/preprocessor errors on
// perfectly valid SystemVerilog -- errors sigils would have published as
// red-squiggle diagnostics on real, unmodified source. Each case here
// must scan clean (zero diagnostics).
func TestScanConstructsPreviouslyReportedSpuriousDiagnostics(t *testing.T) {
	cases := map[string]string{
		"generate-for with instantiation": `module m;
			genvar i;
			generate
				for (i = 0; i < 4; i = i + 1) begin : gen_leaf
					leaf u_leaf (.clk(clk));
				end
			endgenerate
		endmodule`,
		"interface with modports": `interface bus_if;
			logic req, gnt;
			modport master (output req, input gnt);
			modport slave  (input req, output gnt);
		endinterface`,
		"class with extern virtual function": `class c;
			extern virtual function void f();
		endclass`,
		"gate primitive instantiation": `module m(input a, b, output y);
			and g1 (y, a, b);
		endmodule`,
		"defparam": `module m;
			defparam u1.WIDTH = 8;
		endmodule`,
		"randcase inside initial begin": `module m;
			initial begin
				randcase
					1: x = 0;
				endcase
				x = 1;
			end
		endmodule`,
		"dangling else with no begin/end": `module m(input clk);
			logic q;
			always_ff @(posedge clk)
				if (q) q <= 0;
				else q <= 1;
		endmodule`,
		"covergroup/property/sequence/clocking/specify/checker": `module m;
			covergroup cg @(posedge clk);
				coverpoint state;
			endgroup
			property p1;
				@(posedge clk) a |-> b;
			endproperty
			sequence s1;
				@(posedge clk) a ##1 b;
			endsequence
			clocking cbk @(posedge clk);
				input a;
			endclocking
			specify
				(a => y) = 3;
			endspecify
		endmodule
		checker my_checker(input logic a, input logic b);
			always_comb assert (a == b);
		endchecker`,
		"macro token-paste and stringize": "`define JOIN(a,b) a``b\n" +
			"`define MSG(x) `\"x`\"\n" +
			"module m;\n" +
			"  logic `JOIN(foo,bar);\n" +
			"  initial $display(`MSG(hello));\n" +
			"endmodule",
	}

	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, diags, _, _ := Scan("test.sv", src, nil, nil)
			if list := diags["test.sv"]; len(list) != 0 {
				t.Fatalf("expected zero diagnostics, got %+v", list)
			}
		})
	}
}

func TestIsIdentifier(t *testing.T) {
	valid := []string{"foo", "_foo", "foo_bar123", "FOO", "a$b"}
	for _, name := range valid {
		if !IsIdentifier(name) {
			t.Errorf("IsIdentifier(%q) = false, want true", name)
		}
	}
	invalid := []string{"", "1foo", "foo bar", "foo-bar", "foo.bar", "logic", "module", "endmodule"}
	for _, name := range invalid {
		if IsIdentifier(name) {
			t.Errorf("IsIdentifier(%q) = true, want false", name)
		}
	}
}

func TestIsKeyword(t *testing.T) {
	for _, name := range []string{"module", "logic", "endmodule", "always_ff"} {
		if !IsKeyword(name) {
			t.Errorf("IsKeyword(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"clk", "data", "my_module", ""} {
		if IsKeyword(name) {
			t.Errorf("IsKeyword(%q) = true, want false", name)
		}
	}
}

func TestScanCollectsWildcardImportAtFileScope(t *testing.T) {
	src := "import pa_pkg::*;\nmodule top;\nendmodule\n"
	_, _, _, imports, _ := Scan("test.sv", src, nil, nil)
	imps := imports["test.sv"]
	if len(imps) != 1 {
		t.Fatalf("expected 1 import, got %+v", imps)
	}
	if imps[0].Package != "pa_pkg" || imps[0].Member != "*" || imps[0].Parent != -1 {
		t.Fatalf("unexpected import: %+v", imps[0])
	}
}

func TestScanCollectsSpecificMemberImport(t *testing.T) {
	src := "import pa_pkg::foo;\nmodule top;\nendmodule\n"
	_, _, _, imports, _ := Scan("test.sv", src, nil, nil)
	imps := imports["test.sv"]
	if len(imps) != 1 {
		t.Fatalf("expected 1 import, got %+v", imps)
	}
	if imps[0].Package != "pa_pkg" || imps[0].Member != "foo" {
		t.Fatalf("unexpected import: %+v", imps[0])
	}
}

func TestScanCollectsImportParentedToEnclosingModule(t *testing.T) {
	src := `module top;
  import pa_pkg::*;
endmodule
`
	decls, _, _, imports, _ := Scan("test.sv", src, nil, nil)
	top := findDecl(t, decls["test.sv"], "top")
	topIdx := -1
	for i, d := range decls["test.sv"] {
		if d.Name == "top" {
			topIdx = i
		}
	}
	if topIdx == -1 {
		t.Fatalf("could not find top's own index: %+v", top)
	}
	imps := imports["test.sv"]
	if len(imps) != 1 {
		t.Fatalf("expected 1 import, got %+v", imps)
	}
	if imps[0].Parent != topIdx {
		t.Fatalf("expected import parented to top (idx %d), got %+v", topIdx, imps[0])
	}
}

func TestScanCollectsModuleHeaderImportSameAsBodyImport(t *testing.T) {
	src := "module top import pa_pkg::*; (input logic clk);\nendmodule\n"
	decls, _, _, imports, _ := Scan("test.sv", src, nil, nil)
	topIdx := -1
	for i, d := range decls["test.sv"] {
		if d.Name == "top" {
			topIdx = i
		}
	}
	if topIdx == -1 {
		t.Fatalf("could not find top's own index")
	}
	imps := imports["test.sv"]
	if len(imps) != 1 {
		t.Fatalf("expected 1 import, got %+v", imps)
	}
	if imps[0].Package != "pa_pkg" || imps[0].Member != "*" || imps[0].Parent != topIdx {
		t.Fatalf("unexpected header import: %+v", imps[0])
	}
}

func TestScanCollectsNamedPortConnectionSite(t *testing.T) {
	src := "module top;\n  leaf u_leaf(.clk(sig));\nendmodule\n"
	_, _, _, _, connections := Scan("test.sv", src, nil, nil)
	conns := connections["test.sv"]
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection, got %+v", conns)
	}
	if conns[0].ModuleType != "leaf" || conns[0].Name != "clk" || conns[0].Line != 1 || conns[0].Character != 15 {
		t.Fatalf("unexpected connection: %+v", conns[0])
	}
}

func TestScanCollectsImplicitPortConnectionSite(t *testing.T) {
	src := "module top;\n  leaf u_leaf(.clk);\nendmodule\n"
	_, _, _, _, connections := Scan("test.sv", src, nil, nil)
	conns := connections["test.sv"]
	if len(conns) != 1 || conns[0].ModuleType != "leaf" || conns[0].Name != "clk" || conns[0].Character != 15 {
		t.Fatalf("unexpected connection: %+v", conns)
	}
}

func TestScanSkipsWildcardConnectionSite(t *testing.T) {
	src := "module top;\n  leaf u_leaf(.*);\nendmodule\n"
	_, _, _, _, connections := Scan("test.sv", src, nil, nil)
	if conns := connections["test.sv"]; len(conns) != 0 {
		t.Fatalf("expected no connections for a wildcard \".*\", got %+v", conns)
	}
}

func TestScanCollectsNamedParamOverrideSite(t *testing.T) {
	src := "module top;\n  leaf #(.WIDTH(8)) u0();\nendmodule\n"
	_, _, _, _, connections := Scan("test.sv", src, nil, nil)
	conns := connections["test.sv"]
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection, got %+v", conns)
	}
	if conns[0].ModuleType != "leaf" || conns[0].Name != "WIDTH" || conns[0].Line != 1 || conns[0].Character != 10 {
		t.Fatalf("unexpected override: %+v", conns[0])
	}
}

func TestScanSkipsPositionalConnectionsAndOverrides(t *testing.T) {
	src := "module top;\n  leaf #(8) u0(sig);\nendmodule\n"
	_, _, _, _, connections := Scan("test.sv", src, nil, nil)
	if conns := connections["test.sv"]; len(conns) != 0 {
		t.Fatalf("expected no connections for positional entries, got %+v", conns)
	}
}

// IsIdentifier gates rename: a name it accepts gets written into the user's
// source. It must therefore accept exactly what svparse's lexer will tokenize
// back as an identifier -- a looser rule (unicode.IsLetter) accepts a name the
// lexer then tags KindInvalid, so the rename produces source that no longer
// tokenizes and a symbol that can never be found again.
func TestIsIdentifierMatchesTheLexer(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"clk", true},
		{"_under", true},
		{"a$b", true},
		{"n0", true},
		{"", false},
		{"0start", false},
		{"has space", false},
		{"module", false}, // reserved word
		{"café", false},
		{"naïve_sig", false},
		{"Ω", false},
	}
	for _, tc := range cases {
		if got := IsIdentifier(tc.name); got != tc.want {
			t.Errorf("IsIdentifier(%q) = %v, want %v", tc.name, got, tc.want)
		}
		if !tc.want {
			continue
		}
		// Anything accepted must round-trip through the lexer as one
		// identifier token, which is the property that actually matters.
		occs := FindOccurrences(tc.name+" ;", tc.name)
		if len(occs) != 1 {
			t.Errorf("IsIdentifier(%q) accepted it, but the lexer found %d occurrences of it", tc.name, len(occs))
		}
	}
}
