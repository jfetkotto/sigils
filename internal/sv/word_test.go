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
