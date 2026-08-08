package sv

import (
	"strings"

	svtoken "github.com/jfetkotto/svparse/token"
)

// WordAt returns the identifier-like word touching the given zero-based
// line/character position (an LSP Position, so character is a UTF-16
// column -- see utf16.go), and the UTF-16 column where it starts (needed
// by QualifierAt to check what precedes it). It operates on raw line text
// without comment/string awareness -- a position inside a comment or
// string literal that happens to spell a real declaration name will still
// resolve. That's an accepted simplification: this package does lexical
// scanning, not parsing, so it has no notion of "inside a comment" at an
// arbitrary cursor position without re-tokenizing the whole prefix.
func WordAt(text string, line, character int) (word string, start int, ok bool) {
	lineText, ok := lineAt(text, line)
	if !ok {
		return "", 0, false
	}
	if character < 0 {
		return "", 0, false
	}
	cursor := svtoken.RuneColumn(lineText, character)

	runeStart := cursor
	for runeStart > 0 && isIdentChar(lineText[runeStart-1]) {
		runeStart--
	}
	end := cursor
	for end < len(lineText) && isIdentChar(lineText[end]) {
		end++
	}
	if runeStart == end {
		return "", 0, false
	}
	return string(lineText[runeStart:end]), svtoken.UTF16Column(lineText, runeStart), true
}

// QualifierAt returns the identifier immediately before a "::" that
// directly precedes wordStart (a UTF-16 column, as WordAt returns) on the
// given line, if any -- e.g. for "pkg::foo" with wordStart at "foo", it
// returns ("pkg", true). SV's scope resolution operator allows no
// whitespace around "::", so this is a simple, reliable check without
// needing a full tokenizer pass.
func QualifierAt(text string, line, wordStart int) (string, bool) {
	lineText, ok := lineAt(text, line)
	if !ok {
		return "", false
	}
	ws := svtoken.RuneColumn(lineText, wordStart)
	if ws < 2 || ws > len(lineText) {
		return "", false
	}
	if lineText[ws-1] != ':' || lineText[ws-2] != ':' {
		return "", false
	}

	end := ws - 2
	start := end
	for start > 0 && isIdentChar(lineText[start-1]) {
		start--
	}
	if start == end {
		return "", false
	}
	return string(lineText[start:end]), true
}

// DotReceiverAt returns the identifier immediately before a "." that
// directly precedes wordStart (a UTF-16 column, as WordAt returns) on the
// given line, if any -- e.g. for "link.addr" with wordStart at "addr", it
// returns ("link", <link's start column>, true). Mirrors QualifierAt's
// shape but for "." instead of "::", used to detect struct/union
// field-access hover and hand the receiver off to the same resolution
// hover already does for any other identifier -- see structFieldHover.
func DotReceiverAt(text string, line, wordStart int) (receiver string, start int, ok bool) {
	lineText, ok := lineAt(text, line)
	if !ok || wordStart < 1 {
		return "", 0, false
	}
	cursor := svtoken.RuneColumn(lineText, wordStart)
	if cursor < 1 || lineText[cursor-1] != '.' {
		return "", 0, false
	}
	return WordAt(text, line, wordStart-1)
}

// CompletionEditRange computes what a completion at (line, character)
// should replace: the run of identifier characters immediately preceding
// the cursor (its start; zero-width, i.e. start == character, if there's
// no such run), and whether that run is itself immediately preceded by a
// "." -- the trigger character for named-port completion. character and
// the returned start are UTF-16 columns, like every position this package
// exchanges with the LSP layer -- see utf16.go.
//
// This exists because relying on a completion item's InsertText alone is
// ambiguous: a client that already inserted the "." which triggered
// completion, and doesn't treat "." as part of a "word", may not replace
// anything and just insert the item's text verbatim at the cursor --
// duplicating the "." if the item's text includes one. An explicit
// TextEdit with a precise range sidesteps that ambiguity entirely.
func CompletionEditRange(text string, line, character int) (start int, hasDot bool) {
	lineText, ok := lineAt(text, line)
	if !ok || character < 0 {
		return character, false
	}
	cursor := svtoken.RuneColumn(lineText, character)

	runeStart := cursor
	for runeStart > 0 && isIdentChar(lineText[runeStart-1]) {
		runeStart--
	}
	hasDot = runeStart > 0 && lineText[runeStart-1] == '.'
	return svtoken.UTF16Column(lineText, runeStart), hasDot
}

// LineSlice returns the substring of line's text between the start and
// end UTF-16 column offsets (clamped to the line's bounds), or "" if line
// is out of range or the range is empty/inverted. Used to pull out the
// already-typed prefix CompletionEditRange identified.
func LineSlice(text string, line, start, end int) string {
	lineText, ok := lineAt(text, line)
	if !ok {
		return ""
	}
	rs := svtoken.RuneColumn(lineText, start)
	re := svtoken.RuneColumn(lineText, end)
	if rs >= re {
		return ""
	}
	return string(lineText[rs:re])
}

// lineAt returns line's text (zero-based) as runes, with any trailing '\r'
// stripped, or ok=false if line is out of range.
//
// Deliberately not strings.Split: every exported function in this file
// calls this, and a single completion request makes about five such calls
// (CompletionEditRange twice, WordAt, QualifierAt, LineSlice). Splitting
// allocates a slice covering the whole document each time, so on a large
// file that was five full-document allocations per keystroke to read one
// line. Scanning to the line's own bounds and converting only that slice
// keeps the cost proportional to the line, not the file.
func lineAt(text string, line int) ([]rune, bool) {
	if line < 0 {
		return nil, false
	}
	start := 0
	for range line {
		nl := strings.IndexByte(text[start:], '\n')
		if nl < 0 {
			return nil, false // fewer than line+1 lines
		}
		start += nl + 1
	}
	end := len(text)
	if nl := strings.IndexByte(text[start:], '\n'); nl >= 0 {
		end = start + nl
	}
	return []rune(strings.TrimRight(text[start:end], "\r")), true
}
