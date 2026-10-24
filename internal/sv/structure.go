package sv

import (
	"slices"
	"strings"

	"github.com/jfetkotto/svparse/v2/ast"
)

// StructureKind says what a Structure node is.
type StructureKind uint8

const (
	// StructProcess is an always, initial or final procedure.
	StructProcess StructureKind = iota + 1
	// StructGenerate is a generate construct (if, for, case, or a named
	// block) or one of its branches.
	StructGenerate
)

// Structure is a node of a file's outline that is not a declaration: a
// process or a generate construct. The index keeps these apart from
// Declarations on purpose: they are not names anyone resolves, and the
// scope-chain lookups key off Declaration.Parent, which must stay as it is.
// The outline merges the two by range containment (see lspserver's
// outline).
type Structure struct {
	Kind   StructureKind
	Name   string
	Detail string

	Line, Character       int
	EndLine, EndCharacter int
	// KeywordLen is the length of the keyword the node starts with, which the
	// outline selects.
	KeywordLen int
}

// structureFromAST lists the processes and generate constructs written in f
// itself (not in what it `includes), in source order.
func structureFromAST(f *ast.File) []Structure {
	var out []Structure
	add := func(n ast.Node, kind StructureKind, name, detail string, keyword int) {
		p := n.Pos()
		if p.File != "" && p.File != f.Path {
			return
		}
		out = append(out, Structure{
			Kind: kind, Name: name, Detail: detail,
			Line: p.Line, Character: p.Character, EndLine: p.EndLine, EndCharacter: p.EndCharacter, KeywordLen: keyword,
		})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Always:
			add(n, StructProcess, n.Kind, sensitivity(n.Body), len(n.Kind))
		case *ast.Initial:
			add(n, StructProcess, "initial", "", len("initial"))
		case *ast.Final:
			add(n, StructProcess, "final", "", len("final"))
		case *ast.GenerateIf:
			add(n, StructGenerate, "if ("+ast.ExprString(n.Cond)+")", "", len("if"))
			if blk, ok := n.Else.(*ast.GenerateBlock); ok && blk.Label == "" {
				add(blk, StructGenerate, "else", "", len("begin"))
			}
		case *ast.GenerateFor:
			add(n, StructGenerate, "for ("+n.Var+")", ast.ExprString(n.Cond), len("for"))
		case *ast.GenerateCase:
			add(n, StructGenerate, "case ("+ast.ExprString(n.Expr)+")", "", len("case"))
		case *ast.GenerateBlock:
			if n.Label != "" {
				add(n, StructGenerate, n.Label, "", len("begin"))
			}
		}
		return true
	})
	// An else branch is added with its if, ahead of the block it follows.
	// The outline relies on source order with enclosing nodes first.
	slices.SortStableFunc(out, func(a, b Structure) int {
		switch {
		case a.Line != b.Line:
			return a.Line - b.Line
		case a.Character != b.Character:
			return a.Character - b.Character
		case a.EndLine != b.EndLine:
			return b.EndLine - a.EndLine
		}
		return b.EndCharacter - a.EndCharacter
	})
	return out
}

// sensitivity renders the event control of "always @(...)" /
// "always_ff @(...)" for the outline's detail line.
func sensitivity(body ast.Stmt) string {
	ts, ok := body.(*ast.TimedStmt)
	if !ok || ts.Control == nil {
		return ""
	}
	c := ts.Control
	if c.Star {
		return "@*"
	}
	if len(c.Events) == 0 {
		return ""
	}
	terms := make([]string, len(c.Events))
	for i, e := range c.Events {
		t := ast.ExprString(e.X)
		if e.Edge != "" {
			t = e.Edge + " " + t
		}
		terms[i] = t
	}
	return "@(" + strings.Join(terms, " or ") + ")"
}
