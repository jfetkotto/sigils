// Package semantic answers "what does this mean?" questions about a position
// in the workspace using svparse's semantic model (sema): the resolved type
// of the expression under the cursor and its constant value, beside what the
// declaration index already knows about the name. It is built lazily from
// the parse trees the index keeps, and rebuilt only when they change.
package semantic

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jfetkotto/svparse/v2/ast"
	"github.com/jfetkotto/svparse/v2/diag"
	"github.com/jfetkotto/svparse/v2/elab"
	"github.com/jfetkotto/svparse/v2/lint"
	"github.com/jfetkotto/svparse/v2/preprocessor"
	"github.com/jfetkotto/svparse/v2/sema"
)

// elaborationBudget caps the time elaboration may take in one diagnostics pass.
const elaborationBudget = 5 * time.Second

// Snapshot is the workspace as the semantic model sees it: every file's parse
// tree, the comments that carry lint directives, and a generation number that
// changes whenever any of them does.
type Snapshot struct {
	Files      []*ast.File
	Directives []preprocessor.Comment
	Generation uint64
}

// Source supplies the current Snapshot (see sv.Index.Snapshot).
type Source func() Snapshot

// Service builds the semantic model on demand and caches it per generation.
type Service struct {
	source Source

	mu         sync.Mutex
	gen        uint64
	built      bool
	comp       *sema.Compilation
	model      *sema.Model
	files      map[string]*ast.File
	directives []preprocessor.Comment

	// lint, if set, makes Diagnostics include the lint findings.
	lint *lint.Config

	nav    *navIndex // see navigate.go
	navGen uint64

	diags    map[string][]diag.Diagnostic // by file, for generation diagsGen
	diagsGen uint64
	diagsOK  bool

	// design is the elaboration for generation designGen, with its problems.
	design      *elab.Design
	designDiags []diag.Diagnostic
	designGen   uint64
}

// New returns a Service reading its trees from source.
func New(source Source) *Service { return &Service{source: source} }

// Info is what the semantic model knows about a position. Either part may be
// absent: a keyword has neither, a declaration's name has only Symbol, and an
// operator inside an expression has only Expr.
type Info struct {
	// Symbol describes the name under the cursor when it resolves to
	// something with a type or a value (a signal, a port, a parameter).
	Symbol *Entry
	// Expr describes the smallest expression under the cursor.
	Expr *Entry
	// Parent describes the expression that directly encloses Expr, when it is
	// a different, larger expression: hovering "a" in "a + b" gives the sum.
	Parent *Entry
}

// Entry is one thing described: source text, its type and, when it is a
// constant, its value.
type Entry struct {
	Text   string
	Kind   string // symbol kind, "" for an expression
	Type   string
	Bits   int // 0 when the type has no defined width
	Signed bool
	Value  string // "" when not a constant
	// ContextBits is the width the expression is evaluated at when it is
	// part of an assignment's right-hand side and the assignment widens it
	// beyond its own width (LRM 11.8.1); 0 when it is the same as Bits.
	ContextBits int
}

// Describe resolves the position (line and character are the zero-based LSP
// values, characters in UTF-16 units, as svparse positions are) in uri. word
// is the identifier under the cursor, "" if none.
func (s *Service) Describe(uri string, line, character int, word string) (info Info, ok bool) {
	defer func() {
		if recover() != nil { // hover must never take the server down
			info, ok = Info{}, false
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	file := s.fileLocked(uri)
	if file == nil {
		return Info{}, false
	}
	at := s.locate(file, uri, line, character)
	if at.scope == nil {
		return Info{}, false
	}
	// Inside an expression the expression answers for its names (a member
	// name looked up on its own would find an unrelated symbol). A name
	// outside any expression is a declaration's, resolved from the scope.
	if len(at.path) == 0 && word != "" {
		if e, found := s.symbol(at.scope, word); found {
			info.Symbol = e
		}
	}
	if n := len(at.path); n > 0 {
		ctx := s.assignmentContext(at)
		if e, found := s.expr(at.scope, at.path[n-1], ctx); found {
			info.Expr = e
		}
		for i := n - 2; i >= 0; i-- {
			if _, paren := at.path[i].(*ast.Paren); paren {
				continue
			}
			if e, found := s.expr(at.scope, at.path[i], ctx); found && (info.Expr == nil || e.Text != info.Expr.Text) {
				info.Parent = e
			}
			break
		}
	}
	return info, info.Symbol != nil || info.Expr != nil || info.Parent != nil
}

// assignmentContext binds the right-hand side of the assignment containing
// the position against its target's type, the way the language evaluates it
// (LRM 11.8.1: operands are widened to the larger of the sides), and indexes
// the result by source expression. It is nil outside an assignment.
func (s *Service) assignmentContext(at found) map[ast.Expr]sema.BoundExpr {
	if at.assign == nil || len(at.path) == 0 || at.assign.RHS != at.path[0] {
		return nil
	}
	lhs, _ := s.model.Bind(at.scope, at.assign.LHS)
	if lhs.Type() == nil {
		return nil
	}
	rhs, _ := s.model.BindAssign(at.scope, lhs.Type(), at.assign.RHS)
	if rhs == nil {
		return nil
	}
	byExpr := map[ast.Expr]sema.BoundExpr{}
	sema.WalkBound(rhs, func(b sema.BoundExpr) bool {
		if src := b.Source(); src != nil {
			if _, dup := byExpr[src]; !dup {
				byExpr[src] = b
			}
		}
		return true
	})
	return byExpr
}

// fileLocked returns uri's tree, rebuilding the model if the workspace
// changed since it was last built.
func (s *Service) fileLocked(uri string) *ast.File {
	snap := s.source()
	files, gen := snap.Files, snap.Generation
	if !s.built || gen != s.gen {
		comp := sema.NewCompilation(sema.Options{})
		byURI := make(map[string]*ast.File, len(files))
		for _, f := range files {
			comp.AddFile(f)
			byURI[f.Path] = f
		}
		s.comp, s.model, s.files, s.gen, s.built = comp, comp.Model(), byURI, gen, true
		s.directives = snap.Directives
		s.diagsOK = false
	}
	return s.files[uri]
}

// symbol describes a name looked up from scope.
func (s *Service) symbol(scope *sema.Scope, name string) (*Entry, bool) {
	sym := scope.Lookup(name).Symbol
	if sym == nil {
		return nil, false
	}
	e := &Entry{Text: name, Kind: sym.Kind.String()}
	defer func() {
		if e.Type == invalidType {
			e.Type, e.Bits = "", 0
		}
	}()
	switch sym.Kind {
	case sema.SymVariable, sema.SymNet, sema.SymPort, sema.SymArg, sema.SymParameter, sema.SymLoopVar, sema.SymGenvar:
		if t, _ := s.model.TypeOf(sym); t != nil {
			fillType(e, t)
		}
	}
	if sym.Kind == sema.SymParameter {
		if c, ds := s.model.ParamValue(sym, nil); len(ds) == 0 {
			e.Value = valueString(c)
		}
	}
	return e, e.Type != "" || e.Value != ""
}

// expr describes an expression bound in scope.
func (s *Service) expr(scope *sema.Scope, x ast.Expr, ctx map[ast.Expr]sema.BoundExpr) (*Entry, bool) {
	be, _ := s.model.Bind(scope, x)
	if be.Type() == nil || be.Type().String() == invalidType {
		return nil, false
	}
	e := &Entry{Text: ast.ExprString(x)}
	fillType(e, be.Type())
	if e.Type == "" {
		return nil, false
	}
	if c, ds := s.model.EvalBound(be, nil); len(ds) == 0 {
		e.Value = valueString(c)
	}
	if b, ok := ctx[x]; ok && b.Type() != nil {
		if w, ok := sema.Bits(b.Type()); ok && w != e.Bits {
			e.ContextBits = w
		}
	}
	return e, true
}

// valueString shows an integer constant in decimal, which is how it reads in
// a hover, and anything else (reals, strings, x/z values) as sema prints it.
func valueString(c sema.Const) string {
	if c.Kind == sema.ConstInt {
		if v, ok := c.Int64(); ok {
			return strconv.FormatInt(v, 10)
		}
	}
	return c.String()
}

// invalidType is how sema prints the type of an expression it could not
// resolve; it says nothing a reader wants.
const invalidType = "<invalid>"

func fillType(e *Entry, t sema.Type) {
	e.Type = t.String()
	if w, ok := sema.Bits(t); ok {
		e.Bits = w
	}
	e.Signed = sema.IsIntegral(t) && sema.IsSigned(t)
}

// Markdown renders i as the hover section that follows the declaration: one
// line per entry, "`text` : `type` (width, sign) = value".
func (i Info) Markdown() string {
	var lines []string
	add := func(label string, e *Entry) {
		if e != nil {
			lines = append(lines, label+e.line())
		}
	}
	add("", i.Symbol)
	add("", i.Expr)
	add("in ", i.Parent)
	return strings.Join(lines, "  \n")
}

func (e *Entry) line() string {
	var b strings.Builder
	fmt.Fprintf(&b, "`%s`", e.Text)
	if e.Kind != "" {
		fmt.Fprintf(&b, " (%s)", e.Kind)
	}
	if e.Type != "" {
		fmt.Fprintf(&b, " : `%s`", e.Type)
		var facts []string
		if e.Bits > 0 {
			facts = append(facts, fmt.Sprintf("%d bit", e.Bits))
			if e.Bits != 1 {
				facts[0] += "s"
			}
			if e.Signed {
				facts = append(facts, "signed")
			}
			if e.ContextBits > 0 {
				facts = append(facts, fmt.Sprintf("%d in this assignment", e.ContextBits))
			}
		}
		if len(facts) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(facts, ", "))
		}
	}
	if e.Value != "" {
		fmt.Fprintf(&b, " = `%s`", e.Value)
	}
	return b.String()
}

// found is where a position lies: the scope to resolve names in and the
// chain of expressions containing it, outermost first.
type found struct {
	scope  *sema.Scope
	path   []ast.Expr
	assign *ast.Assign // the innermost assignment around the position
}

// locate finds the innermost scope and the expression chain at a position by
// walking the tree down every node that contains it. Children are visited
// after their parent, so the last node recorded is the deepest.
func (s *Service) locate(f *ast.File, uri string, line, character int) found {
	us, _ := s.model.UnitScope(f.Path)
	res := &found{scope: us}
	ast.Walk(&locator{model: s.model, uri: uri, line: line, char: character, res: res, scope: us}, f)
	return *res
}

type locator struct {
	model      *sema.Model
	uri        string
	line, char int
	res        *found

	scope  *sema.Scope
	path   []ast.Expr
	assign *ast.Assign
}

func (w *locator) contains(p ast.Position) bool {
	if p.File != "" && p.File != w.uri {
		return false
	}
	afterStart := w.line > p.Line || (w.line == p.Line && w.char >= p.Character)
	beforeEnd := w.line < p.EndLine || (w.line == p.EndLine && w.char < p.EndCharacter)
	return afterStart && beforeEnd
}

func (w *locator) Visit(n ast.Node) ast.Visitor {
	if n == nil {
		return nil
	}
	if _, isFile := n.(*ast.File); isFile {
		return w
	}
	if !w.contains(n.Pos()) {
		return nil
	}
	next := *w
	if sc, ok := w.model.ScopeOf(n); ok {
		next.scope = sc
	}
	if e, ok := n.(ast.Expr); ok {
		next.path = append(append([]ast.Expr(nil), w.path...), e)
	}
	if a, ok := n.(*ast.Assign); ok {
		next.assign = a
	}
	*w.res = found{scope: next.scope, path: next.path, assign: next.assign}
	return &next
}

// Diagnostics returns the semantic problems of the whole workspace, grouped
// by the file each is in: duplicate definitions, unresolved names and types,
// illegal assignments and drivers, and the like (everything sema reports
// beyond syntax, which the index already has). The result is for the
// generation of the trees seen at the call, which is also returned; callers
// compare it with the index's to tell whether the answer went stale while
// it was being computed.
func (s *Service) Diagnostics() (map[string][]diag.Diagnostic, uint64) {
	defer func() { _ = recover() }() // diagnostics must never take the server down
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fileLocked("") // rebuild if the workspace changed
	if !s.diagsOK {
		s.diags, s.diagsGen, s.diagsOK = s.computeLocked(nil), s.gen, true
	}
	return s.diags, s.diagsGen
}

// DiagnosticsFor is Diagnostics limited to the files in files: the problems in
// those files only, with an (empty) entry for each so a caller can tell a file
// that became clean. It does the work for those files alone, apart from
// elaborating the design (which it keeps for the generation, so a later full
// pass does not repeat it): a change to one file of a large workspace is
// answered in a fraction of the time of the whole workspace. A change can
// alter problems in other files too (a port renamed breaks its instances);
// those are found by the next Diagnostics.
func (s *Service) DiagnosticsFor(files map[string]bool) (byFile map[string][]diag.Diagnostic, gen uint64) {
	defer func() {
		if recover() != nil {
			byFile, gen = nil, 0
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fileLocked("")
	byFile = s.computeLocked(files)
	for f := range files {
		if _, ok := byFile[f]; !ok {
			byFile[f] = nil
		}
	}
	return byFile, s.gen
}

// computeLocked runs the checks over files (all, if nil) and groups what they
// find by file.
func (s *Service) computeLocked(files map[string]bool) map[string][]diag.Diagnostic {
	var set diag.Set
	want := func(ds []diag.Diagnostic) {
		for _, d := range ds {
			if files == nil || files[d.Range.File] {
				set.Add(d)
			}
		}
	}
	want(s.comp.Diagnostics())
	want(s.model.Diagnostics())
	set.AddAll(s.model.CheckStatementsIn(files))
	set.AddAll(s.model.CheckNamesIn(files))
	// Elaboration finds what only the instance tree shows: modules that are not
	// defined, port and parameter mismatches. It is bounded so a pathological
	// design cannot stall the pass.
	ctx, cancel := context.WithTimeout(context.Background(), elaborationBudget)
	defer cancel()
	if s.design == nil || s.designGen != s.gen {
		design, ds := elab.Elaborate(ctx, s.comp, elab.Options{})
		s.design, s.designDiags, s.designGen = design, ds, s.gen
	}
	want(s.designDiags)
	if s.lint != nil {
		set.AddAll(lint.Run(ctx, lint.Input{Comp: s.comp, Design: s.design, Comments: s.directives, Files: files}, *s.lint))
	}
	byFile := map[string][]diag.Diagnostic{}
	for _, d := range set.Sorted() {
		switch d.Code {
		case diag.CodeSyntaxError, diag.CodePreprocessorError:
			continue // the index reports these
		}
		byFile[d.Range.File] = append(byFile[d.Range.File], d)
	}
	return byFile
}

// SetLint turns lint findings in Diagnostics on, with cfg, or off with nil.
// Findings already computed are discarded.
func (s *Service) SetLint(cfg *lint.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lint = cfg
	s.diagsOK = false
}
