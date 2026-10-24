package lspserver

import (
	"slices"
	"time"

	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/svparse/v2/diag"
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
