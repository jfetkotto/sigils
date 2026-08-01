package workspace

import "context"

// Root is a resolved workspace root as reported by the LSP client.
type Root struct {
	URI  string
	Path string
}

// SourceFile is a single source file reachable from a Discoverer's
// filelist resolution.
type SourceFile struct {
	// LogicalPath is the path as referenced in the workspace -- it may
	// cross a symlink into a read-only network-cached repo checkout.
	LogicalPath string
	// ResolvedPath is the symlink-resolved real path, used for actual
	// file reads.
	ResolvedPath string
}

// Discoverer resolves the set of source files that make up a project view.
// The target workspace is a multi-repo, symlinked monorepo -- some repo
// folders are read-only network-cache symlinks, others are locally fetched
// read-write clones -- with a source view defined by a top-level filelist
// that recursively references other filelists. Implementations must be
// symlink-aware. FilelistDiscoverer is the real implementation;
// StaticDiscoverer is a stub (no filesystem walking) used when a workspace
// has no .sigils.json filelist configuration.
type Discoverer interface {
	Roots(ctx context.Context) ([]Root, error)
	Files(ctx context.Context, root Root) ([]SourceFile, error)
}

// FilelistProvider is an optional capability a Discoverer can implement:
// it exposes filelist-derived configuration beyond the plain file list --
// the filelist files themselves (VisitedFilelists, so a caller can watch
// them for changes and trigger a rescan when they're edited, e.g. a new
// source file gets added to a product's filelist), the `+incdir+` search
// path (IncludeDirs), and the `+define+` set (Defines) -- both captured
// during the same Files() call, for feeding an SV-aware scanner's
// `include resolution and initial macro state.
type FilelistProvider interface {
	VisitedFilelists() []string
	IncludeDirs() []string
	Defines() map[string]string
}

// StaticDiscoverer remembers the roots reported at initialize time. It
// does no filesystem walking or filelist parsing.
type StaticDiscoverer struct {
	roots []Root
}

func NewStaticDiscoverer(roots []Root) *StaticDiscoverer {
	return &StaticDiscoverer{roots: roots}
}

func (d *StaticDiscoverer) Roots(ctx context.Context) ([]Root, error) {
	return d.roots, nil
}

func (d *StaticDiscoverer) Files(ctx context.Context, root Root) ([]SourceFile, error) {
	return nil, nil
}
