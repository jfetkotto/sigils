package lspserver

import (
	"strings"

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
	notify := s.getNotify()
	if notify == nil {
		return
	}
	for _, uri := range uris {
		if !strings.HasPrefix(uri, "file://") {
			if diags := s.index.Diagnostics(uri); len(diags) > 0 {
				s.Log.Warningf("diagnostics for non-file %q not published to the client: %+v", uri, diags)
			}
			continue
		}
		notify("textDocument/publishDiagnostics", protocol.PublishDiagnosticsParams{
			URI:         uri,
			Diagnostics: diagnosticsToProtocol(s.index.Diagnostics(uri)),
		})
	}
}

// diagnosticsToProtocol converts sv.Diagnostic (deliberately minimal and
// LSP-agnostic) into protocol.Diagnostic. svparse's Error doesn't carry
// the offending token's length, only a start position, so Range is a
// minimal one-column span at that position rather than real token-width
// highlighting -- a known, minor cosmetic simplification, not an attempt
// at precise underlining. Severity is always Error: everything here comes
// from svparse failing to preprocess or parse something, not from a lint
// rule with graduated severity.
func diagnosticsToProtocol(diags []sv.Diagnostic) []protocol.Diagnostic {
	severity := protocol.DiagnosticSeverityError
	source := "svparse"
	out := make([]protocol.Diagnostic, len(diags))
	for i, d := range diags {
		out[i] = protocol.Diagnostic{
			Range: protocol.Range{
				Start: position(d.Line, d.Character),
				End:   position(d.Line, d.Character+1),
			},
			Severity: &severity,
			Source:   &source,
			Message:  d.Message,
		}
	}
	return out
}
