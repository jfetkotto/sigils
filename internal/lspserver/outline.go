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
	tree := documentSymbolTree(decls, -1)
	if len(tree) == 0 {
		return nil, nil
	}
	return tree, nil
}

func documentSymbolTree(decls []sv.Declaration, parent int) []protocol.DocumentSymbol {
	var out []protocol.DocumentSymbol
	for i, d := range decls {
		if d.Parent != parent {
			continue
		}
		out = append(out, protocol.DocumentSymbol{
			Name:           d.Name,
			Kind:           symbolKindFor(d.Kind),
			Range:          protocol.Range{Start: position(d.Line, d.Character), End: position(d.EndLine, d.EndCharacter)},
			SelectionRange: nameRange(d.Line, d.Character, d.Name),
			Children:       documentSymbolTree(decls, i),
		})
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
// declaration in a large workspace. Results are sorted by name before
// the cut, so what survives is deterministic and the user narrows the
// query to reach anything cut off.
const maxWorkspaceSymbols = 500

// WorkspaceSymbol backs "go to symbol in workspace" -- a case-insensitive
// substring search (see sv.Index.WorkspaceSymbols) across every declared
// name in every indexed file, not just the open ones.
func (s *Server) WorkspaceSymbol(context *glsp.Context, params *protocol.WorkspaceSymbolParams) ([]protocol.SymbolInformation, error) {
	results := s.index.WorkspaceSymbols(params.Query)
	if len(results) > maxWorkspaceSymbols {
		results = results[:maxWorkspaceSymbols]
	}
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
