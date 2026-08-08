package lspserver

import (
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/sv"
)

// TextDocumentDocumentSymbol builds an outline tree for the file straight
// from the declaration tree sv.Index already maintains (Parent indices and
// all), with no extra scanning.
func (s *Server) TextDocumentDocumentSymbol(context *glsp.Context, params *protocol.DocumentSymbolParams) (any, error) {
	decls := s.index.FileDeclarations(params.TextDocument.URI)
	tree := documentSymbolTree(decls, childIndex(decls), -1)
	if len(tree) == 0 {
		return nil, nil
	}
	return tree, nil
}

// childIndex buckets decls by Parent once, so documentSymbolTree can find a
// declaration's children by lookup instead of rescanning the whole slice.
// children[i] holds the indices parented to i; roots (Parent == -1) come
// back separately, since there's no bucket to hold them.
//
// Without this the tree build is quadratic: it rescanned every declaration
// in the file once per declaration, including the ports, variables and
// parameters that make up the bulk of a real module and never have children
// at all. Editors request documentSymbol on open and again on change, so a
// wide module (a few thousand signals) paid millions of iterations per
// keystroke to produce the same tree.
func childIndex(decls []sv.Declaration) (children [][]int) {
	children = make([][]int, len(decls))
	for i, d := range decls {
		// A Parent index out of range, or one that doesn't point strictly
		// backwards, would mean a malformed bucket; ignoring it here keeps a
		// bad index from panicking or building a cycle the recursion below
		// would never escape.
		if d.Parent >= 0 && d.Parent < len(decls) && d.Parent != i {
			children[d.Parent] = append(children[d.Parent], i)
		}
	}
	return children
}

func documentSymbolTree(decls []sv.Declaration, children [][]int, parent int) []protocol.DocumentSymbol {
	var idxs []int
	if parent == -1 {
		for i, d := range decls {
			if d.Parent == -1 {
				idxs = append(idxs, i)
			}
		}
	} else {
		idxs = children[parent]
	}

	out := make([]protocol.DocumentSymbol, 0, len(idxs))
	for _, i := range idxs {
		d := decls[i]
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
func (s *Server) TextDocumentFoldingRange(context *glsp.Context, params *protocol.FoldingRangeParams) ([]protocol.FoldingRange, error) {
	decls := s.index.FileDeclarations(params.TextDocument.URI)
	var ranges []protocol.FoldingRange
	for _, d := range decls {
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
func (s *Server) WorkspaceSymbol(context *glsp.Context, params *protocol.WorkspaceSymbolParams) ([]protocol.SymbolInformation, error) {
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
