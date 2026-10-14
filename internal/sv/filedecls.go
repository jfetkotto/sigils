package sv

import (
	"slices"
	"strings"
	"sync"
)

// fileDecls is one file's declaration bucket together with two lookup
// structures derived from it: the bucket's indices ordered by name, and the
// indices of its container declarations.
//
// Resolution needs "this file's declarations named X" and "the innermost
// container around this position" several times per request (the
// declaration's own name, each scope-chain rung, each import's package,
// each qualifier), and answering either by scanning the whole bucket made
// one goto-definition a dozen passes over a wide module's thousands of
// declarations. With these, a by-name lookup is a binary search and the
// container search only visits containers.
//
// Both are built on first use rather than in SetFile, so a keystroke that
// rescans a file pays nothing for them until a query actually reaches that
// file. The bucket never changes after SetFile stores it (a rescan stores a
// new fileDecls), so building under the index's read lock is safe; once
// keeps concurrent readers from building it twice.
type fileDecls struct {
	decls []Declaration

	once       sync.Once
	byName     []int // indices into decls, ordered by Name and then index
	containers []int // indices into decls of container-kind declarations, in order
}

// emptyFileDecls stands in for a file the index holds nothing for.
var emptyFileDecls = &fileDecls{}

func newFileDecls(decls []Declaration) *fileDecls {
	return &fileDecls{decls: decls}
}

func (f *fileDecls) build() {
	f.once.Do(func() {
		f.byName = make([]int, len(f.decls))
		for i := range f.decls {
			f.byName[i] = i
			if isContainerKind(f.decls[i].Kind) {
				f.containers = append(f.containers, i)
			}
		}
		slices.SortFunc(f.byName, func(a, b int) int {
			if c := strings.Compare(f.decls[a].Name, f.decls[b].Name); c != 0 {
				return c
			}
			return a - b
		})
	})
}

// named returns the indices of every declaration called name, in bucket
// order. The result aliases f's own storage and must not be modified.
func (f *fileDecls) named(name string) []int {
	f.build()
	lo, found := slices.BinarySearchFunc(f.byName, name, func(i int, name string) int {
		return strings.Compare(f.decls[i].Name, name)
	})
	if !found {
		return nil
	}
	hi := lo + 1
	for hi < len(f.byName) && f.decls[f.byName[hi]].Name == name {
		hi++
	}
	return f.byName[lo:hi]
}

// innermostContaining returns the index of the smallest container
// declaration whose span contains (line, character), or -1 if none does.
func (f *fileDecls) innermostContaining(line, character int) int {
	f.build()
	best := -1
	for _, i := range f.containers {
		d := &f.decls[i]
		if !posWithin(d, line, character) {
			continue
		}
		if best == -1 || narrower(d, &f.decls[best]) {
			best = i
		}
	}
	return best
}
