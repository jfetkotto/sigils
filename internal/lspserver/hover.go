package lspserver

import (
	"fmt"
	"strings"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/sv"
)

// TextDocumentHover resolves the identifier under the cursor the same way
// goto-definition does (see sv.Index.HoverInfo) and formats what the
// index knows about it: its kind, its port list if it's a module/
// interface/program, and its argument list (with real parsed types) if
// it's a function/task.
//
// A named port connection's port name (".clk(" at an instantiation site)
// or a parameter override's name (".WIDTH(") is checked first, mirroring
// resolveWordAt's identical precedence -- see
// sv.Index.InstantiationPortInfo's doc comment for why HoverInfo would
// never find either on its own.
func (s *Server) TextDocumentHover(context *glsp.Context, params *protocol.HoverParams) (*protocol.Hover, error) {
	text, ok := s.textForURI(params.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	line, character := int(params.Position.Line), int(params.Position.Character)

	word, start, ok := sv.WordAt(text, line, character)
	if !ok {
		return nil, nil
	}

	if moduleName, ok := sv.InstantiationPortNameAt(text, line, word, start); ok {
		if decl, ok := s.index.InstantiationPortInfo(moduleName, word); ok {
			return &protocol.Hover{
				Contents: protocol.MarkupContent{Kind: protocol.MarkupKindMarkdown, Value: hoverText(decl)},
			}, nil
		}
	}
	if moduleName, ok := sv.InstantiationParamNameAt(text, line, word, start); ok {
		if decl, ok := s.index.InstantiationPortInfo(moduleName, word); ok {
			return &protocol.Hover{
				Contents: protocol.MarkupContent{Kind: protocol.MarkupKindMarkdown, Value: hoverText(decl)},
			}, nil
		}
	}

	qualifier, hasQualifier := sv.QualifierAt(text, line, start)

	decl, ok := s.index.HoverInfo(params.TextDocument.URI, line, character, word, qualifier, hasQualifier)
	if !ok {
		return nil, nil
	}

	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.MarkupKindMarkdown, Value: hoverText(decl)},
	}, nil
}

func hoverText(d sv.Declaration) string {
	var b strings.Builder
	b.WriteString("```systemverilog\n")
	switch d.Kind {
	case sv.KindModule, sv.KindInterface, sv.KindProgram:
		fmt.Fprintf(&b, "%s %s%s", d.Kind, d.Name, portList(d.Ports))
	case sv.KindFunction, sv.KindTask:
		if d.Prototype {
			b.WriteString("extern ")
		}
		if d.ReturnType != "" {
			fmt.Fprintf(&b, "%s %s %s(%s)", d.Kind, d.ReturnType, d.Name, argSummary(d.Args))
		} else {
			fmt.Fprintf(&b, "%s %s(%s)", d.Kind, d.Name, argSummary(d.Args))
		}
	case sv.KindPort:
		b.WriteString(portEntry(sv.Port{Name: d.Name, Detail: d.Detail}))
	case sv.KindParameter:
		fmt.Fprintf(&b, "parameter %s", parameterText(d))
	case sv.KindTypedef:
		b.WriteString(typedefText(d))
	case sv.KindEnumMember:
		switch {
		case d.Value != "" && d.EnumTypedef != "":
			fmt.Fprintf(&b, "%s = %s  // %s", d.Name, d.Value, d.EnumTypedef)
		case d.Value != "":
			fmt.Fprintf(&b, "%s = %s", d.Name, d.Value)
		case d.EnumTypedef != "":
			fmt.Fprintf(&b, "enum member %s  // %s", d.Name, d.EnumTypedef)
		default:
			fmt.Fprintf(&b, "enum member %s", d.Name)
		}
	default:
		fmt.Fprintf(&b, "%s %s", d.Kind, d.Name)
	}
	b.WriteString("\n```")
	return b.String()
}

// portEntry renders one port/arg as "detail name" (e.g. "input logic
// clk"), falling back to a bare name when there's no direction/type
// detail at all.
func portEntry(p sv.Port) string {
	if p.Detail == "" {
		return p.Name
	}
	return p.Detail + " " + p.Name
}

// parameterText renders "[type] name [= default]" in real SV declaration
// order -- unlike portEntry's "detail name" prefix shape, since a
// parameter's default comes after its name, not before it like a port's
// direction/type.
func parameterText(d sv.Declaration) string {
	var b strings.Builder
	if d.Detail != "" {
		b.WriteString(d.Detail)
		b.WriteString(" ")
	}
	b.WriteString(d.Name)
	if d.Default != "" {
		b.WriteString(" = ")
		b.WriteString(d.Default)
	}
	return b.String()
}

const maxHoverPorts = 10

// commaList renders items as one per line between open/close brackets,
// comma-separated with no trailing comma on the last shown line (real SV
// list syntax -- a port list and an enum's members both work this way) --
// real modules/enums can have many entries, and a single joined line (as
// function/task args use, see argSummary) gets unreadable fast. Capped
// at maxHoverPorts with a trailing "..." line, since hover text has no
// scroll affordance in most clients.
func commaList(open, close string, items []string) string {
	if len(items) == 0 {
		return open + close
	}
	shown := items
	truncated := len(items) > maxHoverPorts
	if truncated {
		shown = items[:maxHoverPorts]
	}
	lines := make([]string, len(shown))
	for i, it := range shown {
		lines[i] = "  " + it
	}
	body := strings.Join(lines, ",\n")
	if truncated {
		body += ",\n  ..."
	}
	return open + "\n" + body + "\n" + close
}

// portList renders a module/interface/program's port list.
func portList(ports []sv.Port) string {
	items := make([]string, len(ports))
	for i, p := range ports {
		items[i] = portEntry(p)
	}
	return commaList("(", ")", items)
}

// enumBody renders an enum typedef's members (already rendered as "NAME"
// or "NAME = value" by sv.Declaration.EnumMembers).
func enumBody(members []string) string {
	return commaList("{", "}", members)
}

// structBody renders a struct/union typedef's fields as one per line,
// each terminated with ";" -- unlike commaList's comma-separated lists,
// every SV struct/union member ends with its own ";", including the
// last one, so this can't reuse commaList directly.
func structBody(fields []sv.Port) string {
	if len(fields) == 0 {
		return "{}"
	}
	shown := fields
	truncated := len(fields) > maxHoverPorts
	if truncated {
		shown = fields[:maxHoverPorts]
	}
	var b strings.Builder
	b.WriteString("{\n")
	for _, f := range shown {
		fmt.Fprintf(&b, "  %s;\n", portEntry(f))
	}
	if truncated {
		b.WriteString("  ...\n")
	}
	b.WriteString("}")
	return b.String()
}

// typedefText renders a typedef's underlying content, dispatched on
// TypedefKind (see sv.Declaration's doc comments) -- "" (a forward
// declaration, e.g. "typedef class Foo;") falls back to the bare name,
// same as any other Kind with nothing further to show.
func typedefText(d sv.Declaration) string {
	switch d.TypedefKind {
	case "alias":
		return fmt.Sprintf("typedef %s %s", d.AliasType, d.Name)
	case "enum":
		prefix := "enum"
		if d.BaseType != "" {
			prefix += " " + d.BaseType
		}
		return fmt.Sprintf("typedef %s %s %s", prefix, enumBody(d.EnumMembers), d.Name)
	case "struct", "union":
		prefix := d.TypedefKind
		if d.Packed {
			prefix += " packed"
		}
		return fmt.Sprintf("typedef %s %s %s", prefix, structBody(d.Fields), d.Name)
	default:
		return fmt.Sprintf("typedef %s", d.Name)
	}
}

// argSummary renders a function/task's argument list as "detail name,
// detail name, ..." (e.g. "int a, int b"), falling back to a bare name
// for an argument with no direction/type detail at all.
func argSummary(args []sv.Port) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = portEntry(a)
	}
	return strings.Join(parts, ", ")
}

// TextDocumentDocumentHighlight highlights every occurrence of the
// identifier under the cursor within the current file. It uses the same
// scope-aware resolution as references/rename (see scopedOccurrences) and
// then additionally filters to this file, since a highlight is a
// within-document visual aid even when the symbol itself is referenceable
// workspace-wide (e.g. a module name).
func (s *Server) TextDocumentDocumentHighlight(context *glsp.Context, params *protocol.DocumentHighlightParams) ([]protocol.DocumentHighlight, error) {
	text, ok := s.textForURI(params.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	line, character := int(params.Position.Line), int(params.Position.Character)

	word, start, ok := sv.WordAt(text, line, character)
	if !ok {
		return nil, nil
	}
	qualifier, hasQualifier := sv.QualifierAt(text, line, start)

	locs := s.scopedOccurrences(params.TextDocument.URI, line, character, start, word, qualifier, hasQualifier)
	out := make([]protocol.DocumentHighlight, 0, len(locs))
	for _, loc := range locs {
		if loc.URI != params.TextDocument.URI {
			continue
		}
		out = append(out, protocol.DocumentHighlight{Range: loc.Range})
	}
	return out, nil
}
