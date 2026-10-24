package lspserver

import (
	"sync"
	"testing"
	"time"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// semanticHarness is a server whose semantic pass runs after a few
// milliseconds and reports each completed run on done, with the diagnostics
// it published collected under a lock (the pass runs on a timer goroutine).
type semanticHarness struct {
	s    *Server
	done chan struct{}

	mu   sync.Mutex
	last map[string][]protocol.Diagnostic
}

func newSemanticHarness(t *testing.T) *semanticHarness {
	t.Helper()
	h := &semanticHarness{s: newTestServer(), done: make(chan struct{}, 16), last: map[string][]protocol.Diagnostic{}}
	h.s.semanticDebounce = 5 * time.Millisecond
	h.s.semanticDone = func() { h.done <- struct{}{} }
	h.s.setNotify(func(method string, params any) {
		if p, ok := params.(protocol.PublishDiagnosticsParams); ok {
			h.mu.Lock()
			h.last[p.URI] = p.Diagnostics
			h.mu.Unlock()
		}
	})
	return h
}

// set indexes the text and waits for the semantic pass it triggers.
func (h *semanticHarness) set(t *testing.T, uri, text string) {
	t.Helper()
	h.s.publishDiagnostics(h.s.index.SetFile(uri, text))
	h.wait(t)
}

func (h *semanticHarness) wait(t *testing.T) {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the semantic pass did not run")
	}
}

func (h *semanticHarness) diagnostics(uri string) []protocol.Diagnostic {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last[uri]
}

const inputDriven = "module m(input a);\n  assign a = 1'b0;\nendmodule\n"

func TestSemanticDiagnosticsArePublishedAndCleared(t *testing.T) {
	h := newSemanticHarness(t)
	h.set(t, "file:///m.sv", inputDriven)
	diags := h.diagnostics("file:///m.sv")
	if len(diags) != 1 {
		t.Fatalf("expected one semantic diagnostic, got %+v", diags)
	}
	d := diags[0]
	if d.Range.Start.Line != 1 || *d.Severity != protocol.DiagnosticSeverityError || d.Code == nil || d.Code.Value != "assign-to-input" {
		t.Fatalf("unexpected diagnostic: %+v", d)
	}

	h.set(t, "file:///m.sv", "module m(output a);\n  assign a = 1'b0;\nendmodule\n")
	if got := h.diagnostics("file:///m.sv"); len(got) != 0 {
		t.Fatalf("the diagnostic should have been cleared, got %+v", got)
	}
}

// A change in one file can fix or break another, so the other file's
// diagnostics follow.
func TestSemanticDiagnosticsFollowOtherFiles(t *testing.T) {
	h := newSemanticHarness(t)
	h.set(t, "file:///top.sv", "module top;\n  leaf u();\nendmodule\n")
	if got := h.diagnostics("file:///top.sv"); len(got) == 0 {
		t.Fatal("expected an unknown module diagnostic while leaf is undefined")
	}
	h.set(t, "file:///leaf.sv", "module leaf;\nendmodule\n")
	if got := h.diagnostics("file:///top.sv"); len(got) != 0 {
		t.Fatalf("defining leaf should clear top's diagnostic, got %+v", got)
	}
}

func TestSemanticDiagnosticsSkipFilesThatDoNotParse(t *testing.T) {
	h := newSemanticHarness(t)
	h.set(t, "file:///bad.sv", "module m(input a);\n  assign a = 1'b0 + ;\nendmodule\n")
	for _, d := range h.diagnostics("file:///bad.sv") {
		if d.Code != nil {
			t.Fatalf("a file with syntax errors should show only those, got %+v", d)
		}
	}
}

func TestSemanticDiagnosticsCanBeTurnedOff(t *testing.T) {
	h := newSemanticHarness(t)
	off := false
	h.s.cfg.SemanticDiagnostics = &off
	h.s.publishDiagnostics(h.s.index.SetFile("file:///m.sv", inputDriven))
	select {
	case <-h.done:
		t.Fatal("the semantic pass ran although it is turned off")
	case <-time.After(100 * time.Millisecond):
	}
	if got := h.diagnostics("file:///m.sv"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

// Rapid edits collapse into one run.
func TestSemanticDiagnosticsAreDebounced(t *testing.T) {
	h := newSemanticHarness(t)
	h.s.semanticDebounce = 50 * time.Millisecond
	for i := 0; i < 5; i++ {
		h.s.publishDiagnostics(h.s.index.SetFile("file:///m.sv", inputDriven))
	}
	h.wait(t)
	select {
	case <-h.done:
		t.Fatal("more than one run for a burst of edits")
	case <-time.After(150 * time.Millisecond):
	}
}
