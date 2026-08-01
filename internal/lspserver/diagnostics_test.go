package lspserver

import (
	"testing"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

// capturingNotify returns a glsp.NotifyFunc that appends every call's
// method/params onto notified for later inspection.
func capturingNotify(notified *[]struct {
	method string
	params any
}) func(method string, params any) {
	return func(method string, params any) {
		*notified = append(*notified, struct {
			method string
			params any
		}{method, params})
	}
}

func TestPublishDiagnosticsSendsOneNotificationPerURI(t *testing.T) {
	var notified []struct {
		method string
		params any
	}
	s := newTestServer()
	s.setNotify(capturingNotify(&notified))
	s.index.SetFile("file:///a.sv", "42;\nmodule top; endmodule\n")
	s.index.SetFile("file:///b.sv", "module clean; endmodule\n")

	s.publishDiagnostics([]string{"file:///a.sv", "file:///b.sv"})

	if len(notified) != 2 {
		t.Fatalf("expected 2 notifications, got %d: %+v", len(notified), notified)
	}
	for _, n := range notified {
		if n.method != "textDocument/publishDiagnostics" {
			t.Fatalf("unexpected method %q", n.method)
		}
	}

	params, ok := notified[0].params.(protocol.PublishDiagnosticsParams)
	if !ok || params.URI != "file:///a.sv" {
		t.Fatalf("unexpected first notification: %+v", notified[0])
	}
	if len(params.Diagnostics) == 0 {
		t.Fatalf("expected a.sv's malformed '42;' to produce at least one diagnostic")
	}
	if *params.Diagnostics[0].Severity != protocol.DiagnosticSeverityError {
		t.Fatalf("expected DiagnosticSeverityError, got %v", *params.Diagnostics[0].Severity)
	}

	params2, ok := notified[1].params.(protocol.PublishDiagnosticsParams)
	if !ok || params2.URI != "file:///b.sv" {
		t.Fatalf("unexpected second notification: %+v", notified[1])
	}
	if len(params2.Diagnostics) != 0 {
		t.Fatalf("expected b.sv (well-formed) to have zero diagnostics, got %+v", params2.Diagnostics)
	}
}

func TestPublishDiagnosticsSkipsNonFilePseudoURI(t *testing.T) {
	var notified []struct {
		method string
		params any
	}
	s := newTestServer()
	s.setNotify(capturingNotify(&notified))

	// A malformed +define+ value (an unterminated string literal) produces
	// a lexer error attributed to svparse's own pseudo-file
	// "<command-line>" (preprocessor.initialMacroFile), not a real
	// file:// URI -- see SetFile/diagnosticsByFile in internal/sv.
	s.Index().SetInitialMacros(map[string]string{"FOO": "\"unterminated"})
	touched := s.index.SetFile("file:///a.sv", "module top; endmodule\n")

	foundPseudoURI := false
	for _, uri := range touched {
		if uri == "<command-line>" {
			foundPseudoURI = true
		}
	}
	if !foundPseudoURI {
		t.Fatalf("expected SetFile's touchedURIs to include the pseudo-file, got %+v", touched)
	}

	s.publishDiagnostics(touched)

	for _, n := range notified {
		if params, ok := n.params.(protocol.PublishDiagnosticsParams); ok && params.URI == "<command-line>" {
			t.Fatalf("expected no publishDiagnostics notification for the non-file pseudo-URI, got %+v", params)
		}
	}
	foundRealFile := false
	for _, n := range notified {
		if params, ok := n.params.(protocol.PublishDiagnosticsParams); ok && params.URI == "file:///a.sv" {
			foundRealFile = true
		}
	}
	if !foundRealFile {
		t.Fatalf("expected a notification for the real file:///a.sv URI, got %+v", notified)
	}
}

func TestPublishDiagnosticsNoOpBeforeNotifyIsSet(t *testing.T) {
	s := newTestServer()
	s.index.SetFile("file:///a.sv", "42;\nmodule top; endmodule\n")
	// setNotify never called -- must not panic.
	s.publishDiagnostics([]string{"file:///a.sv"})
}

func TestInitializeCapturesNotifyFromRealContext(t *testing.T) {
	s := newTestServer()
	var notified []struct {
		method string
		params any
	}
	glspCtx := &glsp.Context{Notify: capturingNotify(&notified)}

	if _, err := s.Initialize(glspCtx, &protocol.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	s.index.SetFile("file:///a.sv", "42;\nmodule top; endmodule\n")
	s.publishDiagnostics([]string{"file:///a.sv"})

	if len(notified) != 1 || notified[0].method != "textDocument/publishDiagnostics" {
		t.Fatalf("expected Initialize to wire a working notify func, got %+v", notified)
	}
}

func TestTextDocumentDidOpenPublishesDiagnostics(t *testing.T) {
	s := newTestServer()
	var notified []struct {
		method string
		params any
	}
	s.setNotify(capturingNotify(&notified))

	err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: "42;\nmodule top; endmodule\n",
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentDidOpen: %v", err)
	}

	if len(notified) != 1 {
		t.Fatalf("expected 1 publishDiagnostics notification, got %+v", notified)
	}
	params, ok := notified[0].params.(protocol.PublishDiagnosticsParams)
	if !ok || params.URI != "file:///a.sv" || len(params.Diagnostics) == 0 {
		t.Fatalf("unexpected notification: %+v", notified[0])
	}
}

func TestTextDocumentDidChangePublishesDiagnostics(t *testing.T) {
	s := newTestServer()
	var notified []struct {
		method string
		params any
	}
	s.setNotify(capturingNotify(&notified))

	if err := s.TextDocumentDidOpen(nil, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI: "file:///a.sv", LanguageID: "systemverilog", Version: 1, Text: "module top; endmodule\n",
		},
	}); err != nil {
		t.Fatalf("TextDocumentDidOpen: %v", err)
	}
	notified = nil // discard the didOpen notification, only care about didChange's

	err := s.TextDocumentDidChange(nil, &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: "file:///a.sv"}, Version: 2},
		ContentChanges: []any{
			protocol.TextDocumentContentChangeEventWhole{Text: "42;\nmodule top; endmodule\n"},
		},
	})
	if err != nil {
		t.Fatalf("TextDocumentDidChange: %v", err)
	}

	if len(notified) != 1 {
		t.Fatalf("expected 1 publishDiagnostics notification, got %+v", notified)
	}
	params, ok := notified[0].params.(protocol.PublishDiagnosticsParams)
	if !ok || params.URI != "file:///a.sv" || len(params.Diagnostics) == 0 {
		t.Fatalf("unexpected notification: %+v", notified[0])
	}
}

func TestInitializeToleratesNilContext(t *testing.T) {
	// Several existing tests call Initialize(nil, ...) -- must not panic,
	// and publishDiagnostics afterward must stay a no-op.
	s := newTestServer()
	if _, err := s.Initialize(nil, &protocol.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	s.index.SetFile("file:///a.sv", "42;\nmodule top; endmodule\n")
	s.publishDiagnostics([]string{"file:///a.sv"}) // must not panic
}
