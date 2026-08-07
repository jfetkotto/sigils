package sv

import (
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/jfetkotto/svparse/lexer"
)

// Location is a declaration's position, independent of any LSP protocol
// type so this package doesn't need to depend on glsp.
type Location struct {
	URI       string
	Line      int
	Character int
	Kind      Kind
	Prototype bool
}

// Symbol is a workspace-wide completion candidate: just enough to label
// and categorize it, without a specific position (unlike Location, which
// exists to jump somewhere -- a completion candidate might have several
// declaration sites, and picking one to jump to isn't the point).
type Symbol struct {
	Name string
	Kind Kind
}

// SymbolLocation is a workspace symbol search result: a name, its kind,
// and where it's declared.
type SymbolLocation struct {
	Name      string
	Kind      Kind
	URI       string
	Line      int
	Character int
}

// Occurrence is a single identifier token: its text and position.
type Occurrence struct {
	Name      string
	Line      int
	Character int
}

// declRef points at one Declaration within Index.byURI, letting the index
// cross-reference a name to its full Declaration (including Parent, for
// walking the scope chain) without duplicating it.
type declRef struct {
	uri string
	idx int
}

// Index maps declaration names to their locations across a set of files,
// and supports scope-aware lookup via FindDefinition. Entries are keyed by
// URI internally so a single file's declarations can be replaced wholesale
// (SetFile) as it's edited or rescanned, without disturbing entries from
// other files.
//
// "Scope-aware" here does not mean full LRM-compliant elaboration -- see
// resolveRefsLocked's own doc comment for the precise, ordered resolution
// steps (qualified lookup, scope-chain walk, this file's own `include d
// files, then a Kind-restricted global fallback).
//
// Beyond declarations, Index also tracks every identifier *occurrence*
// (see Occurrence, occByName below), built by the same Scan call
// SetFile already makes for declarations -- so references/rename/
// documentHighlight can query pre-built data instead of re-reading a
// file from disk and re-tokenizing it on every request. This trades
// memory (an occurrence list is much bigger than a declaration list --
// most identifiers in a file are uses, not declarations) for eliminating
// that per-request I/O and CPU cost, which is the right side of that
// tradeoff for interactive latency on a large workspace. Repeated
// identifier text within one file is interned (see
// occurrencesFromSVParseTokens) to blunt the memory cost somewhat, though
// not across files.
//
// Occurrences are bucketed name -> URI -> that file's occurrences (in
// token order), rather than one flat list per name: replacing a file's
// entries on rescan (which happens on every keystroke via didChange) must
// not touch other files' entries for the same name. With a flat list,
// removing one file's "clk" occurrences means rewriting a slice holding
// every "clk" in the workspace -- per edit, per hot name.
type Index struct {
	mu     sync.RWMutex
	byURI  map[string][]Declaration
	byName map[string][]declRef
	// occByName[name][uri] holds every occurrence of name in uri.
	occByName map[string]map[string][]Occurrence
	// occNamesByURI[uri] lists the distinct identifier names occurring in
	// uri, so removeLocked can clear a file's buckets in O(distinct names).
	occNamesByURI map[string][]string
	// dependsOn[uri] holds every URI uri's last scan resolved an `include
	// to (see IncludeResolver.Resolved) -- already the *full transitive*
	// set, not just direct includes, since svparse's preprocessor follows
	// an included file's own includes within the same Preprocess call,
	// feeding all of them through the same resolver instance. This is
	// what lets Dependents do a plain one-hop reverse lookup instead of a
	// transitive graph walk: if uri's content changes, every W with
	// uri somewhere in dependsOn[W] is affected, full stop -- there's no
	// deeper "W depends on X which depends on uri" case dependsOn[W]
	// wouldn't already contain directly.
	dependsOn map[string][]string

	// errByURI[uri] holds every preprocessing/parsing Diagnostic from
	// uri's last scan -- see Diagnostics.
	errByURI map[string][]Diagnostic

	// importsByURI[uri] holds every import statement in uri, attributed
	// and Parent-scoped exactly like byURI's declarations (see Scan) --
	// consulted only by lookupInImportsRefsLocked, never by name lookup,
	// completion, or hover directly (an import doesn't declare a name of
	// its own -- see importDecl's doc comment).
	importsByURI map[string][]importDecl

	// connectionsByURI[uri] holds every named port connection/parameter
	// override in uri (see connectionSite's doc comment) -- consulted only
	// by connectionOccurrencesLocked, for find-references/rename scoping
	// on a connection site; never by name lookup, completion, or hover
	// directly (a connection doesn't declare a name of its own either).
	connectionsByURI map[string][]connectionSite

	// resolverFactory, if set, builds a fresh IncludeResolver for each
	// SetFile call to resolve `include directives with -- see
	// SetIncludeResolverFactory.
	resolverFactory ResolverFactory
	// initialMacros seeds every SetFile call's preprocessing -- see
	// SetInitialMacros.
	initialMacros map[string]string
}

func NewIndex() *Index {
	return &Index{
		byURI:            make(map[string][]Declaration),
		byName:           make(map[string][]declRef),
		occByName:        make(map[string]map[string][]Occurrence),
		occNamesByURI:    make(map[string][]string),
		dependsOn:        make(map[string][]string),
		errByURI:         make(map[string][]Diagnostic),
		importsByURI:     make(map[string][]importDecl),
		connectionsByURI: make(map[string][]connectionSite),
	}
}

// Diagnostics returns every preprocessing/parsing problem recorded during
// uri's last scan.
func (ix *Index) Diagnostics(uri string) []Diagnostic {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return append([]Diagnostic(nil), ix.errByURI[uri]...)
}

// SetIncludeResolverFactory configures how SetFile resolves `include
// directives: factory is called once per SetFile call to obtain a fresh
// resolver instance (required -- see ResolverFactory's doc comment on why
// sharing one instance across calls isn't safe). A nil factory (the
// default) means `include directives aren't resolved at all -- each
// becomes a recorded, non-fatal preprocessing error rather than pulling in
// another file's content.
func (ix *Index) SetIncludeResolverFactory(factory ResolverFactory) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.resolverFactory = factory
}

// SetInitialMacros configures the object-like macros (see Scan's doc
// comment) seeded into every subsequent SetFile call's preprocessing --
// the workspace's captured `+define+` entries. A nil map (the default)
// means no macros are seeded beyond whatever each file `defines itself.
func (ix *Index) SetInitialMacros(macros map[string]string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.initialMacros = macros
}

// SetFile rescans text for declarations and occurrences, replacing uri's
// prior entries in all three, and returns every URI touched (own key set
// of both declsByURI and diagsByURI -- they can differ, e.g. an `include d
// file that's entirely malformed has diagnostics but no declarations),
// which the caller uses to know which URIs need textDocument/
// publishDiagnostics republished.
//
// A declaration or diagnostic reached via `include may belong to a
// different file than uri (see Scan/declarationsFromAST) -- that file's
// own bucket is replaced too, but its occurrences and dependency-graph
// entry are left alone; those only ever come from that file's own direct
// SetFile call, if one happens (see internal/lspserver's watch/rebuild
// logic, which ensures one eventually does for every file discovered this
// way).
func (ix *Index) SetFile(uri string, text string) (touchedURIs []string) {
	ix.mu.RLock()
	factory := ix.resolverFactory
	macros := ix.initialMacros
	ix.mu.RUnlock()

	var resolver IncludeResolver
	if factory != nil {
		resolver = factory()
	}
	declsByURI, occs, diagsByURI, importsByURI, connectionsByURI := Scan(uri, text, resolver, macros)

	// Group occurrences by name outside the lock; occurrencesFromSVParseTokens
	// already interned each name to a single string per file.
	perName := make(map[string][]Occurrence)
	for _, o := range occs {
		perName[o.Name] = append(perName[o.Name], o)
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()

	touched := make(map[string]bool, len(declsByURI)+len(diagsByURI))
	for fileURI, decls := range declsByURI {
		touched[fileURI] = true
		ix.removeDeclarationsLocked(fileURI)
		ix.byURI[fileURI] = decls
		for i, d := range decls {
			ix.byName[d.Name] = append(ix.byName[d.Name], declRef{uri: fileURI, idx: i})
		}
	}
	for fileURI, diags := range diagsByURI {
		touched[fileURI] = true
		ix.errByURI[fileURI] = diags
	}
	for fileURI, imps := range importsByURI {
		touched[fileURI] = true
		ix.importsByURI[fileURI] = imps
	}
	for fileURI, conns := range connectionsByURI {
		touched[fileURI] = true
		ix.connectionsByURI[fileURI] = conns
	}
	touchedURIs = make([]string, 0, len(touched))
	for fileURI := range touched {
		touchedURIs = append(touchedURIs, fileURI)
	}

	ix.removeOccurrencesLocked(uri)
	names := make([]string, 0, len(perName))
	for name, list := range perName {
		bucket := ix.occByName[name]
		if bucket == nil {
			bucket = make(map[string][]Occurrence)
			ix.occByName[name] = bucket
		}
		bucket[uri] = list
		names = append(names, name)
	}
	ix.occNamesByURI[uri] = names

	var deps []string
	if resolver != nil {
		deps = resolver.Resolved()
	}
	ix.recordDependenciesLocked(uri, deps)

	return touchedURIs
}

// recordDependencies replaces uri's dependency set with deps (the paths an
// IncludeResolver reported resolving during uri's most recent scan) -- a
// no-op with an empty/nil deps for a scan that used none.
func (ix *Index) recordDependencies(uri string, deps []string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.recordDependenciesLocked(uri, deps)
}

func (ix *Index) recordDependenciesLocked(uri string, deps []string) {
	if len(deps) == 0 {
		delete(ix.dependsOn, uri)
		return
	}
	ix.dependsOn[uri] = append([]string(nil), deps...)
}

// Dependents returns every URI whose last scan `include d uri, directly or
// transitively (see dependsOn's doc comment for why one hop is always
// enough) -- the set that needs reindexing when uri's content changes.
func (ix *Index) Dependents(uri string) []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var out []string
	for w, deps := range ix.dependsOn {
		if slices.Contains(deps, uri) {
			out = append(out, w)
		}
	}
	return out
}

// IncludesOf is Dependents' forward counterpart: the URIs uri's own last
// scan resolved an `include to (already the full transitive set -- see
// dependsOn's doc comment). Used to find files reachable only via
// `include starting from a freshly-scanned top-level file, without
// consulting AllKnownURIs, which can still hold a stale entry from a
// file's *previous* scan until the next rebuild's staleness pass runs.
func (ix *Index) IncludesOf(uri string) []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return append([]string(nil), ix.dependsOn[uri]...)
}

// AllKnownURIs returns every URI the index currently holds declarations
// for -- including one discovered only via another file's `include (see
// Scan's file-attribution behavior), not just the workspace's own
// top-level file list. Used to widen file watching and staleness checks
// beyond what discovery alone finds.
func (ix *Index) AllKnownURIs() []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]string, 0, len(ix.byURI))
	for uri := range ix.byURI {
		out = append(out, uri)
	}
	return out
}

// RemoveFile drops uri's entries entirely.
func (ix *Index) RemoveFile(uri string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.removeLocked(uri)
}

// removeLocked drops uri's entries from every part of the index:
// declarations, occurrences, and its dependency-graph entry.
func (ix *Index) removeLocked(uri string) {
	ix.removeDeclarationsLocked(uri)
	ix.removeOccurrencesLocked(uri)
	delete(ix.dependsOn, uri)
	delete(ix.errByURI, uri)
	delete(ix.importsByURI, uri)
	delete(ix.connectionsByURI, uri)
}

// removeDeclarationsLocked drops uri's declaration entries only, leaving
// its occurrences and dependency-graph entry untouched -- used when
// rewriting just the declaration bucket for a file discovered as a side
// effect of scanning a different file's `include (see Scan/SetFile),
// which has no occurrences or dependency data of its own to touch here.
func (ix *Index) removeDeclarationsLocked(uri string) {
	// A file typically declares the same name only once, but prototypes
	// and their bodies (or plain duplicates) can repeat one -- filter each
	// name's global ref list once, not once per repeat.
	seen := make(map[string]bool)
	for _, d := range ix.byURI[uri] {
		if seen[d.Name] {
			continue
		}
		seen[d.Name] = true
		refs := ix.byName[d.Name]
		filtered := refs[:0]
		for _, r := range refs {
			if r.uri != uri {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) == 0 {
			delete(ix.byName, d.Name)
		} else {
			ix.byName[d.Name] = filtered
		}
	}
	delete(ix.byURI, uri)
}

func (ix *Index) removeOccurrencesLocked(uri string) {
	for _, name := range ix.occNamesByURI[uri] {
		if bucket := ix.occByName[name]; bucket != nil {
			delete(bucket, uri)
			if len(bucket) == 0 {
				delete(ix.occByName, name)
			}
		}
	}
	delete(ix.occNamesByURI, uri)
}

func (ix *Index) locationLocked(ref declRef) Location {
	d := ix.byURI[ref.uri][ref.idx]
	return Location{URI: ref.uri, Line: d.Line, Character: d.Character, Kind: d.Kind, Prototype: d.Prototype}
}

func (ix *Index) locationsLocked(refs []declRef) []Location {
	out := make([]Location, len(refs))
	for i, r := range refs {
		out[i] = ix.locationLocked(r)
	}
	return out
}

// Lookup returns every known location declaring name, with no scope
// awareness -- a flat, global search. FindDefinition is almost always the
// better choice for resolving a reference at a specific position; Lookup
// remains useful for tooling that just wants "does this name exist
// anywhere" (tests, future features like workspace symbol search).
func (ix *Index) Lookup(name string) ([]Location, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	refs, ok := ix.byName[name]
	if !ok {
		return nil, false
	}
	out := make([]Location, len(refs))
	for i, r := range refs {
		out[i] = ix.locationLocked(r)
	}
	return out, true
}

// FindDefinition resolves word at (uri, line, character) to its
// declaration(s). qualifier/hasQualifier come from QualifierAt -- pass
// hasQualifier=false when word wasn't preceded by "Something::".
//
// When the resolved result is entirely Prototype declarations (extern/pure
// virtual/DPI-import, no body -- see Declaration.Prototype), this also
// looks globally for a same-name, same-kind declaration that does have a
// body, and prefers that: "definition" should mean the real body when one
// is known, even though our lexical scanner doesn't track the
// class-qualified cross-reference between an extern prototype and its
// out-of-line "function Class::method(...); ... endfunction" body -- it
// only recognizes the prototype and the body as two same-named entries.
func (ix *Index) FindDefinition(uri string, line, character int, word, qualifier string, hasQualifier bool) ([]Location, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	refs, ok := ix.resolveRefsLocked(uri, line, character, word, qualifier, hasQualifier)
	if !ok {
		return nil, false
	}
	locs := ix.locationsLocked(refs)
	if preferred, ok := ix.preferGloballyLocked(word, locs, false); ok {
		return preferred, true
	}
	return locs, true
}

// FindDeclaration resolves word the same way FindDefinition does, but
// prefers a Prototype match when one exists for the resolved name/kind --
// e.g. jumping to a class's "extern function void bar();" line rather
// than its out-of-line body. When there's no prototype anywhere for that
// name/kind, it returns exactly what FindDefinition would, since for most
// SV constructs (module, typedef, in-body function/task, ...) there's no
// meaningful declaration/definition split at all.
func (ix *Index) FindDeclaration(uri string, line, character int, word, qualifier string, hasQualifier bool) ([]Location, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	refs, ok := ix.resolveRefsLocked(uri, line, character, word, qualifier, hasQualifier)
	if !ok {
		return nil, false
	}
	locs := ix.locationsLocked(refs)
	if preferred, ok := ix.preferGloballyLocked(word, locs, true); ok {
		return preferred, true
	}
	return locs, true
}

// HoverInfo resolves word the same way FindDefinition/FindDeclaration do --
// qualified / scope-chain / restricted global fallback, see
// resolveRefsLocked -- but returns the first match's full Declaration
// rather than a stripped-down Location, since hover wants real detail
// (Ports, Prototype, Kind) and only needs one good answer, not every
// location. It doesn't apply FindDefinition/FindDeclaration's
// prototype-vs-body preference; for a hover tooltip either is informative
// enough.
func (ix *Index) HoverInfo(uri string, line, character int, word, qualifier string, hasQualifier bool) (Declaration, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	refs, ok := ix.resolveRefsLocked(uri, line, character, word, qualifier, hasQualifier)
	if !ok || len(refs) == 0 {
		return Declaration{}, false
	}
	return ix.byURI[refs[0].uri][refs[0].idx], true
}

// preferGloballyLocked checks whether every location in locs already has
// Prototype == wantPrototype (nothing to do, in which case ok is false and
// the caller should just use locs as-is). Otherwise it searches globally
// for same-name declarations with the desired Prototype-ness, restricted
// to the same Kind as locs' entries (so, e.g., preferring a prototype
// never substitutes in an unrelated module of the same name).
func (ix *Index) preferGloballyLocked(word string, locs []Location, wantPrototype bool) ([]Location, bool) {
	if len(locs) == 0 {
		return nil, false
	}
	for _, l := range locs {
		if l.Prototype == wantPrototype {
			return nil, false // already what the caller wants; nothing to substitute
		}
	}

	kind := locs[0].Kind
	var out []Location
	for _, r := range ix.byName[word] {
		d := ix.byURI[r.uri][r.idx]
		if d.Kind == kind && d.Prototype == wantPrototype {
			out = append(out, ix.locationLocked(r))
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// resolveRefsLocked is the shared scope-aware resolution used by
// FindDefinition, FindDeclaration, and HoverInfo:
//  1. A qualified reference ("Pkg::name" or "Class::name") is resolved by
//     first finding the qualifier (searched globally among class/package
//     declarations), then searching only its direct children.
//  2. A click directly on a declaration's own name always resolves to
//     that declaration -- the trivial case, but not one the scope-chain
//     walk below covers on its own for a declaration with no enclosing
//     container (e.g. a lone file-scope typedef in a header with nothing
//     else in it: innermostContaining only ever finds a *container*
//     kind, so the walk never even starts).
//  3. Otherwise, an unqualified reference is resolved by walking outward
//     from the declaration enclosing the click position in the current
//     file (function -> class -> package, etc.), preferring the
//     innermost match -- the same shadowing order the SV LRM specifies.
//  4. If nothing in that chain matches, every package this scope can see
//     via an "import pkg::*;"/"import pkg::name;" statement (see
//     importsByURI, lookupInImportsRefsLocked) is searched next. An import
//     is a first-class SV scoping construct with real lexical nesting, so
//     it's checked before `include -- it composes with the same
//     container-ancestor chain step 3 already walks, rather than the
//     unscoped, file-wide visibility `include grants regardless of where
//     the `include line itself sits.
//  5. Still nothing? uri's own `include d files (see dependsOn) are
//     searched next, unrestricted by Kind -- unlike the global fallback
//     below, an `include is an explicit dependency the file itself
//     declared, so a typedef/parameter/anything else visible through it is
//     legitimately in scope, not a guess. This is what makes a type
//     declared in a shared header resolve from every file that includes
//     it.
//  6. Only then does it fall back to a global search restricted to
//     module/interface/program/class/package names, since those are the
//     kinds realistically referenceable by bare name from anywhere in the
//     workspace regardless of any `include; a bare cross-file function/
//     task/typedef match outside of 4/5 would usually be wrong without a
//     real elaborator to confirm reachability.
func (ix *Index) resolveRefsLocked(uri string, line, character int, word, qualifier string, hasQualifier bool) ([]declRef, bool) {
	if hasQualifier {
		return ix.lookupQualifiedRefsLocked(qualifier, word)
	}

	if refs, ok := ix.lookupSelfRefLocked(uri, line, character, word); ok {
		return refs, true
	}

	if refs, ok := ix.lookupInScopeRefsLocked(uri, line, character, word); ok {
		return refs, true
	}

	if refs, ok := ix.lookupInImportsRefsLocked(uri, line, character, word); ok {
		return refs, true
	}

	if refs, ok := ix.lookupInIncludesRefsLocked(uri, word); ok {
		return refs, true
	}

	var out []declRef
	for _, r := range ix.byName[word] {
		d := ix.byURI[r.uri][r.idx]
		if GloballyReferenceableKinds[d.Kind] {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// importVisibleAtLocked reports whether imp is visible from (line,
// character) in uri: a file-scope import (Parent == -1) is visible
// everywhere in the file -- approximating SV's real compilation-unit
// ($unit) import scoping, since this index has no cross-file compilation-
// unit model (see the package doc comment on "declaration-grade, not full
// elaboration"). A container-scoped import (a header or body import
// inside a module/interface/program/class/package) is visible within that
// container and its descendants only -- checked by walking (line,
// character)'s own container-ancestor chain, the same chain
// lookupInScopeRefsLocked walks for ordinary shadowing, and testing
// whether imp.Parent appears on it. Declaration order relative to the use
// site is not checked, consistent with every other scope lookup in this
// package treating a scope's members as a set, not a sequence.
func (ix *Index) importVisibleAtLocked(uri string, imp importDecl, line, character int) bool {
	if imp.Parent == -1 {
		return true
	}
	decls := ix.byURI[uri]
	idx := innermostContaining(decls, line, character)
	for idx != -1 {
		if idx == imp.Parent {
			return true
		}
		idx = decls[idx].Parent
	}
	return false
}

// lookupInImportsRefsLocked resolves word via every import visible at
// (uri, line, character) -- see importVisibleAtLocked. A specific
// ("import pkg::name;") import only grants visibility to that one name; a
// wildcard ("import pkg::*;") grants visibility to anything the package
// declares. Restricted to Kind == KindPackage (SV import syntax, LRM
// 26.3, is package-only, unlike a qualified Pkg::name/Class::name
// reference which also allows a class) -- defensive against a workspace
// where the imported identifier isn't actually a package, matching
// lookupQualifiedRefsLocked's own Kind check. Multiple visible imports
// whose package happens to declare the same word (e.g. two wildcard-
// imported packages both defining "foo") are deliberately NOT
// disambiguated -- every match is returned, the same "return every
// plausible candidate" behavior lookupQualifiedRefsLocked already has when
// a qualifier name is ambiguous across files. Picking one silently could
// easily be wrong; this index has no elaborator to confirm which one a
// real compile would actually bind.
func (ix *Index) lookupInImportsRefsLocked(uri string, line, character int, word string) ([]declRef, bool) {
	var out []declRef
	for _, imp := range ix.importsByURI[uri] {
		if imp.Member != "*" && imp.Member != word {
			continue
		}
		if !ix.importVisibleAtLocked(uri, imp, line, character) {
			continue
		}
		for _, qref := range ix.byName[imp.Package] {
			if ix.byURI[qref.uri][qref.idx].Kind != KindPackage {
				continue
			}
			out = append(out, ix.childRefsLocked(qref.uri, qref.idx, word)...)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// lookupInIncludesRefsLocked searches uri's own `include d files (direct
// or transitive -- dependsOn already holds the full set, see its doc
// comment) for word, unrestricted by Kind.
func (ix *Index) lookupInIncludesRefsLocked(uri, word string) ([]declRef, bool) {
	deps := ix.dependsOn[uri]
	if len(deps) == 0 {
		return nil, false
	}
	depSet := make(map[string]bool, len(deps))
	for _, d := range deps {
		depSet[d] = true
	}
	var out []declRef
	for _, r := range ix.byName[word] {
		if depSet[r.uri] {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// Ports returns the port list of a module/interface/program declaration
// named name, if one exists in the index (the first match wins if there
// happen to be duplicates, which would itself indicate a build problem
// elsewhere). Used for named-port-connection completion at an
// instantiation site.
func (ix *Index) Ports(name string) ([]Port, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for _, r := range ix.byName[name] {
		d := ix.byURI[r.uri][r.idx]
		if d.Kind == KindModule || d.Kind == KindInterface || d.Kind == KindProgram {
			return d.Ports, true
		}
	}
	return nil, false
}

// Params returns the overridable ("parameter", not "localparam") entries
// of a module/interface/program declaration's own "#( ... )" parameter
// port list, if one exists in the index. Used for parameter-override
// completion at an instantiation site, the "#(...)" counterpart to
// Ports/named-port-connection completion.
func (ix *Index) Params(name string) ([]Port, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for _, r := range ix.byName[name] {
		d := ix.byURI[r.uri][r.idx]
		if d.Kind == KindModule || d.Kind == KindInterface || d.Kind == KindProgram {
			return d.Params, true
		}
	}
	return nil, false
}

// StructFields returns the field list of a struct or union typedef named
// typeName, if one exists in the index (the first match wins if there
// happen to be duplicates, the same simplification Ports/Params already
// make). Used for struct-member completion after "receiver." once the
// receiver's own declared type name has been resolved (see Declaration.
// TypeName) -- typeName not matching anything at all (a builtin keyword,
// or a non-struct/union typedef) isn't a special case here, just an
// ordinary "not found".
func (ix *Index) StructFields(typeName string) ([]Port, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for _, r := range ix.byName[typeName] {
		d := ix.byURI[r.uri][r.idx]
		if d.Kind != KindTypedef {
			continue
		}
		switch d.TypedefKind {
		case "struct", "union":
			return d.Fields, true
		}
	}
	return nil, false
}

// Typedef returns the full Declaration of a typedef named name, if one
// exists in the index (first match wins, the same simplification Ports/
// Params/StructFields already make). Unlike StructFields, this isn't
// filtered to struct/union -- it's used by hover to render a variable's
// or port's declared type as a full expansion (e.g. showing a struct's
// member list, the same way hovering the typedef itself does) whenever
// that type turns out to be a typedef, whatever kind it is.
func (ix *Index) Typedef(name string) (Declaration, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for _, r := range ix.byName[name] {
		d := ix.byURI[r.uri][r.idx]
		if d.Kind == KindTypedef {
			return d, true
		}
	}
	return Declaration{}, false
}

// Occurrences returns every identifier occurrence of name across the
// whole index, unrestricted -- see Index's doc comment for why this is
// pre-built rather than scanned at request time. ScopedOccurrences is
// almost always the better choice when resolving a reference at a
// specific position; this is the unrestricted building block it falls
// back to when it can't confirm a scope-restriction is safe.
func (ix *Index) Occurrences(name string) []Location {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.occurrencesLocked(name)
}

func (ix *Index) occurrencesLocked(name string) []Location {
	bucket := ix.occByName[name]

	// Map iteration order is nondeterministic; sort the URIs so results
	// are stable across calls (occurrences within one file are already in
	// token order).
	uris := make([]string, 0, len(bucket))
	total := 0
	for uri, occs := range bucket {
		uris = append(uris, uri)
		total += len(occs)
	}
	sort.Strings(uris)

	out := make([]Location, 0, total)
	for _, uri := range uris {
		for _, occ := range bucket[uri] {
			out = append(out, Location{URI: uri, Line: occ.Line, Character: occ.Character})
		}
	}
	return out
}

// ScopedOccurrences resolves word at (uri, line, character) the same way
// FindDefinition does, then returns every occurrence of it that's
// realistically relevant to that specific declaration rather than every
// occurrence of the same text anywhere in the workspace:
//
//   - If word doesn't resolve to a known declaration at all, there's
//     nothing to restrict by -- return every occurrence of the raw text.
//   - If it resolves to a module/interface/program/class/package (see
//     GloballyReferenceableKinds), those are meant to be referenced from
//     anywhere, so the search stays workspace-wide.
//   - If it resolves to a function/task/typedef/enum member declared
//     directly inside a module/interface/program, the search is
//     restricted to that container's span in that one file: SV gives
//     module-internal declarations no qualified-external-access
//     mechanism, so this is a safe restriction, not just an
//     optimization -- a same-named helper in a different module cannot
//     be the same symbol.
//   - If it resolves to a port or parameter declared directly inside a
//     module/interface/program, the search is likewise restricted to that
//     container's span, UNION every named connection site anywhere in the
//     workspace that targets this specific (module, name) pair -- the
//     declaration-side mirror of
//     ScopedOccurrencesForInstantiationConnection, so starting a
//     rename/references query from the port/parameter's own declaration
//     finds the same instantiation sites that starting from one of those
//     connection sites already would. Without this, renaming a port from
//     its declaration silently leaves every named connection to it (e.g.
//     ".portName(" at another module's instantiation) stale.
//   - Otherwise (a class/package member, or a file-scope declaration
//     with no enclosing container) the search stays workspace-wide,
//     because such a symbol might legitimately be referenced from another
//     file via a Class::name/pkg::name qualifier, or (for a file-scope
//     declaration) via `include -- and this lexical index can't tell
//     those cases apart from "just doesn't happen elsewhere", so it
//     doesn't risk silently dropping a real reference.
func (ix *Index) ScopedOccurrences(uri string, line, character int, word, qualifier string, hasQualifier bool) []Location {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	refs, ok := ix.resolveRefsLocked(uri, line, character, word, qualifier, hasQualifier)
	if !ok || len(refs) == 0 {
		return ix.occurrencesLocked(word)
	}

	d := ix.byURI[refs[0].uri][refs[0].idx]
	container, restrict := ix.containerScopeLocked(refs[0].uri, d)
	if !restrict {
		return ix.occurrencesLocked(word)
	}

	var out []Location
	for _, occ := range ix.occByName[word][refs[0].uri] {
		if !posWithinBounds(occ.Line, occ.Character, container.Line, container.Character, container.EndLine, container.EndCharacter) {
			continue
		}
		out = append(out, Location{URI: refs[0].uri, Line: occ.Line, Character: occ.Character})
	}
	if d.Kind == KindPort || d.Kind == KindParameter {
		out = append(out, ix.connectionOccurrencesLocked(refs[0].uri, d.Parent, word, d.Kind)...)
	}
	return out
}

// containerScopeLocked returns d's enclosing container and whether it's
// safe to restrict an occurrence search to that container's span -- see
// ScopedOccurrences for the reasoning.
func (ix *Index) containerScopeLocked(uri string, d Declaration) (Declaration, bool) {
	if GloballyReferenceableKinds[d.Kind] || d.Parent == -1 {
		return Declaration{}, false
	}
	parent := ix.byURI[uri][d.Parent]
	if parent.Kind != KindModule && parent.Kind != KindInterface && parent.Kind != KindProgram {
		return Declaration{}, false
	}
	return parent, true
}

// connectionOccurrencesLocked returns every named-connection site
// (".name(" or its ".name" implicit-shorthand form, or a "#(.name(...))"
// parameter override) anywhere in the workspace that targets
// containerURI/containerIdx specifically under name -- i.e. every OTHER
// instantiation of the same module/interface/program connecting the same
// port/parameter by name, not just any same-named token. ModuleType is
// matched by resolving it exactly like lookupInstantiationPortRefsLocked's
// own container search, so a same-named-but-different module (e.g. an
// unrelated "leaf2" that also happens to have a "clk" port) is correctly
// excluded.
func (ix *Index) connectionOccurrencesLocked(containerURI string, containerIdx int, name string, kind Kind) []Location {
	var out []Location
	for uri, sites := range ix.connectionsByURI {
		for _, site := range sites {
			if site.Name != name {
				continue
			}
			for _, qref := range ix.byName[site.ModuleType] {
				if qref.uri == containerURI && qref.idx == containerIdx {
					out = append(out, Location{URI: uri, Line: site.Line, Character: site.Character, Kind: kind})
					break
				}
			}
		}
	}
	return out
}

// ScopedOccurrencesForInstantiationConnection is ScopedOccurrences'
// counterpart for a named port connection or parameter override at an
// instantiation site (".clk(" or "#(.WIDTH(...))" -- see
// FindInstantiationPort's doc comment for why this needs a separate
// resolution path at all: the connection site has no scope-chain link to
// the instantiated module, so ScopedOccurrences/containerScopeLocked's
// restriction -- which only ever looks INSIDE the declaring module's own
// span -- would either miss every other connection site entirely (they
// live in whichever file instantiates the module, not the module's own
// file) or, without this, fall through to an entirely unscoped raw-text
// search that conflates every same-named token workspace-wide, including
// an unrelated module's own same-named port (e.g. "leaf2"'s own "clk"
// port, a completely different declaration that just happens to share
// the name).
//
// Returns every occurrence of name within moduleName's own declaration
// span (its port-list/param-list site and any in-body reference), UNION
// every OTHER named connection site anywhere in the workspace that
// targets the same (moduleName, name) pair specifically.
func (ix *Index) ScopedOccurrencesForInstantiationConnection(moduleName, name string) []Location {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	refs, ok := ix.lookupInstantiationPortRefsLocked(moduleName, name)
	if !ok {
		return nil
	}

	var out []Location
	for _, ref := range refs {
		d := ix.byURI[ref.uri][ref.idx]
		if container, restrict := ix.containerScopeLocked(ref.uri, d); restrict {
			for _, occ := range ix.occByName[name][ref.uri] {
				if !posWithinBounds(occ.Line, occ.Character, container.Line, container.Character, container.EndLine, container.EndCharacter) {
					continue
				}
				out = append(out, Location{URI: ref.uri, Line: occ.Line, Character: occ.Character, Kind: d.Kind})
			}
		}
		out = append(out, ix.connectionOccurrencesLocked(ref.uri, d.Parent, name, d.Kind)...)
	}
	return out
}

// CompleteSymbols returns every distinct declared name starting with
// prefix, across the whole index, for general identifier completion
// (types, functions, tasks, classes, packages, enum members, ...). It's
// deliberately not scope-restricted the way FindDefinition is: every
// matching name anywhere in the workspace is a candidate, since narrowing
// that down correctly would need the same reachability information a real
// elaborator has and this lexical index doesn't. A name that happens to
// be declared multiple times (e.g. a prototype and its out-of-line body)
// is only returned once.
func (ix *Index) CompleteSymbols(prefix string) []Symbol {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	var out []Symbol
	for name, refs := range ix.byName {
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		if len(refs) == 0 {
			continue
		}
		d := ix.byURI[refs[0].uri][refs[0].idx]
		out = append(out, Symbol{Name: name, Kind: d.Kind})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// childRefsLocked returns every declRef in uri's bucket that's a direct
// child of the declaration at containerIdx and named name -- the shared
// primitive behind both qualified (Pkg::name) lookup and import-based
// (wildcard/specific) resolution.
func (ix *Index) childRefsLocked(uri string, containerIdx int, name string) []declRef {
	var out []declRef
	for i, d := range ix.byURI[uri] {
		if d.Parent == containerIdx && d.Name == name {
			out = append(out, declRef{uri: uri, idx: i})
		}
	}
	return out
}

// lookupQualifiedRefsLocked resolves qualifier::name. qualifier is
// whatever QualifierAt returns -- the single identifier immediately
// before the final "::" -- so for a doubly-scoped reference like
// "pa_pkg::en_States::S_BAZ" it's always "en_States", never "pa_pkg";
// QualifierAt has no notion of a longer chain, and doesn't need one, by
// construction of the KindTypedef case below.
func (ix *Index) lookupQualifiedRefsLocked(qualifier, name string) ([]declRef, bool) {
	var out []declRef
	for _, qref := range ix.byName[qualifier] {
		qd := ix.byURI[qref.uri][qref.idx]
		switch qd.Kind {
		case KindClass, KindPackage:
			out = append(out, ix.childRefsLocked(qref.uri, qref.idx, name)...)
		case KindTypedef:
			// An enum type name is the one case SV lets a typedef stand as
			// a "::" qualifier (LRM 6.19.9's "MyEnum::MEMBER", optionally
			// itself further prefixed "pkg::MyEnum::MEMBER" -- collapsed to
			// just "MyEnum" here per this function's own doc comment).
			// Enum members are recorded parented to the typedef's OWN
			// parent, not the typedef itself (see addDecl's comment on
			// *ast.Typedef), so the search targets qd.Parent, restricted to
			// KindEnumMember to avoid matching an unrelated same-named
			// sibling declared in the same enclosing scope (e.g. a
			// localparam that happens to share the enum member's name).
			for _, ref := range ix.childRefsLocked(qref.uri, qd.Parent, name) {
				if ix.byURI[ref.uri][ref.idx].Kind == KindEnumMember {
					out = append(out, ref)
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// lookupInstantiationPortRefsLocked resolves portName against any of
// moduleName's own children named portName -- not Kind-restricted, so it
// resolves a KindPort child (a named port connection) exactly as well as
// a KindParameter one (a parameter override), whichever moduleName
// actually declares under that name. The primitive behind
// FindInstantiationPort/InstantiationPortInfo, mirroring
// lookupQualifiedRefsLocked's "find container by name globally, then its
// children" shape, but keyed by an instantiation's implicit module-name
// context (see InstantiationPortNameAt/InstantiationParamNameAt) rather
// than an explicit "::" qualifier.
func (ix *Index) lookupInstantiationPortRefsLocked(moduleName, portName string) ([]declRef, bool) {
	var out []declRef
	for _, qref := range ix.byName[moduleName] {
		qd := ix.byURI[qref.uri][qref.idx]
		if qd.Kind != KindModule && qd.Kind != KindInterface && qd.Kind != KindProgram {
			continue
		}
		out = append(out, ix.childRefsLocked(qref.uri, qref.idx, portName)...)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// FindInstantiationPort resolves portName against moduleName's own
// declarations -- a named port connection's port name (".clk(" at an
// instantiation site resolves to leaf's own "clk" port declaration) or,
// identically, a parameter override's name (".WIDTH(" resolves to
// leaf's own "WIDTH" parameter declaration, see
// InstantiationParamNameAt) -- used for go-to-definition/declaration on
// either kind of connection site, neither of which has any scope-chain
// link to the instantiated module at all (the connection site's
// enclosing scope is the instantiating module, not the instantiated
// one). No meaningful declaration/definition split exists for a port or
// parameter, so this single method backs both handlers for both kinds
// of connection.
func (ix *Index) FindInstantiationPort(moduleName, portName string) ([]Location, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	refs, ok := ix.lookupInstantiationPortRefsLocked(moduleName, portName)
	if !ok {
		return nil, false
	}
	return ix.locationsLocked(refs), true
}

// InstantiationPortInfo is FindInstantiationPort's hover counterpart
// (same dual port-connection/parameter-override reuse -- see its doc
// comment), mirroring HoverInfo's shape (the first match's full
// Declaration, not just a Location -- hover wants the real Kind/Detail).
func (ix *Index) InstantiationPortInfo(moduleName, portName string) (Declaration, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	refs, ok := ix.lookupInstantiationPortRefsLocked(moduleName, portName)
	if !ok {
		return Declaration{}, false
	}
	return ix.byURI[refs[0].uri][refs[0].idx], true
}

// lookupSelfRefLocked reports whether (line, character) falls directly
// within some declaration in uri named word -- resolving a click on a
// declaration's own name to itself. Deliberately separate from (and
// checked before) lookupInScopeRefsLocked's walk, which starts from
// innermostContaining and so only ever finds a match by looking for a
// *child* of some enclosing container -- it has no path to a match for a
// declaration that has no enclosing container of its own at all.
func (ix *Index) lookupSelfRefLocked(uri string, line, character int, word string) ([]declRef, bool) {
	for i, d := range ix.byURI[uri] {
		if d.Name == word && posWithin(d, line, character) {
			return []declRef{{uri: uri, idx: i}}, true
		}
	}
	return nil, false
}

func (ix *Index) lookupInScopeRefsLocked(uri string, line, character int, name string) ([]declRef, bool) {
	decls := ix.byURI[uri]
	idx := innermostContaining(decls, line, character)
	for idx != -1 {
		var out []declRef
		for i, d := range decls {
			if d.Parent == idx && d.Name == name {
				out = append(out, declRef{uri: uri, idx: i})
			}
		}
		if len(out) > 0 {
			return out, true
		}
		idx = decls[idx].Parent
	}
	return nil, false
}

// innermostContaining returns the index of the smallest container
// declaration whose span contains (line, character), or -1 if none does.
func innermostContaining(decls []Declaration, line, character int) int {
	best := -1
	for i, d := range decls {
		if !containerKinds[d.Kind] {
			continue
		}
		if !posWithin(d, line, character) {
			continue
		}
		if best == -1 || narrower(d, decls[best]) {
			best = i
		}
	}
	return best
}

func posWithin(d Declaration, line, character int) bool {
	return posWithinBounds(line, character, d.Line, d.Character, d.EndLine, d.EndCharacter)
}

func posWithinBounds(line, character, startLine, startChar, endLine, endChar int) bool {
	if before(line, character, startLine, startChar) {
		return false
	}
	return before(line, character, endLine, endChar)
}

// narrower reports whether a's span is nested within (no wider than) b's.
func narrower(a, b Declaration) bool {
	return !before(a.Line, a.Character, b.Line, b.Character) &&
		!before(b.EndLine, b.EndCharacter, a.EndLine, a.EndCharacter)
}

func before(l1, c1, l2, c2 int) bool {
	if l1 != l2 {
		return l1 < l2
	}
	return c1 < c2
}

// FileCount reports how many files currently have entries in the index.
func (ix *Index) FileCount() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.byURI)
}

// FileDeclarations returns a copy of uri's declarations, in the same order
// and with the same Parent indices ScanDeclarations produced, for building
// a per-file symbol tree (documentSymbol) or filtering by kind
// (foldingRange).
func (ix *Index) FileDeclarations(uri string) []Declaration {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	decls := ix.byURI[uri]
	out := make([]Declaration, len(decls))
	copy(out, decls)
	return out
}

// IsContainer reports whether kind can hold other declarations, and so
// participates in the scope chain and gets its own folding range.
func IsContainer(kind Kind) bool {
	return containerKinds[kind]
}

// WorkspaceSymbols returns every declaration whose name contains query
// (case-insensitive substring match -- deliberately more permissive than
// CompleteSymbols' prefix match, since this backs an explicit "jump to
// symbol" search box rather than as-you-type completion, and a picker
// user typing "axi" expects to find AXI_master), across the whole
// workspace. Unlike CompleteSymbols, every declaration site is returned,
// not deduplicated by name.
func (ix *Index) WorkspaceSymbols(query string) []SymbolLocation {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	query = strings.ToLower(query)
	var out []SymbolLocation
	for name, refs := range ix.byName {
		if query != "" && !strings.Contains(strings.ToLower(name), query) {
			continue
		}
		for _, r := range refs {
			d := ix.byURI[r.uri][r.idx]
			out = append(out, SymbolLocation{Name: name, Kind: d.Kind, URI: r.uri, Line: d.Line, Character: d.Character})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].URI < out[j].URI
	})
	return out
}

// FindOccurrences lexes text and returns every identifier-like token equal
// to name. Comments and string literals are already skipped by the lexer,
// so this won't match text that merely looks like the name inside one.
// It's a standalone, stateless utility (mainly useful for testing
// occurrence-finding in isolation) -- Index.SetFile builds the same
// information via Scan and keeps it up to date incrementally, and
// Index.ScopedOccurrences is what actually backs
// references/rename/documentHighlight; see Index's doc comment for why
// that's the better choice at request time.
func FindOccurrences(text string, name string) []Occurrence {
	if name == "" {
		return nil
	}
	lexToks, _ := lexer.Lex(text)
	var out []Occurrence
	for _, occ := range occurrencesFromSVParseTokens(lexToks) {
		if occ.Name == name {
			out = append(out, occ)
		}
	}
	return out
}
