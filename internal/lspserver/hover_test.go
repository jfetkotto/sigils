package lspserver

import (
	"fmt"
	"strings"
	"testing"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

func TestTextDocumentHoverModule(t *testing.T) {
	s := newTestServer()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///leaf.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module leaf(input logic clk, input logic rst_n);\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	topText := "module top;\n  leaf u_leaf();\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///top.sv", LanguageID: "systemverilog", Version: 1, Text: topText},
	}); err != nil {
		t.Fatal(err)
	}

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 1, Character: 3},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	if hover == nil {
		t.Fatalf("expected a hover result")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok || content.Kind != protocol.MarkupKindMarkdown {
		t.Fatalf("expected MarkupContent (markdown), got %#v", hover.Contents)
	}
	want := "module leaf(\n  input logic clk,\n  input logic rst_n\n)"
	if !strings.Contains(content.Value, want) {
		t.Fatalf("expected hover text to describe leaf's ports, got %q", content.Value)
	}
}

func TestTextDocumentHoverModuleTruncatesPortListBeyondTen(t *testing.T) {
	s := newTestServer()
	var ports strings.Builder
	for i := range 12 {
		if i > 0 {
			ports.WriteString(", ")
		}
		fmt.Fprintf(&ports, "input logic p%d", i)
	}
	src := fmt.Sprintf("module leaf(%s);\nendmodule\n", ports.String())
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///leaf.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}
	topText := "module top;\n  leaf u_leaf();\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///top.sv", LanguageID: "systemverilog", Version: 1, Text: topText},
	}); err != nil {
		t.Fatal(err)
	}

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 1, Character: 3},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	content, _ := hover.Contents.(protocol.MarkupContent)

	for i := range 10 {
		name := fmt.Sprintf("p%d", i)
		if !strings.Contains(content.Value, name) {
			t.Fatalf("expected hover text to include port %q (within the first 10), got %q", name, content.Value)
		}
	}
	if !strings.Contains(content.Value, "...") {
		t.Fatalf("expected hover text to indicate truncation with \"...\", got %q", content.Value)
	}
	for _, name := range []string{"p10", "p11"} {
		if strings.Contains(content.Value, name) {
			t.Fatalf("expected hover text NOT to include port %q (beyond the 10-port cap), got %q", name, content.Value)
		}
	}
}

func TestTextDocumentHoverModuleNoPorts(t *testing.T) {
	s := newTestServer()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///top.sv", LanguageID: "systemverilog", Version: 1, Text: "module top;\nendmodule\n"},
	}); err != nil {
		t.Fatal(err)
	}

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 0, Character: 8},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "module top()") {
		t.Fatalf("expected hover text to show an empty port list, got %q", content.Value)
	}
}

func TestTextDocumentHoverPrototype(t *testing.T) {
	s := newTestServer()
	src := `class foo;
  extern function void bar();
  function void caller();
    bar();
  endfunction
endclass
`
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 3, Character: 5},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "extern function void bar") {
		t.Fatalf("expected hover text to mention \"extern function void bar\", got %q", content.Value)
	}
}

func TestTextDocumentHoverFunctionShowsArgs(t *testing.T) {
	s := newTestServer()
	src := "module top;\n  function automatic int add(int a, int b);\n    return a + b;\n  endfunction\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 1, Character: 25},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	if hover == nil {
		t.Fatalf("expected a hover result")
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "function int add(int a, int b)") {
		t.Fatalf("expected hover text to show add's return type and argument types, got %q", content.Value)
	}
}

func TestTextDocumentHoverFunctionImplicitReturnType(t *testing.T) {
	s := newTestServer()
	src := "module top;\n  function foo(int a);\n  endfunction\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 1, Character: 13},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "function foo(int a)") {
		t.Fatalf("expected hover text to omit a return type for an implicit-return function, got %q", content.Value)
	}
}

func TestTextDocumentHoverTaskNoReturnType(t *testing.T) {
	s := newTestServer()
	src := "module top;\n  task foo(int a);\n  endtask\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 1, Character: 8},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "task foo(int a)") {
		t.Fatalf("expected hover text to show the task with no return type, got %q", content.Value)
	}
}

func TestTextDocumentHoverPortShowsDirectionAndType(t *testing.T) {
	s := newTestServer()
	src := "module leaf(input logic clk, output logic [7:0] data);\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///leaf.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///leaf.sv"},
			Position:     protocol.Position{Line: 0, Character: protocol.UInteger(strings.Index(src, "data"))},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "output logic [7:0] data") {
		t.Fatalf("expected hover text to show data's direction and type, got %q", content.Value)
	}
}

func TestTextDocumentHoverPortReferenceInsideModuleBody(t *testing.T) {
	s := newTestServer()
	src := "module leaf(input logic clk, output logic q);\n  assign q = clk;\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///leaf.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}
	bodyLine := "  assign q = clk;"

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///leaf.sv"},
			Position:     protocol.Position{Line: 1, Character: protocol.UInteger(strings.Index(bodyLine, "clk"))},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "input logic clk") {
		t.Fatalf("expected hover text (on a body reference to clk, not its port-list declaration) to show its direction and type, got %q", content.Value)
	}
}

func TestTextDocumentHoverVariableShowsTypeAndExpandsStruct(t *testing.T) {
	s := newTestServer()
	src := "typedef struct packed {\n  logic [3:0] addr;\n  logic [7:0] data;\n} bus_t;\n" +
		"module top;\n  bus_t link;\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}
	line := "  bus_t link;"

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 5, Character: protocol.UInteger(strings.Index(line, "link"))},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	if hover == nil {
		t.Fatalf("expected a hover result")
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "bus_t link") {
		t.Fatalf("expected hover text to show link's own declared type, got %q", content.Value)
	}
	want := "typedef struct packed {\n  logic [3:0] addr;\n  logic [7:0] data;\n} bus_t"
	if !strings.Contains(content.Value, want) {
		t.Fatalf("expected hover text to also expand what bus_t contains, got %q", content.Value)
	}
}

func TestTextDocumentHoverPlainVariableShowsTypeNoExpansion(t *testing.T) {
	s := newTestServer()
	src := "module top;\n  logic [7:0] data;\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}
	line := "  logic [7:0] data;"

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 1, Character: protocol.UInteger(strings.Index(line, "data"))},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if content.Value != "```systemverilog\nlogic [7:0] data\n```" {
		t.Fatalf("expected a plain (non-struct) variable's hover to show only its own type with no expansion, got %q", content.Value)
	}
}

func TestTextDocumentHoverStructFieldResolvesThroughReceiverType(t *testing.T) {
	// The shadowing local "addr" deliberately has a DIFFERENT type
	// (int) than the struct's own "addr" field (logic [3:0]) --
	// distinguishing genuine struct-field resolution from a hover that
	// merely fell through to a same-named declaration elsewhere in scope
	// by coincidence, the exact gap this test guards against.
	s := newTestServer()
	src := "typedef struct packed {\n  logic [3:0] addr;\n  logic [7:0] data;\n} bus_t;\n" +
		"module top;\n  bus_t link;\n  int addr;\n  assign addr = link.addr;\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}
	line := "  assign addr = link.addr;"
	fieldChar := strings.LastIndex(line, "addr") // the "addr" after "link."

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 7, Character: protocol.UInteger(fieldChar)},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	if hover == nil {
		t.Fatalf("expected a hover result")
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if content.Value != "```systemverilog\nlogic [3:0] addr\n```" {
		t.Fatalf("expected hover on \"link.addr\" to resolve to bus_t's own addr field, got %q", content.Value)
	}
}

func TestTextDocumentHoverPlainShadowedNameStillResolvesToLocalDeclaration(t *testing.T) {
	// The other half of the coincidence check: the LHS "addr" (not
	// preceded by a dot) must still resolve to the local variable, not be
	// accidentally swallowed by the new struct-field path.
	s := newTestServer()
	src := "typedef struct packed {\n  logic [3:0] addr;\n} bus_t;\n" +
		"module top;\n  bus_t link;\n  int addr;\n  assign addr = link.addr;\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}
	line := "  assign addr = link.addr;"
	lhsChar := strings.Index(line, "addr")

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 6, Character: protocol.UInteger(lhsChar)},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if content.Value != "```systemverilog\nint addr\n```" {
		t.Fatalf("expected the plain (non-dotted) \"addr\" to still resolve to the local int variable, got %q", content.Value)
	}
}

func TestTextDocumentHoverStructFieldFallsBackWhenReceiverIsNotAStruct(t *testing.T) {
	s := newTestServer()
	// "sig.other" isn't meaningful SV (sig is a plain logic, not a
	// struct/union) -- WordAt/DotReceiverAt work on raw text, so this
	// still reaches structFieldHover, which must recognize sig's type
	// ("logic") doesn't resolve via StructFields and fall through to
	// ordinary word resolution for "other" (a real declaration elsewhere
	// in the same module) rather than returning no hover at all.
	src := "module top;\n  logic sig;\n  logic other;\n  assign sig = sig.other;\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}
	line := "  assign sig = sig.other;"
	fieldChar := strings.LastIndex(line, "other")

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 3, Character: protocol.UInteger(fieldChar)},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	if hover == nil {
		t.Fatalf("expected a hover result")
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "logic other") {
		t.Fatalf("expected \"sig.other\" to fall through to ordinary hover on \"other\", got %q", content.Value)
	}
}

func TestTextDocumentHoverParameterOverrideShowsTypeAndDefault(t *testing.T) {
	s := newTestServer()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///leaf.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module leaf #(parameter int WIDTH = 8) ();\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	topText := "module top;\n  leaf #(\n    .WIDTH(16)\n  ) u_leaf (\n  );\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///top.sv", LanguageID: "systemverilog", Version: 1, Text: topText},
	}); err != nil {
		t.Fatal(err)
	}
	overrideLine := "    .WIDTH(16)"

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 2, Character: protocol.UInteger(strings.Index(overrideLine, "WIDTH"))},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	if hover == nil {
		t.Fatalf("expected a hover result")
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "parameter int WIDTH = 8") {
		t.Fatalf("expected hover on the .WIDTH( override to show leaf's own WIDTH default, got %q", content.Value)
	}
}

func TestTextDocumentHoverBodyLevelParameterShowsTypeAndDefault(t *testing.T) {
	s := newTestServer()
	src := "module top;\n  parameter int WIDTH = 8;\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}
	line := "  parameter int WIDTH = 8;"

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 1, Character: protocol.UInteger(strings.Index(line, "WIDTH"))},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "parameter int WIDTH = 8") {
		t.Fatalf("expected hover on a body-level parameter to show its type and default, got %q", content.Value)
	}
}

func TestTextDocumentHoverNamedPortConnectionResolvesToTargetPort(t *testing.T) {
	s := newTestServer()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///leaf.sv", LanguageID: "systemverilog", Version: 1,
			Text: "module leaf(input logic clk, output logic [7:0] data);\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}
	topText := "module top;\n  leaf u_leaf (\n    .clk(sig),\n    .data(bus)\n  );\nendmodule\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///top.sv", LanguageID: "systemverilog", Version: 1, Text: topText},
	}); err != nil {
		t.Fatal(err)
	}
	connectionLine := "    .clk(sig),"

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///top.sv"},
			Position:     protocol.Position{Line: 2, Character: protocol.UInteger(strings.Index(connectionLine, "clk"))},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	if hover == nil {
		t.Fatalf("expected a hover result")
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "input logic clk") {
		t.Fatalf("expected hover on the .clk( connection to show leaf's clk port, got %q", content.Value)
	}
}

func TestTextDocumentHoverTypedefAlias(t *testing.T) {
	s := newTestServer()
	src := "typedef logic [7:0] byte_t;\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 0, Character: protocol.UInteger(strings.Index(src, "byte_t"))},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "typedef logic [7:0] byte_t") {
		t.Fatalf("expected hover text to show the aliased type, got %q", content.Value)
	}
}

func TestTextDocumentHoverTypedefEnum(t *testing.T) {
	s := newTestServer()
	src := "typedef enum {S_FOO, S_BAR, S_BAZ} en_States;\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 0, Character: protocol.UInteger(strings.Index(src, "en_States"))},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	want := "typedef enum {\n  S_FOO = 0,\n  S_BAR = 1,\n  S_BAZ = 2\n} en_States"
	if !strings.Contains(content.Value, want) {
		t.Fatalf("expected hover text to show computed enum values, got %q", content.Value)
	}
}

func TestTextDocumentHoverTypedefEnumTruncatesBeyondTen(t *testing.T) {
	s := newTestServer()
	var members strings.Builder
	for i := range 12 {
		if i > 0 {
			members.WriteString(", ")
		}
		fmt.Fprintf(&members, "M%d", i)
	}
	src := fmt.Sprintf("typedef enum {%s} big_t;\n", members.String())
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 0, Character: protocol.UInteger(strings.Index(src, "big_t"))},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	for i := range 10 {
		name := fmt.Sprintf("M%d = %d", i, i)
		if !strings.Contains(content.Value, name) {
			t.Fatalf("expected hover text to include member %q (within the first 10), got %q", name, content.Value)
		}
	}
	if !strings.Contains(content.Value, "...") {
		t.Fatalf("expected hover text to indicate truncation with \"...\", got %q", content.Value)
	}
	for _, name := range []string{"M10", "M11"} {
		if strings.Contains(content.Value, name) {
			t.Fatalf("expected hover text NOT to include member %q (beyond the 10-member cap), got %q", name, content.Value)
		}
	}
}

func TestTextDocumentHoverTypedefStruct(t *testing.T) {
	s := newTestServer()
	src := "typedef struct packed {\n  logic [7:0] data;\n  logic valid;\n} pkt_t;\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}
	lastLine := "} pkt_t;"

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 3, Character: protocol.UInteger(strings.Index(lastLine, "pkt_t"))},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	want := "typedef struct packed {\n  logic [7:0] data;\n  logic valid;\n} pkt_t"
	if !strings.Contains(content.Value, want) {
		t.Fatalf("expected hover text to show struct fields, got %q", content.Value)
	}
}

func TestTextDocumentHoverEnumMemberShowsComputedValue(t *testing.T) {
	s := newTestServer()
	src := "typedef enum {S_FOO, S_BAR, S_BAZ} en_States;\n"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: src},
	}); err != nil {
		t.Fatal(err)
	}

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 0, Character: protocol.UInteger(strings.Index(src, "S_BAR"))},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	content, _ := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "S_BAR = 1  // en_States") {
		t.Fatalf("expected hover text to show S_BAR's computed value and enclosing enum, got %q", content.Value)
	}
}

func TestTextDocumentHoverNoMatchReturnsNil(t *testing.T) {
	s := newTestServer()
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: "module a;\nendmodule\n"},
	}); err != nil {
		t.Fatal(err)
	}

	hover, err := s.TextDocumentHover(nil, &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: "file:///a.sv"},
			Position:     protocol.Position{Line: 0, Character: 0}, // "module" keyword, not a name
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentHover: %v", err)
	}
	if hover != nil {
		t.Fatalf("expected a nil hover, got %+v", hover)
	}
}

func TestTextDocumentDocumentHighlight(t *testing.T) {
	s := newTestServer()
	uri := "file:///top.sv"
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: uri, LanguageID: "systemverilog", Version: 1,
			Text: "module top;\n  leaf u_leaf ();\n  leaf u_leaf2 ();\nendmodule\n",
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Cursor on "leaf" on line 1.
	highlights, err := s.TextDocumentDocumentHighlight(nil, &protocol.DocumentHighlightParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Position:     protocol.Position{Line: 1, Character: 3},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentDocumentHighlight: %v", err)
	}
	if len(highlights) != 2 {
		t.Fatalf("expected 2 highlights, got %+v", highlights)
	}
}

func TestTextDocumentDocumentHighlightNoWordReturnsNil(t *testing.T) {
	s := newTestServer()
	uri := "file:///a.sv"
	// Three spaces between "module" and "a" so a cursor in the middle one
	// isn't touching either identifier on either side.
	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{URI: uri, LanguageID: "systemverilog", Version: 1, Text: "module   a; endmodule"},
	}); err != nil {
		t.Fatal(err)
	}

	highlights, err := s.TextDocumentDocumentHighlight(nil, &protocol.DocumentHighlightParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri},
			Position:     protocol.Position{Line: 0, Character: 7},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentDocumentHighlight: %v", err)
	}
	if highlights != nil {
		t.Fatalf("expected nil highlights, got %+v", highlights)
	}
}
