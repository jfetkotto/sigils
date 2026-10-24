package lspserver

import (
	"fmt"
	"strings"
	"testing"

	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/sv"
)

func TestWorkspaceSymbolCapsResults(t *testing.T) {
	s := newTestServer()
	var b strings.Builder
	for i := range maxWorkspaceSymbols + 100 {
		fmt.Fprintf(&b, "module mod_%04d;\nendmodule\n", i)
	}
	s.index.SetFile("file:///many.sv", b.String())

	results, err := s.WorkspaceSymbol(nil, &protocol.WorkspaceSymbolParams{Query: "mod_"})
	if err != nil {
		t.Fatalf("WorkspaceSymbol: %v", err)
	}
	if len(results) != maxWorkspaceSymbols {
		t.Fatalf("expected results capped at %d, got %d", maxWorkspaceSymbols, len(results))
	}
}

func TestTextDocumentDocumentSymbolBuildsTree(t *testing.T) {
	s := newTestServer()
	uri := "file:///a.sv"
	src := `class foo;
  function void bar();
  endfunction
endclass
`
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	result, err := s.TextDocumentDocumentSymbol(nil, &protocol.DocumentSymbolParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
	})
	if err != nil {
		t.Fatalf("TextDocumentDocumentSymbol: %v", err)
	}
	tree, ok := result.([]protocol.DocumentSymbol)
	if !ok || len(tree) != 1 || tree[0].Name != "foo" {
		t.Fatalf("expected exactly [foo], got %#v", result)
	}
	if tree[0].Kind != protocol.SymbolKindClass {
		t.Fatalf("expected SymbolKindClass, got %v", tree[0].Kind)
	}
	if len(tree[0].Children) != 1 || tree[0].Children[0].Name != "bar" {
		t.Fatalf("expected foo to have child bar, got %+v", tree[0].Children)
	}
	if tree[0].Children[0].Kind != protocol.SymbolKindFunction {
		t.Fatalf("expected SymbolKindFunction for bar, got %v", tree[0].Children[0].Kind)
	}
}

func TestTextDocumentDocumentSymbolEmptyFileReturnsNil(t *testing.T) {
	s := newTestServer()
	uri := "file:///a.sv"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "systemverilog", Version: 1, Text: "// nothing here\n"},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	result, err := s.TextDocumentDocumentSymbol(nil, &protocol.DocumentSymbolParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
	})
	if err != nil {
		t.Fatalf("TextDocumentDocumentSymbol: %v", err)
	}
	if result != nil {
		t.Fatalf("expected a nil result for a file with no declarations, got %#v", result)
	}
}

func TestTextDocumentFoldingRangeCoversContainersOnly(t *testing.T) {
	s := newTestServer()
	uri := "file:///a.sv"
	src := "typedef logic [7:0] byte_t;\nmodule top;\n  function void f();\n  endfunction\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	ranges, err := s.TextDocumentFoldingRange(nil, &protocol.FoldingRangeParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
	})
	if err != nil {
		t.Fatalf("TextDocumentFoldingRange: %v", err)
	}
	// Exactly "top" and "f" get folds -- the typedef isn't a container.
	if len(ranges) != 2 {
		t.Fatalf("expected 2 folding ranges (typedef excluded), got %+v", ranges)
	}
	if ranges[0].StartLine != 1 || ranges[0].EndLine != 4 {
		t.Fatalf("unexpected top module fold: %+v", ranges[0])
	}
}

func TestWorkspaceSymbolSubstringSearch(t *testing.T) {
	s := newTestServer()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: "module leaf_driver;\nendmodule\n"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///b.sv", LanguageID: "systemverilog", Version: 1, Text: "class other;\nendclass\n"},
	}); err != nil {
		t.Fatal(err)
	}

	results, err := s.WorkspaceSymbol(nil, &protocol.WorkspaceSymbolParams{Query: "driver"})
	if err != nil {
		t.Fatalf("WorkspaceSymbol: %v", err)
	}
	if len(results) != 1 || results[0].Name != "leaf_driver" {
		t.Fatalf("WorkspaceSymbol(driver) = %+v", results)
	}
	if results[0].Location.URI != "file:///a.sv" {
		t.Fatalf("unexpected location: %+v", results[0].Location)
	}
	if results[0].Kind != protocol.SymbolKindModule {
		t.Fatalf("expected SymbolKindModule, got %v", results[0].Kind)
	}
}

func TestSymbolKindForNewDeclarationKinds(t *testing.T) {
	cases := []struct {
		kind sv.Kind
		want protocol.SymbolKind
	}{
		{sv.KindVariable, protocol.SymbolKindVariable},
		{sv.KindPort, protocol.SymbolKindProperty},
		{sv.KindParameter, protocol.SymbolKindConstant},
	}
	for _, c := range cases {
		if got := symbolKindFor(c.kind); got != c.want {
			t.Errorf("symbolKindFor(%s) = %v, want %v", c.kind, got, c.want)
		}
	}
}

// wideModuleDecls builds one module containing n leaf ports -- the shape a
// real chip-scale module has, and the one the tree build used to be
// quadratic in.
func wideModuleDecls(n int) []sv.Declaration {
	decls := make([]sv.Declaration, 0, n+1)
	decls = append(decls, sv.Declaration{
		Kind: sv.KindModule, Name: "wide", Line: 0, Character: 7,
		EndLine: n + 1, EndCharacter: 0, Parent: -1,
	})
	for i := range n {
		decls = append(decls, sv.Declaration{
			Kind: sv.KindPort, Name: fmt.Sprintf("sig_%04d", i),
			Line: i + 1, Character: 2, EndLine: i + 1, EndCharacter: 10, Parent: 0,
		})
	}
	return decls
}

func TestDocumentSymbolTreeWideModule(t *testing.T) {
	decls := wideModuleDecls(500)
	tree := documentSymbolTree(decls, childIndex(decls), -1)
	if len(tree) != 1 {
		t.Fatalf("expected 1 root symbol, got %d", len(tree))
	}
	if got := len(tree[0].Children); got != 500 {
		t.Fatalf("expected 500 children, got %d", got)
	}
	if tree[0].Children[0].Name != "sig_0000" || tree[0].Children[499].Name != "sig_0499" {
		t.Fatalf("children out of order: first=%q last=%q", tree[0].Children[0].Name, tree[0].Children[499].Name)
	}
	if tree[0].Children[0].Children != nil {
		t.Fatalf("a leaf port should have no children, got %+v", tree[0].Children[0].Children)
	}
}

// A Parent index pointing at itself would make the recursion never
// terminate; childIndex drops it rather than trusting the bucket.
func TestDocumentSymbolTreeIgnoresSelfParent(t *testing.T) {
	decls := []sv.Declaration{{Kind: sv.KindModule, Name: "loop", Parent: 0}}
	if tree := documentSymbolTree(decls, childIndex(decls), -1); len(tree) != 0 {
		t.Fatalf("expected no roots for a self-parented decl, got %+v", tree)
	}
}

func BenchmarkDocumentSymbolTreeWideModule(b *testing.B) {
	decls := wideModuleDecls(2000)
	b.ResetTimer()
	for range b.N {
		documentSymbolTree(decls, childIndex(decls), -1)
	}
}

// mapResolver resolves `include directives from an in-memory map, the
// lspserver-side counterpart to internal/sv's own stubResolver (which is
// unexported there).
type mapResolver struct {
	files    map[string]string
	resolved []string
}

func (r *mapResolver) Resolve(includedPath, fromFile string) (text, resolvedPath string, err error) {
	t, ok := r.files[includedPath]
	if !ok {
		return "", "", fmt.Errorf("mapResolver: %q not found", includedPath)
	}
	uri := "file:///" + includedPath
	r.resolved = append(r.resolved, uri)
	return t, uri, nil
}

func (r *mapResolver) Resolved() []string { return append([]string(nil), r.resolved...) }

func TestDocumentSymbolKeepsIncludedDeclarationsAtTheHeadersOwnRoot(t *testing.T) {
	// A declaration pulled into a package body still belongs to the
	// header's own outline, at its root: cross-`include membership is
	// tracked beside the buckets, never as a Parent pointing into another
	// file's slice, which childIndex would drop and documentSymbolTree
	// would never root.
	s := newTestServer()
	s.index.SetIncludeResolverFactory(func() sv.IncludeResolver {
		return &mapResolver{files: map[string]string{
			"cfg_defs.svh": "typedef struct { int fieldA; } t_header_cfg;\n",
		}}
	})
	s.index.SetFile("file:///pkg_cfg.sv", "package pkg_cfg;\n  `include \"cfg_defs.svh\"\nendpackage\n")

	result, err := s.TextDocumentDocumentSymbol(nil, &protocol.DocumentSymbolParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: "file:///cfg_defs.svh"},
	})
	if err != nil {
		t.Fatalf("TextDocumentDocumentSymbol: %v", err)
	}
	tree, ok := result.([]protocol.DocumentSymbol)
	if !ok || len(tree) != 1 || tree[0].Name != "t_header_cfg" {
		t.Fatalf("expected exactly [t_header_cfg] at the header's outline root, got %#v", result)
	}
}

// outlineOf opens src and returns the documentSymbol tree.
func outlineOf(t *testing.T, src string) []protocol.DocumentSymbol {
	t.Helper()
	s := newTestServer()
	uri := "file:///o.sv"
	openText(t, s, uri, src)
	result, err := s.TextDocumentDocumentSymbol(nil, &protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: uri}})
	if err != nil {
		t.Fatal(err)
	}
	tree, _ := result.([]protocol.DocumentSymbol)
	return tree
}

// shape renders a tree as "name(child,child)" for comparison.
func shape(syms []protocol.DocumentSymbol) string {
	parts := make([]string, len(syms))
	for i, s := range syms {
		parts[i] = s.Name
		if len(s.Children) > 0 {
			parts[i] += "(" + shape(s.Children) + ")"
		}
	}
	return strings.Join(parts, ",")
}

func TestOutlineShowsProcessesAndGenerateBlocks(t *testing.T) {
	tree := outlineOf(t, `module m #(parameter N = 2)(input clk, input rst_n, output logic q);
  logic [N-1:0] state;
  always_ff @(posedge clk or negedge rst_n) begin
    state <= '0;
  end
  always_comb q = state[0];
  initial state = '0;
  for (genvar i = 0; i < N; i++) begin : lane
    logic bit_q;
    assign bit_q = state[i];
  end
  if (N > 1) begin : wide
    logic extra;
  end else begin
    logic narrow;
  end
endmodule
`)
	got := shape(tree)
	want := "m(N,clk,rst_n,q,state,always_ff,always_comb,initial,for (i)(lane(bit_q)),if (N > 1)(wide(extra),else(narrow)))"
	if got != want {
		t.Fatalf("outline\n got  %s\n want %s", got, want)
	}
	mod := tree[0]
	for _, c := range mod.Children {
		if c.Name == "always_ff" {
			if c.Detail == nil || *c.Detail != "@(posedge clk or negedge rst_n)" || c.Kind != protocol.SymbolKindEvent {
				t.Errorf("always_ff: %+v", c)
			}
		}
		if c.Name == "for (i)" && c.Kind != protocol.SymbolKindNamespace {
			t.Errorf("for kind: %+v", c)
		}
	}
}

func TestOutlineWithoutStructureIsUnchanged(t *testing.T) {
	tree := outlineOf(t, "module m(input a);\n  logic b;\nendmodule\n")
	if got := shape(tree); got != "m(a,b)" {
		t.Fatalf("got %s", got)
	}
}

// A process inside a class method's module neighbour must not capture
// declarations of the next module.
func TestOutlineProcessesStayInTheirModule(t *testing.T) {
	tree := outlineOf(t, "module a;\n  initial begin end\nendmodule\nmodule b;\n  logic x;\n  always_comb x = 1;\nendmodule\n")
	if got := shape(tree); got != "a(initial),b(x,always_comb)" {
		t.Fatalf("got %s", got)
	}
}

func BenchmarkOutlineWideModule(bench *testing.B) {
	var b strings.Builder
	b.WriteString("module wide(input clk);\n")
	for i := range 3000 {
		fmt.Fprintf(&b, "  logic s%d;\n", i)
		if i%10 == 0 {
			fmt.Fprintf(&b, "  always_ff @(posedge clk) s%d <= 1'b0;\n", i)
		}
	}
	b.WriteString("endmodule\n")
	s := newTestServer()
	s.index.SetFile("file:///w.sv", b.String())
	params := &protocol.DocumentSymbolParams{TextDocument: protocol.TextDocumentIdentifier{URI: "file:///w.sv"}}
	bench.ResetTimer()
	for range bench.N {
		if r, err := s.TextDocumentDocumentSymbol(nil, params); err != nil || r == nil {
			bench.Fatal(r, err)
		}
	}
}
