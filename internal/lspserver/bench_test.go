package lspserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

// A package typedef used by bare name (through import) in every module:
// the package-member filter runs over every one of those occurrences.
func BenchmarkReferencesPackageMember(b *testing.B) {
	s, probe := benchWorkspace(b)
	params := &protocol.ReferenceParams{
		TextDocumentPositionParams: benchTextDocPos(probe, benchPos(b, benchModule(0), "bus_t")),
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

// One keystroke in a module whose port names (clk, rst, dataIn, dataOut)
// are declared by every other module too: the rescan has to retract this
// file's entries from name buckets that span the whole workspace.
func BenchmarkDidChange(b *testing.B) {
	s, probe := benchWorkspace(b)
	texts := [2]string{benchModule(0), benchModule(0) + "\n"}
	b.ResetTimer()
	for i := range b.N {
		params := &protocol.DidChangeTextDocumentParams{
			TextDocument: protocol.VersionedTextDocumentIdentifier{
				TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: protocol.DocumentUri(probe)},
				Version:                int32(i + 2),
			},
			ContentChanges: []any{protocol.TextDocumentContentChangeEventWhole{Text: texts[i%2]}},
		}
		if err := s.TextDocumentDidChange(nil, params); err != nil {
			b.Fatal(err)
		}
	}
}

// Hover on "st_link.ckSideband": the receiver has to be resolved before
// the field can be.
func BenchmarkHoverStructField(b *testing.B) {
	s, probe := benchWorkspace(b)
	pos := benchPos(b, benchModule(0), "ckSideband")
	pos.Character += 2
	params := &protocol.HoverParams{TextDocumentPositionParams: benchTextDocPos(probe, pos)}
	b.ResetTimer()
	for range b.N {
		if h, err := s.TextDocumentHover(nil, params); err != nil || h == nil {
			b.Fatalf("hover = %v, %v", h, err)
		}
	}
}

// benchWideModule is one module declaring n signals, ending with a use of
// the first: resolving it walks a single file's very large declaration
// bucket rather than many small ones.
func benchWideModule(n int) string {
	var b strings.Builder
	b.WriteString("module wide (\n  input logic clk\n);\n")
	for i := range n {
		fmt.Fprintf(&b, "  logic [7:0] sig%d;\n", i)
	}
	b.WriteString("  assign sig1 = sig0;\nendmodule\n")
	return b.String()
}

func BenchmarkDefinitionWideModule(b *testing.B) {
	s := newTestServer()
	const uri = "file:///wide.sv"
	src := benchWideModule(3000)
	s.index.SetFile(uri, src)
	s.docs.Open(document.URI(uri), "systemverilog", 1, src)
	params := &protocol.DefinitionParams{TextDocumentPositionParams: benchTextDocPos(uri, benchPos(b, src, "= sig0"))}
	params.Position.Character += 2
	b.ResetTimer()
	for range b.N {
		if locs, err := s.TextDocumentDefinition(nil, params); err != nil || len(locs.([]protocol.Location)) != 1 {
			b.Fatalf("definition = %v, %v", locs, err)
		}
	}
}

// Saving a header every module includes: each includer is reread from
// disk and rescanned. Each file holds ten modules (about 1,100 lines), so
// scanning outweighs the per-file open, as it does in a real workspace.
func BenchmarkCascadeHeaderSave(b *testing.B) {
	dir := b.TempDir()
	s := newTestServer()
	s.index.SetIncludeResolverFactory(newIncludeResolverFactory(nil))
	hdr := filepath.Join(dir, "hdr.svh")
	if err := os.WriteFile(hdr, []byte("typedef logic [7:0] byte_t;\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	for i := range benchFiles {
		path := filepath.Join(dir, fmt.Sprintf("blk%d.sv", i))
		var src strings.Builder
		src.WriteString("`include \"hdr.svh\"\n")
		for j := range 10 {
			src.WriteString(benchModule(i*10 + j))
		}
		if err := os.WriteFile(path, []byte(src.String()), 0o644); err != nil {
			b.Fatal(err)
		}
		s.index.SetFile(pathToURI(path), src.String())
	}
	changed := []string{pathToURI(hdr)}
	b.ResetTimer()
	for range b.N {
		s.cascadeReindexDependents(context.Background(), changed)
	}
}

// Hover on "bus.req" where bus is an interface-typed port: the receiver
// isn't a struct, so the struct-field lookup fails before the interface
// member lookup answers.
func BenchmarkHoverInterfaceMember(b *testing.B) {
	s, _ := benchWorkspace(b)
	s.index.SetFile("file:///bus_if.sv", "interface bus_if;\n  logic req;\n  logic gnt;\n  modport m (output req, input gnt);\nendinterface\n")
	const uri = "file:///user.sv"
	src := "module user (\n  bus_if bus\n);\n  logic x;\n  assign x = bus.req;\nendmodule\n"
	s.index.SetFile(uri, src)
	s.docs.Open(document.URI(uri), "systemverilog", 1, src)
	pos := benchPos(b, src, "req;")
	params := &protocol.HoverParams{TextDocumentPositionParams: benchTextDocPos(uri, pos)}
	b.ResetTimer()
	for range b.N {
		if h, err := s.TextDocumentHover(nil, params); err != nil || h == nil {
			b.Fatalf("hover = %v, %v", h, err)
		}
	}
}
