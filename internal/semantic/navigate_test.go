package semantic

import (
	"strings"
	"testing"

	"github.com/jfetkotto/svparse/v2/ast"
	"github.com/jfetkotto/svparse/v2/parser"
	"github.com/jfetkotto/svparse/v2/preprocessor"
)

func serviceForFiles(t *testing.T, files map[string]string) *Service {
	t.Helper()
	var trees []*ast.File
	for uri, text := range files {
		toks, _ := preprocessor.Preprocess(uri, text, nil)
		f, _ := parser.Parse(uri, toks)
		trees = append(trees, f)
	}
	return New(func() Snapshot { return Snapshot{Files: trees, Generation: 1} })
}

// posAt is the position of delta characters into the first occurrence of marker.
func posAt(t *testing.T, text, marker string, delta int) (line, character int) {
	t.Helper()
	idx := strings.Index(text, marker)
	if idx < 0 {
		t.Fatalf("%q not found", marker)
	}
	idx += delta
	return strings.Count(text[:idx], "\n"), idx - (strings.LastIndex(text[:idx], "\n") + 1)
}

func lines(locs []Location) []int {
	var out []int
	for _, l := range locs {
		out = append(out, l.Line)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDefinitionAndReferencesOfASignal(t *testing.T) {
	text := "module m(input a, output b);\n  logic t;\n  assign t = a;\n  assign b = t;\nendmodule\n"
	s := serviceForFiles(t, map[string]string{"u": text})
	l, c := posAt(t, text, "assign b = t", 11)
	def, ok := s.Definition("u", l, c)
	if !ok || len(def) != 1 || def[0].Line != 1 || def[0].Character != 8 {
		t.Fatalf("definition: %+v", def)
	}
	refs, ok := s.References("u", l, c, false)
	if !ok || !equalInts(lines(refs), []int{2, 3}) {
		t.Fatalf("references: %+v", refs)
	}
	refs, _ = s.References("u", l, c, true)
	if !equalInts(lines(refs), []int{1, 2, 3}) {
		t.Fatalf("references with the declaration: %+v", refs)
	}
	// From the declaration itself.
	dl, dc := posAt(t, text, "logic t", 6)
	if refs, _ := s.References("u", dl, dc, false); !equalInts(lines(refs), []int{2, 3}) {
		t.Fatalf("references from the declaration: %+v", refs)
	}
}

// Same spelling, different symbols: a local shadowing a port, and one name in
// two modules.
func TestReferencesTellShadowedNamesApart(t *testing.T) {
	text := "module a(input x, output y);\n  assign y = x;\nendmodule\n" +
		"module b(input x, output y);\n  always_comb begin : blk\n    logic x2;\n    x2 = x;\n  end\n  assign y = x;\nendmodule\n"
	s := serviceForFiles(t, map[string]string{"u": text})
	l, c := posAt(t, text, "assign y = x;", 11) // module a
	refs, _ := s.References("u", l, c, true)
	if !equalInts(lines(refs), []int{0, 1}) {
		t.Fatalf("module a's x: %v", lines(refs))
	}
	l, c = posAt(t, text, "x2 = x", 5) // module b
	refs, _ = s.References("u", l, c, true)
	if !equalInts(lines(refs), []int{3, 6, 8}) {
		t.Fatalf("module b's x: %v", lines(refs))
	}
}

func TestInstantiationNamesResolveToTheInstantiatedModule(t *testing.T) {
	leaf := "module leaf #(parameter W = 1)(input p, output q);\nendmodule\n"
	top := "module top(input a, output b);\n  leaf #(.W(2)) u(.p(a), .q(b));\nendmodule\n"
	s := serviceForFiles(t, map[string]string{"leaf.sv": leaf, "top.sv": top})
	for _, tc := range []struct {
		marker string
		delta  int
		line   int
		char   int
	}{
		{"leaf #", 1, 0, 7}, // the module type
		{".W(2)", 1, 0, 20}, // the parameter name
		{".p(a)", 1, 0, 40}, // the port names
		{".q(b)", 1, 0, 53},
	} {
		l, c := posAt(t, top, tc.marker, tc.delta)
		def, ok := s.Definition("top.sv", l, c)
		if !ok || len(def) != 1 || def[0].URI != "leaf.sv" {
			t.Fatalf("%s: %+v", tc.marker, def)
		}
	}
	l, c := posAt(t, top, "leaf #", 1)
	if def, _ := s.Definition("top.sv", l, c); def[0].Character != 7 {
		t.Fatalf("module name column: %+v", def)
	}
	// Every use of the port p, from its declaration.
	pl, pc := posAt(t, leaf, "input p", 7)
	refs, _ := s.References("leaf.sv", pl, pc, false)
	if len(refs) != 1 || refs[0].URI != "top.sv" {
		t.Fatalf("p's uses: %+v", refs)
	}
}

func TestPackageMembersResolveThroughImportsAndScopes(t *testing.T) {
	pkg := "package p;\n  localparam int K = 2;\n  typedef logic [3:0] nib_t;\n  function int twice(int v); return v * 2; endfunction\nendpackage\n"
	use := "module m;\n  import p::*;\n  nib_t n;\n  int a, b;\n  assign a = K;\n  assign b = p::K + twice(1);\nendmodule\n"
	s := serviceForFiles(t, map[string]string{"p.sv": pkg, "m.sv": use})
	for _, tc := range []struct {
		marker, wantDecl string
		delta, line      int
	}{
		{"assign a = K", "K", 11, 1},
		{"p::K", "K", 3, 1},
		{"twice(1)", "twice", 1, 3},
		{"nib_t n", "nib_t", 1, 2},
	} {
		l, c := posAt(t, use, tc.marker, tc.delta)
		def, ok := s.Definition("m.sv", l, c)
		if !ok || len(def) != 1 || def[0].URI != "p.sv" || def[0].Line != tc.line {
			t.Fatalf("%s: %+v", tc.marker, def)
		}
	}
	// K is used twice in m.sv.
	kl, kc := posAt(t, pkg, "K = 2", 0)
	refs, _ := s.References("p.sv", kl, kc, false)
	if len(refs) != 2 {
		t.Fatalf("K's uses: %+v", refs)
	}
}

func TestNavigationOffNames(t *testing.T) {
	text := "module m;\n  int a;\n  initial $display(a + 1);\nendmodule\n"
	s := serviceForFiles(t, map[string]string{"u": text})
	for _, marker := range []string{"$display", "+ 1", "module m"} {
		l, c := posAt(t, text, marker, 1)
		if def, ok := s.Definition("u", l, c); ok {
			t.Errorf("%s: unexpected definition %+v", marker, def)
		}
	}
}
