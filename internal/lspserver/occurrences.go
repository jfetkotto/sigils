package lspserver

import (
	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/sv"
)

// scopedOccurrences resolves word at (uri, line, character) against the
// index and returns every occurrence relevant to it -- see
// sv.Index.ScopedOccurrences for exactly what "relevant" means (workspace-
// wide for a module/class/package/etc., restricted to the enclosing
// module/interface/program's span when that's a safe restriction, and
// workspace-wide otherwise). This queries pre-built index data: no disk
// I/O or re-tokenizing happens here, however large the workspace, since
// Index.SetFile already did that work when the file was last scanned (at
// startup, on open/change, or via the file watcher).
//
// A named port connection's name (".clk(" at an instantiation site) or a
// parameter override's name (".WIDTH(") is checked first, mirroring
// resolveWordAt/TextDocumentHover's identical precedence -- see
// sv.Index.ScopedOccurrencesForInstantiationConnection's doc comment for
// why ScopedOccurrences would never scope either one correctly on its
// own when the cursor sits AT the connection site (it has no scope-chain
// link to the instantiated module at all, so it either misses every other
// connection site or falls back to an unscoped, every-same-named-token-
// workspace-wide search). start is word's own start column (as WordAt
// returns, not the raw cursor position both callers already computed it
// from).
//
// The other direction -- cursor on the port/parameter's own declaration --
// falls through to plain sv.Index.ScopedOccurrences below, which itself
// unions in every matching connection site for that case (see its doc
// comment), so both directions resolve to the same result set.
//
// toks is the caller's already-lexed document (see sv.Tokens). Taking it as
// a parameter rather than re-deriving it here matters twice over: every
// caller already holds the text, so re-fetching it meant a second
// os.ReadFile of the same file per request for any document not open in the
// editor, and both probes below would otherwise lex the whole file
// separately.
func (s *Server) scopedOccurrences(toks sv.Tokens, uri string, line, character, start int, word, qualifier string, hasQualifier bool) []protocol.Location {
	if moduleName, ok := sv.InstantiationPortNameIn(toks, line, word, start); ok {
		return formatLocations(s.index.ScopedOccurrencesForInstantiationConnection(moduleName, word), word)
	}
	if moduleName, ok := sv.InstantiationParamNameIn(toks, line, word, start); ok {
		return formatLocations(s.index.ScopedOccurrencesForInstantiationConnection(moduleName, word), word)
	}
	return formatLocations(s.index.ScopedOccurrences(uri, line, character, word, qualifier, hasQualifier), word)
}
