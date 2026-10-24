package lspserver

import (
	"slices"

	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/sv"
)

// publishDiagnostics republishes textDocument/publishDiagnostics for every
// URI in uris, reading each one's current diagnostics from the index --
// called after any Index.SetFile (with its returned touchedURIs) or
// Index.RemoveFile (with just the removed URI, so its diagnostics clear
// client-side) changes what a file's diagnostics are. A no-op before
// Initialize has run (no client to notify yet), which matters for tests
// that call SetFile directly without going through the real handshake.
//
// A non-"file://" URI is skipped rather than sent to the client, which
// couldn't do anything useful with it anyway: svparse tags a lex/parse
// error inside a workspace's own +define+ value (an Options.InitialMacros
// entry, seeded from workspace.FilelistDiscoverer.Defines) with the
// pseudo-file "<command-line>" rather than a real path, and that tag
// flows through diagsByURI/touchedURIs like any other file. Logged
// instead of silently dropped, so a broken +define+ isn't invisible.
func (s *Server) publishDiagnostics(uris []string) {
	s.publish(uris)
	s.scheduleSemantic()
}

// publish sends each URI's current diagnostics, unless they are what the
// client already has. A URI's diagnostics are the index's syntax errors, or,
// when it has none, the semantic ones computed for it: semantic checks of a
// file that does not parse mostly repeat the same mistake as cascades.
func (s *Server) publish(uris []string) {
	notify := s.getNotify()
	if notify == nil {
		return
	}
	for _, uri := range uris {
		syntax := s.index.Diagnostics(uri)
		if !isFileURI(uri) {
			if len(syntax) > 0 {
				s.log.Warningf("diagnostics for non-file %q not published to the client: %+v", uri, syntax)
			}
			continue
		}
		var diags []shownDiagnostic
		if len(syntax) > 0 {
			diags = syntaxDiagnostics(syntax)
		} else {
			s.mu.Lock()
			diags = s.semDiags[uri]
			s.mu.Unlock()
		}
		if !s.diagnosticsChanged(uri, diags) {
			continue
		}
		notify("textDocument/publishDiagnostics", protocol.PublishDiagnosticsParams{
			URI:         uri,
			Diagnostics: diagnosticsToProtocol(diags),
		})
	}
}

// shownDiagnostic is a diagnostic as the client sees it. It is comparable,
// which is what lets diagnosticsChanged tell a repeat from a change.
type shownDiagnostic struct {
	Line, Character       int
	EndLine, EndCharacter int
	Severity              protocol.DiagnosticSeverity
	Code                  string
	Message               string
}

// syntaxDiagnostics wraps the index's preprocessing and parsing errors.
// svparse's Error doesn't carry the offending token's length, only a start
// position, so the range is a one-column span at that position rather than
// real token-width highlighting -- a minor cosmetic simplification.
func syntaxDiagnostics(diags []sv.Diagnostic) []shownDiagnostic {
	out := make([]shownDiagnostic, len(diags))
	for i, d := range diags {
		out[i] = shownDiagnostic{
			Line: d.Line, Character: d.Character, EndLine: d.Line, EndCharacter: d.Character + 1,
			Severity: protocol.DiagnosticSeverityError, Message: d.Message,
		}
	}
	return out
}

const diagnosticSource = "svparse"

func diagnosticsToProtocol(diags []shownDiagnostic) []protocol.Diagnostic {
	source := diagnosticSource
	out := make([]protocol.Diagnostic, len(diags))
	for i, d := range diags {
		severity := d.Severity
		out[i] = protocol.Diagnostic{
			Range:    protocol.Range{Start: position(d.Line, d.Character), End: position(d.EndLine, d.EndCharacter)},
			Severity: &severity,
			Source:   &source,
			Message:  d.Message,
		}
		if d.Code != "" {
			code := protocol.IntegerOrString{Value: d.Code}
			out[i].Code = &code
		}
	}
	return out
}

// diagnosticsChanged reports whether uri's diagnostics differ from what
// was last published for it, recording the new state when they do.
//
// SetFile marks a URI touched if it has any content at all, not if its
// diagnostics actually changed, and startup calls publishDiagnostics once
// per scanned file. In a 20,000-file workspace where one header is
// included by 5,000 of them, that is 20,000-plus notifications -- roughly
// 5,000 of them for that one header, all carrying the identical (usually
// empty) list -- written down the same stdio pipe the editor needs for the
// hover and completion the user is trying to do meanwhile.
//
// Publishing an empty list for a URI that previously had errors still
// happens, which is what clears stale squiggles; only a genuine repeat is
// suppressed. A URI with nothing recorded counts as having been published
// an empty list: the client holds no diagnostics for a URI the server never
// sent any for, so there is nothing to clear, and a startup pass over a
// clean workspace sends nothing at all rather than one empty list per file.
// That also lets an empty list drop its entry, so published only ever holds
// the URIs currently showing errors.
func (s *Server) diagnosticsChanged(uri string, diags []shownDiagnostic) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.Equal(s.published[uri], diags) { // a missing entry reads as nil, i.e. empty
		return false
	}
	if len(diags) == 0 {
		delete(s.published, uri)
	} else {
		s.published[uri] = diags // the slice is never modified after it is built, so keeping it is safe
	}
	return true
}
