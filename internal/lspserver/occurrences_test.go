package lspserver

import (
	"testing"

	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/sv"
)

// scopedOccurrencesAt is the convenience the production scopedOccurrences
// deliberately no longer offers: it looks uri's text up and lexes it before
// delegating. Every real handler already holds both, which is exactly why
// scopedOccurrences takes them as parameters instead of re-deriving them
// (see its doc comment); only tests need this step spelled out.
func scopedOccurrencesAt(t *testing.T, s *Server, uri string, line, character, start int, word, qualifier string, hasQualifier bool) []protocol.Location {
	t.Helper()
	text, ok := s.textForURI(uri)
	if !ok {
		t.Fatalf("no text available for %s", uri)
	}
	return s.scopedOccurrences(sv.Lex(text), uri, line, character, start, word, qualifier, hasQualifier)
}

func TestScopedOccurrencesSameFile(t *testing.T) {
	s := newTestServer()
	uri := "file:///top.sv"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: uri, LanguageID: "systemverilog", Version: 1,
			Text: "module top;\n  leaf u_leaf ();\n  leaf u_leaf2 ();\nendmodule\n",
		},
	}); err != nil {
		t.Fatalf("DidOpen: %v", err)
	}

	// "leaf" on line 1 -- a module name, so this stays workspace-wide, but
	// there's only one file open so the result set is the same either way.
	locs := scopedOccurrencesAt(t, s, uri, 1, 3, 2, "leaf", "", false)
	if len(locs) != 2 {
		t.Fatalf("scopedOccurrences(leaf) = %+v, want 2", locs)
	}
	if locs[0].URI != uri || locs[0].Range.Start.Line != 1 || locs[1].Range.Start.Line != 2 {
		t.Fatalf("unexpected locations: %+v", locs)
	}
}

func TestScopedOccurrencesAggregatesAcrossFiles(t *testing.T) {
	s := newTestServer()
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

	locs := scopedOccurrencesAt(t, s, "file:///top.sv", 1, 3, 2, "leaf", "", false)
	if len(locs) != 2 {
		t.Fatalf("scopedOccurrences(leaf) = %+v, want 2 (one per file)", locs)
	}
	uris := map[string]bool{}
	for _, l := range locs {
		uris[l.URI] = true
	}
	if !uris["file:///leaf.sv"] || !uris["file:///top.sv"] {
		t.Fatalf("expected occurrences in both files, got %+v", locs)
	}
}

func TestScopedOccurrencesRestrictsModuleInternalHelper(t *testing.T) {
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

	// "helper" call inside mod_a (line 3) must not pull in mod_b's
	// unrelated same-named helper.
	locs := scopedOccurrencesAt(t, s, "file:///a.sv", 3, 12, 10, "helper", "", false)
	if len(locs) != 2 {
		t.Fatalf("expected 2 occurrences within mod_a only, got %+v", locs)
	}
	for _, l := range locs {
		if l.URI != "file:///a.sv" {
			t.Fatalf("expected only file:///a.sv occurrences, got %+v", locs)
		}
	}
}
