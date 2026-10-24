package lspserver

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/sv"
)

// comparisonWorkspace is a small design that exercises what navigation has to
// resolve: package members through scope and import, typedefs, parameters,
// instance ports, locals shadowing outer names, functions, generate blocks.
var comparisonWorkspace = map[string]string{
	"file:///pkg.sv": `package defs;
  localparam int WIDTH = 8;
  typedef logic [WIDTH-1:0] word_t;
  typedef enum logic [1:0] {IDLE, BUSY, DONE} state_t;
  function automatic word_t twice(word_t v);
    return v << 1;
  endfunction
endpackage
`,
	"file:///leaf.sv": `module leaf #(parameter int DEPTH = 4)(
  input  logic clk,
  input  defs::word_t din,
  output defs::word_t dout
);
  defs::word_t mem [DEPTH];
  always_ff @(posedge clk) begin
    mem[0] <= din;
    dout   <= mem[DEPTH-1];
  end
endmodule
`,
	"file:///top.sv": `module top(input logic clk, input defs::word_t a, output defs::word_t y);
  import defs::*;
  state_t state;
  word_t t0, t1;
  leaf #(.DEPTH(2)) u_leaf(.clk(clk), .din(t0), .dout(t1));
  always_comb begin
    t0 = twice(a);
    state = IDLE;
    if (t1 == WIDTH) state = DONE;
  end
  for (genvar i = 0; i < 2; i++) begin : g
    logic bit_q;
    assign bit_q = t1[i];
  end
  assign y = t1;
endmodule
`,
}

// identPositions lists the position of the first character of every
// identifier-like word in text.
func identPositions(text string) [][2]int {
	var out [][2]int
	for l, line := range strings.Split(text, "\n") {
		start := -1
		for c := 0; c <= len(line); c++ {
			isWord := c < len(line) && (line[c] == '_' || line[c] >= '0' && line[c] <= '9' || line[c] >= 'a' && line[c] <= 'z' || line[c] >= 'A' && line[c] <= 'Z')
			switch {
			case isWord && start < 0:
				start = c
			case !isWord && start >= 0:
				if !sv.IsKeyword(line[start:c]) && !(line[start] >= '0' && line[start] <= '9') {
					out = append(out, [2]int{l, start})
				}
				start = -1
			}
		}
	}
	return out
}

func navDefinitionAt(t *testing.T, s *Server, uri string, line, character int) []string {
	t.Helper()
	res, err := s.TextDocumentDefinition(nil, &protocol.DefinitionParams{TextDocumentPositionParams: protocol.TextDocumentPositionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
		Position:     protocol.Position{Line: protocol.UInteger(line), Character: protocol.UInteger(character)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	locs, _ := res.([]protocol.Location)
	var out []string
	for _, l := range locs {
		out = append(out, fmt.Sprintf("%s:%d:%d", filepath.Base(l.URI), l.Range.Start.Line, l.Range.Start.Character))
	}
	sort.Strings(out)
	return out
}

// compareNavigation checks the semantic definition against the lexical one at
// every identifier of the workspace: where both answer they must agree. It
// returns how many positions each answered, which a test can bound.
func compareNavigation(t *testing.T, files map[string]string) (both, onlySemantic, onlyLexical int) {
	t.Helper()
	lexical := newTestServer()
	semantic := newTestServer()
	for uri, text := range files {
		openText(t, lexical, uri, text)
		openText(t, semantic, uri, text)
	}
	var uris []string
	for uri := range files {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	for _, uri := range uris {
		for _, p := range identPositions(files[uri]) {
			lex := navDefinitionAt(t, lexical, uri, p[0], p[1])
			var sem []string
			if locs, ok := semantic.sem.Definition(uri, p[0], p[1]); ok { // the model alone, without the lexical fallback
				for _, l := range locs {
					sem = append(sem, fmt.Sprintf("%s:%d:%d", filepath.Base(l.URI), l.Line, l.Character))
				}
			}
			switch {
			case len(lex) > 0 && len(sem) > 0:
				both++
				if strings.Join(lex, ",") != strings.Join(sem, ",") {
					t.Errorf("%s:%d:%d: lexical %v, semantic %v", filepath.Base(uri), p[0], p[1], lex, sem)
				}
			case len(sem) > 0:
				onlySemantic++
				t.Logf("only semantic: %s:%d:%d -> %v", filepath.Base(uri), p[0], p[1], sem)
			case len(lex) > 0:
				onlyLexical++
				t.Logf("only lexical: %s:%d:%d -> %v", filepath.Base(uri), p[0], p[1], lex)
			}
		}
	}
	return
}

func TestSemanticDefinitionAgreesWithLexicalWhereBothAnswer(t *testing.T) {
	both, onlySem, onlyLex := compareNavigation(t, comparisonWorkspace)
	t.Logf("both=%d only semantic=%d only lexical=%d", both, onlySem, onlyLex)
	if both < 40 {
		t.Fatalf("only %d positions were answered by both: the comparison proves little", both)
	}
}

// With ROOT set, the same comparison runs over a real tree of .sv files.
func TestSemanticDefinitionAgreesOnARealTree(t *testing.T) {
	root := os.Getenv("SIGILS_COMPARE_ROOT")
	if root == "" {
		t.Skip("set SIGILS_COMPARE_ROOT to a directory of SystemVerilog files")
	}
	files := map[string]string{}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(path, ".sv") || strings.HasSuffix(path, ".v")) {
			if data, err := os.ReadFile(path); err == nil {
				files[pathToURI(path)] = string(data)
			}
		}
		return nil
	})
	both, onlySem, onlyLex := compareNavigation(t, files)
	t.Logf("%d files: both=%d only semantic=%d only lexical=%d", len(files), both, onlySem, onlyLex)
}

// The lexical references are every occurrence of the name in the module (or
// workspace) that could be it; the semantic ones are exactly the uses of the
// symbol, so each must be among the lexical ones.
func TestSemanticReferencesAreAmongTheLexicalOnes(t *testing.T) {
	lexical := newTestServer()
	semantic := newTestServer()
	for uri, text := range comparisonWorkspace {
		openText(t, lexical, uri, text)
		openText(t, semantic, uri, text)
	}
	checked := 0
	for uri, text := range comparisonWorkspace {
		for _, p := range identPositions(text) {
			sem, ok := semantic.sem.References(uri, p[0], p[1], true)
			if !ok {
				continue
			}
			res, err := lexical.TextDocumentReferences(nil, &protocol.ReferenceParams{
				TextDocumentPositionParams: protocol.TextDocumentPositionParams{
					TextDocument: protocol.TextDocumentIdentifier{URI: uri},
					Position:     protocol.Position{Line: protocol.UInteger(p[0]), Character: protocol.UInteger(p[1])},
				},
				Context: protocol.ReferenceContext{IncludeDeclaration: true},
			})
			if err != nil {
				t.Fatal(err)
			}
			have := map[string]bool{}
			for _, l := range res {
				have[fmt.Sprintf("%s:%d:%d", l.URI, l.Range.Start.Line, l.Range.Start.Character)] = true
			}
			for _, l := range sem {
				checked++
				if !have[fmt.Sprintf("%s:%d:%d", l.URI, l.Line, l.Character)] {
					t.Errorf("from %s:%d:%d the semantic reference %s:%d:%d is not among the lexical ones", filepath.Base(uri), p[0], p[1], filepath.Base(l.URI), l.Line, l.Character)
				}
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only %d references compared", checked)
	}
}

// With the flag on, the handlers use the model and fall back where it has
// no answer.
func TestSemanticNavigationFlagSelectsTheModel(t *testing.T) {
	on := true
	s := newTestServer()
	s.cfg.SemanticNavigation = &on
	for uri, text := range comparisonWorkspace {
		openText(t, s, uri, text)
	}
	// "t0" in "t0 = twice(a);" : the model resolves it.
	if got := navDefinitionAt(t, s, "file:///top.sv", 6, 4); len(got) != 1 || got[0] != "top.sv:3:9" {
		t.Fatalf("definition of t0: %v", got)
	}
	// "defs" in "import defs::*;" : only the lexical lookup resolves it.
	if got := navDefinitionAt(t, s, "file:///top.sv", 1, 9); len(got) != 1 || got[0] != "pkg.sv:0:8" {
		t.Fatalf("fallback for the import: %v", got)
	}
	refs, err := s.TextDocumentReferences(nil, &protocol.ReferenceParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 6, Character: 4},
		},
	})
	if err != nil || len(refs) != 2 {
		t.Fatalf("references of t0 without the declaration: %v %v", refs, err)
	}
}
