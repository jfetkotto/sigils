package semantic

import (
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/jfetkotto/svparse/v2/ast"
	"github.com/jfetkotto/svparse/v2/sema"
)

// Location is a range in a file, zero-based, in UTF-16 units as the LSP wants.
type Location struct {
	URI                   string
	Line, Character       int
	EndLine, EndCharacter int
}

// navRef is one place a symbol is named: its declaration or a use.
type navRef struct {
	loc Location
	// sym is the thing named: a *sema.Symbol, or a *sema.Definition for a
	// module, interface, program, package or class (which the model keeps
	// in the compilation rather than as symbols).
	sym  any
	decl bool
}

// navIndex answers go-to-definition and find-references from the resolved
// names of the whole workspace. It is built when first asked for and kept for
// the generation it was built from.
type navIndex struct {
	byFile map[string][]navRef
	bySym  map[any][]navRef
	decls  map[any]Location
}

// Definition returns where the name at the position is declared, resolved by
// the semantic model: through scopes, imports, packages, instance ports and
// parameters, and class members, instead of by spelling. ok is false when the
// position is not on a name the model resolves.
func (s *Service) Definition(uri string, line, character int) (locs []Location, ok bool) {
	defer func() {
		if recover() != nil {
			locs, ok = nil, false
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	nav := s.navLocked()
	ref, found := nav.at(uri, line, character)
	if !found {
		return nil, false
	}
	loc, has := nav.decls[ref.sym]
	if !has {
		return nil, false
	}
	return []Location{loc}, true
}

// References returns every place the name at the position is used, and its
// declaration when includeDeclaration is set. Two names that are spelt alike
// but are different symbols (a local shadowing a port, the same signal name in
// two modules) are told apart.
func (s *Service) References(uri string, line, character int, includeDeclaration bool) (locs []Location, ok bool) {
	defer func() {
		if recover() != nil {
			locs, ok = nil, false
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	nav := s.navLocked()
	ref, found := nav.at(uri, line, character)
	if !found {
		return nil, false
	}
	for _, r := range nav.bySym[ref.sym] {
		if r.decl && !includeDeclaration {
			continue
		}
		locs = append(locs, r.loc)
	}
	slices.SortFunc(locs, compareLocations)
	return locs, len(locs) > 0
}

func compareLocations(a, b Location) int {
	switch {
	case a.URI != b.URI:
		if a.URI < b.URI {
			return -1
		}
		return 1
	case a.Line != b.Line:
		return a.Line - b.Line
	}
	return a.Character - b.Character
}

func (n *navIndex) at(uri string, line, character int) (navRef, bool) {
	for _, r := range n.byFile[uri] {
		l := r.loc
		afterStart := line > l.Line || (line == l.Line && character >= l.Character)
		beforeEnd := line < l.EndLine || (line == l.EndLine && character <= l.EndCharacter)
		if afterStart && beforeEnd {
			return r, true
		}
	}
	return navRef{}, false
}

func (s *Service) navLocked() *navIndex {
	s.fileLocked("") // rebuild the model if the workspace changed
	if s.nav != nil && s.navGen == s.gen {
		return s.nav
	}
	nav := &navIndex{
		byFile: map[string][]navRef{},
		bySym:  map[any][]navRef{},
		decls:  map[any]Location{},
	}
	seen := map[refKey]bool{}
	scopes := map[*sema.Scope]bool{}
	for _, def := range s.comp.Definitions() {
		b := &navBuilder{s: s, nav: nav, seen: seen, scopes: scopes}
		if def.Decl == nil {
			continue
		}
		loc := nameLocation(def.Decl.Pos(), def.Name)
		if _, dup := nav.decls[def]; !dup {
			nav.decls[def] = loc
		}
		b.add(loc, def, true)
	}
	for _, f := range s.snapshotFiles() {
		us, _ := s.model.UnitScope(f.Path)
		b := &navBuilder{s: s, nav: nav, seen: seen, scopes: scopes}
		b.declare(us)
		ast.Walk(&navWalker{b: b, scope: us}, f)
	}
	for uri := range nav.byFile {
		refs := nav.byFile[uri]
		slices.SortFunc(refs, func(a, b navRef) int { return compareLocations(a.loc, b.loc) })
	}
	s.nav, s.navGen = nav, s.gen
	return nav
}

// snapshotFiles are the trees the current model was built from.
func (s *Service) snapshotFiles() []*ast.File {
	files := make([]*ast.File, 0, len(s.files))
	for _, f := range s.files {
		files = append(files, f)
	}
	slices.SortFunc(files, func(a, b *ast.File) int {
		switch {
		case a.Path < b.Path:
			return -1
		case a.Path > b.Path:
			return 1
		}
		return 0
	})
	return files
}

type refKey struct {
	loc  Location
	decl bool
}

type navBuilder struct {
	s      *Service
	nav    *navIndex
	seen   map[refKey]bool
	scopes map[*sema.Scope]bool
}

func (b *navBuilder) add(loc Location, sym any, decl bool) {
	if sym == nil || loc.URI == "" {
		return
	}
	key := refKey{loc, decl}
	if b.seen[key] {
		return
	}
	b.seen[key] = true
	r := navRef{loc: loc, sym: sym, decl: decl}
	b.nav.byFile[loc.URI] = append(b.nav.byFile[loc.URI], r)
	b.nav.bySym[sym] = append(b.nav.bySym[sym], r)
}

// declare records the declaration of every symbol of a scope, once.
func (b *navBuilder) declare(s *sema.Scope) {
	if s == nil || b.scopes[s] {
		return
	}
	b.scopes[s] = true
	for _, sym := range s.Symbols() {
		switch sym.Kind {
		case sema.SymBlock, sema.SymGenerateBlock:
			continue // blocks are named by their labels in the source, not declared as names
		}
		switch sym.Decl.(type) {
		case nil, *ast.GenerateFor, *ast.Foreach:
			continue // the tree has no position for the name of a loop variable
		}
		loc := nameLocation(sym.Decl.Pos(), sym.Name)
		if loc.URI == "" {
			continue
		}
		if _, dup := b.nav.decls[sym]; !dup {
			b.nav.decls[sym] = loc
		}
		b.add(loc, sym, true)
	}
}

// nameLocation is the range of a declaration's name: declarations are
// positioned at their name.
func nameLocation(p ast.Position, name string) Location {
	return Location{URI: p.File, Line: p.Line, Character: p.Character, EndLine: p.Line, EndCharacter: p.Character + utf16Len(name)}
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

type navWalker struct {
	b     *navBuilder
	scope *sema.Scope
}

func (w *navWalker) Visit(n ast.Node) ast.Visitor {
	if n == nil {
		return nil
	}
	next := w
	if sc, ok := w.b.s.model.ScopeOf(n); ok {
		w.b.declare(sc)
		next = &navWalker{b: w.b, scope: sc}
	}
	switch n := n.(type) {
	case *ast.Ident:
		if len(n.Name) > 0 && n.Name[0] != '$' {
			w.use(n.Pos(), n.Name, w.resolve(n.Name))
		}
	case *ast.ScopedName:
		w.scoped(n)
	case *ast.Instantiation:
		w.instantiation(n)
	case *ast.Type:
		w.typeName(n)
	}
	return next
}

// use records a reference of length len(name) at p.
func (w *navWalker) use(p ast.Position, name string, sym any) {
	if sym == nil {
		return
	}
	w.b.add(nameLocation(p, name), sym, false)
}

// resolve finds what a plain name denotes from the current scope: a symbol, or
// a definition when no scope declares it (a module, interface, program or
// package used as a name). nil if it is neither; the result is never a typed
// nil, so it can be tested against nil.
func (w *navWalker) resolve(name string) any {
	if sym := w.scope.Lookup(name).Symbol; sym != nil {
		return sym
	}
	if def, ok := w.b.s.comp.Lookup(name); ok {
		return def
	}
	if def, ok := w.b.s.comp.Package(name); ok {
		return def
	}
	return nil
}

// scoped records the member of "pkg::name".
func (w *navWalker) scoped(e *ast.ScopedName) {
	path, ok := scopedNamePath(e)
	if !ok || len(path) < 2 {
		return
	}
	p := e.Pos() // the scope part is an Ident of its own, recorded when walked
	last := path[len(path)-1]
	if p.EndLine == p.Line && p.EndCharacter >= utf16Len(last) {
		if sym := w.scope.LookupQualified(path).Symbol; sym != nil {
			at := p
			at.Character = p.EndCharacter - utf16Len(last)
			w.use(at, last, sym)
		}
	}
}

// typeName records the name of a type, and the package of "pkg::name_t".
func (w *navWalker) typeName(t *ast.Type) {
	if t.Name == "" || t.Inline != nil || t.TypeRef != nil {
		return
	}
	if t.PackageQualifier == "" {
		w.use(t.Pos(), t.Name, w.resolve(t.Name))
		return
	}
	// "pkg::name_t" is written without spaces in practice; a type spelt any
	// other way is left to the lexical lookup.
	path := strings.Split(t.PackageQualifier, "::")
	if len(path) != 1 {
		return
	}
	p := t.Pos()
	w.use(p, path[0], w.resolve(path[0]))
	if sym := w.scope.LookupQualified(append(path, t.Name)).Symbol; sym != nil {
		at := p
		at.Character += utf16Len(path[0]) + 2
		w.use(at, t.Name, sym)
	}
}

// scopedNamePath flattens "a::b::c" into its names.
func scopedNamePath(e ast.Expr) ([]string, bool) {
	switch e := e.(type) {
	case *ast.Ident:
		return []string{e.Name}, true
	case *ast.ScopedName:
		base, ok := scopedNamePath(e.Scope)
		if !ok {
			return nil, false
		}
		return append(base, e.Name), true
	}
	return nil, false
}

// instantiation records the module type, the named port connections and the
// named parameter overrides of an instantiation.
func (w *navWalker) instantiation(in *ast.Instantiation) {
	m := w.b.s.model
	def, ok := w.b.s.comp.Lookup(in.ModuleType)
	if !ok {
		w.use(in.Pos(), in.ModuleType, w.resolve(in.ModuleType))
		return
	}
	w.use(in.Pos(), in.ModuleType, def)
	target, ok := m.DefinitionScope(def)
	if !ok {
		return
	}
	for i := range in.ParamOverrides {
		if o := &in.ParamOverrides[i]; o.Name != "" {
			if sym, found := target.Own(o.Name); found {
				w.use(o.Pos(), o.Name, sym)
			}
		}
	}
	for i := range in.Instances {
		for j := range in.Instances[i].Connections {
			if c := &in.Instances[i].Connections[j]; c.Name != "" && !c.Wildcard {
				if sym, found := target.Own(c.Name); found {
					w.use(c.Pos(), c.Name, sym)
				}
			}
		}
	}
}
