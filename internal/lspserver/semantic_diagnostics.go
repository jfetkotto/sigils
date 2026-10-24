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
// semantic checks are rerun. They cover the whole workspace (a change to one
// file can fix or break another), so running them on every keystroke would
// cost more than the answer is worth; the syntax errors stay immediate.
const defaultSemanticDebounce = 400 * time.Millisecond

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
	if s.semTimer == nil {
		s.semTimer = time.AfterFunc(s.semanticDebounce, s.refreshSemantic)
		return
	}
	s.semTimer.Reset(s.semanticDebounce)
}

// refreshSemantic recomputes the semantic diagnostics and publishes every
// file whose list changed, including files that just became clean. If the
// index changed while the checks ran, the result is discarded and the work
// is rescheduled rather than shown stale.
func (s *Server) refreshSemantic() {
	if done := s.semanticDone; done != nil {
		defer done()
	}
	byFile, gen := s.sem.Diagnostics()
	if gen != s.index.ASTGeneration() {
		s.scheduleSemantic()
		return
	}
	fresh := make(map[string][]shownDiagnostic, len(byFile))
	for uri, ds := range byFile {
		fresh[uri] = semanticDiagnostics(ds)
	}

	s.mu.Lock()
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
