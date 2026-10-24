package lspserver

import (
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/semantic"
)

// semanticTokensLegend is the legend advertised at initialize; the integers
// in a response index into it (see semantic.TokenTypes).
func semanticTokensLegend() protocol.SemanticTokensLegend {
	return protocol.SemanticTokensLegend{TokenTypes: semantic.TokenTypes, TokenModifiers: semantic.TokenModifiers}
}

// TextDocumentSemanticTokensFull colours the names the semantic model
// resolves in the document: packages, types, parameters, ports, signals,
// functions and so on, telling apart what looks alike in text. Keywords,
// literals and comments are left to the editor's grammar.
func (s *Server) TextDocumentSemanticTokensFull(_ *glsp.Context, params *protocol.SemanticTokensParams) (*protocol.SemanticTokens, error) {
	if !s.semanticTokensEnabled() {
		return nil, nil
	}
	tokens := s.sem.SemanticTokens(params.TextDocument.URI)
	return &protocol.SemanticTokens{Data: encodeSemanticTokens(tokens)}, nil
}

func (s *Server) semanticTokensEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.SemanticTokensEnabled()
}

// encodeSemanticTokens packs tokens into the protocol's relative form: five
// integers per token, the position relative to the previous token (LSP 3.16,
// textDocument/semanticTokens).
func encodeSemanticTokens(tokens []semantic.SemToken) []protocol.UInteger {
	data := make([]protocol.UInteger, 0, 5*len(tokens))
	prevLine, prevChar := 0, 0
	for _, t := range tokens {
		deltaLine := t.Line - prevLine
		deltaChar := t.Character
		if deltaLine == 0 {
			deltaChar = t.Character - prevChar
		}
		data = append(data,
			protocol.UInteger(deltaLine), protocol.UInteger(deltaChar),
			protocol.UInteger(t.Length), protocol.UInteger(t.Type), protocol.UInteger(t.Modifiers))
		prevLine, prevChar = t.Line, t.Character
	}
	return data
}
