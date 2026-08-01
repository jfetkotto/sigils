package lspserver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tliron/commonlog"
	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/document"
	"github.com/jfetkotto/sigils/internal/sv"
	"github.com/jfetkotto/sigils/internal/workspace"
)

func newTestServer() *Server {
	return NewServer(commonlog.GetLogger("test"))
}

func TestInitializeFindsWorkspaceRootAndConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, workspace.ConfigFileName), []byte(`{"filelists":["products/Spu/SpuTO1/abc/top.f"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "ip", "Spu")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	s := newTestServer()
	params := &protocol.InitializeParams{
		WorkspaceFolders: []protocol.WorkspaceFolder{
			{URI: "file://" + nested, Name: "Spu"},
		},
	}

	result, err := s.Initialize(nil, params)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	initResult, ok := result.(protocol.InitializeResult)
	if !ok {
		t.Fatalf("expected protocol.InitializeResult, got %T", result)
	}
	sync, ok := initResult.Capabilities.TextDocumentSync.(*protocol.TextDocumentSyncOptions)
	if !ok {
		t.Fatalf("expected *TextDocumentSyncOptions, got %T", initResult.Capabilities.TextDocumentSync)
	}
	if sync.Change == nil || *sync.Change != protocol.TextDocumentSyncKindFull {
		t.Fatalf("expected full-document sync, got %+v", sync.Change)
	}
	if sync.OpenClose == nil || !*sync.OpenClose {
		t.Fatalf("expected OpenClose capability to be advertised")
	}

	gotRoot, _ := filepath.EvalSymlinks(s.Root())
	wantRoot, _ := filepath.EvalSymlinks(root)
	if gotRoot != wantRoot {
		t.Fatalf("Root() = %q, want %q", s.Root(), root)
	}
	if len(s.Config().Filelists) != 1 || s.Config().Filelists[0] != "products/Spu/SpuTO1/abc/top.f" {
		t.Fatalf("unexpected config: %+v", s.Config())
	}
	if s.Discoverer() == nil {
		t.Fatalf("expected a discoverer to be set")
	}
}

func TestInitializeToleratesMissingWorkspaceRoot(t *testing.T) {
	dir := t.TempDir() // no .sigils.json anywhere above this
	s := newTestServer()
	params := &protocol.InitializeParams{
		WorkspaceFolders: []protocol.WorkspaceFolder{{URI: "file://" + dir}},
	}

	if _, err := s.Initialize(nil, params); err != nil {
		t.Fatalf("Initialize should not fail when no workspace root is found: %v", err)
	}
	if s.Root() != "" {
		t.Fatalf("expected empty root, got %q", s.Root())
	}
}

func TestShutdownTracksReceipt(t *testing.T) {
	s := newTestServer()
	if s.ShutdownReceived() {
		t.Fatalf("shutdown should not be received yet")
	}
	if err := s.Shutdown(nil); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !s.ShutdownReceived() {
		t.Fatalf("expected ShutdownReceived to be true after Shutdown")
	}
}

func TestDocumentLifecycle(t *testing.T) {
	s := newTestServer()
	uri := "file:///a.sv"

	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "systemverilog", Version: 1, Text: "module a; endmodule"},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}
	if doc, ok := s.Documents().Get(document.URI(uri)); !ok || doc.Text != "module a; endmodule" {
		t.Fatalf("unexpected document after open: %+v, ok=%v", doc, ok)
	}

	err := s.TextDocumentDidChange(nil, &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: uri},
			Version:                2,
		},
		ContentChanges: []any{protocol.TextDocumentContentChangeEventWhole{Text: "module a; initial begin end endmodule"}},
	})
	if err != nil {
		t.Fatalf("DidChange: %v", err)
	}
	doc, ok := s.Documents().Get(document.URI(uri))
	if !ok || doc.Version != 2 || doc.Text != "module a; initial begin end endmodule" {
		t.Fatalf("unexpected document after change: %+v, ok=%v", doc, ok)
	}

	if err := s.TextDocumentDidClose(nil, &protocol.DidCloseTextDocumentParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
	}); err != nil {
		t.Fatalf("DidClose: %v", err)
	}
	if _, ok := s.Documents().Get(document.URI(uri)); ok {
		t.Fatalf("expected document to be gone after close")
	}
}

func TestDidCloseRescansFromDiskDiscardingUnsavedEdits(t *testing.T) {
	// A close-without-save leaves the index reflecting the discarded
	// buffer edits forever: the file's own content never changed on disk,
	// so no watch event would ever come along to correct it. DidClose
	// must rescan from disk itself.
	dir := t.TempDir()
	path := filepath.Join(dir, "top.sv")
	writeFileT(t, path, "module m;\n  logic disk_only_ref;\nendmodule\n")
	uri := pathToURI(path)

	s := newTestServer()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "systemverilog", Version: 1, Text: "module m;\n  logic disk_only_ref;\nendmodule\n"},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}
	if err := s.TextDocumentDidChange(nil, &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: uri}, Version: 2,
		},
		ContentChanges: []any{protocol.TextDocumentContentChangeEventWhole{Text: "module m;\n  logic buffer_only_ref;\nendmodule\n"}},
	}); err != nil {
		t.Fatalf("DidChange: %v", err)
	}
	if _, ok := s.Index().Lookup("buffer_only_ref"); !ok {
		t.Fatalf("expected the unsaved edit to be indexed before close")
	}

	if err := s.TextDocumentDidClose(nil, &protocol.DidCloseTextDocumentParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
	}); err != nil {
		t.Fatalf("DidClose: %v", err)
	}

	if _, ok := s.Index().Lookup("disk_only_ref"); !ok {
		t.Fatalf("expected the on-disk content to be indexed after close")
	}
	if _, ok := s.Index().Lookup("buffer_only_ref"); ok {
		t.Fatalf("expected the discarded unsaved edit to no longer be indexed after close")
	}
}

func TestDidChangeToleratesIncrementalShape(t *testing.T) {
	s := newTestServer()
	uri := "file:///a.sv"
	s.Documents().Open(document.URI(uri), "systemverilog", 1, "old")

	rangeVal := protocol.Range{}
	err := s.TextDocumentDidChange(nil, &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: uri},
			Version:                2,
		},
		ContentChanges: []any{protocol.TextDocumentContentChangeEvent{Range: &rangeVal, Text: "new"}},
	})
	if err != nil {
		t.Fatalf("DidChange: %v", err)
	}
	doc, _ := s.Documents().Get(document.URI(uri))
	if doc.Text != "new" {
		t.Fatalf("expected incremental change's Text to be used as the full replacement, got %q", doc.Text)
	}
}

func TestUriToPath(t *testing.T) {
	path, err := uriToPath("file:///workspace/ip/Spu")
	if err != nil {
		t.Fatalf("uriToPath: %v", err)
	}
	if path != "/workspace/ip/Spu" {
		t.Fatalf("uriToPath = %q, want /workspace/ip/Spu", path)
	}

	if _, err := uriToPath("http://example.com"); err == nil {
		t.Fatalf("expected an error for a non-file URI scheme")
	}
}

func TestPathToURIRoundTrips(t *testing.T) {
	path, err := uriToPath(pathToURI("/workspace/ip/Spu/top.sv"))
	if err != nil {
		t.Fatalf("uriToPath: %v", err)
	}
	if path != "/workspace/ip/Spu/top.sv" {
		t.Fatalf("round trip = %q, want /workspace/ip/Spu/top.sv", path)
	}
}

func TestTextDocumentDefinitionFindsDeclarationAcrossOpenDocuments(t *testing.T) {
	s := newTestServer()

	leafURI := "file:///leaf.sv"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: leafURI, LanguageID: "systemverilog", Version: 1, Text: "module leaf;\nendmodule\n"},
	}); err != nil {
		t.Fatalf("DidOpen leaf: %v", err)
	}

	topURI := "file:///top.sv"
	topText := "module top;\n  leaf u_leaf ();\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: topURI, LanguageID: "systemverilog", Version: 1, Text: topText},
	}); err != nil {
		t.Fatalf("DidOpen top: %v", err)
	}

	// "leaf" on line 1 of top.sv starts at character 2.
	result, err := s.TextDocumentDefinition(nil, &protocol.DefinitionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: topURI},
			Position:     protocol.Position{Line: 1, Character: 3},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentDefinition: %v", err)
	}

	locs, ok := result.([]protocol.Location)
	if !ok || len(locs) != 1 {
		t.Fatalf("expected exactly one location, got %#v", result)
	}
	if locs[0].URI != leafURI {
		t.Fatalf("definition URI = %q, want %q", locs[0].URI, leafURI)
	}
	if locs[0].Range.Start.Line != 0 || locs[0].Range.Start.Character != 7 {
		t.Fatalf("unexpected range: %+v", locs[0].Range)
	}
}

func TestTextDocumentDefinitionParameterOverrideResolvesToTargetParam(t *testing.T) {
	s := newTestServer()

	leafURI := "file:///leaf.sv"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: leafURI, LanguageID: "systemverilog", Version: 1,
			Text: "module leaf #(parameter int WIDTH = 8) ();\nendmodule\n",
		},
	}); err != nil {
		t.Fatalf("DidOpen leaf: %v", err)
	}

	topURI := "file:///top.sv"
	topText := "module top;\n  leaf #(\n    .WIDTH(16)\n  ) u_leaf (\n  );\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: topURI, LanguageID: "systemverilog", Version: 1, Text: topText},
	}); err != nil {
		t.Fatalf("DidOpen top: %v", err)
	}
	overrideLine := "    .WIDTH(16)"

	result, err := s.TextDocumentDefinition(nil, &protocol.DefinitionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: topURI},
			Position:     protocol.Position{Line: 2, Character: protocol.UInteger(strings.Index(overrideLine, "WIDTH"))},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentDefinition: %v", err)
	}
	locs, ok := result.([]protocol.Location)
	if !ok || len(locs) != 1 || locs[0].URI != leafURI {
		t.Fatalf("expected exactly one location in leaf.sv, got %#v", result)
	}
}

func TestTextDocumentDefinitionNamedPortConnectionResolvesToTargetPort(t *testing.T) {
	s := newTestServer()

	leafURI := "file:///leaf.sv"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: leafURI, LanguageID: "systemverilog", Version: 1,
			Text: "module leaf(input logic clk, output logic [7:0] data);\nendmodule\n",
		},
	}); err != nil {
		t.Fatalf("DidOpen leaf: %v", err)
	}

	topURI := "file:///top.sv"
	topText := "module top;\n  leaf u_leaf (\n    .clk(sig),\n    .data(bus)\n  );\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: topURI, LanguageID: "systemverilog", Version: 1, Text: topText},
	}); err != nil {
		t.Fatalf("DidOpen top: %v", err)
	}
	connectionLine := "    .clk(sig),"

	result, err := s.TextDocumentDefinition(nil, &protocol.DefinitionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: topURI},
			Position:     protocol.Position{Line: 2, Character: protocol.UInteger(strings.Index(connectionLine, "clk"))},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentDefinition: %v", err)
	}
	locs, ok := result.([]protocol.Location)
	if !ok || len(locs) != 1 || locs[0].URI != leafURI {
		t.Fatalf("expected exactly one location in leaf.sv, got %#v", result)
	}
}

func TestTextDocumentDeclarationNamedPortConnectionResolvesToTargetPort(t *testing.T) {
	s := newTestServer()

	leafURI := "file:///leaf.sv"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: leafURI, LanguageID: "systemverilog", Version: 1,
			Text: "module leaf(input logic clk, output logic [7:0] data);\nendmodule\n",
		},
	}); err != nil {
		t.Fatalf("DidOpen leaf: %v", err)
	}

	topURI := "file:///top.sv"
	topText := "module top;\n  leaf u_leaf (\n    .clk(sig),\n    .data(bus)\n  );\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: topURI, LanguageID: "systemverilog", Version: 1, Text: topText},
	}); err != nil {
		t.Fatalf("DidOpen top: %v", err)
	}
	connectionLine := "    .data(bus)"

	result, err := s.TextDocumentDeclaration(nil, &protocol.DeclarationParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: topURI},
			Position:     protocol.Position{Line: 3, Character: protocol.UInteger(strings.Index(connectionLine, "data"))},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentDeclaration: %v", err)
	}
	locs, ok := result.([]protocol.Location)
	if !ok || len(locs) != 1 || locs[0].URI != leafURI {
		t.Fatalf("expected exactly one location in leaf.sv, got %#v", result)
	}
}

func TestTextDocumentDefinitionNoMatchReturnsNil(t *testing.T) {
	s := newTestServer()
	uri := "file:///a.sv"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "systemverilog", Version: 1, Text: "module a;\nendmodule\n"},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	result, err := s.TextDocumentDefinition(nil, &protocol.DefinitionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Position:     protocol.Position{Line: 0, Character: 0}, // "module" keyword itself, not a declared name
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentDefinition: %v", err)
	}
	if result != nil {
		t.Fatalf("expected a nil result, got %#v", result)
	}
}

func TestTextDocumentDefinitionFollowsLiveEditsOverDisk(t *testing.T) {
	s := newTestServer()
	uri := "file:///a.sv"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "systemverilog", Version: 1, Text: "module old_name;\nendmodule\n"},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}
	if _, ok := s.Index().Lookup("old_name"); !ok {
		t.Fatalf("expected old_name to be indexed after open")
	}

	err := s.TextDocumentDidChange(nil, &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: uri},
			Version:                2,
		},
		ContentChanges: []any{protocol.TextDocumentContentChangeEventWhole{Text: "module new_name;\nendmodule\n"}},
	})
	if err != nil {
		t.Fatalf("DidChange: %v", err)
	}

	if _, ok := s.Index().Lookup("old_name"); ok {
		t.Fatalf("expected old_name to be gone from the index after the edit")
	}
	if _, ok := s.Index().Lookup("new_name"); !ok {
		t.Fatalf("expected new_name to be indexed after the edit")
	}
}

func TestInitializeWithFilelistConfigIndexesWorkspaceInBackground(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, workspace.ConfigFileName), []byte(`{"filelists":["top.f"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "top.f"), []byte("leaf.sv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "leaf.sv"), []byte("module leaf;\nendmodule\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newTestServer()
	if _, err := s.Initialize(nil, &protocol.InitializeParams{
		WorkspaceFolders: []protocol.WorkspaceFolder{{URI: "file://" + root}},
	}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := s.Index().Lookup("leaf"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background indexing did not pick up leaf.sv in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTextDocumentDeclarationPrefersPrototypeOverBody(t *testing.T) {
	s := newTestServer()
	uri := "file:///a.sv"
	// Call bar() from a sibling method: unqualified, so it resolves via
	// the scope chain to the class's own children -- which only contains
	// the extern prototype (the out-of-line "foo::bar" body isn't
	// lexically nested under the class at all).
	src := `class foo;
  extern function void bar();
  function void caller();
    bar();
  endfunction
endclass
function foo::bar();
endfunction
`
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	// "bar" on line 3 ("    bar();") starts at character 4.
	pos := protocol.Position{Line: 3, Character: 5}

	declResult, err := s.TextDocumentDeclaration(nil, &protocol.DeclarationParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Position:     pos,
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentDeclaration: %v", err)
	}
	declLocs, ok := declResult.([]protocol.Location)
	if !ok || len(declLocs) != 1 || declLocs[0].Range.Start.Line != 1 {
		t.Fatalf("expected the prototype at line 1, got %#v", declResult)
	}

	defResult, err := s.TextDocumentDefinition(nil, &protocol.DefinitionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Position:     pos,
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentDefinition: %v", err)
	}
	defLocs, ok := defResult.([]protocol.Location)
	if !ok || len(defLocs) != 1 || defLocs[0].Range.Start.Line != 6 {
		t.Fatalf("expected the out-of-line body at line 6, got %#v", defResult)
	}
}

func TestClientSupportsSnippets(t *testing.T) {
	var withSupport protocol.InitializeParams
	if err := json.Unmarshal([]byte(`{"capabilities":{"textDocument":{"completion":{"completionItem":{"snippetSupport":true}}}}}`), &withSupport); err != nil {
		t.Fatal(err)
	}
	if !clientSupportsSnippets(&withSupport) {
		t.Fatalf("expected snippet support to be detected")
	}

	var withoutSupport protocol.InitializeParams
	if err := json.Unmarshal([]byte(`{"capabilities":{}}`), &withoutSupport); err != nil {
		t.Fatal(err)
	}
	if clientSupportsSnippets(&withoutSupport) {
		t.Fatalf("expected no snippet support when capabilities are absent")
	}
}

func TestTextDocumentCompletionSuggestsUnconnectedPorts(t *testing.T) {
	s := newTestServer()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///leaf.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module leaf(input logic clk, input logic rst_n);\nendmodule\n",
		},
	}); err != nil {
		t.Fatalf("DidOpen leaf: %v", err)
	}

	topText := "module top;\n  leaf u_leaf (\n    .clk(c),\n    .\n  );\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///top.sv", LanguageID: "systemverilog", Version: 1, Text: topText},
	}); err != nil {
		t.Fatalf("DidOpen top: %v", err)
	}

	// Cursor right after the "." on the incomplete last line.
	result, err := s.TextDocumentCompletion(nil, &protocol.CompletionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 3, Character: 5},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentCompletion: %v", err)
	}
	items, ok := result.([]protocol.CompletionItem)
	if !ok || len(items) != 1 || items[0].Label != "rst_n" {
		t.Fatalf("expected exactly [rst_n] (clk already connected), got %#v", result)
	}
}

func TestTextDocumentCompletionSuggestsParameterOverrides(t *testing.T) {
	s := newTestServer()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///leaf.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module leaf #(parameter int WIDTH = 8, localparam int FIXED = 1) ();\nendmodule\n",
		},
	}); err != nil {
		t.Fatalf("DidOpen leaf: %v", err)
	}

	topText := "module top;\n  leaf #(\n    .\n  ) u_leaf (\n  );\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///top.sv", LanguageID: "systemverilog", Version: 1, Text: topText},
	}); err != nil {
		t.Fatalf("DidOpen top: %v", err)
	}

	// Cursor right after the "." on the incomplete line inside "#(...)".
	result, err := s.TextDocumentCompletion(nil, &protocol.CompletionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 2, Character: 5},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentCompletion: %v", err)
	}
	items, ok := result.([]protocol.CompletionItem)
	if !ok || len(items) != 1 || items[0].Label != "WIDTH" {
		t.Fatalf("expected exactly [WIDTH] (FIXED is a localparam, not overridable), got %#v", result)
	}
}

func TestTextDocumentCompletionPortItemsIncludeDirectionInDetail(t *testing.T) {
	s := newTestServer()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///leaf.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module leaf(input logic clk, output logic [7:0] data);\nendmodule\n",
		},
	}); err != nil {
		t.Fatalf("DidOpen leaf: %v", err)
	}

	topText := "module top;\n  leaf u_leaf (\n    .\n  );\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///top.sv", LanguageID: "systemverilog", Version: 1, Text: topText},
	}); err != nil {
		t.Fatalf("DidOpen top: %v", err)
	}

	result, err := s.TextDocumentCompletion(nil, &protocol.CompletionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 2, Character: 5},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentCompletion: %v", err)
	}
	items, ok := result.([]protocol.CompletionItem)
	if !ok || len(items) != 2 {
		t.Fatalf("expected 2 port completion items, got %#v", result)
	}

	wantDetail := map[string]string{"clk": "input logic", "data": "output logic [7:0]"}
	for _, item := range items {
		want, ok := wantDetail[item.Label]
		if !ok {
			t.Fatalf("unexpected completion item %q", item.Label)
		}
		if item.Detail == nil || *item.Detail != want {
			t.Fatalf("item %q: Detail = %v, want %q", item.Label, item.Detail, want)
		}
	}
}

func TestTextDocumentCompletionNoInstantiationContextReturnsNil(t *testing.T) {
	s := newTestServer()
	uri := "file:///a.sv"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "systemverilog", Version: 1, Text: "module a;\nendmodule\n"},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	result, err := s.TextDocumentCompletion(nil, &protocol.CompletionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Position:     protocol.Position{Line: 0, Character: 0},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentCompletion: %v", err)
	}
	if result != nil {
		t.Fatalf("expected a nil result outside any instantiation, got %#v", result)
	}
}

func TestTextDocumentCompletionInsertTextFormat(t *testing.T) {
	newInitializedServer := func(t *testing.T, snippetSupport bool) *Server {
		t.Helper()
		s := newTestServer()
		raw := `{"capabilities":{}}`
		if snippetSupport {
			raw = `{"capabilities":{"textDocument":{"completion":{"completionItem":{"snippetSupport":true}}}}}`
		}
		var params protocol.InitializeParams
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Initialize(nil, &params); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		return s
	}

	leafText := "module leaf(input logic clk);\nendmodule\n"
	topText := "module top;\n  leaf u_leaf (\n    .\n  );\nendmodule\n"

	open := func(t *testing.T, s *Server) {
		t.Helper()
		if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
			TextDocument: protocol.TextDocumentItem{URI: "file:///leaf.sv", LanguageID: "systemverilog", Version: 1, Text: leafText},
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
			TextDocument: protocol.TextDocumentItem{URI: "file:///top.sv", LanguageID: "systemverilog", Version: 1, Text: topText},
		}); err != nil {
			t.Fatal(err)
		}
	}

	complete := func(t *testing.T, s *Server) protocol.CompletionItem {
		t.Helper()
		result, err := s.TextDocumentCompletion(nil, &protocol.CompletionParams{
			TextDocumentPositionParams: protocol.TextDocumentPositionParams{
				TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
				Position:     protocol.Position{Line: 2, Character: 5},
			},
		})
		if err != nil {
			t.Fatalf("TextDocumentCompletion: %v", err)
		}
		items, ok := result.([]protocol.CompletionItem)
		if !ok || len(items) != 1 {
			t.Fatalf("expected exactly one completion item, got %#v", result)
		}
		return items[0]
	}

	t.Run("with snippet support", func(t *testing.T) {
		s := newInitializedServer(t, true)
		open(t, s)
		item := complete(t, s)
		if item.InsertTextFormat == nil || *item.InsertTextFormat != protocol.InsertTextFormatSnippet {
			t.Fatalf("expected InsertTextFormatSnippet, got %+v", item)
		}
		edit, ok := item.TextEdit.(*protocol.TextEdit)
		if !ok {
			t.Fatalf("expected a TextEdit, got %#v", item.TextEdit)
		}
		// No leading "." here: one was already typed in topText, and the
		// edit must not duplicate it (this is the bug this design avoids).
		if edit.NewText != "clk($0)" {
			t.Fatalf("expected newText \"clk($0)\" with no leading dot, got %q", edit.NewText)
		}
		if edit.Range.Start.Character != 5 || edit.Range.End.Character != 5 {
			t.Fatalf("expected a zero-width edit range at the cursor, got %+v", edit.Range)
		}
	})

	t.Run("without snippet support", func(t *testing.T) {
		s := newInitializedServer(t, false)
		open(t, s)
		item := complete(t, s)
		if item.InsertTextFormat != nil {
			t.Fatalf("expected no InsertTextFormat (defaults to plain text), got %+v", item)
		}
		edit, ok := item.TextEdit.(*protocol.TextEdit)
		if !ok {
			t.Fatalf("expected a TextEdit, got %#v", item.TextEdit)
		}
		if edit.NewText != "clk" {
			t.Fatalf("expected newText \"clk\", got %q", edit.NewText)
		}
	})
}

func TestTextDocumentCompletionAddsDotWhenNoneWasTyped(t *testing.T) {
	s := newTestServer()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///leaf.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module leaf(input logic clk);\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	topText := "module top;\n  leaf u_leaf (\n  \n  );\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///top.sv", LanguageID: "systemverilog", Version: 1, Text: topText},
	}); err != nil {
		t.Fatal(err)
	}

	// Cursor on the blank line inside the parens -- no "." typed at all,
	// as with a manual completion invocation (Ctrl+Space).
	result, err := s.TextDocumentCompletion(nil, &protocol.CompletionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 2, Character: 2},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentCompletion: %v", err)
	}
	items, ok := result.([]protocol.CompletionItem)
	if !ok || len(items) != 1 {
		t.Fatalf("expected exactly one completion item, got %#v", result)
	}
	edit, ok := items[0].TextEdit.(*protocol.TextEdit)
	if !ok {
		t.Fatalf("expected a TextEdit, got %#v", items[0].TextEdit)
	}
	if edit.NewText != ".clk" {
		t.Fatalf("expected newText \".clk\" (the dot must be added since none was typed), got %q", edit.NewText)
	}
}

func TestTextDocumentCompletionReplacesPartiallyTypedPortName(t *testing.T) {
	s := newTestServer()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///leaf.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module leaf(input logic rst_n);\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	topText := "module top;\n  leaf u_leaf (\n    .rs\n  );\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///top.sv", LanguageID: "systemverilog", Version: 1, Text: topText},
	}); err != nil {
		t.Fatal(err)
	}

	// Cursor right after "rs" in ".rs".
	result, err := s.TextDocumentCompletion(nil, &protocol.CompletionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 2, Character: 7},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentCompletion: %v", err)
	}
	items, ok := result.([]protocol.CompletionItem)
	if !ok || len(items) != 1 {
		t.Fatalf("expected exactly one completion item, got %#v", result)
	}
	edit, ok := items[0].TextEdit.(*protocol.TextEdit)
	if !ok {
		t.Fatalf("expected a TextEdit, got %#v", items[0].TextEdit)
	}
	if edit.NewText != "rst_n" {
		t.Fatalf("expected newText \"rst_n\" with no leading dot, got %q", edit.NewText)
	}
	// The edit must cover "rs" (characters 5-7), replacing it -- not just
	// insert at the cursor, which would read ".rsrst_n" instead of
	// ".rst_n".
	if edit.Range.Start.Character != 5 || edit.Range.End.Character != 7 {
		t.Fatalf("expected edit range [5,7) covering the already-typed \"rs\", got %+v", edit.Range)
	}
}

func TestTextDocumentCompletionGeneralSymbols(t *testing.T) {
	s := newTestServer()
	uri := "file:///a.sv"
	src := `typedef enum {IDLE, RUNNING} state_t;
function void helper();
endfunction
module top;
  he
endmodule
`
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	// "he" on line 4 ("  he") ends at character 4.
	result, err := s.TextDocumentCompletion(nil, &protocol.CompletionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Position:     protocol.Position{Line: 4, Character: 4},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentCompletion: %v", err)
	}
	items, ok := result.([]protocol.CompletionItem)
	if !ok || len(items) != 1 || items[0].Label != "helper" {
		t.Fatalf("expected exactly [helper], got %#v", result)
	}
	if items[0].Kind == nil || *items[0].Kind != protocol.CompletionItemKindFunction {
		t.Fatalf("expected CompletionItemKindFunction, got %+v", items[0].Kind)
	}
	edit, ok := items[0].TextEdit.(*protocol.TextEdit)
	if !ok {
		t.Fatalf("expected a TextEdit, got %#v", items[0].TextEdit)
	}
	if edit.NewText != "helper" {
		t.Fatalf("expected newText \"helper\", got %q", edit.NewText)
	}
	if edit.Range.Start.Character != 2 || edit.Range.End.Character != 4 {
		t.Fatalf("expected edit range [2,4) covering \"he\", got %+v", edit.Range)
	}
}

func TestTextDocumentCompletionGeneralSymbolsEmptyPrefixReturnsNil(t *testing.T) {
	s := newTestServer()
	uri := "file:///a.sv"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "systemverilog", Version: 1, Text: "module top;\n\nendmodule\n"},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	result, err := s.TextDocumentCompletion(nil, &protocol.CompletionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Position:     protocol.Position{Line: 1, Character: 0},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentCompletion: %v", err)
	}
	if result != nil {
		t.Fatalf("expected a nil result for an empty prefix, got %#v", result)
	}
}

func TestTextDocumentCompletionGeneralSymbolsIncludesEnumMember(t *testing.T) {
	s := newTestServer()
	uri := "file:///a.sv"
	src := "typedef enum {IDLE, RUNNING} state_t;\nmodule top;\n  RUN\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	// "RUN" on line 2 ("  RUN") ends at character 5.
	result, err := s.TextDocumentCompletion(nil, &protocol.CompletionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Position:     protocol.Position{Line: 2, Character: 5},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentCompletion: %v", err)
	}
	items, ok := result.([]protocol.CompletionItem)
	if !ok || len(items) != 1 || items[0].Label != "RUNNING" {
		t.Fatalf("expected exactly [RUNNING], got %#v", result)
	}
	if items[0].Kind == nil || *items[0].Kind != protocol.CompletionItemKindEnumMember {
		t.Fatalf("expected CompletionItemKindEnumMember, got %+v", items[0].Kind)
	}
}

func TestTextDocumentCompletionTruncatedResultIsIncomplete(t *testing.T) {
	s := newTestServer()
	uri := "file:///a.sv"

	var b strings.Builder
	const total = maxSymbolCompletions + 50
	for i := range total {
		fmt.Fprintf(&b, "typedef logic pfx_%04d;\n", i)
	}
	b.WriteString("module top;\n  pfx_\nendmodule\n")
	src := b.String()

	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	// Lines 0..total-1 are the typedefs, line total is "module top;", and
	// "  pfx_" (where completion is requested, ending at character 6) is
	// line total+1.
	result, err := s.TextDocumentCompletion(nil, &protocol.CompletionParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Position:     protocol.Position{Line: uint32(total + 1), Character: 6},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentCompletion: %v", err)
	}
	list, ok := result.(protocol.CompletionList)
	if !ok {
		t.Fatalf("expected a protocol.CompletionList for a truncated result, got %#v", result)
	}
	if !list.IsIncomplete {
		t.Fatalf("expected IsIncomplete=true, got %+v", list)
	}
	if len(list.Items) != maxSymbolCompletions {
		t.Fatalf("expected exactly %d items, got %d", maxSymbolCompletions, len(list.Items))
	}
}

func TestCompletionItemKindForNewDeclarationKinds(t *testing.T) {
	cases := []struct {
		kind sv.Kind
		want protocol.CompletionItemKind
	}{
		{sv.KindVariable, protocol.CompletionItemKindVariable},
		{sv.KindPort, protocol.CompletionItemKindProperty},
		{sv.KindParameter, protocol.CompletionItemKindConstant},
	}
	for _, c := range cases {
		if got := completionItemKindFor(c.kind); got != c.want {
			t.Errorf("completionItemKindFor(%s) = %v, want %v", c.kind, got, c.want)
		}
	}
}
