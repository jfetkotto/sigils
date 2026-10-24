package lspserver

import (
	"slices"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/sv"
)

// TextDocumentDocumentSymbol builds an outline tree for the file straight
// from the declaration tree sv.Index already maintains (Parent indices and
// all), with no extra scanning.
func (s *Server) TextDocumentDocumentSymbol(_ *glsp.Context, params *protocol.DocumentSymbolParams) (any, error) {
	decls := s.index.FileDeclarations(params.TextDocument.URI)
	var tree []protocol.DocumentSymbol
	if structure := s.index.FileStructure(params.TextDocument.URI); len(structure) > 0 {
		tree = outlineTree(decls, structure)
	} else {
		tree = documentSymbolTree(decls, childIndex(decls), -1)
	}
	if len(tree) == 0 {
		return nil, nil
	}
	return tree, nil
}

// childIndex buckets decls by Parent once, so documentSymbolTree can find a
// declaration's children by lookup instead of rescanning the whole slice.
// children[i] holds the indices parented to i; roots (Parent == -1) have
// no bucket, so documentSymbolTree finds those by scanning for them.
//
// Without this the tree build is quadratic: it rescanned every declaration
// in the file once per declaration, including the ports, variables and
// parameters that make up the bulk of a real module and never have children
// at all. Editors request documentSymbol on open and again on change, so a
// wide module (a few thousand signals) paid millions of iterations per
// keystroke to produce the same tree.
func childIndex(decls []sv.Declaration) (children [][]int) {
	children = make([][]int, len(decls))
	for i := range decls {
		// A Parent index out of range, or one that doesn't point strictly
		// backwards, would mean a malformed bucket; ignoring it here keeps a
		// bad index from panicking or building a cycle the recursion below
		// would never escape.
		if p := decls[i].Parent; p >= 0 && p < len(decls) && p != i {
			children[p] = append(children[p], i)
		}
	}
	return children
}

func documentSymbolTree(decls []sv.Declaration, children [][]int, parent int) []protocol.DocumentSymbol {
	var idxs []int
	if parent == -1 {
		for i := range decls {
			if decls[i].Parent == -1 {
				idxs = append(idxs, i)
			}
		}
	} else {
		idxs = children[parent]
	}

	out := make([]protocol.DocumentSymbol, 0, len(idxs))
	for _, i := range idxs {
		d := &decls[i]
		out = append(out, protocol.DocumentSymbol{
			Name:           d.Name,
			Kind:           symbolKindFor(d.Kind),
			Range:          protocol.Range{Start: position(d.Line, d.Character), End: position(d.EndLine, d.EndCharacter)},
			SelectionRange: nameRange(d.Line, d.Character, d.Name),
			Children:       documentSymbolTree(decls, children, i),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// TextDocumentFoldingRange offers one fold per container declaration
// (module/interface/program/class/package/function/task) in the file --
// their Start/End spans are exactly what sv.Index already tracks to
// support the scope chain, reused here as-is.
func (s *Server) TextDocumentFoldingRange(_ *glsp.Context, params *protocol.FoldingRangeParams) ([]protocol.FoldingRange, error) {
	decls := s.index.FileDeclarations(params.TextDocument.URI)
	var ranges []protocol.FoldingRange
	for i := range decls {
		d := &decls[i]
		if !sv.IsContainer(d.Kind) {
			continue
		}
		ranges = append(ranges, protocol.FoldingRange{
			StartLine: protocol.UInteger(d.Line),
			EndLine:   protocol.UInteger(d.EndLine),
		})
	}
	return ranges, nil
}

// maxWorkspaceSymbols bounds a workspace/symbol response the same way
// maxSymbolCompletions bounds completion: some clients send an empty
// query the moment the picker opens, which would otherwise return every
// declaration in a large workspace. Results are ordered by name before
// the cut, so what survives is deterministic and the user narrows the
// query to reach anything cut off. The cut happens inside the index (see
// sv.Index.WorkspaceSymbols) rather than here, so the discarded remainder
// is never built in the first place.
const maxWorkspaceSymbols = 500

// WorkspaceSymbol backs "go to symbol in workspace" -- a case-insensitive
// substring search (see sv.Index.WorkspaceSymbols) across every declared
// name in every indexed file, not just the open ones.
//
// A truncated result is returned as-is: unlike completion, LSP 3.16's
// workspace/symbol response has no "incomplete" flag to set (it's a bare
// SymbolInformation array), so narrowing the query is the only way to reach
// what was cut.
func (s *Server) WorkspaceSymbol(_ *glsp.Context, params *protocol.WorkspaceSymbolParams) ([]protocol.SymbolInformation, error) {
	results, _ := s.index.WorkspaceSymbols(params.Query, maxWorkspaceSymbols)
	out := make([]protocol.SymbolInformation, 0, len(results))
	for _, r := range results {
		out = append(out, protocol.SymbolInformation{
			Name:     r.Name,
			Kind:     symbolKindFor(r.Kind),
			Location: protocol.Location{URI: r.URI, Range: nameRange(r.Line, r.Character, r.Name)},
		})
	}
	return out, nil
}

func symbolKindFor(kind sv.Kind) protocol.SymbolKind {
	switch kind {
	case sv.KindModule, sv.KindProgram:
		return protocol.SymbolKindModule
	case sv.KindPackage:
		return protocol.SymbolKindPackage
	case sv.KindInterface:
		return protocol.SymbolKindInterface
	case sv.KindClass:
		return protocol.SymbolKindClass
	case sv.KindFunction, sv.KindTask:
		return protocol.SymbolKindFunction
	case sv.KindTypedef:
		return protocol.SymbolKindStruct
	case sv.KindEnumMember:
		return protocol.SymbolKindEnumMember
	case sv.KindPort:
		return protocol.SymbolKindProperty
	case sv.KindParameter:
		return protocol.SymbolKindConstant
	default:
		return protocol.SymbolKindVariable
	}
}

func position(line, character int) protocol.Position {
	return protocol.Position{Line: protocol.UInteger(line), Character: protocol.UInteger(character)}
}

func nameRange(line, character int, name string) protocol.Range {
	start := position(line, character)
	end := position(line, character+sv.UTF16Len(name))
	return protocol.Range{Start: start, End: end}
}

// outlineTree is documentSymbolTree for a file that also has processes and
// generate constructs (see sv.Structure). They are merged into the declaration
// tree by range: a structure sits under the smallest structure, or failing
// that the innermost container declaration, that contains it, and a
// declaration sits under the innermost structure that contains it, within its
// own container. Declaration.Parent is left alone, since scope lookups depend
// on it (a signal declared in a generate block is still the module's, as far
// as name resolution goes).
func outlineTree(decls []sv.Declaration, structure []sv.Structure) []protocol.DocumentSymbol {
	n := len(decls)
	total := n + len(structure)
	less := func(l1, c1, l2, c2 int) bool { return l1 < l2 || (l1 == l2 && c1 < c2) }
	within := func(s *sv.Structure, line, character int) bool {
		return !less(line, character, s.Line, s.Character) && less(line, character, s.EndLine, s.EndCharacter)
	}

	// structure is in source order and properly nested, so the stack finds each
	// one's enclosing structure.
	sParent := make([]int, len(structure))
	var stack []int
	for j := range structure {
		for len(stack) > 0 && !within(&structure[stack[len(stack)-1]], structure[j].Line, structure[j].Character) {
			stack = stack[:len(stack)-1]
		}
		sParent[j] = -1
		if len(stack) > 0 {
			sParent[j] = stack[len(stack)-1]
		}
		stack = append(stack, j)
	}
	innermost := func(line, character int) int {
		lo, hi := 0, len(structure) // the last structure starting at or before the position
		for lo < hi {
			mid := (lo + hi) / 2
			if less(line, character, structure[mid].Line, structure[mid].Character) {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
		for j := lo - 1; j >= 0; j = sParent[j] {
			if within(&structure[j], line, character) {
				return j
			}
		}
		return -1
	}

	var containers []int
	for i := range decls {
		if sv.IsContainer(decls[i].Kind) {
			containers = append(containers, i)
		}
	}
	containerOf := func(line, character int) int {
		best := -1
		for _, i := range containers {
			d := &decls[i]
			if !less(line, character, d.Line, d.Character) && less(line, character, d.EndLine, d.EndCharacter) {
				if best == -1 || less(decls[best].Line, decls[best].Character, d.Line, d.Character) {
					best = i
				}
			}
		}
		return best
	}

	children := make([][]int, total)
	var roots []int
	attach := func(item, parent int) {
		if parent == -1 {
			roots = append(roots, item)
		} else {
			children[parent] = append(children[parent], item)
		}
	}
	for i := range decls {
		parent := decls[i].Parent
		if parent < 0 || parent >= n || parent == i {
			parent = -1
		}
		if j := innermost(decls[i].Line, decls[i].Character); j != -1 && parent != -1 &&
			!less(structure[j].Line, structure[j].Character, decls[parent].Line, decls[parent].Character) {
			parent = n + j
		}
		attach(i, parent)
	}
	for j := range structure {
		parent := -1
		if sParent[j] != -1 {
			parent = n + sParent[j]
		} else if c := containerOf(structure[j].Line, structure[j].Character); c != -1 {
			parent = c
		}
		attach(n+j, parent)
	}

	pos := func(item int) (int, int) {
		if item < n {
			return decls[item].Line, decls[item].Character
		}
		return structure[item-n].Line, structure[item-n].Character
	}
	order := func(items []int) {
		slices.SortStableFunc(items, func(a, b int) int {
			la, ca := pos(a)
			lb, cb := pos(b)
			if la != lb {
				return la - lb
			}
			return ca - cb
		})
	}
	var build func(items []int) []protocol.DocumentSymbol
	build = func(items []int) []protocol.DocumentSymbol {
		order(items)
		out := make([]protocol.DocumentSymbol, 0, len(items))
		for _, item := range items {
			if item < n {
				d := &decls[item]
				out = append(out, protocol.DocumentSymbol{
					Name:           d.Name,
					Kind:           symbolKindFor(d.Kind),
					Range:          protocol.Range{Start: position(d.Line, d.Character), End: position(d.EndLine, d.EndCharacter)},
					SelectionRange: nameRange(d.Line, d.Character, d.Name),
					Children:       build(children[item]),
				})
				continue
			}
			s := &structure[item-n]
			kind := protocol.SymbolKindNamespace
			if s.Kind == sv.StructProcess {
				kind = protocol.SymbolKindEvent
			}
			var detail *string
			if s.Detail != "" {
				detail = &s.Detail
			}
			out = append(out, protocol.DocumentSymbol{
				Name:           s.Name,
				Detail:         detail,
				Kind:           kind,
				Range:          protocol.Range{Start: position(s.Line, s.Character), End: position(s.EndLine, s.EndCharacter)},
				SelectionRange: protocol.Range{Start: position(s.Line, s.Character), End: position(s.Line, s.Character+s.KeywordLen)},
				Children:       build(children[item]),
			})
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return build(roots)
}
