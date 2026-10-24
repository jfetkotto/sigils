package semantic

import (
	"strings"
	"testing"

	"github.com/jfetkotto/svparse/v2/ast"
	"github.com/jfetkotto/svparse/v2/parser"
	"github.com/jfetkotto/svparse/v2/preprocessor"
)

func serviceFor(t *testing.T, uri, text string) *Service {
	t.Helper()
	toks, _ := preprocessor.Preprocess(uri, text, nil)
	f, _ := parser.Parse(uri, toks)
	return New(func() ([]*ast.File, uint64) { return []*ast.File{f}, 1 })
}

// at finds the marker "@" in text, removes it, and describes that position.
func at(t *testing.T, text, word string) (Info, bool) {
	t.Helper()
	i := strings.Index(text, "@")
	if i < 0 {
		t.Fatal("no marker")
	}
	text = text[:i] + text[i+1:]
	line := strings.Count(text[:i], "\n")
	col := i - (strings.LastIndex(text[:i], "\n") + 1)
	return serviceFor(t, "file:///t.sv", text).Describe("file:///t.sv", line, col, word)
}

func TestIdentifierShowsTypeAndWidth(t *testing.T) {
	info, ok := at(t, "module m;\n  logic [7:0] data;\n  logic [7:0] q;\n  assign q = d@ata;\nendmodule\n", "data")
	if !ok || info.Expr == nil {
		t.Fatalf("no info: %+v", info)
	}
	if info.Expr.Text != "data" || info.Expr.Type != "logic [7:0]" || info.Expr.Bits != 8 || info.Expr.Signed {
		t.Fatalf("expr: %+v", info.Expr)
	}
}

func TestOperatorOperandShowsEnclosingResult(t *testing.T) {
	info, ok := at(t, "module m;\n  logic [7:0] a, b;\n  logic [8:0] s;\n  assign s = @a + b;\nendmodule\n", "a")
	if !ok || info.Expr == nil || info.Parent == nil {
		t.Fatalf("no info: %+v", info)
	}
	if info.Expr.Text != "a" || info.Parent.Text != "a + b" || info.Parent.Bits != 8 || info.Parent.ContextBits != 9 {
		t.Fatalf("expr %+v parent %+v", info.Expr, info.Parent)
	}
}

func TestParameterShowsValue(t *testing.T) {
	info, ok := at(t, "module m #(parameter W = 4 * 2);\n  logic [W-1:0] x;\n  assign x = @W;\nendmodule\n", "W")
	if !ok || info.Expr == nil || info.Expr.Value != "8" {
		t.Fatalf("no value: %+v %+v", info.Expr, info.Symbol)
	}
}

func TestConstantExpressionShowsValue(t *testing.T) {
	info, ok := at(t, "module m;\n  localparam int N = 3;\n  logic [7:0] x;\n  assign x = N + @5;\nendmodule\n", "")
	if !ok || info.Expr == nil || info.Expr.Text != "5" {
		t.Fatalf("got %+v", info)
	}
	if info.Parent == nil || info.Parent.Value != "8" {
		t.Fatalf("parent %+v", info.Parent)
	}
}

func TestDeclarationNameShowsType(t *testing.T) {
	info, ok := at(t, "module m;\n  logic signed [3:0] sig@nal;\nendmodule\n", "signal")
	if !ok || info.Symbol == nil || info.Symbol.Type == "" || !info.Symbol.Signed || info.Symbol.Bits != 4 {
		t.Fatalf("got %+v", info.Symbol)
	}
}

func TestNothingToSayAboutKeywords(t *testing.T) {
	if info, ok := at(t, "mod@ule m;\nendmodule\n", "module"); ok {
		t.Fatalf("got %+v", info)
	}
}

func TestMarkdown(t *testing.T) {
	info, _ := at(t, "module m;\n  logic [7:0] a, b;\n  logic [8:0] s;\n  assign s = @a + b;\nendmodule\n", "a")
	md := info.Markdown()
	for _, want := range []string{"`a` : `logic [7:0]` (8 bits)", "in `a + b` : `logic [7:0]` (8 bits, 9 in this assignment)"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in %q", want, md)
		}
	}
}

func TestRebuildsWhenGenerationChanges(t *testing.T) {
	toks, _ := preprocessor.Preprocess("u", "module m;\n  logic [3:0] x;\n  assign x = x;\nendmodule\n", nil)
	f1, _ := parser.Parse("u", toks)
	toks, _ = preprocessor.Preprocess("u", "module m;\n  logic [7:0] x;\n  assign x = x;\nendmodule\n", nil)
	f2, _ := parser.Parse("u", toks)
	cur, gen := f1, uint64(1)
	s := New(func() ([]*ast.File, uint64) { return []*ast.File{cur}, gen })
	a, _ := s.Describe("u", 2, 13, "x")
	cur, gen = f2, 2
	b, _ := s.Describe("u", 2, 13, "x")
	if a.Expr == nil || b.Expr == nil || a.Expr.Bits != 4 || b.Expr.Bits != 8 {
		t.Fatalf("a=%+v b=%+v", a.Expr, b.Expr)
	}
}

func TestDiagnosticsGroupedByFile(t *testing.T) {
	s := serviceFor(t, "file:///d.sv", "module m(input a);\n  assign a = 1'b0;\nendmodule\nmodule top;\n  nothere u();\nendmodule\n")
	byFile, gen := s.Diagnostics()
	if gen != 1 {
		t.Fatalf("generation %d", gen)
	}
	codes := map[string]bool{}
	for _, d := range byFile["file:///d.sv"] {
		codes[d.Code] = true
	}
	if !codes["assign-to-input"] || !codes["unknown-module"] || len(byFile) != 1 {
		t.Fatalf("got %+v", byFile)
	}
}

func TestDiagnosticsOfACleanDesignAreEmpty(t *testing.T) {
	s := serviceFor(t, "file:///c.sv", "module leaf(input a, output b);\n  assign b = a;\nendmodule\nmodule top(input x, output y);\n  leaf u(.a(x), .b(y));\nendmodule\n")
	if byFile, _ := s.Diagnostics(); len(byFile) != 0 {
		t.Fatalf("got %+v", byFile)
	}
}
