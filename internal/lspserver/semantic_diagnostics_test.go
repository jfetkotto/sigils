package lspserver

import (
	"fmt"
	"sync"
	"testing"
	"time"

	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/workspace"
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

const unusedSpare = "module m(input a, output b);\n  logic spare;\n  assign b = a;\nendmodule\n"

func TestLintFindingsArePublishedWithTheirSeverity(t *testing.T) {
	h := newSemanticHarness(t)
	h.s.sem.SetLint(h.s.lintConfig(h.s.cfg))
	h.set(t, "file:///m.sv", unusedSpare)
	diags := h.diagnostics("file:///m.sv")
	if len(diags) != 1 || diags[0].Code == nil || diags[0].Code.Value != "unused-signal" || *diags[0].Severity != protocol.DiagnosticSeverityWarning {
		t.Fatalf("got %+v", diags)
	}
}

func TestLintSuppressionCommentReachesTheClient(t *testing.T) {
	h := newSemanticHarness(t)
	h.s.sem.SetLint(h.s.lintConfig(h.s.cfg))
	h.set(t, "file:///m.sv", "module m(input a, output b);\n  logic spare; // svparse: disable=unused-signal\n  assign b = a;\nendmodule\n")
	if got := h.diagnostics("file:///m.sv"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestLintSettings(t *testing.T) {
	h := newSemanticHarness(t)
	off := false
	if h.s.lintConfig(workspace.Config{Lint: &workspace.LintSettings{Enabled: &off}}) != nil {
		t.Error("lint.enabled=false should turn lint off")
	}
	cfg := h.s.lintConfig(workspace.Config{Lint: &workspace.LintSettings{Rules: map[string]string{"unused-signal": "off"}, Ignore: []string{"third_party/**"}}})
	if cfg == nil || cfg.Rules["unused-signal"] != "off" || len(cfg.Ignore) != 1 {
		t.Errorf("got %+v", cfg)
	}
	bad := h.s.lintConfig(workspace.Config{Lint: &workspace.LintSettings{Rules: map[string]string{"no-such-rule": "off"}}})
	if bad == nil || len(bad.Rules) != 0 {
		t.Errorf("an invalid setting should fall back to the defaults, got %+v", bad)
	}
}

// scopedHarness is a server that takes the scoped path as soon as it has done
// one full pass, and brings the full pass back after a short quiet time.
func scopedHarness(t *testing.T) *semanticHarness {
	h := newSemanticHarness(t)
	h.s.semScopedAfter = 0
	h.s.semFullDelay = 60 * time.Millisecond
	return h
}

func TestScopedPassUpdatesOnlyTheChangedFile(t *testing.T) {
	h := scopedHarness(t)
	h.set(t, "file:///a.sv", "module a(input x);\n  assign x = 1'b0;\nendmodule\n")
	h.set(t, "file:///b.sv", "module b(input y);\n  assign y = 1'b0;\nendmodule\n")
	// Both files have the assign-to-input error after the first (full) passes.
	for _, uri := range []string{"file:///a.sv", "file:///b.sv"} {
		if got := h.diagnostics(uri); len(got) != 1 {
			t.Fatalf("%s: %+v", uri, got)
		}
	}
	h.wait(t) // the full pass that follows the scoped one for b.sv
	h.s.mu.Lock()
	full := h.s.semHaveFull
	h.s.mu.Unlock()
	if !full {
		t.Fatal("no full pass recorded")
	}

	// Fix a.sv only: its error clears from the scoped pass, b.sv is left as it is.
	h.s.publishDiagnostics(h.s.index.SetFile("file:///a.sv", "module a(output x);\n  assign x = 1'b0;\nendmodule\n"))
	h.wait(t)
	if got := h.diagnostics("file:///a.sv"); len(got) != 0 {
		t.Fatalf("a.sv should be clean, got %+v", got)
	}
	if got := h.diagnostics("file:///b.sv"); len(got) != 1 {
		t.Fatalf("b.sv should keep its error, got %+v", got)
	}
	h.wait(t) // the full pass
}

// A change in one file can fix a problem in another; the scoped pass leaves
// that other file alone and the full pass that follows corrects it.
func TestFullPassFollowsAScopedOneAndRefreshesOtherFiles(t *testing.T) {
	h := scopedHarness(t)
	h.set(t, "file:///top.sv", "module top;\n  leaf u();\nendmodule\n")
	if got := h.diagnostics("file:///top.sv"); len(got) == 0 {
		t.Fatal("expected unknown module while leaf is undefined")
	}
	h.s.publishDiagnostics(h.s.index.SetFile("file:///leaf.sv", "module leaf;\nendmodule\n"))
	h.wait(t) // scoped: leaf.sv only
	h.wait(t) // full: top.sv refreshed
	if got := h.diagnostics("file:///top.sv"); len(got) != 0 {
		t.Fatalf("top.sv should be clean after the full pass, got %+v", got)
	}
}

func TestSmallWorkspacesKeepTheFullPass(t *testing.T) {
	h := newSemanticHarness(t) // the default threshold: a tiny workspace is fast
	h.set(t, "file:///a.sv", "module a(input x);\n  assign x = 1'b0;\nendmodule\n")
	h.s.publishDiagnostics(h.s.index.SetFile("file:///a.sv", "module a(output x);\n  assign x = 1'b0;\nendmodule\n"))
	h.wait(t)
	h.s.mu.Lock()
	pending := h.s.semFullPending
	h.s.mu.Unlock()
	if pending {
		t.Fatal("a fast workspace should not arm a follow-up full pass")
	}
}

// While a big workspace is first indexed every file counts as changed; the
// first pass answers for the open documents, then the full pass follows.
func TestFirstPassOfABigWorkspaceAnswersForOpenDocumentsFirst(t *testing.T) {
	h := newSemanticHarness(t)
	h.s.semanticDebounce = 300 * time.Millisecond // indexing finishes well inside it
	h.s.semFullDelay = 60 * time.Millisecond
	openText(t, h.s, "file:///open.sv", "module open_m(input x);\n  assign x = 1'b0;\nendmodule\n")
	var touched []string
	touched = append(touched, h.s.index.SetFile("file:///open.sv", "module open_m(input x);\n  assign x = 1'b0;\nendmodule\n")...)
	for i := 0; i < bootstrapFiles+10; i++ {
		touched = append(touched, h.s.index.SetFile(fmt.Sprintf("file:///f%03d.sv", i), fmt.Sprintf("module m%03d(input a, output b);\n  assign b = a;\nendmodule\n", i))...)
	}
	h.s.publishDiagnostics(touched)
	h.wait(t)
	if got := h.diagnostics("file:///open.sv"); len(got) != 1 {
		t.Fatalf("the open document should be answered first, got %+v", got)
	}
	h.s.mu.Lock()
	pending := h.s.semFullPending
	h.s.mu.Unlock()
	if !pending {
		t.Fatal("the first pass should have been the scoped one, with the full pass armed")
	}
	h.wait(t) // the full pass
}
