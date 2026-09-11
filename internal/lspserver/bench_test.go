package lspserver

import (
	"fmt"
	"strings"
	"testing"

	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/document"
)

// A synthetic workspace big enough for the O(workspace) costs these
// benchmarks exist to catch to show up: 200 modules that each import a
// shared package, declare a struct-typed signal, and instantiate a common
// leaf with named port connections.
const benchFiles = 200

func benchPkg() string {
	return "package pa_cfg;\n" +
		"  typedef struct packed {\n    logic [3:0] ckSideband;\n    logic [7:0] payload;\n  } ty_bundle;\n" +
		"  typedef logic [7:0] bus_t;\n" +
		"  localparam int WIDTH = 8;\n" +
		"endpackage\n"
}

func benchLeaf() string {
	return "module leaf(\n  input  logic clk,\n  input  logic rst,\n  input  logic [7:0] dataIn,\n  output logic [7:0] dataOut\n);\nendmodule\n"
}

func benchModule(i int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "import pa_cfg::*;\nmodule blk%d #(parameter int W = 8) (\n", i)
	b.WriteString("  input  logic clk,\n  input  logic rst,\n  input  logic [7:0] dataIn,\n  output logic [7:0] dataOut\n);\n")
	// Imported at both file and module scope, the belt-and-braces pattern
	// real code uses -- and the one that exercises the scope-chain walk.
	b.WriteString("  import pa_cfg::*;\n")
	b.WriteString("  pa_cfg::ty_bundle st_link;\n  bus_t sig;\n")
	for j := range 25 {
		fmt.Fprintf(&b, "  logic [7:0] node%d;\n", j)
		fmt.Fprintf(&b, "  always_comb begin\n    node%d = dataIn ^ st_link.ckSideband;\n  end\n", j)
	}
	fmt.Fprintf(&b, "  leaf u_leaf%d (.clk(clk), .rst(rst), .dataIn(dataIn), .dataOut(dataOut));\n", i)
	b.WriteString("endmodule\n")
	return b.String()
}

func benchWorkspace(tb testing.TB) (*Server, string) {
	tb.Helper()
	s := newTestServer()
	s.index.SetFile("file:///pa_cfg.sv", benchPkg())
	s.index.SetFile("file:///leaf.sv", benchLeaf())
	for i := range benchFiles {
		s.index.SetFile(fmt.Sprintf("file:///blk%d.sv", i), benchModule(i))
	}
	probe := "file:///blk0.sv"
	s.docs.Open(document.URI(probe), "systemverilog", 1, benchModule(0))
	return s, probe
}

// benchPos locates needle's first occurrence and returns a position inside
// it, so a benchmark doesn't hard-code line/column numbers that drift when
// the generator changes.
func benchPos(tb testing.TB, src, needle string) protocol.Position {
	tb.Helper()
	for i, l := range strings.Split(src, "\n") {
		if c := strings.Index(l, needle); c >= 0 {
			return protocol.Position{Line: uint32(i), Character: uint32(c + 1)}
		}
	}
	tb.Fatalf("token %q not found in generated source", needle)
	return protocol.Position{}
}

func benchTextDocPos(uri string, pos protocol.Position) protocol.TextDocumentPositionParams {
	return protocol.TextDocumentPositionParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: protocol.DocumentUri(uri)},
		Position:     pos,
	}
}

// BenchmarkIndexWorkspace covers the startup path: scanning every file
// once. Dominated by svparse's lexer and parser, so it's where a change
// there shows up end to end.
func BenchmarkIndexWorkspace(b *testing.B) {
	srcs := make([]string, benchFiles)
	for i := range benchFiles {
		srcs[i] = benchModule(i)
	}
	b.ResetTimer()
	for range b.N {
		s := newTestServer()
		s.index.SetFile("file:///pa_cfg.sv", benchPkg())
		s.index.SetFile("file:///leaf.sv", benchLeaf())
		for i, src := range srcs {
			s.index.SetFile(fmt.Sprintf("file:///blk%d.sv", i), src)
		}
	}
}

// Resolution through a wildcard import, with imports visible at two
// scopes -- the ancestor-chain walk runs once per import statement.
func BenchmarkDefinitionThroughImport(b *testing.B) {
	s, probe := benchWorkspace(b)
	params := &protocol.DefinitionParams{
		TextDocumentPositionParams: benchTextDocPos(probe, benchPos(b, benchModule(0), "bus_t sig")),
	}
	b.ResetTimer()
	for range b.N {
		if _, err := s.TextDocumentDefinition(nil, params); err != nil {
			b.Fatal(err)
		}
	}
}

// Find-references on a port unions in every named connection site, which
// is why connection sites need a by-name index rather than a full scan.
func BenchmarkReferencesPort(b *testing.B) {
	s, probe := benchWorkspace(b)
	params := &protocol.ReferenceParams{
		TextDocumentPositionParams: benchTextDocPos(probe, benchPos(b, benchModule(0), "dataOut")),
		Context:                    protocol.ReferenceContext{IncludeDeclaration: true},
	}
	b.ResetTimer()
	for range b.N {
		if _, err := s.TextDocumentReferences(nil, params); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCompletionPrefix(b *testing.B) {
	s, probe := benchWorkspace(b)
	pos := benchPos(b, benchModule(0), "bus_t sig")
	pos.Character = 7
	params := &protocol.CompletionParams{TextDocumentPositionParams: benchTextDocPos(probe, pos)}
	b.ResetTimer()
	for range b.N {
		if _, err := s.TextDocumentCompletion(nil, params); err != nil {
			b.Fatal(err)
		}
	}
}

// Iterates every distinct name in the workspace, so it's the guard on any
// attempt to make that loop cheaper -- see WorkspaceSymbols' own note
// about the lowercase cache that measured slower.
func BenchmarkWorkspaceSymbolsQuery(b *testing.B) {
	s, _ := benchWorkspace(b)
	b.ResetTimer()
	for range b.N {
		if _, err := s.WorkspaceSymbol(nil, &protocol.WorkspaceSymbolParams{Query: "node"}); err != nil {
			b.Fatal(err)
		}
	}
}

// Cursor on "begin". A keyword never resolves to a declaration, and
// occurrences deliberately record keyword tokens, so this used to fall
// into the unscoped fallback and flatten every "begin" in the workspace
// before discarding all but the current file's.
func BenchmarkDocumentHighlightKeyword(b *testing.B) {
	s, probe := benchWorkspace(b)
	params := &protocol.DocumentHighlightParams{
		TextDocumentPositionParams: benchTextDocPos(probe, benchPos(b, benchModule(0), "begin")),
	}
	b.ResetTimer()
	for range b.N {
		if _, err := s.TextDocumentDocumentHighlight(nil, params); err != nil {
			b.Fatal(err)
		}
	}
}

// The same request on an ordinary signal, so a change that only helps the
// keyword case can't be mistaken for a general win.
func BenchmarkDocumentHighlightSignal(b *testing.B) {
	s, probe := benchWorkspace(b)
	params := &protocol.DocumentHighlightParams{
		TextDocumentPositionParams: benchTextDocPos(probe, benchPos(b, benchModule(0), "dataIn")),
	}
	b.ResetTimer()
	for range b.N {
		if _, err := s.TextDocumentDocumentHighlight(nil, params); err != nil {
			b.Fatal(err)
		}
	}
}
