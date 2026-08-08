package lspserver

import (
	"fmt"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/sv"
)

// TextDocumentReferences finds every occurrence relevant to the
// identifier under the cursor (see scopedOccurrences for exactly what
// "relevant" means -- workspace-wide for a module/class/etc., restricted
// to the enclosing module/interface/program when that's safe). When
// IncludeDeclaration is false, occurrences that are themselves a
// declaration site of that name (anywhere within the already-computed
// result set) are filtered out.
func (s *Server) TextDocumentReferences(context *glsp.Context, params *protocol.ReferenceParams) ([]protocol.Location, error) {
	text, ok := s.textForURI(params.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	line, character := int(params.Position.Line), int(params.Position.Character)

	word, start, ok := sv.WordAt(text, line, character)
	if !ok {
		return nil, nil
	}
	qualifier, hasQualifier := sv.QualifierAt(text, line, start)

	locs := s.scopedOccurrences(sv.Lex(text), params.TextDocument.URI, line, character, start, word, qualifier, hasQualifier)
	if params.Context.IncludeDeclaration {
		return locs, nil
	}
	declLocs, haveDecls := s.index.Lookup(word)
	return excludeDeclarationSites(locs, declLocs, haveDecls), nil
}

type occKey struct {
	uri       string
	line      int
	character int
}

func excludeDeclarationSites(locs []protocol.Location, declLocs []sv.Location, haveDecls bool) []protocol.Location {
	if !haveDecls {
		return locs
	}
	declSet := make(map[occKey]bool, len(declLocs))
	for _, d := range declLocs {
		declSet[occKey{d.URI, d.Line, d.Character}] = true
	}

	filtered := locs[:0]
	for _, l := range locs {
		key := occKey{l.URI, int(l.Range.Start.Line), int(l.Range.Start.Character)}
		if !declSet[key] {
			filtered = append(filtered, l)
		}
	}
	return filtered
}

// TextDocumentRename renames the identifier under the cursor everywhere
// scopedOccurrences finds it relevant (always including declaration
// sites, unlike references). The scope restriction cuts down false
// positives for module-internal names, but class/package members and
// file-scope declarations still search the whole workspace (see
// sv.Index.ScopedOccurrences) -- most editors show the resulting
// WorkspaceEdit as a reviewable diff before applying it, which remains
// the practical safety net for those, not anything this server does.
//
// Refuses outright (no WorkspaceEdit at all) when the cursor sits on a
// reserved keyword: the occurrence index deliberately records keyword
// tokens too (see occurrencesFromSVParseTokens), and a keyword never
// resolves to a declaration, so ScopedOccurrences falls back to every raw
// textual occurrence of it workspace-wide -- without this guard, renaming
// with the cursor on, say, "module" would rewrite every occurrence of
// that word in the whole workspace. A non-keyword word that simply
// doesn't resolve to any declaration is NOT refused -- lexical,
// workspace-wide rename of such a name is existing, intentional behavior.
func (s *Server) TextDocumentRename(context *glsp.Context, params *protocol.RenameParams) (*protocol.WorkspaceEdit, error) {
	if !sv.IsIdentifier(params.NewName) {
		return nil, fmt.Errorf("%q is not a valid identifier", params.NewName)
	}

	text, ok := s.textForURI(params.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	line, character := int(params.Position.Line), int(params.Position.Character)

	word, start, ok := sv.WordAt(text, line, character)
	if !ok {
		return nil, nil
	}
	if sv.IsKeyword(word) {
		return nil, fmt.Errorf("cannot rename %q: it is a SystemVerilog keyword", word)
	}
	qualifier, hasQualifier := sv.QualifierAt(text, line, start)

	locs := s.scopedOccurrences(sv.Lex(text), params.TextDocument.URI, line, character, start, word, qualifier, hasQualifier)
	if len(locs) == 0 {
		return nil, nil
	}

	changes := make(map[protocol.DocumentUri][]protocol.TextEdit)
	for _, loc := range locs {
		changes[loc.URI] = append(changes[loc.URI], protocol.TextEdit{Range: loc.Range, NewText: params.NewName})
	}
	return &protocol.WorkspaceEdit{Changes: changes}, nil
}
