package sv

import (
	"strings"
	"testing"
)

func TestInstantiationContextAtInsideEmptyParens(t *testing.T) {
	text := "module top;\n  leaf u_leaf (\n  );\nendmodule\n"
	// Cursor on the blank line inside the parens.
	name, connected, ok := InstantiationContextAt(text, 2, 2)
	if !ok {
		t.Fatalf("expected ok")
	}
	if name != "leaf" {
		t.Fatalf("moduleName = %q, want \"leaf\"", name)
	}
	if len(connected) != 0 {
		t.Fatalf("expected no connected ports, got %v", connected)
	}
}

func TestInstantiationContextAtSameLine(t *testing.T) {
	text := "module top;\n  leaf u_leaf ()\nendmodule\n"
	// Cursor between the parens on the same line.
	name, _, ok := InstantiationContextAt(text, 1, 15)
	if !ok || name != "leaf" {
		t.Fatalf("InstantiationContextAt = %q, %v", name, ok)
	}
}

func TestInstantiationContextAtExcludesConnectedPorts(t *testing.T) {
	text := "module top;\n  leaf u_leaf (\n    .clk(sys_clk),\n    .\n  );\nendmodule\n"
	// Cursor right after the "." on the last (incomplete) line.
	name, connected, ok := InstantiationContextAt(text, 3, 5)
	if !ok || name != "leaf" {
		t.Fatalf("InstantiationContextAt = %q, %v", name, ok)
	}
	if !connected["clk"] {
		t.Fatalf("expected \"clk\" to be recorded as already connected, got %v", connected)
	}
}

func TestInstantiationContextAtNotInsideAnyParens(t *testing.T) {
	text := "module top;\n  leaf u_leaf ();\nendmodule\n"
	if _, _, ok := InstantiationContextAt(text, 2, 0); ok {
		t.Fatalf("expected no instantiation context outside any parens")
	}
}

func TestInstantiationContextAtNestedFunctionCallDoesNotConfuseDepth(t *testing.T) {
	text := "module top;\n  leaf u_leaf (\n    .data(clog2(WIDTH)),\n    .\n  );\nendmodule\n"
	name, connected, ok := InstantiationContextAt(text, 3, 5)
	if !ok || name != "leaf" {
		t.Fatalf("InstantiationContextAt = %q, %v", name, ok)
	}
	if !connected["data"] {
		t.Fatalf("expected \"data\" to be recorded as connected despite its nested clog2(...) call, got %v", connected)
	}
	if len(connected) != 1 {
		t.Fatalf("expected exactly one connected port, got %v", connected)
	}
}

func TestInstantiationContextAtRequiresTwoIdentifiersBeforeParen(t *testing.T) {
	// A bare function call, not an instantiation -- only one identifier
	// (not two) directly precedes the "(".
	text := "module top;\n  foo(\n  );\nendmodule\n"
	if _, _, ok := InstantiationContextAt(text, 2, 2); ok {
		t.Fatalf("expected no instantiation context for a plain call with only one preceding identifier")
	}
}

func TestInstantiationContextAtParameterOverrides(t *testing.T) {
	// "my_mod #(.W(4)) u0 (" -- the dominant shape in real RTL. Previously
	// the closing ')' of the override list sat where the module-type
	// identifier was expected, so this returned ok=false entirely.
	text := "module top;\n  my_mod #(.W(4)) u0 (\n  );\nendmodule\n"
	name, connected, ok := InstantiationContextAt(text, 2, 2)
	if !ok || name != "my_mod" {
		t.Fatalf("InstantiationContextAt = %q, %v", name, ok)
	}
	if len(connected) != 0 {
		t.Fatalf("expected no connected ports, got %v", connected)
	}
}

func TestInstantiationContextAtParameterOverridesWithConnection(t *testing.T) {
	text := "module top;\n  my_mod #(.W(4)) u0 (\n    .clk(clk),\n    .\n  );\nendmodule\n"
	name, connected, ok := InstantiationContextAt(text, 3, 5)
	if !ok || name != "my_mod" {
		t.Fatalf("InstantiationContextAt = %q, %v", name, ok)
	}
	if !connected["clk"] {
		t.Fatalf("expected \"clk\" to be recorded as already connected, got %v", connected)
	}
}

func TestInstantiationContextAtParameterOverridesWithNestedCall(t *testing.T) {
	// A nested function call inside the override list's own parens
	// ("#(.W($clog2(4)))") must not desync the balanced-paren walk back
	// to the module type.
	text := "module top;\n  my_mod #(.W($clog2(4))) u0 (\n  );\nendmodule\n"
	name, _, ok := InstantiationContextAt(text, 2, 2)
	if !ok || name != "my_mod" {
		t.Fatalf("InstantiationContextAt = %q, %v", name, ok)
	}
}

func TestInstantiationContextAtInstanceArray(t *testing.T) {
	// "leaf u_leaf[3] (" -- an instance array; the ']' must not be
	// mistaken for the instance name itself.
	text := "module top;\n  leaf u_leaf[3] (\n  );\nendmodule\n"
	name, _, ok := InstantiationContextAt(text, 2, 2)
	if !ok || name != "leaf" {
		t.Fatalf("InstantiationContextAt = %q, %v", name, ok)
	}
}

func TestInstantiationContextAtParameterOverridesAndInstanceArray(t *testing.T) {
	text := "module top;\n  my_mod #(.W(4)) u_leaf[3] (\n  );\nendmodule\n"
	name, _, ok := InstantiationContextAt(text, 2, 2)
	if !ok || name != "my_mod" {
		t.Fatalf("InstantiationContextAt = %q, %v", name, ok)
	}
}

func TestInstantiationPortNameAtMatchesConnectedPort(t *testing.T) {
	text := "module top;\n  leaf u_leaf (\n    .clk(sig)\n  );\nendmodule\n"
	line := "    .clk(sig)"
	word, start, ok := WordAt(text, 2, strings.Index(line, "clk"))
	if !ok || word != "clk" {
		t.Fatalf("WordAt = %q, %v", word, ok)
	}
	moduleName, ok := InstantiationPortNameAt(text, 2, word, start)
	if !ok || moduleName != "leaf" {
		t.Fatalf("InstantiationPortNameAt = %q, %v, want (\"leaf\", true)", moduleName, ok)
	}
}

func TestInstantiationPortNameAtRejectsConnectionValue(t *testing.T) {
	text := "module top;\n  leaf u_leaf (\n    .clk(sig)\n  );\nendmodule\n"
	line := "    .clk(sig)"
	word, start, ok := WordAt(text, 2, strings.Index(line, "sig"))
	if !ok || word != "sig" {
		t.Fatalf("WordAt = %q, %v", word, ok)
	}
	if _, ok := InstantiationPortNameAt(text, 2, word, start); ok {
		t.Fatalf("expected sig (a connection value, not a port name) not to match")
	}
}

func TestInstantiationPortNameAtRejectsOutsideInstantiation(t *testing.T) {
	text := "module top;\n  logic clk;\nendmodule\n"
	line := "  logic clk;"
	word, start, ok := WordAt(text, 1, strings.Index(line, "clk"))
	if !ok || word != "clk" {
		t.Fatalf("WordAt = %q, %v", word, ok)
	}
	if _, ok := InstantiationPortNameAt(text, 1, word, start); ok {
		t.Fatalf("expected no match outside any instantiation")
	}
}

func TestInstantiationParamContextAtInsideHashParens(t *testing.T) {
	text := "module top;\n  my_mod #(\n    .WIDTH(8),\n    .\n  ) u0 (\n  );\nendmodule\n"
	// Cursor right after the "." on the incomplete line.
	name, connected, ok := InstantiationParamContextAt(text, 3, 5)
	if !ok || name != "my_mod" {
		t.Fatalf("InstantiationParamContextAt = %q, %v", name, ok)
	}
	if !connected["WIDTH"] {
		t.Fatalf("expected \"WIDTH\" to be recorded as already connected, got %v", connected)
	}
}

func TestInstantiationParamNameAtMatchesConnectedParam(t *testing.T) {
	text := "module top;\n  my_mod #(.WIDTH(8)) u0 (\n  );\nendmodule\n"
	line := "  my_mod #(.WIDTH(8)) u0 ("
	word, start, ok := WordAt(text, 1, strings.Index(line, "WIDTH"))
	if !ok || word != "WIDTH" {
		t.Fatalf("WordAt = %q, %v", word, ok)
	}
	moduleName, ok := InstantiationParamNameAt(text, 1, word, start)
	if !ok || moduleName != "my_mod" {
		t.Fatalf("InstantiationParamNameAt = %q, %v, want (\"my_mod\", true)", moduleName, ok)
	}
}

func TestInstantiationParamNameAtDoesNotMatchPortConnection(t *testing.T) {
	text := "module top;\n  leaf u_leaf (.clk(sig));\nendmodule\n"
	line := "  leaf u_leaf (.clk(sig));"
	word, start, ok := WordAt(text, 1, strings.Index(line, "clk"))
	if !ok || word != "clk" {
		t.Fatalf("WordAt = %q, %v", word, ok)
	}
	if _, ok := InstantiationParamNameAt(text, 1, word, start); ok {
		t.Fatalf("expected a port connection's name not to be mistaken for a parameter override")
	}
}
