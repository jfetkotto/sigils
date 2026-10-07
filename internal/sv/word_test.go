package sv

import (
	"strings"
	"testing"
)

func TestWordAtMiddleOfWord(t *testing.T) {
	word, start, ok := WordAt("  top u_top (a, b);", 0, 4)
	if !ok || word != "top" || start != 2 {
		t.Fatalf("WordAt = %q, %d, %v; want \"top\", 2, true", word, start, ok)
	}
}

func TestWordAtEndOfWord(t *testing.T) {
	// cursor sitting right after "top" (common when a client reports the
	// position at the end of a selection/click).
	word, start, ok := WordAt("  top u_top (a, b);", 0, 5)
	if !ok || word != "top" || start != 2 {
		t.Fatalf("WordAt = %q, %d, %v; want \"top\", 2, true", word, start, ok)
	}
}

func TestWordAtStartOfWord(t *testing.T) {
	word, start, ok := WordAt("  top u_top (a, b);", 0, 2)
	if !ok || word != "top" || start != 2 {
		t.Fatalf("WordAt = %q, %d, %v; want \"top\", 2, true", word, start, ok)
	}
}

func TestWordAtWhitespace(t *testing.T) {
	if _, _, ok := WordAt("top   u_top", 0, 4); ok {
		t.Fatalf("expected no word in whitespace gap")
	}
}

func TestWordAtOutOfRange(t *testing.T) {
	if _, _, ok := WordAt("top", 5, 0); ok {
		t.Fatalf("expected no word for an out-of-range line")
	}
	if _, _, ok := WordAt("top", 0, -1); ok {
		t.Fatalf("expected no word for a negative character")
	}
}

func TestWordAtClampsCharacterPastEndOfLine(t *testing.T) {
	word, _, ok := WordAt("top", 0, 100)
	if !ok || word != "top" {
		t.Fatalf("WordAt = %q, %v; want \"top\", true", word, ok)
	}
}

func TestWordAtMultilineSelectsCorrectLine(t *testing.T) {
	text := "module a;\n  wire foo;\nendmodule\n"
	word, _, ok := WordAt(text, 1, 8)
	if !ok || word != "foo" {
		t.Fatalf("WordAt = %q, %v; want \"foo\", true", word, ok)
	}
}

func TestQualifierAtFindsPackageQualifier(t *testing.T) {
	_, start, ok := WordAt("pkg::foo bar;", 0, 6)
	if !ok || start != 5 {
		t.Fatalf("WordAt setup failed: start=%d ok=%v", start, ok)
	}
	qualifier, ok := QualifierAt("pkg::foo bar;", 0, start)
	if !ok || qualifier != "pkg" {
		t.Fatalf("QualifierAt = %q, %v; want \"pkg\", true", qualifier, ok)
	}
}

func TestQualifierAtNoneForPlainIdentifier(t *testing.T) {
	_, start, _ := WordAt("foo bar;", 0, 1)
	if _, ok := QualifierAt("foo bar;", 0, start); ok {
		t.Fatalf("expected no qualifier for a plain identifier")
	}
}

func TestQualifierAtRequiresDoubleColon(t *testing.T) {
	// A single colon directly adjacent to the identifier (e.g. a case
	// label) must not be mistaken for the "::" scope resolution operator.
	_, start, _ := WordAt("default:foo();", 0, 9)
	if _, ok := QualifierAt("default:foo();", 0, start); ok {
		t.Fatalf("expected a single ':' to not be treated as a qualifier")
	}
}

func TestQualifierAtAtStartOfLine(t *testing.T) {
	if _, ok := QualifierAt("foo", 0, 0); ok {
		t.Fatalf("expected no qualifier when the word starts at column 0")
	}
}

func TestDotReceiverAtFindsReceiverBeforeDot(t *testing.T) {
	_, start, ok := WordAt("link.addr;", 0, 7)
	if !ok || start != 5 {
		t.Fatalf("WordAt setup failed: start=%d ok=%v", start, ok)
	}
	receiver, receiverStart, ok := DotReceiverAt("link.addr;", 0, start)
	if !ok || receiver != "link" || receiverStart != 0 {
		t.Fatalf("DotReceiverAt = %q, %d, %v; want \"link\", 0, true", receiver, receiverStart, ok)
	}
}

func TestDotReceiverAtNoneForPlainIdentifier(t *testing.T) {
	_, start, _ := WordAt("foo bar;", 0, 1)
	if _, _, ok := DotReceiverAt("foo bar;", 0, start); ok {
		t.Fatalf("expected no receiver for a plain identifier")
	}
}

func TestDotReceiverAtRequiresSingleDot(t *testing.T) {
	// "::" is QualifierAt's territory, not DotReceiverAt's -- a single "."
	// glued onto one of its colons must not be mistaken for the field
	// receiver dot.
	_, start, _ := WordAt("pkg::foo;", 0, 6)
	if _, _, ok := DotReceiverAt("pkg::foo;", 0, start); ok {
		t.Fatalf("expected no dot-receiver for a \"::\"-qualified identifier")
	}
}

func TestDotReceiverAtAtStartOfLine(t *testing.T) {
	if _, _, ok := DotReceiverAt("foo", 0, 0); ok {
		t.Fatalf("expected no dot-receiver when the word starts at column 0")
	}
}

func TestCompletionEditRangeRightAfterDot(t *testing.T) {
	start, hasDot := CompletionEditRange("    .", 0, 5)
	if start != 5 || !hasDot {
		t.Fatalf("CompletionEditRange = %d, %v; want 5, true", start, hasDot)
	}
}

func TestCompletionEditRangeNoDot(t *testing.T) {
	start, hasDot := CompletionEditRange("    ", 0, 4)
	if start != 4 || hasDot {
		t.Fatalf("CompletionEditRange = %d, %v; want 4, false", start, hasDot)
	}
}

func TestCompletionEditRangePartiallyTypedName(t *testing.T) {
	start, hasDot := CompletionEditRange("    .rs", 0, 7)
	if start != 5 || !hasDot {
		t.Fatalf("CompletionEditRange = %d, %v; want 5, true", start, hasDot)
	}
}

func TestCompletionEditRangeSingleColonIsNotADot(t *testing.T) {
	start, hasDot := CompletionEditRange("default:rs", 0, 10)
	if start != 8 || hasDot {
		t.Fatalf("CompletionEditRange = %d, %v; want 8, false", start, hasDot)
	}
}

func TestLineSlice(t *testing.T) {
	if got := LineSlice("  leaf u_leaf", 0, 2, 6); got != "leaf" {
		t.Fatalf("LineSlice = %q, want \"leaf\"", got)
	}
}

func TestLineSliceEmptyRange(t *testing.T) {
	if got := LineSlice("leaf", 0, 3, 3); got != "" {
		t.Fatalf("LineSlice = %q, want \"\"", got)
	}
}

func TestLineSliceOutOfRangeLine(t *testing.T) {
	if got := LineSlice("leaf", 5, 0, 2); got != "" {
		t.Fatalf("LineSlice = %q, want \"\"", got)
	}
}

func TestLineSliceClampsEnd(t *testing.T) {
	if got := LineSlice("leaf", 0, 0, 100); got != "leaf" {
		t.Fatalf("LineSlice = %q, want \"leaf\"", got)
	}
}

// lineAt is reached only through the exported helpers, so its boundary
// behaviour is pinned through WordAt: which line indices exist for a given
// text is exactly what strings.Split used to decide, and the hand-rolled
// scan that replaced it must agree -- notably that a trailing newline
// yields one more (empty) line, and that an out-of-range line is not found.
func TestWordAtLineBoundaries(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		line     int
		wantWord string
		wantOK   bool
	}{
		{"only line, no trailing newline", "alpha", 0, "alpha", true},
		{"past the only line", "alpha", 1, "", false},
		{"empty line after trailing newline", "alpha\n", 1, "", false},
		{"past the empty trailing line", "alpha\n", 2, "", false},
		{"empty text", "", 0, "", false},
		{"second of two", "alpha\nbeta", 1, "beta", true},
		{"blank line between", "alpha\n\nbeta", 2, "beta", true},
		{"crlf line endings", "alpha\r\nbeta\r\n", 1, "beta", true},
		{"negative line", "alpha", -1, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			word, _, ok := WordAt(tc.text, tc.line, 0)
			if ok != tc.wantOK || word != tc.wantWord {
				t.Errorf("WordAt(%q, %d, 0) = (%q, %v), want (%q, %v)", tc.text, tc.line, word, ok, tc.wantWord, tc.wantOK)
			}
		})
	}
}

func BenchmarkWordAtLateLine(b *testing.B) {
	var sb strings.Builder
	for range 5000 {
		sb.WriteString("  logic [31:0] some_signal_name;\n")
	}
	text := sb.String()
	b.ResetTimer()
	for range b.N {
		WordAt(text, 4999, 17)
	}
}

// lineStart skips whole blocks with strings.Count; it must land on the
// same offset as walking one newline at a time, including when the target
// line starts exactly at, or just either side of, a block boundary.
func TestLineStartMatchesANewlineByNewlineWalk(t *testing.T) {
	walk := func(text string, line int) (int, bool) {
		start := 0
		for range line {
			nl := strings.IndexByte(text[start:], '\n')
			if nl < 0 {
				return 0, false
			}
			start += nl + 1
		}
		return start, true
	}
	var texts []string
	for _, width := range []int{1, 7, lineStartBlock - 1, lineStartBlock, lineStartBlock + 1} {
		texts = append(texts, strings.Repeat(strings.Repeat("x", width-1)+"\n", 3*lineStartBlock/width+2))
	}
	texts = append(texts, strings.Repeat("\n", 3*lineStartBlock), "no newline at all", "")
	for _, text := range texts {
		lines := strings.Count(text, "\n")
		for _, line := range []int{-1, 0, 1, lines / 2, lines - 1, lines, lines + 1, lines + 50} {
			wantStart, wantOK := walk(text, line)
			if line < 0 {
				wantStart, wantOK = 0, false
			}
			if gotStart, gotOK := lineStart(text, line); gotStart != wantStart || gotOK != wantOK {
				t.Fatalf("lineStart(len %d, %d) = (%d, %v), want (%d, %v)", len(text), line, gotStart, gotOK, wantStart, wantOK)
			}
		}
	}
}
