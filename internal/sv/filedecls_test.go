package sv

import (
	"slices"
	"testing"
)

func TestFileDeclsNamedReturnsEveryMatchInBucketOrder(t *testing.T) {
	f := newFileDecls([]Declaration{
		{Name: "b"}, {Name: "a"}, {Name: "c"}, {Name: "a"}, {Name: "b"}, {Name: "a"},
	})
	for name, want := range map[string][]int{
		"a": {1, 3, 5},
		"b": {0, 4},
		"c": {2},
		"z": nil,
		"":  nil,
	} {
		if got := f.named(name); !slices.Equal(got, want) {
			t.Errorf("named(%q) = %v, want %v", name, got, want)
		}
	}
	if got := emptyFileDecls.named("a"); got != nil {
		t.Errorf("empty file returned %v", got)
	}
}

func TestFileDeclsInnermostContainingPicksTheNarrowestContainer(t *testing.T) {
	f := newFileDecls([]Declaration{
		{Kind: KindModule, Name: "m", Line: 0, EndLine: 20},
		{Kind: KindVariable, Name: "v", Line: 2, EndLine: 2, EndCharacter: 5}, // not a container
		{Kind: KindFunction, Name: "fn", Line: 5, EndLine: 10, Parent: 0},
		{Kind: KindModule, Name: "other", Line: 30, EndLine: 40},
	})
	for _, tc := range []struct{ line, want int }{
		{2, 0}, {7, 2}, {35, 3}, {25, -1},
	} {
		if got := f.innermostContaining(tc.line, 1); got != tc.want {
			t.Errorf("innermostContaining(%d) = %d, want %d", tc.line, got, tc.want)
		}
	}
}
