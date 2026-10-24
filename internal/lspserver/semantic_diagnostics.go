package lspserver

import (
	"slices"
	"time"

	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/svparse/v2/diag"
	"github.com/jfetkotto/svparse/v2/lint"

	"github.com/jfetkotto/sigils/internal/semantic"
	"github.com/jfetkotto/sigils/internal/workspace"
)

// defaultSemanticDebounce is how long the index must be quiet before the
// semantic checks are rerun, so a burst of keystrokes costs one run. The
// syntax errors stay immediate.
const defaultSemanticDebounce = 200 * time.Millisecond

// defaultScopedAfter is how slow a full semantic pass must have been for the
// next ones to be scoped to the changed files. A workspace whose full pass is
// faster than this just reruns it: scoping buys nothing there.
const defaultScopedAfter = 300 * time.Millisecond

// bootstrapFiles is how many files must have changed at once, as they do while
// a workspace is first indexed, for the first semantic pass to answer for the
// open documents before it does the whole workspace.
const bootstrapFiles = 200

// openDocuments returns the open documents that are indexed files.
func (s *Server) openDocuments() map[string]bool {
	out := map[string]bool{}
	for _, uri := range s.docs.URIs() {
		if isFileURI(string(uri)) {
			out[string(uri)] = true
		}
	}
	return out
}

// defaultSemanticFullDelay is how long after the last edit the full pass comes
// back, to refresh the files a scoped pass leaves alone (a change to a module
// can alter what is wrong in the places that instantiate it).
const defaultSemanticFullDelay = 2 * time.Second

// scheduleSemantic arranges for the semantic diagnostics to be recomputed
// once the index has been quiet for semanticDebounce. It is a no-op before
// Initialize (no client to publish to) and when the workspace turned semantic
// diagnostics off in .sigils.json.
func (s *Server) scheduleSemantic() {
	if s.getNotify() == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shutdownReceived || !s.cfg.SemanticDiagnosticsEnabled() {
		return
	}
	if s.semFullPending && s.semFullTimer != nil {
		s.semFullTimer.Reset(s.semFullDelay) // the full pass waits for the edits to stop
	}
	if s.semTimer == nil {
		s.semTimer = time.AfterFunc(s.semanticDebounce, s.refreshSemantic)
		return
	}
	s.semTimer.Reset(s.semanticDebounce)
}

// armFullPass makes the full pass run semFullDelay from now (or later, if more
// edits come).
func (s *Server) armFullPass() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shutdownReceived {
		return
	}
	s.semFullPending = true
	if s.semFullTimer == nil {
		s.semFullTimer = time.AfterFunc(s.semFullDelay, func() {
			s.mu.Lock()
			s.semForceFull, s.semFullPending = true, false
			s.mu.Unlock()
			s.refreshSemantic()
		})
		return
	}
	s.semFullTimer.Reset(s.semFullDelay)
}

// refreshSemantic recomputes the semantic diagnostics and publishes every
// file whose list changed, including files that just became clean.
//
// On a workspace big enough that the whole pass takes a noticeable time, only
// the files changed since the last pass are analysed (a few tens of
// milliseconds each, plus elaboration, against seconds), and the full pass is
// armed to follow once the edits stop. Otherwise it is the full pass.
func (s *Server) refreshSemantic() {
	if done := s.semanticDone; done != nil {
		defer done()
	}
	s.mu.Lock()
	dirty := s.semDirty
	s.semDirty = make(map[string]bool)
	scoped := !s.semForceFull && s.semHaveFull && len(dirty) > 0 && s.semLastFull > s.semScopedAfter
	// The first pass of a big workspace, when indexing has just finished and
	// every file counts as changed, answers for the open documents first: the
	// full pass then follows. Without an open document there is no one waiting.
	if !scoped && !s.semHaveFull && !s.semForceFull && len(dirty) > bootstrapFiles {
		if open := s.openDocuments(); len(open) > 0 {
			dirty, scoped = open, true
			s.semHaveFull, s.semLastFull = true, max(s.semLastFull, s.semScopedAfter+1)
		}
	}
	s.semForceFull = false
	s.mu.Unlock()
	if scoped {
		s.refreshScoped(dirty)
		return
	}
	s.refreshFull()
}

func (s *Server) refreshFull() {
	start := time.Now()
	byFile, gen := s.sem.Diagnostics()
	if gen != s.index.ASTGeneration() {
		// The index changed while the checks ran: discard the result, and make
		// sure the retry is a full pass.
		s.mu.Lock()
		s.semForceFull = true
		s.mu.Unlock()
		s.scheduleSemantic()
		return
	}
	fresh := make(map[string][]shownDiagnostic, len(byFile))
	for uri, ds := range byFile {
		fresh[uri] = semanticDiagnostics(ds)
	}

	s.mu.Lock()
	s.semLastFull, s.semHaveFull = time.Since(start), true
	if s.semFullPending && s.semFullTimer != nil {
		s.semFullTimer.Stop()
	}
	s.semFullPending = false
	old := s.semDiags
	s.semDiags = fresh
	var changed []string
	for uri, ds := range fresh {
		if !slices.Equal(old[uri], ds) {
			changed = append(changed, uri)
		}
	}
	for uri := range old {
		if _, still := fresh[uri]; !still {
			changed = append(changed, uri)
		}
	}
	s.mu.Unlock()
	s.publish(changed)
}

// refreshScoped analyses only the files in dirty and publishes those that
// changed, then arms the full pass.
func (s *Server) refreshScoped(dirty map[string]bool) {
	byFile, gen := s.sem.DiagnosticsFor(dirty)
	if byFile == nil || gen != s.index.ASTGeneration() {
		s.mu.Lock()
		for uri := range dirty {
			s.semDirty[uri] = true
		}
		s.mu.Unlock()
		s.scheduleSemantic()
		return
	}
	s.mu.Lock()
	var changed []string
	for uri := range dirty {
		ds := semanticDiagnostics(byFile[uri])
		if slices.Equal(s.semDiags[uri], ds) {
			continue
		}
		changed = append(changed, uri)
		if len(ds) == 0 {
			delete(s.semDiags, uri)
		} else {
			s.semDiags[uri] = ds
		}
	}
	s.mu.Unlock()
	s.publish(changed)
	s.armFullPass()
}

func semanticDiagnostics(ds []diag.Diagnostic) []shownDiagnostic {
	out := make([]shownDiagnostic, len(ds))
	for i, d := range ds {
		r := d.Range
		endLine, endChar := r.EndLine, r.EndCharacter
		if endLine < r.Line || (endLine == r.Line && endChar <= r.Character) {
			endLine, endChar = r.Line, r.Character+1
		}
		out[i] = shownDiagnostic{
			Line: r.Line, Character: r.Character, EndLine: endLine, EndCharacter: endChar,
			Severity: lspSeverity(d.Severity), Code: d.Code, Message: d.Message,
		}
	}
	return out
}

func lspSeverity(s diag.Severity) protocol.DiagnosticSeverity {
	switch s {
	case diag.Error:
		return protocol.DiagnosticSeverityError
	case diag.Warning:
		return protocol.DiagnosticSeverityWarning
	case diag.Info:
		return protocol.DiagnosticSeverityInformation
	}
	return protocol.DiagnosticSeverityHint
}

// lintConfig turns the workspace's lint settings into svparse's, nil when lint
// is off. Settings that do not validate (a misspelt rule name) are reported
// and replaced by the defaults, rather than silently disabling nothing or
// everything.
func (s *Server) lintConfig(cfg workspace.Config) *lint.Config {
	if !cfg.LintEnabled() {
		return nil
	}
	out := lint.Config{}
	if cfg.Lint != nil {
		out = lint.Config{Rules: cfg.Lint.Rules, Ignore: cfg.Lint.Ignore}
	}
	if err := out.Validate(); err != nil {
		s.log.Warningf("ignoring the lint settings in %s: %s", workspace.ConfigFileName, err)
		out = lint.Config{}
	}
	return &out
}

// semanticNavigation reports whether the workspace turned semantic navigation
// on.
func (s *Server) semanticNavigation() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.SemanticNavigationEnabled()
}

// semanticDefinition answers go-to-definition from the semantic model, when
// that is turned on and the model resolves the name under the cursor.
func (s *Server) semanticDefinition(uri string, pos protocol.Position) ([]protocol.Location, bool) {
	if !s.semanticNavigation() {
		return nil, false
	}
	locs, ok := s.sem.Definition(uri, int(pos.Line), int(pos.Character))
	if !ok {
		return nil, false
	}
	return toProtocolLocations(locs), true
}

// semanticReferences answers find-references from the semantic model, when
// that is turned on and the model resolves the name under the cursor.
func (s *Server) semanticReferences(uri string, pos protocol.Position, includeDeclaration bool) ([]protocol.Location, bool) {
	if !s.semanticNavigation() {
		return nil, false
	}
	locs, ok := s.sem.References(uri, int(pos.Line), int(pos.Character), includeDeclaration)
	if !ok {
		return nil, false
	}
	return toProtocolLocations(locs), true
}

func toProtocolLocations(locs []semantic.Location) []protocol.Location {
	out := make([]protocol.Location, len(locs))
	for i, l := range locs {
		out[i] = protocol.Location{
			URI: l.URI,
			Range: protocol.Range{
				Start: position(l.Line, l.Character),
				End:   position(l.EndLine, l.EndCharacter),
			},
		}
	}
	return out
}
