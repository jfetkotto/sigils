package sv

import "sort"

// topK collects the limit smallest items under less, without ever holding
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
	less  func(a, b T) bool
	// heap is a max-heap under less (heap[0] is the largest kept item, the
	// first candidate for eviction). When limit <= 0 it's an unordered
	// bucket instead -- see push.
	heap    []T
	dropped bool
}

// newTopK returns a collector for the limit smallest items under less. A
// limit <= 0 means unlimited: every pushed item is kept and sorted, which
// is what a caller with no cap of its own (a test, chiefly) wants.
func newTopK[T any](limit int, less func(a, b T) bool) *topK[T] {
	return &topK[T]{limit: limit, less: less}
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
	if t.less(v, t.heap[0]) {
		t.heap[0] = v
		t.siftDown(0)
	}
}

// sorted returns the collected items in ascending order under less, and
// whether any candidate was dropped for exceeding the limit (so a caller
// can mark its response incomplete rather than let a client mistake a
// capped result for the whole answer).
func (t *topK[T]) sorted() ([]T, bool) {
	sort.Slice(t.heap, func(i, j int) bool { return t.less(t.heap[i], t.heap[j]) })
	return t.heap, t.dropped
}

func (t *topK[T]) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if !t.less(t.heap[parent], t.heap[i]) {
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
			if child < len(t.heap) && t.less(t.heap[largest], t.heap[child]) {
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
