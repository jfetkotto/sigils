package lspserver

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jfetkotto/sigils/internal/sv"
)

// includeResolver resolves an `include d path by trying, in order: the
// directory of the file that referenced it, then each of incdirs (from
// the workspace's captured +incdir+ entries -- see
// workspace.FilelistDiscoverer.IncludeDirs) -- matching how a real SV
// compiler/simulator resolves includes. It implements sv.IncludeResolver,
// tracking every path it successfully resolved so the caller can record a
// dependency-graph entry once the scan that used it finishes.
//
// A fresh instance is required per Scan call: resolved (and seen, its
// dedup set) accumulate across the instance's own lifetime, with no
// synchronization, so sharing one instance across concurrent scans (the
// parallel indexing worker pool runs many at once) would both race and
// conflate unrelated files' dependency sets. newIncludeResolverFactory
// exists specifically so callers can't accidentally share one.
type includeResolver struct {
	incdirs  []string
	resolved []string
	seen     map[string]bool
}

// newIncludeResolverFactory returns a sv.ResolverFactory that builds a
// fresh includeResolver, configured with incdirs, on every call.
func newIncludeResolverFactory(incdirs []string) sv.ResolverFactory {
	// incdirs is shared read-only across every resolver instance the
	// factory produces -- safe, since nothing ever mutates it after the
	// factory is built (a workspace rebuild replaces the factory itself,
	// via a fresh call to this function, rather than mutating incdirs in
	// place).
	dirs := append([]string(nil), incdirs...)
	return func() sv.IncludeResolver {
		return &includeResolver{incdirs: dirs, seen: make(map[string]bool)}
	}
}

// Resolve implements preprocessor.IncludeResolver (and so sv.IncludeResolver).
// includedPath is the path as written in an include directive's source
// text; fromFile is the URI of the file that referenced it -- the same URI form
// used everywhere else in this package (see uriToPath/pathToURI), since
// svparse tags every token's File with whatever path string the root Scan
// call passed in, and that tag is what reaches fromFile here for a nested
// include.
func (r *includeResolver) Resolve(includedPath, fromFile string) (text, resolvedPath string, err error) {
	fromPath, err := uriToPath(fromFile)
	if err != nil {
		return "", "", fmt.Errorf("could not resolve %q: including file %q is not a file:// URI: %w", includedPath, fromFile, err)
	}

	candidates := make([]string, 0, 1+len(r.incdirs))
	candidates = append(candidates, filepath.Dir(fromPath))
	candidates = append(candidates, r.incdirs...)

	var tried []string
	for _, dir := range candidates {
		candidate := includedPath
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(dir, includedPath)
		}
		tried = append(tried, candidate)

		data, readErr := os.ReadFile(candidate)
		if readErr != nil {
			if filepath.IsAbs(includedPath) {
				break // an absolute path has exactly one candidate to try
			}
			continue
		}

		// candidate (the as-written/as-joined path), not its symlink-
		// resolved form, becomes the URI -- matching every other Index key
		// in sigils, which is always pathToURI(SourceFile.LogicalPath), the
		// same "as referenced" form an editor's didOpen/didChange would
		// report. Resolving here would put an `include d file's Index
		// entry in a different URI "namespace" than the one it'd land in
		// if it were also opened directly or discovered via the normal
		// filelist walk, splitting one real file into two Index entries
		// on a workspace that references it through a symlink -- exactly
		// the layout this project targets. os.ReadFile above already
		// follows symlinks transparently for the actual read; nothing
		// else here needs the resolved form.
		uri := pathToURI(candidate)
		if !r.seen[uri] {
			r.seen[uri] = true
			r.resolved = append(r.resolved, uri)
		}
		return string(data), uri, nil
	}

	return "", "", fmt.Errorf("could not find %q (tried: %v)", includedPath, tried)
}

// Resolved implements sv.IncludeResolver.
func (r *includeResolver) Resolved() []string {
	return append([]string(nil), r.resolved...)
}
