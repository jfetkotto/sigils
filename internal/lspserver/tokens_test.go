package lspserver

import (
	"testing"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestTokensForFollowsEditsAndForgetsClosedDocuments(t *testing.T) {
	s := newTestServer()
	const uri = "file:///top.sv"
	openDoc(t, s, uri, "module top;\nendmodule\n")

	first := s.tokensFor(uri, "module top;\nendmodule\n")
	if again := s.tokensFor(uri, "module top;\nendmodule\n"); &again[0] != &first[0] {
		t.Fatalf("unchanged text was lexed again instead of reusing the cached tokens")
	}

	edited := "module renamed;\nendmodule\n"
	if err := s.TextDocumentDidChange(nil, &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: uri},
			Version:                2,
		},
		ContentChanges: []any{protocol.TextDocumentContentChangeEventWhole{Text: edited}},
	}); err != nil {
		t.Fatal(err)
	}
	if toks := s.tokensFor(uri, edited); toks[1].Text != "renamed" {
		t.Fatalf("tokens after an edit came from the old text: %+v", toks[:2])
	}

	if err := s.TextDocumentDidClose(nil, &protocol.DidCloseTextDocumentParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: uri},
	}); err != nil {
		t.Fatal(err)
	}
	s.tokensFor(uri, edited)
	if _, ok := s.tokens[uri]; ok {
		t.Fatalf("a closed document's tokens are still cached")
	}
}
