package sv

import "testing"

// The 😀 below is U+1F600, a supplementary-plane character: 1 rune but 2
// UTF-16 code units. Everything after it on the same line sits one column
// further in UTF-16 than a rune count would say -- which is exactly what
// these tests pin down, since LSP positions are UTF-16 code units.

func TestUTF16Len(t *testing.T) {
	if got := UTF16Len("a😀b"); got != 4 {
		t.Fatalf("UTF16Len(a😀b) = %d, want 4", got)
	}
	if got := UTF16Len("wide_top"); got != 8 {
		t.Fatalf("UTF16Len(wide_top) = %d, want 8", got)
	}
}

func TestScanDeclarationsPositionsAreUTF16AfterNonBMPCharacter(t *testing.T) {
	// Columns: "/* 😀 */ " occupies UTF-16 columns 0-8 (the emoji is 2),
	// so "module" starts at 9 and "wide_top" at 16.
	src := "/* 😀 */ module wide_top;\nendmodule\n"
	decls := ScanDeclarations("test.sv", src)
	top := findDecl(t, decls, "wide_top")
	if top.Character != 16 {
		t.Fatalf("expected wide_top's name at UTF-16 column 16, got %d", top.Character)
	}
}

func TestScanDeclarationsEndCharacterIsUTF16AfterNonBMPCharacter(t *testing.T) {
	// "typedef" at column 9, "int" at 17, "my_t" at 21 -- EndCharacter is
	// the name's end, 21 + len("my_t") = 25.
	src := "/* 😀 */ typedef int my_t;\n"
	decls := ScanDeclarations("test.sv", src)
	td := findDecl(t, decls, "my_t")
	if td.Character != 21 || td.EndCharacter != 25 {
		t.Fatalf("expected my_t at UTF-16 columns [21, 25), got [%d, %d)", td.Character, td.EndCharacter)
	}
}

func TestWordAtAcceptsAndReturnsUTF16Columns(t *testing.T) {
	src := "/* 😀 */ module wide_top;\nendmodule\n"
	word, start, ok := WordAt(src, 0, 18) // a click inside "wide_top"
	if !ok || word != "wide_top" {
		t.Fatalf("WordAt = (%q, %d, %v), want wide_top", word, start, ok)
	}
	if start != 16 {
		t.Fatalf("expected wide_top's start at UTF-16 column 16, got %d", start)
	}
}
