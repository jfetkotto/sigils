package sv

import "github.com/jfetkotto/svparse/preprocessor"

// IncludeResolver resolves `include paths the same way
// preprocessor.IncludeResolver does, but also reports which paths it
// successfully resolved during one Scan call, so Index can track
// cross-file dependencies (see Index.recordDependencies/Dependents). A
// concrete implementation lives outside this package (internal/lspserver),
// since resolving a path needs filesystem access and workspace-specific
// `+incdir+` configuration this package deliberately doesn't have -- this
// package only defines the seam, mirroring how preprocessor.IncludeResolver
// itself works one layer down.
type IncludeResolver interface {
	preprocessor.IncludeResolver

	// Resolved returns every resolvedPath this instance successfully
	// resolved an `include to, in first-seen order. A fresh instance must
	// be used per Scan call for this to mean anything -- see
	// ResolverFactory.
	Resolved() []string
}

// ResolverFactory builds a fresh IncludeResolver for one Scan call.
// A fresh instance per call is required, not just convenient: Resolved()
// must reflect exactly that one call's includes, and Scan calls can run
// concurrently across files (see the parallel indexing worker pool in
// internal/lspserver), so a shared resolver instance would race.
type ResolverFactory func() IncludeResolver
