package sv

import "slices"

// topK collects the limit smallest items under cmp, without ever holding
// more than limit of them.
//
// The symbol queries it backs (CompleteSymbols, WorkspaceSymbols) are
// answered from a truncated, sorted prefix: the client is shown the first
// 200 or 500 results and narrows its query to reach the rest. Producing
// that by materializing every match, sorting the lot, and slicing the front
// off costs time and -- far more importantly -- memory proportional to the
// whole workspace, on a request an editor fires per keystroke. A workspace
// with a million declarations answered an empty-query symbol-picker request
// by allocating and sorting a million-entry slice to return 500 of them.
//
// Keeping a bounded max-heap instead makes the work O(n log limit) with
// O(limit) live memory: each candidate is compared against the largest item
// kept so far and discarded immediately unless it beats it.
type topK[T any] struct {
	limit int
	cmp   func(a, b T) int // negative when a sorts before b, as for slices.SortFunc
	// heap is a max-heap under cmp (heap[0] is the largest kept item, the
	// first candidate for eviction). When limit <= 0 it's an unordered
	// bucket instead -- see push.
	heap    []T
	dropped bool
}

// newTopK returns a collector for the limit smallest items under cmp. A
// limit <= 0 means unlimited: every pushed item is kept and sorted, which
// is what a caller with no cap of its own (a test, chiefly) wants.
func newTopK[T any](limit int, cmp func(a, b T) int) *topK[T] {
	return &topK[T]{limit: limit, cmp: cmp}
}

func (t *topK[T]) push(v T) {
	if t.limit <= 0 {
		t.heap = append(t.heap, v)
		return
	}
	if len(t.heap) < t.limit {
		t.heap = append(t.heap, v)
		t.siftUp(len(t.heap) - 1)
		return
	}
	// At capacity: v earns a place only by beating the current largest,
	// which it then replaces. Either way something is being left out.
	t.dropped = true
	if t.cmp(v, t.heap[0]) < 0 {
		t.heap[0] = v
		t.siftDown(0)
	}
}

// sorted returns the collected items in ascending order under cmp, and
// whether any candidate was dropped for exceeding the limit (so a caller
// can mark its response incomplete rather than let a client mistake a
// capped result for the whole answer).
func (t *topK[T]) sorted() ([]T, bool) {
	slices.SortFunc(t.heap, t.cmp)
	return t.heap, t.dropped
}

func (t *topK[T]) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if t.cmp(t.heap[parent], t.heap[i]) >= 0 {
			return
		}
		t.heap[parent], t.heap[i] = t.heap[i], t.heap[parent]
		i = parent
	}
}

func (t *topK[T]) siftDown(i int) {
	for {
		largest := i
		for _, child := range [2]int{2*i + 1, 2*i + 2} {
			if child < len(t.heap) && t.cmp(t.heap[largest], t.heap[child]) < 0 {
				largest = child
			}
		}
		if largest == i {
			return
		}
		t.heap[i], t.heap[largest] = t.heap[largest], t.heap[i]
		i = largest
	}
}
