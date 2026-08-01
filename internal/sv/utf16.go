package sv

import svtoken "github.com/jfetkotto/svparse/token"

// LSP positions are UTF-16 code units (the protocol's default encoding,
// and the only one protocol 3.16 offers -- positionEncoding negotiation
// doesn't exist before 3.17). This package therefore speaks UTF-16
// columns at every public boundary -- token/Declaration/Occurrence
// Character fields, WordAt/CompletionEditRange arguments and results --
// keeping rune-based indexing purely internal. ASCII and all other BMP
// text agree between the two encodings; supplementary-plane characters
// (an emoji in a comment, say) are where they diverge, and where a
// rune-counted column would mis-place every position after it on the
// line -- including rename edits, which corrupt the file when applied at
// a shifted range.
//
// The actual rune<->UTF-16 conversions live in svparse/token (aliased
// svtoken here to read distinctly from this package's own Token-adjacent
// types). UTF16Len stays exported from sv as a thin wrapper so this
// package's public API doesn't change for callers like lspserver's
// nameRange.

// UTF16Len returns s's length in UTF-16 code units.
func UTF16Len(s string) int {
	return svtoken.UTF16Len(s)
}
