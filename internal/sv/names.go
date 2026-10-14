package sv

import (
	"maps"
	"slices"
)

// maxIncrementalNameChanges bounds how many distinct names one SetFile or
// RemoveFile may add or remove while the sorted name list is kept up to
// date in place. Past it (a bulk indexing pass), the list is dropped and
// rebuilt by the next query instead: each in-place insert or delete moves
// the rest of the list, which is cheap for the few names one keystroke
// changes and quadratic for a whole workspace's worth.
const maxIncrementalNameChanges = 64

// sortedNamesLocked returns every name with at least one declaration, in
// ascending order, for prefix queries to binary-search (see
// CompleteSymbols). It is built on first use and then kept current by
// syncNamesLocked; the result must not be modified.
//
// Building here, under the read lock, is safe: byName can't change while
// any reader holds the lock, so concurrent builders produce the same list
// and whichever stores last wins. Writers only touch the list under the
// write lock, when no reader can be looking at it.
func (ix *Index) sortedNamesLocked() []string {
	if p := ix.names.Load(); p != nil {
		return *p
	}
	names := slices.Sorted(maps.Keys(ix.byName))
	ix.names.Store(&names)
	return names
}

// noteNameLocked records that name just gained (delta +1) or lost (delta
// -1) its last declaration, for syncNamesLocked to apply. A no-op while
// there is no list to keep current.
func (ix *Index) noteNameLocked(name string, delta int) {
	if ix.names.Load() == nil {
		return
	}
	if ix.nameChanges[name] += delta; ix.nameChanges[name] == 0 {
		delete(ix.nameChanges, name) // removed and re-added in the same update
	}
}

// syncNamesLocked applies the name changes noted since the last call to
// the sorted list, or drops the list when there are too many to apply in
// place. Called once at the end of every public mutation.
func (ix *Index) syncNamesLocked() {
	defer clear(ix.nameChanges)
	p := ix.names.Load()
	if p == nil || len(ix.nameChanges) == 0 {
		return
	}
	if len(ix.nameChanges) > maxIncrementalNameChanges {
		ix.names.Store(nil)
		return
	}
	names := *p
	for name, delta := range ix.nameChanges {
		i, found := slices.BinarySearch(names, name)
		switch {
		case delta > 0 && !found:
			names = slices.Insert(names, i, name)
		case delta < 0 && found:
			names = slices.Delete(names, i, i+1)
		}
	}
	ix.names.Store(&names)
}
