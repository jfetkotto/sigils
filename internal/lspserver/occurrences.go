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
// A struct/union field access ("st_bundle.ckSideband") is checked next, via
// sv.Index.ScopedOccurrencesForStructField -- the receiver-type resolution
// hover (structFieldHover) and completion (structMemberCompletionItems)
// already do, now on the references/rename side too. Without it a field name
// resolves to no declaration at all, and sv.Index.ScopedOccurrences falls
// back to its unscoped, name-wide list: every identically-spelled identifier
// in the workspace, including unrelated modules' ports and the named-port
// connections to them. That fallback stays for names that genuinely can't be
// resolved; a field access only looked unresolvable.
//
// toks is the caller's already-lexed document (see sv.Tokens). Taking it as
// a parameter rather than re-deriving it here matters twice over: every
// caller already holds the text, so re-fetching it meant a second
// os.ReadFile of the same file per request for any document not open in the
// editor, and both probes below would otherwise lex the whole file
// separately.
func (s *Server) scopedOccurrences(toks sv.Tokens, text, uri string, line, character, start int, word, qualifier string, hasQualifier bool) []protocol.Location {
	if locs, ok := s.probeOccurrences(toks, text, uri, line, character, start, word); ok {
		return formatLocations(locs, word)
	}
	return formatLocations(s.index.ScopedOccurrences(uri, line, character, word, qualifier, hasQualifier), word)
}

// fileOccurrences is scopedOccurrences for document highlight: the same
// probes, but the general path is restricted to uri inside the index
// rather than by the caller filtering a workspace-wide result afterwards.
// See sv.Index.OccurrencesInFile for why that matters on a request that
// runs for every cursor move.
//
// The probe results still need filtering here: an instantiation-connection
// or struct-field query is answered workspace-wide by design, and only
// this caller wants one file's worth.
func (s *Server) fileOccurrences(toks sv.Tokens, text, uri string, line, character, start int, word, qualifier string, hasQualifier bool) []protocol.Location {
	if locs, ok := s.probeOccurrences(toks, text, uri, line, character, start, word); ok {
		return inFile(uri, formatLocations(locs, word))
	}
	return formatLocations(s.index.OccurrencesInFile(uri, line, character, word, qualifier, hasQualifier), word)
}

// probeOccurrences runs the three special-case queries -- named port
// connection, parameter override, struct/union field access -- and reports
// whether one of them applied. ok=false means the caller should fall back
// to its own general path.
func (s *Server) probeOccurrences(toks sv.Tokens, text, uri string, line, character, start int, word string) ([]sv.Location, bool) {
	if moduleName, ok := sv.InstantiationPortNameIn(toks, line, word, start); ok {
		return s.index.ScopedOccurrencesForInstantiationConnection(moduleName, word), true
	}
	if moduleName, ok := sv.InstantiationParamNameIn(toks, line, word, start); ok {
		return s.index.ScopedOccurrencesForInstantiationConnection(moduleName, word), true
	}
	if receiver, receiverStart, ok := sv.DotReceiverAt(text, line, start); ok {
		recvQualifier, recvHasQualifier := sv.QualifierAt(text, line, receiverStart)
		if locs, ok := s.index.ScopedOccurrencesForStructField(uri, line, character, receiver, recvQualifier, recvHasQualifier, word); ok {
			return locs, true
		}
	}
	return nil, false
}

func inFile(uri string, locs []protocol.Location) []protocol.Location {
	out := locs[:0]
	for _, l := range locs {
		if string(l.URI) == uri {
			out = append(out, l)
		}
	}
	return out
}
