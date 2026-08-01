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
	s.Index().SetFile("file:///many.sv", b.String())

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
