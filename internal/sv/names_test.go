package sv

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// The sorted name list is updated in place by every SetFile and RemoveFile
// once a query has built it, so after any sequence of edits it must still
// equal a fresh sort of byName's keys.
func TestSortedNamesStayInStepWithByName(t *testing.T) {
	ix := NewIndex()
	ix.SetIncludeResolverFactory(func() IncludeResolver {
		return &stubResolver{files: map[string]string{"hdr.svh": "typedef logic [7:0] hdr_t;\n"}}
	})
	rng := rand.New(rand.NewPCG(1, 2))
	module := func(file, n int) string {
		var b strings.Builder
		if rng.IntN(3) == 0 {
			b.WriteString("`include \"hdr.svh\"\n")
		}
		fmt.Fprintf(&b, "module m%d;\n", file)
		for range n {
			fmt.Fprintf(&b, "  logic sig%d;\n", rng.IntN(200)) // shared across files
		}
		b.WriteString("endmodule\n")
		return b.String()
	}
	check := func(step string) {
		t.Helper()
		syms, _ := ix.CompleteSymbols("", 0)
		got := make([]string, len(syms))
		for i, s := range syms {
			got[i] = s.Name
		}
		if want := slices.Sorted(maps.Keys(ix.byName)); !slices.Equal(got, want) {
			t.Fatalf("%s: completion names %v\nwant %v", step, got, want)
		}
	}

	for f := range 10 {
		ix.SetFile(fmt.Sprintf("file:///f%d.sv", f), module(f, 5))
	}
	check("initial build")
	for step := range 500 {
		uri := fmt.Sprintf("file:///f%d.sv", rng.IntN(12))
		switch rng.IntN(10) {
		case 0:
			ix.RemoveFile(uri)
		case 1:
			// More new names than maxIncrementalNameChanges: the list is
			// dropped and rebuilt rather than updated in place.
			var b strings.Builder
			fmt.Fprintf(&b, "module big%d;\n", step)
			for k := range 2 * maxIncrementalNameChanges {
				fmt.Fprintf(&b, "  logic big%d_%d;\n", step, k)
			}
			b.WriteString("endmodule\n")
			ix.SetFile(uri, b.String())
		default:
			ix.SetFile(uri, module(step, 1+rng.IntN(4)))
		}
		check(fmt.Sprintf("step %d", step))
	}
}
