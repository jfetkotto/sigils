package semantic

import (
	"fmt"
	"testing"
)

func tokenText(text string, toks []SemToken) []string {
	lines := splitLines(text)
	var out []string
	for _, t := range toks {
		word := lines[t.Line][t.Character : t.Character+t.Length]
		out = append(out, fmt.Sprintf("%s:%s:%d", word, TokenTypes[t.Type], t.Modifiers))
	}
	return out
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func TestSemanticTokensClassifyResolvedNames(t *testing.T) {
	pkg := "package p;\n  typedef logic [3:0] nib_t;\n  localparam int K = 2;\nendpackage\n"
	top := "module top #(parameter W = 4)(input clk, output logic q);\n  import p::*;\n  nib_t n;\n  assign q = n[0] & clk;\n  function int f(int a); return a; endfunction\nendmodule\n"
	s := serviceForFiles(t, map[string]string{"p.sv": pkg, "top.sv": top})
	got := tokenText(top, s.SemanticTokens("top.sv"))
	want := map[string]bool{
		"top:class:1":    true, // module declaration
		"W:variable:3":   true, // parameter: declaration + readonly
		"clk:property:1": true, // port
		"q:property:1":   true,
		"nib_t:type:0":   true, // typedef use
		"n:variable:1":   true, // signal declaration
		"f:function:1":   true,
		"a:parameter:1":  true, // function argument
	}
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	for w := range want {
		if !have[w] {
			t.Errorf("missing %s in %v", w, got)
		}
	}
	// Uses are not declarations.
	if !have["q:property:0"] || !have["n:variable:0"] || !have["clk:property:0"] {
		t.Errorf("uses missing: %v", got)
	}
}

func TestSemanticTokensAreInSourceOrderAndDoNotOverlap(t *testing.T) {
	text := "module m(input a, output b);\n  logic t;\n  assign t = a;\n  assign b = t;\nendmodule\n"
	s := serviceForFiles(t, map[string]string{"u": text})
	toks := s.SemanticTokens("u")
	if len(toks) < 8 {
		t.Fatalf("got %d tokens", len(toks))
	}
	for i := 1; i < len(toks); i++ {
		p, c := toks[i-1], toks[i]
		if c.Line < p.Line || (c.Line == p.Line && c.Character < p.Character+p.Length) {
			t.Fatalf("tokens %d and %d overlap or are out of order: %+v %+v", i-1, i, p, c)
		}
	}
}

func TestSemanticTokensOfUnknownFile(t *testing.T) {
	s := serviceForFiles(t, map[string]string{"u": "module m; endmodule\n"})
	if toks := s.SemanticTokens("nope"); len(toks) != 0 {
		t.Fatalf("got %v", toks)
	}
}
