package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// buildBinary compiles the sigils binary once per test run so the end-to-end
// test drives the real stdio transport (glsp's, not a mock), not just our
// own handler code.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "sigils")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// rpcClient speaks real Content-Length-framed JSON-RPC 2.0, the same wire
// format an editor would use, over the given pipes. A background readLoop
// dispatches every incoming message into responses (no "method" key) or
// notifications (has one), so request() can read only responses while
// waitForNotification reads only notifications -- neither blocks on or
// races the other, which matters once the server sends unprompted
// notifications (textDocument/publishDiagnostics) at times uncorrelated
// with the client's own requests.
type rpcClient struct {
	w             io.Writer
	id            int
	t             *testing.T
	responses     chan map[string]any
	notifications chan map[string]any
}

func newRPCClient(t *testing.T, w io.Writer, r io.Reader) *rpcClient {
	c := &rpcClient{
		w:             w,
		t:             t,
		responses:     make(chan map[string]any, 16),
		notifications: make(chan map[string]any, 64),
	}
	go c.readLoop(bufio.NewReader(r))
	return c
}

// readLoop runs for the client's whole lifetime. A read error (including
// the pipe closing when the server process exits, which every test
// eventually causes) just closes both channels -- t.Fatalf is only safe
// to call from the goroutine actually running the test, not from here.
func (c *rpcClient) readLoop(r *bufio.Reader) {
	defer close(c.responses)
	defer close(c.notifications)
	for {
		msg, err := readFramedMessage(r)
		if err != nil {
			return
		}
		if _, hasMethod := msg["method"]; hasMethod {
			c.notifications <- msg
		} else {
			c.responses <- msg
		}
	}
}

// readFramedMessage reads one Content-Length-framed JSON-RPC message.
func readFramedMessage(r *bufio.Reader) (map[string]any, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, err
			}
		}
	}
	if length < 0 {
		return nil, fmt.Errorf("message had no Content-Length header")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	var msg map[string]any
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func (c *rpcClient) writeMessage(v any) {
	c.t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		c.t.Fatalf("marshal: %v", err)
	}
	if _, err := fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		c.t.Fatalf("write header: %v", err)
	}
	if _, err := c.w.Write(body); err != nil {
		c.t.Fatalf("write body: %v", err)
	}
}

// request writes a request and returns the matching response. Any
// server-initiated notification received while waiting is routed to
// notifications by readLoop, not returned here.
func (c *rpcClient) request(method string, params any) map[string]any {
	c.t.Helper()
	c.id++
	c.writeMessage(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	resp, ok := <-c.responses
	if !ok {
		c.t.Fatalf("connection closed while waiting for a response to %s", method)
	}
	return resp
}

func (c *rpcClient) notify(method string, params any) {
	c.t.Helper()
	c.writeMessage(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// waitForNotification drains notifications until one with the given
// method arrives, or timeout elapses -- for a server-initiated push
// (textDocument/publishDiagnostics) that arrives asynchronously relative
// to the client's own requests, unlike request()'s reply.
func (c *rpcClient) waitForNotification(method string, timeout time.Duration) (map[string]any, bool) {
	c.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case msg, ok := <-c.notifications:
			if !ok {
				return nil, false
			}
			if msg["method"] == method {
				return msg, true
			}
		case <-deadline:
			return nil, false
		}
	}
}

func TestEndToEndLifecycle(t *testing.T) {
	bin := buildBinary(t)

	workspaceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceRoot, ".sigils.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootURI := "file://" + workspaceRoot

	cmd := exec.Command(bin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	client := newRPCClient(t, stdin, stdout)

	initResp := client.request("initialize", map[string]any{
		"rootUri":      rootURI,
		"capabilities": map[string]any{},
		"workspaceFolders": []map[string]any{
			{"uri": rootURI, "name": "root"},
		},
	})
	if initResp["error"] != nil {
		t.Fatalf("initialize returned an error: %v\nstderr:\n%s", initResp["error"], stderr.String())
	}
	result, _ := initResp["result"].(map[string]any)
	caps, _ := result["capabilities"].(map[string]any)
	sync, _ := caps["textDocumentSync"].(map[string]any)
	if sync == nil || sync["change"] != float64(1) { // TextDocumentSyncKindFull == 1
		t.Fatalf("expected full-document sync capability, got %+v", caps)
	}
	if caps["definitionProvider"] != true {
		t.Fatalf("expected definitionProvider capability to be advertised, got %+v", caps)
	}
	if caps["declarationProvider"] != true {
		t.Fatalf("expected declarationProvider capability to be advertised, got %+v", caps)
	}
	completion, _ := caps["completionProvider"].(map[string]any)
	if completion == nil {
		t.Fatalf("expected completionProvider capability to be advertised, got %+v", caps)
	}
	for _, cap := range []string{"documentSymbolProvider", "foldingRangeProvider", "workspaceSymbolProvider", "hoverProvider", "documentHighlightProvider", "referencesProvider", "renameProvider"} {
		if caps[cap] != true {
			t.Fatalf("expected %s capability to be advertised, got %+v", cap, caps)
		}
	}

	client.notify("initialized", map[string]any{})

	client.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": "file:///a.sv", "languageId": "systemverilog", "version": 1, "text": "module a; endmodule",
		},
	})

	client.notify("textDocument/didChange", map[string]any{
		"textDocument": map[string]any{"uri": "file:///a.sv", "version": 2},
		"contentChanges": []map[string]any{
			{"text": "module a; initial begin end endmodule"},
		},
	})

	client.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": "file:///leaf.sv", "languageId": "systemverilog", "version": 1,
			"text": "module leaf(input logic clk, input logic rst_n);\nendmodule\n",
		},
	})
	topText := "module top;\n  leaf u_leaf (\n    .clk(c),\n    .\n  );\nendmodule\n"
	client.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": "file:///top.sv", "languageId": "systemverilog", "version": 1, "text": topText,
		},
	})

	// "leaf" on line 1 of top.sv starts at character 2.
	defResp := client.request("textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": "file:///top.sv"},
		"position":     map[string]any{"line": 1, "character": 3},
	})
	if defResp["error"] != nil {
		t.Fatalf("textDocument/definition returned an error: %v\nstderr:\n%s", defResp["error"], stderr.String())
	}
	locs, ok := defResp["result"].([]any)
	if !ok || len(locs) != 1 {
		t.Fatalf("expected exactly one definition location, got %#v\nstderr:\n%s", defResp["result"], stderr.String())
	}
	loc, _ := locs[0].(map[string]any)
	if loc["uri"] != "file:///leaf.sv" {
		t.Fatalf("definition uri = %v, want file:///leaf.sv", loc["uri"])
	}

	// Modules have no declaration/definition split, so declaration should
	// resolve to the same place.
	declResp := client.request("textDocument/declaration", map[string]any{
		"textDocument": map[string]any{"uri": "file:///top.sv"},
		"position":     map[string]any{"line": 1, "character": 3},
	})
	if declResp["error"] != nil {
		t.Fatalf("textDocument/declaration returned an error: %v\nstderr:\n%s", declResp["error"], stderr.String())
	}
	declLocs, ok := declResp["result"].([]any)
	if !ok || len(declLocs) != 1 {
		t.Fatalf("expected exactly one declaration location, got %#v\nstderr:\n%s", declResp["result"], stderr.String())
	}

	// Cursor right after the "." on the incomplete last line inside the
	// instantiation's parens -- "clk" is already connected, so only
	// "rst_n" should be suggested.
	compResp := client.request("textDocument/completion", map[string]any{
		"textDocument": map[string]any{"uri": "file:///top.sv"},
		"position":     map[string]any{"line": 3, "character": 5},
	})
	if compResp["error"] != nil {
		t.Fatalf("textDocument/completion returned an error: %v\nstderr:\n%s", compResp["error"], stderr.String())
	}
	items, ok := compResp["result"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("expected exactly one completion item, got %#v\nstderr:\n%s", compResp["result"], stderr.String())
	}
	item, _ := items[0].(map[string]any)
	if item["label"] != "rst_n" {
		t.Fatalf("completion label = %v, want \"rst_n\"", item["label"])
	}
	// The "." that triggered completion is already in topText at this
	// position; the returned edit must not duplicate it (regression check
	// for the reported "..foo" bug).
	textEdit, _ := item["textEdit"].(map[string]any)
	if textEdit == nil {
		t.Fatalf("expected a textEdit on the completion item, got %+v", item)
	}
	if textEdit["newText"] != "rst_n" {
		t.Fatalf("textEdit.newText = %v, want \"rst_n\" (no leading dot -- one was already typed)", textEdit["newText"])
	}

	// General symbol completion (types, functions, enum members, ...)
	// outside any instantiation context.
	symText := "typedef enum {IDLE, RUNNING} state_t;\nfunction void helper();\nendfunction\nmodule top2;\n  hel\nendmodule\n"
	client.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": "file:///sym.sv", "languageId": "systemverilog", "version": 1, "text": symText,
		},
	})

	// "hel" on line 4 ("  hel") ends at character 5.
	symResp := client.request("textDocument/completion", map[string]any{
		"textDocument": map[string]any{"uri": "file:///sym.sv"},
		"position":     map[string]any{"line": 4, "character": 5},
	})
	if symResp["error"] != nil {
		t.Fatalf("textDocument/completion (general) returned an error: %v\nstderr:\n%s", symResp["error"], stderr.String())
	}
	symItems, ok := symResp["result"].([]any)
	if !ok || len(symItems) != 1 {
		t.Fatalf("expected exactly one general completion item, got %#v\nstderr:\n%s", symResp["result"], stderr.String())
	}
	symItem, _ := symItems[0].(map[string]any)
	if symItem["label"] != "helper" {
		t.Fatalf("completion label = %v, want \"helper\"", symItem["label"])
	}

	// documentSymbol: sym.sv has 5 top-level declarations (2 enum members,
	// a typedef, a function, a module), none nested.
	docSymResp := client.request("textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": "file:///sym.sv"},
	})
	if docSymResp["error"] != nil {
		t.Fatalf("textDocument/documentSymbol returned an error: %v\nstderr:\n%s", docSymResp["error"], stderr.String())
	}
	docSyms, ok := docSymResp["result"].([]any)
	if !ok || len(docSyms) != 5 {
		t.Fatalf("expected 5 document symbols, got %#v\nstderr:\n%s", docSymResp["result"], stderr.String())
	}

	// foldingRange: only "helper" (function) and "top2" (module) are
	// containers.
	foldResp := client.request("textDocument/foldingRange", map[string]any{
		"textDocument": map[string]any{"uri": "file:///sym.sv"},
	})
	if foldResp["error"] != nil {
		t.Fatalf("textDocument/foldingRange returned an error: %v\nstderr:\n%s", foldResp["error"], stderr.String())
	}
	folds, ok := foldResp["result"].([]any)
	if !ok || len(folds) != 2 {
		t.Fatalf("expected 2 folding ranges, got %#v\nstderr:\n%s", foldResp["result"], stderr.String())
	}

	// workspace/symbol
	wsSymResp := client.request("workspace/symbol", map[string]any{"query": "helper"})
	if wsSymResp["error"] != nil {
		t.Fatalf("workspace/symbol returned an error: %v\nstderr:\n%s", wsSymResp["error"], stderr.String())
	}
	wsSyms, ok := wsSymResp["result"].([]any)
	if !ok || len(wsSyms) != 1 {
		t.Fatalf("expected exactly one workspace symbol, got %#v\nstderr:\n%s", wsSymResp["result"], stderr.String())
	}

	// hover over "leaf" in top.sv.
	hoverResp := client.request("textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": "file:///top.sv"},
		"position":     map[string]any{"line": 1, "character": 3},
	})
	if hoverResp["error"] != nil {
		t.Fatalf("textDocument/hover returned an error: %v\nstderr:\n%s", hoverResp["error"], stderr.String())
	}
	hoverResult, _ := hoverResp["result"].(map[string]any)
	hoverContents, _ := hoverResult["contents"].(map[string]any)
	hoverValue, _ := hoverContents["value"].(string)
	want := "module leaf(\n  input logic clk,\n  input logic rst_n\n)"
	if !strings.Contains(hoverValue, want) {
		t.Fatalf("hover value = %q, want it to contain %q", hoverValue, want)
	}

	// documentHighlight over "leaf" in top.sv -- one occurrence there.
	highlightResp := client.request("textDocument/documentHighlight", map[string]any{
		"textDocument": map[string]any{"uri": "file:///top.sv"},
		"position":     map[string]any{"line": 1, "character": 3},
	})
	if highlightResp["error"] != nil {
		t.Fatalf("textDocument/documentHighlight returned an error: %v\nstderr:\n%s", highlightResp["error"], stderr.String())
	}
	highlights, ok := highlightResp["result"].([]any)
	if !ok || len(highlights) != 1 {
		t.Fatalf("expected exactly one highlight, got %#v\nstderr:\n%s", highlightResp["result"], stderr.String())
	}

	// references (including declaration): leaf's declaration in leaf.sv
	// plus its use in top.sv.
	refResp := client.request("textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": "file:///top.sv"},
		"position":     map[string]any{"line": 1, "character": 3},
		"context":      map[string]any{"includeDeclaration": true},
	})
	if refResp["error"] != nil {
		t.Fatalf("textDocument/references returned an error: %v\nstderr:\n%s", refResp["error"], stderr.String())
	}
	refs, ok := refResp["result"].([]any)
	if !ok || len(refs) != 2 {
		t.Fatalf("expected 2 references, got %#v\nstderr:\n%s", refResp["result"], stderr.String())
	}

	// rename "helper" in sym.sv -- a single-occurrence, single-file edit.
	renameResp := client.request("textDocument/rename", map[string]any{
		"textDocument": map[string]any{"uri": "file:///sym.sv"},
		"position":     map[string]any{"line": 1, "character": 16},
		"newName":      "helper2",
	})
	if renameResp["error"] != nil {
		t.Fatalf("textDocument/rename returned an error: %v\nstderr:\n%s", renameResp["error"], stderr.String())
	}
	renameResult, _ := renameResp["result"].(map[string]any)
	changes, _ := renameResult["changes"].(map[string]any)
	symEdits, _ := changes["file:///sym.sv"].([]any)
	if len(changes) != 1 || len(symEdits) != 1 {
		t.Fatalf("expected exactly one edit in sym.sv, got %#v\nstderr:\n%s", renameResult, stderr.String())
	}

	// Scope restriction: two modules with an unrelated, same-named
	// internal helper -- references from within mod_a must not pull in
	// mod_b's helper.
	client.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": "file:///mod_a.sv", "languageId": "systemverilog", "version": 1,
			"text": "module mod_a;\n  function void helper();\n  endfunction\n  initial helper();\nendmodule\n",
		},
	})
	client.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": "file:///mod_b.sv", "languageId": "systemverilog", "version": 1,
			"text": "module mod_b;\n  function void helper();\n  endfunction\n  initial helper();\nendmodule\n",
		},
	})
	scopedRefResp := client.request("textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": "file:///mod_a.sv"},
		"position":     map[string]any{"line": 3, "character": 12},
		"context":      map[string]any{"includeDeclaration": true},
	})
	if scopedRefResp["error"] != nil {
		t.Fatalf("textDocument/references (scoped) returned an error: %v\nstderr:\n%s", scopedRefResp["error"], stderr.String())
	}
	scopedRefs, ok := scopedRefResp["result"].([]any)
	if !ok || len(scopedRefs) != 2 {
		t.Fatalf("expected exactly 2 references within mod_a, got %#v\nstderr:\n%s", scopedRefResp["result"], stderr.String())
	}
	for _, r := range scopedRefs {
		loc, _ := r.(map[string]any)
		if loc["uri"] != "file:///mod_a.sv" {
			t.Fatalf("expected only mod_a.sv references, got %#v", scopedRefs)
		}
	}

	shutdownResp := client.request("shutdown", nil)
	if shutdownResp["error"] != nil {
		t.Fatalf("shutdown returned an error: %v\nstderr:\n%s", shutdownResp["error"], stderr.String())
	}

	client.notify("exit", nil)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process exited with error (want exit code 0): %v\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("process did not exit within 5s after exit notification\nstderr:\n%s", stderr.String())
	}
}

// TestEndToEndFilelistWorkspaceWithInclude drives the real binary over a
// filelist-configured, multi-file workspace exercising the svparse-backed
// engine's cross-file `include support end to end: +incdir+ capture,
// `include resolution attributing a shared header's declarations to its
// own file, goto-definition and hover reflecting real parsed types, and
// the file watcher's dependency-aware cascade re-indexing top.sv after an
// on-disk edit to the header it includes -- the scenario every layer of
// this milestone's unit/integration tests targets individually, run here
// through the actual stdio/JSON-RPC surface an editor uses.
func TestEndToEndFilelistWorkspaceWithInclude(t *testing.T) {
	bin := buildBinary(t)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".sigils.json"), []byte(`{"filelists": ["top.f"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "common"), 0o755); err != nil {
		t.Fatal(err)
	}
	defsPath := filepath.Join(root, "common", "defs.svh")
	if err := os.WriteFile(defsPath, []byte("typedef logic [7:0] bus_t;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "top.sv"),
		[]byte("`include \"defs.svh\"\nmodule top;\n  bus_t data;\nendmodule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "top.f"), []byte("+incdir+common\ntop.sv\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	client := newRPCClient(t, stdin, stdout)
	rootURI := "file://" + root
	initResp := client.request("initialize", map[string]any{
		"rootUri":      rootURI,
		"capabilities": map[string]any{},
		"workspaceFolders": []map[string]any{
			{"uri": rootURI, "name": "root"},
		},
	})
	if initResp["error"] != nil {
		t.Fatalf("initialize returned an error: %v\nstderr:\n%s", initResp["error"], stderr.String())
	}
	client.notify("initialized", map[string]any{})

	topURI := rootURI + "/top.sv"
	defsURI := rootURI + "/common/defs.svh"

	// Background indexing (buildIndex, kicked off from Initialize) hasn't
	// necessarily finished yet -- poll goto-definition on "bus_t" (line 2,
	// "  bus_t data;") until it resolves rather than assuming a fixed
	// delay is enough.
	var lastResp map[string]any
	definitionResolvesTo := func(wantURI string) bool {
		lastResp = client.request("textDocument/definition", map[string]any{
			"textDocument": map[string]any{"uri": topURI},
			"position":     map[string]any{"line": 2, "character": 4},
		})
		locs, ok := lastResp["result"].([]any)
		if !ok || len(locs) != 1 {
			return false
		}
		loc, _ := locs[0].(map[string]any)
		return loc["uri"] == wantURI
	}
	deadline := time.Now().Add(10 * time.Second)
	for !definitionResolvesTo(defsURI) {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill() // read stderr only once the process can no longer write to it concurrently
			t.Fatalf("goto-definition on bus_t never resolved to defs.svh (indexing/`include resolution), last response: %#v\nstderr:\n%s", lastResp, stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Hover on bus_t's own declaration in defs.svh shows its real parsed
	// type (svparse-backed, not a token guess).
	hoverResp := client.request("textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": defsURI},
		"position":     map[string]any{"line": 0, "character": 20},
	})
	if hoverResp["error"] != nil {
		t.Fatalf("textDocument/hover returned an error: %v\nstderr:\n%s", hoverResp["error"], stderr.String())
	}
	hoverResult, _ := hoverResp["result"].(map[string]any)
	hoverContents, _ := hoverResult["contents"].(map[string]any)
	hoverValue, _ := hoverContents["value"].(string)
	if !strings.Contains(hoverValue, "typedef logic [7:0] bus_t") {
		t.Fatalf("hover value = %q, want it to mention \"typedef logic [7:0] bus_t\"", hoverValue)
	}

	// Editing defs.svh on disk and letting the debounced watch cascade
	// fire should reindex top.sv too -- bus_t going away makes this
	// directly observable via goto-definition again.
	if err := os.WriteFile(defsPath, []byte("// bus_t removed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		resp := client.request("textDocument/definition", map[string]any{
			"textDocument": map[string]any{"uri": topURI},
			"position":     map[string]any{"line": 2, "character": 4},
		})
		if locs, ok := resp["result"].([]any); !ok || len(locs) == 0 {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("expected the cascade to reindex top.sv after defs.svh changed on disk\nstderr:\n%s", stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	shutdownResp := client.request("shutdown", nil)
	if shutdownResp["error"] != nil {
		t.Fatalf("shutdown returned an error: %v\nstderr:\n%s", shutdownResp["error"], stderr.String())
	}
	client.notify("exit", nil)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process exited with error (want exit code 0): %v\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("process did not exit within 5s after exit notification\nstderr:\n%s", stderr.String())
	}
}

// TestEndToEndDiagnostics drives the real binary through the diagnostics
// feature's whole point: opening a file with a genuine syntax error
// produces a textDocument/publishDiagnostics push (unprompted -- the
// client never asks for it) with a non-empty diagnostics list; fixing the
// file via didChange produces a following push with an empty one,
// clearing the squiggle.
func TestEndToEndDiagnostics(t *testing.T) {
	bin := buildBinary(t)

	workspaceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceRoot, ".sigils.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootURI := "file://" + workspaceRoot

	cmd := exec.Command(bin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	client := newRPCClient(t, stdin, stdout)
	initResp := client.request("initialize", map[string]any{
		"rootUri":      rootURI,
		"capabilities": map[string]any{},
		"workspaceFolders": []map[string]any{
			{"uri": rootURI, "name": "root"},
		},
	})
	if initResp["error"] != nil {
		t.Fatalf("initialize returned an error: %v\nstderr:\n%s", initResp["error"], stderr.String())
	}
	client.notify("initialized", map[string]any{})

	uri := "file:///bad.sv"
	client.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": uri, "languageId": "systemverilog", "version": 1, "text": "42;\nmodule top; endmodule\n",
		},
	})

	msg, ok := client.waitForNotification("textDocument/publishDiagnostics", 5*time.Second)
	if !ok {
		_ = cmd.Process.Kill()
		t.Fatalf("expected a publishDiagnostics notification after didOpen with a syntax error\nstderr:\n%s", stderr.String())
	}
	params, _ := msg["params"].(map[string]any)
	if params["uri"] != uri {
		t.Fatalf("publishDiagnostics uri = %v, want %v", params["uri"], uri)
	}
	diags, _ := params["diagnostics"].([]any)
	if len(diags) == 0 {
		t.Fatalf("expected a non-empty diagnostics list for the malformed '42;', got %+v", params)
	}
	first, _ := diags[0].(map[string]any)
	if first["severity"] != float64(1) { // DiagnosticSeverityError == 1
		t.Fatalf("expected DiagnosticSeverityError, got %+v", first)
	}
	if msg, _ := first["message"].(string); msg == "" {
		t.Fatalf("expected a non-empty diagnostic message, got %+v", first)
	}

	client.notify("textDocument/didChange", map[string]any{
		"textDocument": map[string]any{"uri": uri, "version": 2},
		"contentChanges": []map[string]any{
			{"text": "module top; endmodule\n"},
		},
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		msg, ok := client.waitForNotification("textDocument/publishDiagnostics", time.Until(deadline))
		if !ok {
			_ = cmd.Process.Kill()
			t.Fatalf("expected a publishDiagnostics notification clearing the fixed file\nstderr:\n%s", stderr.String())
		}
		params, _ := msg["params"].(map[string]any)
		if params["uri"] != uri {
			continue // a stray notification for a different file, keep waiting
		}
		diags, _ := params["diagnostics"].([]any)
		if len(diags) == 0 {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("expected diagnostics to clear after fixing the syntax error, got %+v", params)
		}
	}

	shutdownResp := client.request("shutdown", nil)
	if shutdownResp["error"] != nil {
		t.Fatalf("shutdown returned an error: %v\nstderr:\n%s", shutdownResp["error"], stderr.String())
	}
	client.notify("exit", nil)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process exited with error (want exit code 0): %v\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("process did not exit within 5s after exit notification\nstderr:\n%s", stderr.String())
	}
}

func TestExitWithoutShutdownYieldsNonZeroExitCode(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	client := newRPCClient(t, stdin, stdout)
	initResp := client.request("initialize", map[string]any{"capabilities": map[string]any{}})
	if initResp["error"] != nil {
		t.Fatalf("initialize returned an error: %v\nstderr:\n%s", initResp["error"], stderr.String())
	}
	client.notify("exit", nil)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		exitErr, ok := err.(*exec.ExitError)
		if !ok || exitErr.ExitCode() != 1 {
			t.Fatalf("expected exit code 1 (no shutdown before exit), got err=%v\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("process did not exit within 5s after exit notification\nstderr:\n%s", stderr.String())
	}
}
