package lspserver

import (
	"strings"
	"testing"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

func openTwoFilesReferencingLeaf(t *testing.T, s *Server) {
	t.Helper()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///leaf.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module leaf;\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///top.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module top;\n  leaf u_leaf ();\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTextDocumentReferencesIncludesDeclaration(t *testing.T) {
	s := newTestServer()
	openTwoFilesReferencingLeaf(t, s)

	// Cursor on "leaf" (the instantiation) in top.sv, line 1.
	locs, err := s.TextDocumentReferences(nil, &protocol.ReferenceParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 1, Character: 3},
		},
		Context: protocol.ReferenceContext{IncludeDeclaration: true},
	})
	if err != nil {
		t.Fatalf("TextDocumentReferences: %v", err)
	}
	if len(locs) != 2 {
		t.Fatalf("expected 2 locations (declaration + use), got %+v", locs)
	}
}

func TestTextDocumentReferencesExcludesDeclaration(t *testing.T) {
	s := newTestServer()
	openTwoFilesReferencingLeaf(t, s)

	locs, err := s.TextDocumentReferences(nil, &protocol.ReferenceParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 1, Character: 3},
		},
		Context: protocol.ReferenceContext{IncludeDeclaration: false},
	})
	if err != nil {
		t.Fatalf("TextDocumentReferences: %v", err)
	}
	if len(locs) != 1 || locs[0].URI != "file:///top.sv" {
		t.Fatalf("expected exactly the use in top.sv, got %+v", locs)
	}
}

func TestTextDocumentRenameAcrossMultipleFiles(t *testing.T) {
	s := newTestServer()
	openTwoFilesReferencingLeaf(t, s)

	edit, err := s.TextDocumentRename(nil, &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 1, Character: 3},
		},
		NewName: "leaf_renamed",
	})
	if err != nil {
		t.Fatalf("TextDocumentRename: %v", err)
	}
	if edit == nil {
		t.Fatalf("expected a WorkspaceEdit")
	}
	if len(edit.Changes) != 2 {
		t.Fatalf("expected edits in 2 files, got %+v", edit.Changes)
	}
	leafEdits := edit.Changes["file:///leaf.sv"]
	topEdits := edit.Changes["file:///top.sv"]
	if len(leafEdits) != 1 || leafEdits[0].NewText != "leaf_renamed" {
		t.Fatalf("unexpected leaf.sv edits: %+v", leafEdits)
	}
	if len(topEdits) != 1 || topEdits[0].NewText != "leaf_renamed" {
		t.Fatalf("unexpected top.sv edits: %+v", topEdits)
	}
}

func TestTextDocumentRenameEmitsUTF16RangesAfterNonBMPCharacter(t *testing.T) {
	s := newTestServer()
	// The 😀 (U+1F600) is 1 rune but 2 UTF-16 code units, so "wide_mod"
	// spans UTF-16 columns [16, 24) -- a rune-counted range would be one
	// column short and corrupt the file when the edit is applied.
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///w.sv", LanguageID: "systemverilog", Version: 1,
			Text: "/* 😀 */ module wide_mod;\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}

	edit, err := s.TextDocumentRename(nil, &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///w.sv"},
			Position:     protocol.Position{Line: 0, Character: 18},
		},
		NewName: "renamed_mod",
	})
	if err != nil {
		t.Fatalf("TextDocumentRename: %v", err)
	}
	if edit == nil {
		t.Fatalf("expected a WorkspaceEdit")
	}
	edits := edit.Changes["file:///w.sv"]
	if len(edits) != 1 {
		t.Fatalf("expected exactly one edit, got %+v", edits)
	}
	r := edits[0].Range
	if r.Start.Character != 16 || r.End.Character != 24 {
		t.Fatalf("expected the edit to span UTF-16 columns [16, 24), got [%d, %d)", r.Start.Character, r.End.Character)
	}
}

func TestTextDocumentRenameRejectsInvalidIdentifier(t *testing.T) {
	s := newTestServer()
	openTwoFilesReferencingLeaf(t, s)

	_, err := s.TextDocumentRename(nil, &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 1, Character: 3},
		},
		NewName: "not a valid name",
	})
	if err == nil {
		t.Fatalf("expected an error for an invalid new name")
	}
}

func TestTextDocumentRenameRejectsKeywordAtCursor(t *testing.T) {
	s := newTestServer()
	openTwoFilesReferencingLeaf(t, s)

	// Cursor on "module" (a keyword), not on "top" itself -- character 0.
	edit, err := s.TextDocumentRename(nil, &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 0, Character: 0},
		},
		NewName: "whatever",
	})
	if err == nil {
		t.Fatalf("expected an error for renaming a keyword occurrence")
	}
	if edit != nil {
		t.Fatalf("expected no WorkspaceEdit, got %+v", edit)
	}
}

func TestTextDocumentRenameRejectsKeywordAsNewName(t *testing.T) {
	s := newTestServer()
	openTwoFilesReferencingLeaf(t, s)

	_, err := s.TextDocumentRename(nil, &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 1, Character: 3},
		},
		NewName: "logic",
	})
	if err == nil {
		t.Fatalf("expected an error for renaming to a keyword-shaped new name")
	}
}

func TestTextDocumentRenameStillWorksForOrdinaryIdentifier(t *testing.T) {
	// Regression guard alongside the keyword-rejection tests above: a
	// normal signal/module name at the cursor must still rename normally.
	s := newTestServer()
	openTwoFilesReferencingLeaf(t, s)

	edit, err := s.TextDocumentRename(nil, &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 1, Character: 3},
		},
		NewName: "leaf_renamed",
	})
	if err != nil {
		t.Fatalf("TextDocumentRename: %v", err)
	}
	if edit == nil {
		t.Fatalf("expected a WorkspaceEdit")
	}
}

func TestTextDocumentRenameNoWordAtCursorReturnsNil(t *testing.T) {
	s := newTestServer()
	// Three spaces so a cursor in the middle one isn't touching either
	// identifier on either side (see the equivalent documentHighlight test).
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: "module   a; endmodule"},
	}); err != nil {
		t.Fatal(err)
	}

	edit, err := s.TextDocumentRename(nil, &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 0, Character: 7},
		},
		NewName: "b",
	})
	if err != nil {
		t.Fatalf("TextDocumentRename: %v", err)
	}
	if edit != nil {
		t.Fatalf("expected a nil WorkspaceEdit, got %+v", edit)
	}
}

func TestTextDocumentRenameRestrictsToModuleScope(t *testing.T) {
	s := newTestServer()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module mod_a;\n  function void helper();\n  endfunction\n  initial helper();\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///b.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module mod_b;\n  function void helper();\n  endfunction\n  initial helper();\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}

	edit, err := s.TextDocumentRename(nil, &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 3, Character: 12}, // the "helper()" call
		},
		NewName: "helper_renamed",
	})
	if err != nil {
		t.Fatalf("TextDocumentRename: %v", err)
	}
	if edit == nil {
		t.Fatalf("expected a WorkspaceEdit")
	}
	if len(edit.Changes) != 1 {
		t.Fatalf("expected mod_b to be untouched, got edits in %d file(s): %+v", len(edit.Changes), edit.Changes)
	}
	aEdits := edit.Changes["file:///a.sv"]
	if len(aEdits) != 2 {
		t.Fatalf("expected 2 edits in mod_a (declaration + call), got %+v", aEdits)
	}
	if _, touched := edit.Changes["file:///b.sv"]; touched {
		t.Fatalf("expected mod_b's unrelated same-named helper to be untouched")
	}
}

func openLeafLeaf2AndTopBothInstantiated(t *testing.T, s *Server) {
	t.Helper()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///leaf.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module leaf(input logic clk);\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///leaf2.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module leaf2(input logic clk);\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///top.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module top;\n  leaf u_leaf(.clk(a));\n  leaf2 u_leaf2(.clk(b));\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTextDocumentReferencesNamedPortConnectionExcludesUnrelatedModule(t *testing.T) {
	s := newTestServer()
	openLeafLeaf2AndTopBothInstantiated(t, s)

	connectionLine := "  leaf u_leaf(.clk(a));"
	locs, err := s.TextDocumentReferences(nil, &protocol.ReferenceParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 1, Character: protocol.UInteger(strings.Index(connectionLine, "clk"))},
		},
		Context: protocol.ReferenceContext{IncludeDeclaration: true},
	})
	if err != nil {
		t.Fatalf("TextDocumentReferences: %v", err)
	}

	foundLeaf2Connection := false
	for _, l := range locs {
		if l.URI == "file:///top.sv" && l.Range.Start.Line == 2 {
			foundLeaf2Connection = true
		}
	}
	if foundLeaf2Connection {
		t.Fatalf("expected u_leaf2's unrelated .clk( connection to be excluded, got %+v", locs)
	}
	if len(locs) == 0 {
		t.Fatalf("expected at least leaf's own clk declaration and u_leaf's connection")
	}
}

func TestTextDocumentRenameNamedPortConnectionScopedCorrectly(t *testing.T) {
	s := newTestServer()
	openLeafLeaf2AndTopBothInstantiated(t, s)

	connectionLine := "  leaf u_leaf(.clk(a));"
	edit, err := s.TextDocumentRename(nil, &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 1, Character: protocol.UInteger(strings.Index(connectionLine, "clk"))},
		},
		NewName: "clk_renamed",
	})
	if err != nil {
		t.Fatalf("TextDocumentRename: %v", err)
	}
	if edit == nil {
		t.Fatalf("expected a WorkspaceEdit")
	}
	if _, touched := edit.Changes["file:///leaf2.sv"]; touched {
		t.Fatalf("expected leaf2's unrelated clk to be untouched, got %+v", edit.Changes)
	}
	topEdits := edit.Changes["file:///top.sv"]
	if len(topEdits) != 1 || topEdits[0].Range.Start.Line != 1 {
		t.Fatalf("expected exactly 1 edit in top.sv (u_leaf's connection only), got %+v", topEdits)
	}
	leafEdits := edit.Changes["file:///leaf.sv"]
	if len(leafEdits) != 1 {
		t.Fatalf("expected exactly 1 edit in leaf.sv (its own clk declaration), got %+v", leafEdits)
	}
}

func TestTextDocumentRenamePortDeclarationUpdatesConnectionSites(t *testing.T) {
	// Mirror image of TestTextDocumentRenameNamedPortConnectionScopedCorrectly:
	// starting the rename from the port's own DECLARATION in leaf.sv (not
	// from a connection site) must still reach top.sv's named connection to
	// it, or the rename silently leaves that instance disconnected on that
	// pin. leaf2's unrelated same-named "clk" -- and its own connection in
	// top.sv -- must stay untouched.
	s := newTestServer()
	openLeafLeaf2AndTopBothInstantiated(t, s)

	declLine := "module leaf(input logic clk);"
	edit, err := s.TextDocumentRename(nil, &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///leaf.sv"},
			Position:     protocol.Position{Line: 0, Character: protocol.UInteger(strings.Index(declLine, "clk"))},
		},
		NewName: "clk_renamed",
	})
	if err != nil {
		t.Fatalf("TextDocumentRename: %v", err)
	}
	if edit == nil {
		t.Fatalf("expected a WorkspaceEdit")
	}
	if _, touched := edit.Changes["file:///leaf2.sv"]; touched {
		t.Fatalf("expected leaf2's unrelated clk to be untouched, got %+v", edit.Changes)
	}
	leafEdits := edit.Changes["file:///leaf.sv"]
	if len(leafEdits) != 1 {
		t.Fatalf("expected exactly 1 edit in leaf.sv (its own clk declaration), got %+v", leafEdits)
	}
	topEdits := edit.Changes["file:///top.sv"]
	if len(topEdits) != 1 || topEdits[0].Range.Start.Line != 1 {
		t.Fatalf("expected exactly 1 edit in top.sv (u_leaf's connection only), got %+v", topEdits)
	}
}

func openLeafLeaf2AndTopWithOverridableParams(t *testing.T, s *Server) {
	t.Helper()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///leaf.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module leaf #(parameter int WIDTH = 8) ();\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///leaf2.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module leaf2 #(parameter int WIDTH = 8) ();\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///top.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module top;\n  leaf #(.WIDTH(16)) u_leaf ();\n  leaf2 #(.WIDTH(16)) u_leaf2 ();\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTextDocumentRenameParameterDeclarationUpdatesOverrideSites(t *testing.T) {
	// Parameter counterpart to TestTextDocumentRenamePortDeclarationUpdatesConnectionSites:
	// renaming leaf's own "WIDTH" parameter from its declaration must reach
	// top.sv's "#(.WIDTH(16))" override on u_leaf, while leaving leaf2's
	// unrelated same-named WIDTH (and u_leaf2's override) untouched.
	s := newTestServer()
	openLeafLeaf2AndTopWithOverridableParams(t, s)

	declLine := "module leaf #(parameter int WIDTH = 8) ();"
	edit, err := s.TextDocumentRename(nil, &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///leaf.sv"},
			Position:     protocol.Position{Line: 0, Character: protocol.UInteger(strings.Index(declLine, "WIDTH"))},
		},
		NewName: "WIDTH_RENAMED",
	})
	if err != nil {
		t.Fatalf("TextDocumentRename: %v", err)
	}
	if edit == nil {
		t.Fatalf("expected a WorkspaceEdit")
	}
	if _, touched := edit.Changes["file:///leaf2.sv"]; touched {
		t.Fatalf("expected leaf2's unrelated WIDTH to be untouched, got %+v", edit.Changes)
	}
	leafEdits := edit.Changes["file:///leaf.sv"]
	if len(leafEdits) != 1 {
		t.Fatalf("expected exactly 1 edit in leaf.sv (its own WIDTH declaration), got %+v", leafEdits)
	}
	topEdits := edit.Changes["file:///top.sv"]
	if len(topEdits) != 1 || topEdits[0].Range.Start.Line != 1 {
		t.Fatalf("expected exactly 1 edit in top.sv (u_leaf's override only), got %+v", topEdits)
	}
}
