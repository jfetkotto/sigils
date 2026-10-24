package lspserver

import (
	"testing"

	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/semantic"
	"github.com/jfetkotto/sigils/internal/workspace"
)

func TestEncodeSemanticTokensIsRelativeToThePreviousToken(t *testing.T) {
	got := encodeSemanticTokens([]semantic.SemToken{
		{Line: 0, Character: 7, Length: 3, Type: 1, Modifiers: 1},
		{Line: 0, Character: 12, Length: 2, Type: 6},
		{Line: 2, Character: 4, Length: 5, Type: 3, Modifiers: 2},
	})
	want := []protocol.UInteger{0, 7, 3, 1, 1, 0, 5, 2, 6, 0, 2, 4, 5, 3, 2}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSemanticTokensRequest(t *testing.T) {
	s := newTestServer()
	openText(t, s, "file:///t.sv", "module m(input a);\n  logic b;\n  assign b = a;\nendmodule\n")
	res, err := s.TextDocumentSemanticTokensFull(nil, &protocol.SemanticTokensParams{TextDocument: protocol.TextDocumentIdentifier{URI: "file:///t.sv"}})
	if err != nil || res == nil || len(res.Data) == 0 || len(res.Data)%5 != 0 {
		t.Fatalf("got %+v, %v", res, err)
	}
	off := false
	s.cfg = workspace.Config{SemanticTokens: &off}
	if res, _ := s.TextDocumentSemanticTokensFull(nil, &protocol.SemanticTokensParams{TextDocument: protocol.TextDocumentIdentifier{URI: "file:///t.sv"}}); res != nil {
		t.Fatalf("tokens served although switched off: %+v", res)
	}
}

func TestLegendMatchesTheTokenTypes(t *testing.T) {
	l := semanticTokensLegend()
	if len(l.TokenTypes) != semantic.TokMethod+1 || l.TokenTypes[semantic.TokMethod] != "method" || l.TokenTypes[semantic.TokNamespace] != "namespace" {
		t.Fatalf("legend %+v", l)
	}
}
