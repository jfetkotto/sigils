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
	Name string
	// Receiver is the identifier this occurrence was written as a field of
	// ("st_bundle" for the "ckSideband" in "st_bundle.ckSideband"), "" when
	// the occurrence isn't a "<ident>.<Name>" access at all. Recorded at
	// scan time rather than re-derived per query because the index keeps no
	// document text: filtering a workspace-wide candidate list by receiver
	// would otherwise mean re-reading every candidate's file (see
	// ScopedOccurrencesForStructField). Interned alongside Name, so a field
	// accessed hundreds of times off one receiver costs one string.
	Receiver  string
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

// ownedLink is a memberLink plus the URI whose scan recorded it -- the
// same header included by two different files contributes one link from
// each, and rescanning one of them must retract only its own.
type ownedLink struct {
	memberLink
	owner string
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

	// dependedOnBy is dependsOn inverted: which files `include each URI.
	// Dependents used to answer that by scanning the whole graph, and the
	// watcher calls it once per changed file, so saving one widely
	// included header walked every file's (already transitive, so long)
	// dependency slice.
	dependedOnBy map[string]map[string]bool

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

	// connByName[name][uri] holds every connection site named name in uri,
	// and connNamesByURI[uri] the distinct names uri contributes -- the
	// same two-map shape occByName/occNamesByURI use, for the same reason.
	//
	// connectionOccurrencesLocked used to iterate connectionsByURI in full,
	// i.e. every named connection in the workspace, and it runs from
	// ScopedOccurrences whenever the resolved declaration is a port or
	// parameter -- so on essentially every documentHighlight inside a
	// module body. A design with 5,000 instantiations averaging 20 named
	// connections is 100k iterations per cursor move.
	connByName     map[string]map[string][]connectionSite
	connNamesByURI map[string][]string

	// memberLinksByOwner[uri] holds every cross-`include container
	// membership uri's own last scan recorded (see memberLink), keyed by
	// the scanning file because that's the unit of invalidation: a rescan
	// of uri must retract exactly the links that scan contributed, and no
	// others.
	//
	// membersOf and containerOf are the two query directions derived from
	// it, maintained together in recordMemberLinksLocked. membersOf
	// answers "which files hold this container's included members" (for
	// childRefsLocked, so Pkg::name and import Pkg::* reach them);
	// containerOf answers the inverse, "which container is this file's
	// content a member of" (for lookupInEnclosingContainersRefsLocked, so
	// a reference written inside the header can see the rest of its own
	// package).
	memberLinksByOwner map[string][]memberLink
	membersOf          map[declRef][]ownedLink
	containerOf        map[string][]ownedLink

	// contributedTo[owner] lists every URI owner's last scan wrote a
	// bucket for -- itself, plus every file it reached through an
	// `include. contributorsOf is the inverse: which owners currently
	// back each URI's buckets.
	//
	// Without this pair, SetFile could only ever write the URIs the
	// CURRENT scan produced, so a file that dropped out of a scan (an
	// `include line deleted, a conditional now excluding it) kept its
	// declarations and its diagnostics forever, and was absent from
	// touchedURIs so the server never even got the chance to clear them.
	// Same owner-keyed retraction shape as memberLinksByOwner above, for
	// the same reason: a rescan must retract exactly what that scan
	// contributed and nothing another file still backs.
	contributedTo  map[string][]string
	contributorsOf map[string]map[string]bool

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
		dependedOnBy:     make(map[string]map[string]bool),
		errByURI:         make(map[string][]Diagnostic),
		importsByURI:     make(map[string][]importDecl),
		connectionsByURI: make(map[string][]connectionSite),
		connByName:       make(map[string]map[string][]connectionSite),
		connNamesByURI:   make(map[string][]string),

		memberLinksByOwner: make(map[string][]memberLink),
		contributedTo:      make(map[string][]string),
		contributorsOf:     make(map[string]map[string]bool),
		membersOf:          make(map[declRef][]ownedLink),
		containerOf:        make(map[string][]ownedLink),
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
	declsByURI, occs, diagsByURI, importsByURI, connectionsByURI, memberLinks := Scan(uri, text, resolver, macros)

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
		ix.indexConnectionsLocked(fileURI, conns)
	}
	// Retract whatever the PREVIOUS scan of uri backed and this one no
	// longer does -- a header whose `include line was just deleted, say.
	// Those URIs are touched too, so their now-stale diagnostics get
	// republished (as an empty list) rather than sitting in the editor
	// forever.
	for _, cleared := range ix.recordContributionsLocked(uri, touched) {
		touched[cleared] = true
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

	ix.recordMemberLinksLocked(uri, memberLinks)

	var deps []string
	if resolver != nil {
		deps = resolver.Resolved()
	}
	ix.recordDependenciesLocked(uri, deps)

	return touchedURIs
}

// recordDependenciesLocked replaces uri's dependency set with deps (the
// paths an IncludeResolver reported resolving during uri's most recent
// scan) -- clearing the entry entirely for a scan that resolved none.
func (ix *Index) recordDependenciesLocked(uri string, deps []string) {
	for _, prev := range ix.dependsOn[uri] {
		if backers := ix.dependedOnBy[prev]; backers != nil {
			delete(backers, uri)
			if len(backers) == 0 {
				delete(ix.dependedOnBy, prev)
			}
		}
	}
	if len(deps) == 0 {
		delete(ix.dependsOn, uri)
		return
	}
	ix.dependsOn[uri] = append([]string(nil), deps...)
	for _, dep := range deps {
		backers := ix.dependedOnBy[dep]
		if backers == nil {
			backers = make(map[string]bool)
			ix.dependedOnBy[dep] = backers
		}
		backers[uri] = true
	}
}

// recordMemberLinksLocked replaces owner's contribution to the cross-
// `include membership maps with links, retracting whatever its previous
// scan contributed first. Retraction is bounded by owner's own link count
// rather than the workspace's, since memberLinksByOwner already names the
// exact keys to revisit -- this runs on every keystroke, via SetFile.
//
// Entries from *other* owners are deliberately left in place: two files
// including the same header into the same container each record the link,
// and one of them being rescanned (or losing its `include) says nothing
// about the other.
// recordContributionsLocked records that owner's latest scan backs exactly
// the URIs in produced, and retracts whatever its previous scan backed and
// this one does not. A URI left with no contributor at all has its buckets
// cleared and is returned, so the caller can republish (an empty
// diagnostic list clears the stale one in the editor).
func (ix *Index) recordContributionsLocked(owner string, produced map[string]bool) []string {
	var dropped []string
	for _, prev := range ix.contributedTo[owner] {
		if produced[prev] {
			continue
		}
		backers := ix.contributorsOf[prev]
		delete(backers, owner)
		if len(backers) > 0 {
			continue
		}
		delete(ix.contributorsOf, prev)
		// Nothing in the workspace reaches this file any more. Clearing
		// the declarations matters as much as the diagnostics: a name that
		// arrived through an `include this file no longer has must stop
		// resolving, or goto-definition keeps opening a header the build
		// no longer reads.
		ix.removeDeclarationsLocked(prev)
		delete(ix.byURI, prev)
		delete(ix.errByURI, prev)
		delete(ix.importsByURI, prev)
		delete(ix.connectionsByURI, prev)
		dropped = append(dropped, prev)
	}

	if len(produced) == 0 {
		delete(ix.contributedTo, owner)
		return dropped
	}
	now := make([]string, 0, len(produced))
	for u := range produced {
		now = append(now, u)
		backers := ix.contributorsOf[u]
		if backers == nil {
			backers = make(map[string]bool)
			ix.contributorsOf[u] = backers
		}
		backers[owner] = true
	}
	sort.Strings(now) // stable storage order; the set semantics don't depend on it
	ix.contributedTo[owner] = now
	return dropped
}

func (ix *Index) recordMemberLinksLocked(owner string, links []memberLink) {
	for _, l := range ix.memberLinksByOwner[owner] {
		key := declRef{uri: l.ContainerURI, idx: l.ContainerIdx}
		if kept := dropOwner(ix.membersOf[key], owner); len(kept) > 0 {
			ix.membersOf[key] = kept
		} else {
			delete(ix.membersOf, key)
		}
		if kept := dropOwner(ix.containerOf[l.IncludedURI], owner); len(kept) > 0 {
			ix.containerOf[l.IncludedURI] = kept
		} else {
			delete(ix.containerOf, l.IncludedURI)
		}
	}

	if len(links) == 0 {
		delete(ix.memberLinksByOwner, owner)
		return
	}
	ix.memberLinksByOwner[owner] = links
	for _, l := range links {
		key := declRef{uri: l.ContainerURI, idx: l.ContainerIdx}
		ix.membersOf[key] = append(ix.membersOf[key], ownedLink{memberLink: l, owner: owner})
		ix.containerOf[l.IncludedURI] = append(ix.containerOf[l.IncludedURI], ownedLink{memberLink: l, owner: owner})
	}
}

func dropOwner(links []ownedLink, owner string) []ownedLink {
	out := links[:0:0]
	for _, l := range links {
		if l.owner != owner {
			out = append(out, l)
		}
	}
	return out
}

// containerStillNamedLocked reports whether (uri, idx) still denotes the
// declaration a memberLink was recorded against. A container's bucket can
// be rewritten, and its indices shifted, by a different file's scan (see
// SetFile), so a link is only trusted while the name still matches --
// otherwise stale membership would attach a header's declarations to
// whatever now sits at that index.
func (ix *Index) containerStillNamedLocked(uri string, idx int, name string) bool {
	decls := ix.byURI[uri]
	return idx >= 0 && idx < len(decls) && decls[idx].Name == name
}

// Dependents returns every URI whose last scan `include d uri, directly or
// transitively (see dependsOn's doc comment for why one hop is always
// enough) -- the set that needs reindexing when uri's content changes.
func (ix *Index) Dependents(uri string) []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var out []string
	for w := range ix.dependedOnBy[uri] {
		out = append(out, w)
	}
	sort.Strings(out) // map order is nondeterministic; keep results stable
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

// RemoveFile drops uri's entries entirely, returning every OTHER URI whose
// entries went with it -- a header uri was the last file to `include. Like
// SetFile's return value, those need republishing so their diagnostics
// clear.
func (ix *Index) RemoveFile(uri string) []string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return append(ix.removeLocked(uri), uri)
}

// removeLocked drops uri's entries from every part of the index:
// declarations, occurrences, and its dependency-graph entry. Its
// cross-`include membership links go too -- but only the ones uri's own
// scan recorded (see recordMemberLinksLocked); links recorded by another
// file that happens to `include uri belong to that file, and it may well
// still exist.
func (ix *Index) removeLocked(uri string) []string {
	// Retract what uri's own scan backed elsewhere before clearing uri
	// itself, so an included header nothing else reaches goes too.
	cleared := ix.recordContributionsLocked(uri, nil)
	// uri may still be backed by another file that `include s it, but an
	// explicit removal means gone: drop its own buckets unconditionally
	// and stop counting it as its own backer.
	if backers := ix.contributorsOf[uri]; backers != nil {
		delete(backers, uri)
		if len(backers) == 0 {
			delete(ix.contributorsOf, uri)
		}
	}
	ix.removeDeclarationsLocked(uri)
	ix.removeOccurrencesLocked(uri)
	ix.removeConnectionsLocked(uri)
	ix.recordMemberLinksLocked(uri, nil)
	ix.recordDependenciesLocked(uri, nil)
	delete(ix.byURI, uri)
	delete(ix.errByURI, uri)
	delete(ix.importsByURI, uri)
	delete(ix.connectionsByURI, uri)
	return cleared
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

// indexConnectionsLocked rebuilds uri's slice of connByName, retracting
// whatever its previous scan contributed first.
func (ix *Index) indexConnectionsLocked(uri string, conns []connectionSite) {
	ix.removeConnectionsLocked(uri)
	if len(conns) == 0 {
		return
	}
	byName := make(map[string][]connectionSite)
	for _, site := range conns {
		byName[site.Name] = append(byName[site.Name], site)
	}
	names := make([]string, 0, len(byName))
	for name, sites := range byName {
		names = append(names, name)
		bucket := ix.connByName[name]
		if bucket == nil {
			bucket = make(map[string][]connectionSite)
			ix.connByName[name] = bucket
		}
		bucket[uri] = sites
	}
	ix.connNamesByURI[uri] = names
}

func (ix *Index) removeConnectionsLocked(uri string) {
	for _, name := range ix.connNamesByURI[uri] {
		if bucket := ix.connByName[name]; bucket != nil {
			delete(bucket, uri)
			if len(bucket) == 0 {
				delete(ix.connByName, name)
			}
		}
	}
	delete(ix.connNamesByURI, uri)
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
	if preferred, ok := ix.preferGloballyLocked(word, refs, locs, false); ok {
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
	if preferred, ok := ix.preferGloballyLocked(word, refs, locs, true); ok {
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
	ref := ix.primaryRefLocked(refs)
	return ix.byURI[ref.uri][ref.idx], true
}

// preferGloballyLocked checks whether every location in locs already has
// Prototype == wantPrototype (nothing to do, in which case ok is false and
// the caller should just use locs as-is). Otherwise it searches globally
// for same-name declarations with the desired Prototype-ness, restricted
// to the same Kind as locs' entries (so, e.g., preferring a prototype
// never substitutes in an unrelated module of the same name).
func (ix *Index) preferGloballyLocked(word string, refs []declRef, locs []Location, wantPrototype bool) ([]Location, bool) {
	if len(locs) == 0 {
		return nil, false
	}
	wantContainer := ix.containerNameOfLocked(refs[0])
	for _, l := range locs {
		if l.Prototype == wantPrototype {
			return nil, false // already what the caller wants; nothing to substitute
		}
	}

	kind := locs[0].Kind
	var sameFile, sameContainer, anywhere []Location
	for _, r := range ix.byName[word] {
		d := ix.byURI[r.uri][r.idx]
		if d.Kind != kind || d.Prototype != wantPrototype {
			continue
		}
		loc := ix.locationLocked(r)
		container := ix.containerNameOfLocked(r)
		switch {
		case r.uri == locs[0].URI:
			sameFile = append(sameFile, loc)
		case container != "" && container == wantContainer:
			sameContainer = append(sameContainer, loc)
		default:
			anywhere = append(anywhere, loc)
		}
	}
	// Ranked, not merged. The filter is name + Kind + Prototype only, with
	// no scope anywhere in it, so in UVM-style code -- where hundreds of
	// classes each define build_phase, run_phase, do_copy and new -- the
	// unranked set is hundreds of locations of which at most one is right.
	// Preferring the resolved declaration's own file, then its own
	// enclosing container, keeps the common case exact; falling back to
	// the whole workspace preserves the cross-file "extern prototype here,
	// out-of-line body there" case this exists for in the first place.
	for _, tier := range [][]Location{sameFile, sameContainer, anywhere} {
		if len(tier) > 0 {
			return tier, true
		}
	}
	return nil, false
}

// containerNameOfLocked returns the name of the declaration enclosing r,
// or "" if r is at file scope.
func (ix *Index) containerNameOfLocked(r declRef) string {
	decls := ix.byURI[r.uri]
	if parent := decls[r.idx].Parent; parent != -1 {
		return decls[parent].Name
	}
	return ""
}

// isContainerKind reports whether d is a module/interface/program, the
// three kinds that carry a port and parameter list.
func isContainerKind(d Declaration) bool {
	return d.Kind == KindModule || d.Kind == KindInterface || d.Kind == KindProgram
}

// appendUniqueRefs appends refs to out, skipping any already present.
//
// The same declaration legitimately arrives twice: "import pkg::*;" at
// file scope AND in a module header (a common belt-and-braces pattern)
// both resolve to it, and returning it twice makes an editor render two
// identical entries in its peek list. The three sibling lookups
// (includedImportRefsLocked, childRefsLocked,
// lookupInEnclosingContainersRefsLocked) already guarded against this
// individually; this is the shared form.
func appendUniqueRefs(out []declRef, refs ...declRef) []declRef {
	for _, r := range refs {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// firstDeclLocked returns the deterministically-first declaration named
// name that keep accepts -- lowest (URI, line, character), not whichever
// the scan happened to index first. Shared by every "look this name up and
// take one" accessor (Ports, Params, Typedef, structTypedefLocked), which
// each used to stop at byName's first match and so answered differently
// depending on the order files were indexed in. See primaryRefLocked.
func (ix *Index) firstDeclLocked(name string, keep func(Declaration) bool) (declRef, Declaration, bool) {
	var matches []declRef
	for _, r := range ix.byName[name] {
		if keep(ix.byURI[r.uri][r.idx]) {
			matches = append(matches, r)
		}
	}
	if len(matches) == 0 {
		return declRef{}, Declaration{}, false
	}
	ref := ix.primaryRefLocked(matches)
	return ref, ix.byURI[ref.uri][ref.idx], true
}

// primaryRefLocked picks the one declaration a single-answer query should
// report, deterministically: lowest (URI, line, character) rather than
// whichever happened to be indexed first.
//
// refs order comes from byName, which is append order across SetFile
// calls, and the indexing worker pool runs those in nondeterministic file
// order. So with two packages each declaring cfg_t, hover showed one
// today and the other after a restart, with no source change. Same
// ordering discipline WorkspaceSymbols already applies for the same
// reason -- see its topK comparator.
func (ix *Index) primaryRefLocked(refs []declRef) declRef {
	best := refs[0]
	bestDecl := ix.byURI[best.uri][best.idx]
	for _, r := range refs[1:] {
		d := ix.byURI[r.uri][r.idx]
		switch {
		case r.uri != best.uri:
			if r.uri > best.uri {
				continue
			}
		case d.Line != bestDecl.Line:
			if d.Line > bestDecl.Line {
				continue
			}
		case d.Character >= bestDecl.Character:
			continue
		}
		best, bestDecl = r, d
	}
	return best
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
//     innermost match -- the same shadowing order the SV LRM specifies --
//     and finally that file's own file scope.
//  4. If nothing in that chain matches, and uri's content was itself
//     `include d into some container's body, that container's scope is
//     searched (see lookupInEnclosingContainersRefsLocked): a declaration
//     written in a header pulled into a package body is lexically inside
//     that package, so the package's other members are in scope for it.
//     It comes before the import and `include steps below for the same
//     reason step 3 does -- it's the rest of this reference's own
//     enclosing scope, not a name some other file made visible.
//  5. Then every package this scope can see via an "import pkg::*;"/
//     "import pkg::name;" statement (see importsByURI,
//     lookupInImportsRefsLocked) is searched -- those written in this
//     file first, then those it picked up from an `include d header. An
//     import is a first-class SV scoping construct with real lexical
//     nesting, so it's checked before `include -- it composes with the
//     same container-ancestor chain step 3 already walks, rather than the
//     unscoped, file-wide visibility `include grants regardless of where
//     the `include line itself sits.
//  6. Still nothing? uri's own `include d files (see dependsOn) are
//     searched next, unrestricted by Kind -- unlike the global fallback
//     below, an `include is an explicit dependency the file itself
//     declared, so a typedef/parameter/anything else visible through it is
//     legitimately in scope, not a guess. This is what makes a type
//     declared in a shared header resolve from every file that includes
//     it.
//  7. Only then does it fall back to a global search restricted to
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

	if refs, ok := ix.lookupInEnclosingContainersRefsLocked(uri, word); ok {
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
func (ix *Index) importVisibleAtLocked(imp importDecl, ancestors map[int]bool) bool {
	return imp.Parent == -1 || ancestors[imp.Parent]
}

// ancestorScopesLocked returns every container index enclosing (line,
// character) in uri.
//
// Computed once per request rather than per import: innermostContaining is
// an O(declarations-in-file) scan and the chain walk another, and
// importVisibleAtLocked used to redo both for every import statement in
// the file. A UVM-style file with 30 imports and 3,000 declarations did
// that work 30 times over for one identical answer.
func (ix *Index) ancestorScopesLocked(uri string, line, character int) map[int]bool {
	decls := ix.byURI[uri]
	out := make(map[int]bool)
	for idx := innermostContaining(decls, line, character); idx != -1; idx = decls[idx].Parent {
		out[idx] = true
	}
	return out
}

// lookupInImportsRefsLocked resolves word via every import visible at
// (uri, line, character) -- see importVisibleAtLocked for which those
// are, and importMemberRefsLocked for what one of them grants.
//
// Failing that, the imports uri picked up from the files it `include s
// are tried (see includedImportRefsLocked). They rank second because an
// import written in this file is the more specific statement of intent,
// and because an included one is only visible file-wide by
// approximation.
//
// Multiple visible imports whose package happens to declare the same word
// (e.g. two wildcard-imported packages both defining "foo") are
// deliberately NOT disambiguated -- every match is returned, the same
// "return every plausible candidate" behavior lookupQualifiedRefsLocked
// already has when a qualifier name is ambiguous across files. Picking
// one silently could easily be wrong; this index has no elaborator to
// confirm which one a real compile would actually bind.
func (ix *Index) lookupInImportsRefsLocked(uri string, line, character int, word string) ([]declRef, bool) {
	ancestors := ix.ancestorScopesLocked(uri, line, character)
	var out []declRef
	for _, imp := range ix.importsByURI[uri] {
		if !ix.importVisibleAtLocked(imp, ancestors) {
			continue
		}
		out = appendUniqueRefs(out, ix.importMemberRefsLocked(imp, word)...)
	}
	if len(out) == 0 {
		out = ix.includedImportRefsLocked(uri, word)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// importMemberRefsLocked resolves word through one import statement: a
// specific ("import pkg::name;") import only grants visibility to that
// one name, a wildcard ("import pkg::*;") to anything the package
// declares. Restricted to Kind == KindPackage (SV import syntax, LRM
// 26.3, is package-only, unlike a qualified Pkg::name/Class::name
// reference which also allows a class) -- defensive against a workspace
// where the imported identifier isn't actually a package, matching
// lookupQualifiedRefsLocked's own Kind check. Deciding *whether* an
// import applies at all is the caller's job; the three callers each scope
// it differently (a position, a container, an `include).
func (ix *Index) importMemberRefsLocked(imp importDecl, word string) []declRef {
	if imp.Member != "*" && imp.Member != word {
		return nil
	}
	var out []declRef
	for _, qref := range ix.byName[imp.Package] {
		if ix.byURI[qref.uri][qref.idx].Kind != KindPackage {
			continue
		}
		out = append(out, ix.childRefsLocked(qref.uri, qref.idx, word)...)
	}
	return out
}

// includedImportRefsLocked resolves word through the imports ownerURI
// picked up from the files it `include s (direct or transitive --
// dependsOn already holds the full set). After preprocessing an included
// import is just an import statement sitting in the includer, so a shared
// "project imports" header pulled into many modules grants them all the
// visibility it names.
//
// Only the included file's *file-scope* imports carry over. One nested in
// a container declared inside the header itself ("module m; import
// p::*; endmodule" in a .svh) stays that container's, and references
// inside it are in the header's own file, where the ordinary lookup
// already finds it.
//
// The result is visible file-wide in ownerURI rather than scoped to
// wherever the `include line sits: the index doesn't record include-site
// positions at all, and this matches the unscoped visibility an `include
// already grants for declarations reached through it (see
// resolveRefsLocked step 6).
func (ix *Index) includedImportRefsLocked(ownerURI, word string) []declRef {
	deps := ix.dependsOn[ownerURI]
	if len(deps) == 0 {
		return nil
	}
	var out []declRef
	for _, dep := range deps {
		for _, imp := range ix.importsByURI[dep] {
			if imp.Parent != -1 {
				continue
			}
			// Two headers importing the same package resolve to one place.
			for _, ref := range ix.importMemberRefsLocked(imp, word) {
				if !slices.Contains(out, ref) {
					out = append(out, ref)
				}
			}
		}
	}
	return out
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
		// File scope only. An `include makes the header's top-level
		// content visible to the includer, but a name declared inside a
		// module/class/package in that header is not in scope unqualified
		// -- resolving it here is a WRONG answer that masks a real compile
		// error, and hover and rename then propagate it. Same rule
		// childRefsLocked applies to the membership direction.
		if depSet[r.uri] && ix.byURI[r.uri][r.idx].Parent == -1 {
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
	_, d, ok := ix.firstDeclLocked(name, isContainerKind)
	return d.Ports, ok
}

// Params returns the overridable ("parameter", not "localparam") entries
// of a module/interface/program declaration's own "#( ... )" parameter
// port list, if one exists in the index. Used for parameter-override
// completion at an instantiation site, the "#(...)" counterpart to
// Ports/named-port-connection completion.
func (ix *Index) Params(name string) ([]Port, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	_, d, ok := ix.firstDeclLocked(name, isContainerKind)
	return d.Params, ok
}

// StructFieldLocation returns where field is declared inside the struct or
// union typedef named typeName.
//
// A field has no Declaration of its own (it lives on the typedef, as
// Declaration.Fields), so this is the only way to point at one -- what
// goto-definition on "receiver.field" needs, alongside the receiver-type
// resolution hover and completion already do. Port.Line/Character are
// populated for typedef fields specifically; see structUnionFields.
func (ix *Index) StructFieldLocation(typeName, field string) (Location, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	ref, fields, ok := ix.structTypedefLocked(typeName)
	if !ok {
		return Location{}, false
	}
	for _, f := range fields {
		if f.Name == field {
			return Location{URI: ref.uri, Line: f.Line, Character: f.Character}, true
		}
	}
	return Location{}, false
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
	_, fields, ok := ix.structTypedefLocked(typeName)
	return fields, ok
}

// structTypedefLocked is StructFields' body, additionally handing back a
// reference to the typedef declaration itself.
// ScopedOccurrencesForStructField needs the typedef's own source span,
// because a field has no Declaration of its own whose position it could
// look up instead (see Declaration.Fields, which reuses Port's
// name-and-detail shape and carries no position).
func (ix *Index) structTypedefLocked(typeName string) (declRef, []Port, bool) {
	ref, d, ok := ix.firstDeclLocked(typeName, func(d Declaration) bool {
		return d.Kind == KindTypedef && (d.TypedefKind == "struct" || d.TypedefKind == "union")
	})
	if !ok {
		return declRef{}, nil, false
	}
	return ref, d.Fields, true
}

// ScopedOccurrencesForStructField returns every occurrence of field that is
// itself a "<x>.field" access whose receiver has the same struct/union type
// receiver has at (uri, line, character), plus field's own declaration site
// inside that typedef's body. ok is false when the query isn't a struct-field
// access after all -- receiver doesn't resolve, isn't struct/union-typed, or
// that struct has no field by this name -- and the caller then falls back to
// plain ScopedOccurrences, exactly as structFieldHover falls back to HoverInfo.
//
// It exists because struct/union members are not indexed as Declarations of
// their own: they live only on the typedef, as Declaration.Fields. A bare
// field name therefore resolves to nothing, and ScopedOccurrences hands back
// its unscoped, name-wide fallback -- every identically-spelled identifier in
// the workspace, unrelated modules' ports and signals included. That fallback
// is right for a genuinely unresolvable name and wrong here, where the
// information needed to resolve the query (the receiver's type) is sitting in
// the query itself.
//
// Occurrence.Receiver is what makes the result-side filter affordable: only
// occurrences that are a field access at all get resolved, so a common field
// name's thousands of bare-identifier occurrences are rejected on a string
// comparison rather than a scope-chain walk each.
//
// Candidate receivers are resolved unqualified -- the token stream records
// the identifier before the dot and not any "pkg::" ahead of it -- so an
// access written "pkg::st.field" simply won't match and drops out. Receiver
// types are compared by bare TypeName, matching what StructFields itself
// does, so two same-named struct typedefs in different packages still merge:
// a pre-existing limitation shared with hover and completion, not one
// introduced here.
func (ix *Index) ScopedOccurrencesForStructField(uri string, line, character int, receiver, qualifier string, hasQualifier bool, field string) ([]Location, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	typeName, ok := ix.receiverTypeNameLocked(uri, line, character, receiver, qualifier, hasQualifier)
	if !ok {
		return nil, false
	}
	typedef, fields, ok := ix.structTypedefLocked(typeName)
	if !ok {
		return nil, false
	}
	i := slices.IndexFunc(fields, func(f Port) bool { return f.Name == field })
	if i < 0 {
		return nil, false
	}
	decl := fields[i]

	bucket := ix.occByName[field]
	uris := make([]string, 0, len(bucket))
	for u := range bucket {
		uris = append(uris, u)
	}
	sort.Strings(uris) // map order is nondeterministic; occurrencesLocked sorts for the same reason

	// One file accesses the same receiver over and over ("txn.addr" 300
	// times), and each resolution walks that file's whole declaration
	// bucket. The scope the occurrence sits in is what decides the answer,
	// so memoizing on (file, receiver, enclosing scope) collapses those 300
	// walks to one or two.
	type receiverKey struct {
		uri      string
		receiver string
		scope    int
	}
	memo := make(map[receiverKey]string)

	var out []Location
	for _, u := range uris {
		for _, occ := range bucket[u] {
			var keep bool
			switch {
			case occ.Receiver != "":
				key := receiverKey{u, occ.Receiver, innermostContaining(ix.byURI[u], occ.Line, occ.Character)}
				t, seen := memo[key]
				if !seen {
					t, _ = ix.receiverTypeNameLocked(u, occ.Line, occ.Character, occ.Receiver, "", false)
					memo[key] = t
				}
				keep = t != "" && t == typeName
			case u == typedef.uri:
				// The field's own declaration inside the typedef body, which
				// has no receiver to match on. Matched by the position
				// Port.Line/Character recorded for it, the only one a field
				// has -- a struct body pulled in across an `include boundary
				// therefore won't match here, the same cross-file gap
				// Declaration.Fields has generally.
				keep = occ.Line == decl.Line && occ.Character == decl.Character
			}
			if keep {
				out = append(out, Location{URI: u, Line: occ.Line, Character: occ.Character})
			}
		}
	}
	return out, true
}

// receiverTypeNameLocked resolves receiver at (uri, line, character) and
// reports its declared type's bare name -- the same Declaration.TypeName
// struct-member completion and hover already key off.
func (ix *Index) receiverTypeNameLocked(uri string, line, character int, receiver, qualifier string, hasQualifier bool) (string, bool) {
	refs, ok := ix.resolveRefsLocked(uri, line, character, receiver, qualifier, hasQualifier)
	if !ok || len(refs) == 0 {
		return "", false
	}
	ref := ix.primaryRefLocked(refs)
	if d := ix.byURI[ref.uri][ref.idx]; d.TypeName != "" {
		return d.TypeName, true
	}
	return "", false
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
	_, d, ok := ix.firstDeclLocked(name, func(d Declaration) bool { return d.Kind == KindTypedef })
	return d, ok
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

// OccurrencesInFile is ScopedOccurrences restricted to one file, for
// document highlight -- a within-document visual aid that discards
// everything outside the current file anyway.
//
// Doing that filtering here rather than in the caller is the point: the
// unscoped fallback flattens every occurrence of the name in the whole
// workspace into a slice first, and the caller then throws away all but
// one file's worth. On a common signal name in a large workspace that is
// a five-figure allocation per cursor move.
func (ix *Index) OccurrencesInFile(uri string, line, character int, word, qualifier string, hasQualifier bool) []Location {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	if IsKeyword(word) {
		return nil
	}

	refs, ok := ix.resolveRefsLocked(uri, line, character, word, qualifier, hasQualifier)
	if !ok || len(refs) == 0 {
		return ix.occurrencesInFileLocked(word, uri)
	}

	ref := ix.primaryRefLocked(refs)
	d := ix.byURI[ref.uri][ref.idx]
	container, restrict := ix.containerScopeLocked(ref.uri, d)
	if !restrict {
		return ix.occurrencesInFileLocked(word, uri)
	}
	if ref.uri != uri {
		// The declaration's scope is a container in another file, so no
		// occurrence in this one can be inside it. Connection sites still
		// can be, though.
		return ix.connectionOccurrencesInFileLocked(ref.uri, d, word, uri)
	}

	var out []Location
	for _, occ := range ix.occByName[word][uri] {
		if !posWithinBounds(occ.Line, occ.Character, container.Line, container.Character, container.EndLine, container.EndCharacter) {
			continue
		}
		out = append(out, Location{URI: uri, Line: occ.Line, Character: occ.Character})
	}
	return append(out, ix.connectionOccurrencesInFileLocked(ref.uri, d, word, uri)...)
}

func (ix *Index) connectionOccurrencesInFileLocked(containerURI string, d Declaration, word, uri string) []Location {
	if d.Kind != KindPort && d.Kind != KindParameter {
		return nil
	}
	var out []Location
	for _, loc := range ix.connectionOccurrencesLocked(containerURI, d.Parent, word, d.Kind) {
		if loc.URI == uri {
			out = append(out, loc)
		}
	}
	return out
}

// occurrencesInFileLocked is occurrencesLocked for a single URI.
func (ix *Index) occurrencesInFileLocked(name, uri string) []Location {
	occs := ix.occByName[name][uri]
	out := make([]Location, 0, len(occs))
	for _, occ := range occs {
		out = append(out, Location{URI: uri, Line: occ.Line, Character: occ.Character})
	}
	return out
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

	ref := ix.primaryRefLocked(refs)
	d := ix.byURI[ref.uri][ref.idx]
	container, restrict := ix.containerScopeLocked(ref.uri, d)
	if !restrict {
		return ix.occurrencesLocked(word)
	}

	var out []Location
	for _, occ := range ix.occByName[word][ref.uri] {
		if !posWithinBounds(occ.Line, occ.Character, container.Line, container.Character, container.EndLine, container.EndCharacter) {
			continue
		}
		out = append(out, Location{URI: ref.uri, Line: occ.Line, Character: occ.Character})
	}
	if d.Kind == KindPort || d.Kind == KindParameter {
		out = append(out, ix.connectionOccurrencesLocked(ref.uri, d.Parent, word, d.Kind)...)
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
	bucket := ix.connByName[name]
	if len(bucket) == 0 {
		return nil
	}
	uris := make([]string, 0, len(bucket))
	for uri := range bucket {
		uris = append(uris, uri)
	}
	sort.Strings(uris) // map order is nondeterministic; keep results stable

	var out []Location
	for _, uri := range uris {
		for _, site := range bucket[uri] {
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

// CompleteSymbols returns the first limit distinct declared names starting
// with prefix, in name order, across the whole index -- for general
// identifier completion (types, functions, tasks, classes, packages, enum
// members, ...). It's deliberately not scope-restricted the way
// FindDefinition is: every matching name anywhere in the workspace is a
// candidate, since narrowing that down correctly would need the same
// reachability information a real elaborator has and this lexical index
// doesn't. A name that happens to be declared multiple times (e.g. a
// prototype and its out-of-line body) is only returned once.
//
// truncated reports that at least one further match existed beyond limit,
// so the caller can mark its response incomplete. A limit <= 0 means no cap
// (truncated is then always false). Selecting the limit best rather than
// sorting everything and slicing keeps a short prefix in a large workspace
// from allocating the whole symbol table per keystroke -- see topK.
func (ix *Index) CompleteSymbols(prefix string, limit int) (syms []Symbol, truncated bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	top := newTopK(limit, func(a, b Symbol) bool { return a.Name < b.Name })
	for name, refs := range ix.byName {
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		if len(refs) == 0 {
			continue
		}
		ref := ix.primaryRefLocked(refs)
		d := ix.byURI[ref.uri][ref.idx]
		top.push(Symbol{Name: name, Kind: d.Kind})
	}
	return top.sorted()
}

// childRefsLocked returns every declRef that's a direct child of the
// declaration at (uri, containerIdx) and named name -- the shared
// primitive behind qualified (Pkg::name) lookup, import-based
// (wildcard/specific) resolution, and instantiation port lookup.
//
// Children come from two places. Most are in the container's own bucket,
// carrying its index as their Parent. The rest arrived through an
// `include in the container's body: those live in the included file's
// bucket at file scope (Parent -1), since Parent can't point across
// files, and membersOf is what remembers they're members at all (see
// memberLink). Whether a package member was typed inline or textually
// included is a source-organization detail; after preprocessing both are
// members of the same package scope, so both are found here.
func (ix *Index) childRefsLocked(uri string, containerIdx int, name string) []declRef {
	var out []declRef
	for i, d := range ix.byURI[uri] {
		if d.Parent == containerIdx && d.Name == name {
			out = append(out, declRef{uri: uri, idx: i})
		}
	}
	for _, l := range ix.membersOf[declRef{uri: uri, idx: containerIdx}] {
		if !ix.containerStillNamedLocked(uri, containerIdx, l.ContainerName) {
			continue
		}
		for i, d := range ix.byURI[l.IncludedURI] {
			// Only file scope: nesting *within* the included file is
			// tracked normally, so a typedef inside a class inside the
			// header is that class's child, not the container's.
			if d.Parent != -1 || d.Name != name {
				continue
			}
			// The same header included by two different files yields two
			// links to one bucket.
			if ref := (declRef{uri: l.IncludedURI, idx: i}); !slices.Contains(out, ref) {
				out = append(out, ref)
			}
		}
	}
	return out
}

// lookupInEnclosingContainersRefsLocked resolves word from inside a file
// whose own content was `include d into some container's body: the
// mirror of childRefsLocked's extra step. A declaration written in such a
// header is lexically inside that package/class/module, so the rest of
// that scope is visible to it -- its siblings typed inline in the file
// that opens the container, the members of its *other* headers (one
// childRefsLocked call reaches both), and whatever the container itself
// imports.
//
// Container members are preferred over the container's imports, matching
// the LRM's rule that a real declaration in a scope shadows a name a
// wildcard import merely makes visible there.
func (ix *Index) lookupInEnclosingContainersRefsLocked(uri, word string) ([]declRef, bool) {
	var out []declRef
	for _, l := range ix.containerOf[uri] {
		if !ix.containerStillNamedLocked(l.ContainerURI, l.ContainerIdx, l.ContainerName) {
			continue
		}
		refs := ix.childRefsLocked(l.ContainerURI, l.ContainerIdx, word)
		if len(refs) == 0 {
			refs = ix.containerImportRefsLocked(l.ContainerURI, l.ContainerIdx, word)
		}
		for _, ref := range refs {
			if !slices.Contains(out, ref) {
				out = append(out, ref)
			}
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// containerImportRefsLocked resolves word through the imports visible
// inside the container at (containerURI, containerIdx) -- the container's
// own body imports plus its file's file-scope ones, the same visibility
// importVisibleAtLocked computes for a position, minus the position (a
// caller here is in a different file entirely, so there's no container-
// ancestor chain of its own to walk). Kind and member matching mirror
// lookupInImportsRefsLocked, which does the same job for imports written
// in the referencing file itself.
func (ix *Index) containerImportRefsLocked(containerURI string, containerIdx int, word string) []declRef {
	var out []declRef
	for _, imp := range ix.importsByURI[containerURI] {
		if imp.Parent != containerIdx && imp.Parent != -1 {
			continue
		}
		out = appendUniqueRefs(out, ix.importMemberRefsLocked(imp, word)...)
	}
	if len(out) == 0 {
		// The import may itself have arrived through a *different*
		// `include into the same container -- the two-header package
		// shape, one header carrying the imports and another using them.
		out = ix.includedImportRefsLocked(containerURI, word)
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
		if !isContainerKind(ix.byURI[qref.uri][qref.idx]) {
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
	ref := ix.primaryRefLocked(refs)
	return ix.byURI[ref.uri][ref.idx], true
}

// lookupModportRefsLocked resolves modportName against interfaceName's own
// modports -- the same "find container by name globally, then its
// children" shape as lookupInstantiationPortRefsLocked, restricted to
// KindInterface (a modport can only ever belong to an interface, unlike an
// instantiation port/param lookup which spans module/interface/program).
func (ix *Index) lookupModportRefsLocked(interfaceName, modportName string) ([]declRef, bool) {
	var out []declRef
	for _, qref := range ix.byName[interfaceName] {
		if ix.byURI[qref.uri][qref.idx].Kind != KindInterface {
			continue
		}
		out = append(out, ix.childRefsLocked(qref.uri, qref.idx, modportName)...)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// FindModport resolves modportName against interfaceName's own modport
// declarations -- goto-definition/declaration for the modport-qualifier
// half of "IfaceName.modport", e.g. an interface port's type header or a
// virtual interface handle's type. Mirrors FindInstantiationPort.
func (ix *Index) FindModport(interfaceName, modportName string) ([]Location, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	refs, ok := ix.lookupModportRefsLocked(interfaceName, modportName)
	if !ok {
		return nil, false
	}
	return ix.locationsLocked(refs), true
}

// ModportInfo is FindModport's hover counterpart, mirroring
// InstantiationPortInfo.
func (ix *Index) ModportInfo(interfaceName, modportName string) (Declaration, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	refs, ok := ix.lookupModportRefsLocked(interfaceName, modportName)
	if !ok {
		return Declaration{}, false
	}
	ref := ix.primaryRefLocked(refs)
	return ix.byURI[ref.uri][ref.idx], true
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

// lookupInScopeRefsLocked walks outward from the container enclosing
// (line, character), innermost first, and then searches uri's own file
// scope -- the outermost rung, which the walk itself can't reach: -1 is
// both "file scope" and the loop's terminator, so a file-scope typedef
// used inside a module in the same file would otherwise never match.
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
	if out := ix.childRefsLocked(uri, -1, name); len(out) > 0 {
		return out, true
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

// WorkspaceSymbols returns the first limit declarations whose name contains
// query (case-insensitive substring match -- deliberately more permissive
// than CompleteSymbols' prefix match, since this backs an explicit "jump to
// symbol" search box rather than as-you-type completion, and a picker user
// typing "axi" expects to find AXI_master), across the whole workspace.
// Unlike CompleteSymbols, every declaration site is returned, not
// deduplicated by name.
//
// truncated, and a limit <= 0 meaning no cap, work exactly as in
// CompleteSymbols -- and matter more here, since clients routinely fire an
// empty query the moment the picker opens, which matches every declaration
// in the workspace.
//
// Results order by name, then URI, then position. The trailing position
// tiebreak isn't cosmetic: without it, declarations sharing a name and file
// order by map iteration, so which of them survives the cut differs between
// otherwise identical requests.
func (ix *Index) WorkspaceSymbols(query string, limit int) (syms []SymbolLocation, truncated bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	query = strings.ToLower(query)
	top := newTopK(limit, func(a, b SymbolLocation) bool {
		switch {
		case a.Name != b.Name:
			return a.Name < b.Name
		case a.URI != b.URI:
			return a.URI < b.URI
		case a.Line != b.Line:
			return a.Line < b.Line
		default:
			return a.Character < b.Character
		}
	})
	for name, refs := range ix.byName {
		// strings.ToLower returns its input unchanged, without allocating,
		// when the string has no uppercase -- so this is already free for
		// most names and cheap for the rest. A precomputed lowercase cache
		// was tried here and measured SLOWER (the map hash costs more than
		// the fast path it replaces: -8% on lowercase-heavy names, no
		// measurable gain on camelCase), besides costing a string per
		// distinct name. Don't reintroduce one without a benchmark.
		if query != "" && !strings.Contains(strings.ToLower(name), query) {
			continue
		}
		for _, r := range refs {
			d := ix.byURI[r.uri][r.idx]
			top.push(SymbolLocation{Name: name, Kind: d.Kind, URI: r.uri, Line: d.Line, Character: d.Character})
		}
	}
	return top.sorted()
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
