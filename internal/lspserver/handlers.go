package lspserver

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/jfetkotto/sigils/internal/document"
	"github.com/jfetkotto/sigils/internal/sv"
	"github.com/jfetkotto/sigils/internal/workspace"
)

const serverName = "sigils"
const serverVersion = "0.0.1"

// glspCtx (not "context" -- shadowing the stdlib context package used
// below for the background indexing goroutine) carries Notify, captured
// once via setNotify so publishDiagnostics can push
// textDocument/publishDiagnostics from background goroutines too, not
// just from within a request handler. glspCtx is nil in some existing
// tests that don't exercise notifications; publishDiagnostics already
// treats an unset notify as a no-op, so that's fine.
func (s *Server) Initialize(glspCtx *glsp.Context, params *protocol.InitializeParams) (any, error) {
	if glspCtx != nil {
		s.setNotify(glspCtx.Notify)
	}

	root, err := workspace.FindRoot(initializeSearchStart(params))
	if err != nil {
		s.Log.Warningf("could not locate workspace root: %s; continuing without one", err)
		root = ""
	}

	var cfg workspace.Config
	if root != "" {
		if cfg, err = workspace.LoadConfig(root); err != nil {
			s.Log.Warningf("failed to load %s: %s", workspace.ConfigFileName, err)
		}
	}

	var discoverer workspace.Discoverer
	if root != "" && len(cfg.Filelists) > 0 {
		discoverer = workspace.NewFilelistDiscoverer(workspace.Root{Path: root}, cfg, func(msg string) { s.Log.Warning(msg) })
	} else {
		discoverer = workspace.NewStaticDiscoverer(workspaceRoots(params))
	}

	ctx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	// glsp gates only NON-initialize methods before initialization, so a
	// client that re-handshakes on the same connection lands here twice.
	// Without cancelling the first run, its worker pool and its fsnotify
	// watcher (one open fd per watched directory) leak for the life of the
	// process: Shutdown can only ever cancel the newest.
	prevCancel := s.watchCancel
	s.root = root
	s.cfg = cfg
	s.discoverer = discoverer
	s.watchCancel = cancel
	s.snippetSupport = clientSupportsSnippets(params)
	s.mu.Unlock()
	if prevCancel != nil {
		s.Log.Warning("initialize received again; cancelling the previous indexing pass")
		prevCancel()
	}

	// Indexing can mean scanning a large chip workspace's worth of files,
	// so it runs in the background rather than blocking the handshake.
	// indexAndWatch keeps running afterwards, watching for on-disk changes,
	// until Shutdown cancels ctx.
	go s.indexAndWatch(ctx, discoverer)

	openClose := true
	change := protocol.TextDocumentSyncKindFull
	version := serverVersion

	return protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{
			TextDocumentSync: &protocol.TextDocumentSyncOptions{
				OpenClose: &openClose,
				Change:    &change,
			},
			DefinitionProvider:  true,
			DeclarationProvider: true,
			CompletionProvider: &protocol.CompletionOptions{
				TriggerCharacters: []string{"."},
			},
			DocumentSymbolProvider:    true,
			FoldingRangeProvider:      true,
			WorkspaceSymbolProvider:   true,
			HoverProvider:             true,
			DocumentHighlightProvider: true,
			ReferencesProvider:        true,
			RenameProvider:            true,
		},
		ServerInfo: &protocol.InitializeResultServerInfo{
			Name:    serverName,
			Version: &version,
		},
	}, nil
}

// clientSupportsSnippets reports whether the client can accept a
// snippet-format completion (with tab stops like "$0"), per its declared
// capabilities. Captured once at Initialize since CompletionParams itself
// doesn't carry client capabilities.
func clientSupportsSnippets(params *protocol.InitializeParams) bool {
	td := params.Capabilities.TextDocument
	if td == nil || td.Completion == nil || td.Completion.CompletionItem == nil {
		return false
	}
	support := td.Completion.CompletionItem.SnippetSupport
	return support != nil && *support
}

func (s *Server) Initialized(context *glsp.Context, params *protocol.InitializedParams) error {
	s.Log.Info("initialized")
	return nil
}

func (s *Server) Shutdown(context *glsp.Context) error {
	s.mu.Lock()
	s.shutdownReceived = true
	cancel := s.watchCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.Log.Info("shutdown requested")
	return nil
}

func (s *Server) SetTrace(context *glsp.Context, params *protocol.SetTraceParams) error {
	protocol.SetTraceValue(params.Value)
	return nil
}

func (s *Server) TextDocumentDidOpen(context *glsp.Context, params *protocol.DidOpenTextDocumentParams) error {
	doc := params.TextDocument
	s.docs.Open(document.URI(doc.URI), doc.LanguageID, doc.Version, doc.Text)
	// The document store takes anything the client opens, so hover and
	// completion work in that buffer; the workspace index takes only real
	// files -- see isFileURI.
	if isFileURI(doc.URI) {
		s.publishDiagnostics(s.index.SetFile(doc.URI, doc.Text))
	}
	s.Log.Infof("opened %s", doc.URI)
	return nil
}

func (s *Server) TextDocumentDidChange(context *glsp.Context, params *protocol.DidChangeTextDocumentParams) error {
	if len(params.ContentChanges) == 0 {
		return nil
	}

	// We only advertise full-document sync, so this should always be a
	// TextDocumentContentChangeEventWhole; tolerate the incremental shape
	// too rather than erroring, since taking its Text as the replacement
	// is still the most useful thing to do with a misbehaving client.
	var text string
	switch change := params.ContentChanges[len(params.ContentChanges)-1].(type) {
	case protocol.TextDocumentContentChangeEventWhole:
		text = change.Text
	case protocol.TextDocumentContentChangeEvent:
		s.Log.Warningf("received an incremental change for %s despite advertising full-document sync; using its text as the full replacement", params.TextDocument.URI)
		text = change.Text
	default:
		return fmt.Errorf("unrecognized content change type %T", change)
	}

	if !s.docs.ApplyFullChange(document.URI(params.TextDocument.URI), params.TextDocument.Version, text) {
		s.Log.Warningf("didChange for unknown document %s", params.TextDocument.URI)
	}
	if isFileURI(string(params.TextDocument.URI)) {
		s.publishDiagnostics(s.index.SetFile(string(params.TextDocument.URI), text))
	}
	return nil
}

func (s *Server) TextDocumentDidClose(context *glsp.Context, params *protocol.DidCloseTextDocumentParams) error {
	uri := params.TextDocument.URI
	s.docs.Close(document.URI(uri))
	s.Log.Infof("closed %s", uri)

	// Deliberately not removing the file from s.index: it still exists on
	// disk, and other files' goto-definition results should keep resolving
	// into it after the editor buffer closes. It IS re-scanned from disk,
	// though -- the index still holds whatever the buffer's last state
	// was, which reflects discarded edits if the user closed without
	// saving. Since the file's own content never changed on disk, no
	// watch event would ever come along to correct that on its own.
	path, err := uriToPath(string(uri))
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// Deleted or otherwise unreadable -- leave the index as-is; the
		// next workspace rebuild's staleness pass is what actually
		// removes a file no longer on disk.
		return nil
	}
	s.publishDiagnostics(s.index.SetFile(string(uri), string(data)))
	return nil
}

// TextDocumentDefinition resolves the identifier under the cursor against
// the declaration index. See sv.Index's doc comment for exactly what
// "scope-aware" means here -- qualified (Pkg::name) and scope-chain
// resolution, not full LRM-compliant elaboration. It prefers a real body
// over an extern/pure-virtual/DPI-import prototype when both are known for
// the same name -- see sv.Index.FindDefinition.
func (s *Server) TextDocumentDefinition(context *glsp.Context, params *protocol.DefinitionParams) (any, error) {
	return s.resolveWordAt(params.TextDocument.URI, params.Position, s.index.FindDefinition)
}

// TextDocumentDeclaration is TextDocumentDefinition's counterpart: it
// prefers an extern/pure-virtual/DPI-import prototype over a real body
// when both are known for the same name -- see sv.Index.FindDeclaration.
// For every other SV construct (module, typedef, in-body function/task,
// ...) there's no meaningful declaration/definition split, so the two
// handlers return the same thing.
func (s *Server) TextDocumentDeclaration(context *glsp.Context, params *protocol.DeclarationParams) (any, error) {
	return s.resolveWordAt(params.TextDocument.URI, params.Position, s.index.FindDeclaration)
}

// resolveWordAt extracts the identifier at uri/position and resolves it
// via resolve (either sv.Index.FindDefinition or FindDeclaration -- both
// share this exact signature), formatting the result as LSP locations.
//
// An `include directive's path is checked first: it's the one place in a
// SystemVerilog file where the source text really is a file reference, and
// the resolved target is already recorded in the index (see
// sv.Index.IncludesOf) -- often reachable no other way, since the path may
// resolve through a filelist's "+incdir+" rather than relative to the
// including file. A cursor there never falls through to name resolution,
// which would otherwise answer "`include \"pa_cfg.svh\"" with an unrelated
// module named pa_cfg (see sv.IncludePathIn).
//
// A named port connection's port name (".clk(" at an instantiation site)
// or a parameter override's name (".WIDTH(") is checked next, mirroring
// TextDocumentCompletion's own precedence -- neither has a scope-chain
// link to the instantiated module at all (see
// sv.Index.FindInstantiationPort's doc comment), so resolve would never
// find either on its own.
func (s *Server) resolveWordAt(
	uri string,
	position protocol.Position,
	resolve func(uri string, line, character int, word, qualifier string, hasQualifier bool) ([]sv.Location, bool),
) (any, error) {
	text, ok := s.textForURI(uri)
	if !ok {
		return nil, nil
	}
	line, character := int(position.Line), int(position.Character)

	toks := sv.Lex(text) // lexed once, shared by every probe below -- see sv.Tokens

	if path, inDirective, ok := sv.IncludePathIn(toks, line, character); inDirective {
		if !ok {
			return nil, nil
		}
		return includeLocations(s.index.IncludesOf(uri), path), nil
	}

	word, start, ok := sv.WordAt(text, line, character)
	if !ok {
		return nil, nil
	}

	if moduleName, ok := sv.InstantiationPortNameIn(toks, line, word, start); ok {
		if locs, ok := s.index.FindInstantiationPort(moduleName, word); ok {
			return formatLocations(locs, word), nil
		}
	}
	if moduleName, ok := sv.InstantiationParamNameIn(toks, line, word, start); ok {
		if locs, ok := s.index.FindInstantiationPort(moduleName, word); ok {
			return formatLocations(locs, word), nil
		}
	}

	if receiver, receiverStart, ok := sv.DotReceiverAt(text, line, start); ok {
		if loc, ok := s.structFieldLocation(uri, text, line, character, receiver, receiverStart, word); ok {
			return []protocol.Location{{URI: protocol.DocumentUri(loc.URI), Range: nameRange(loc.Line, loc.Character, word)}}, nil
		}
		if loc, ok := s.interfaceMemberLocation(uri, text, line, character, receiver, receiverStart, word); ok {
			return []protocol.Location{{URI: protocol.DocumentUri(loc.URI), Range: nameRange(loc.Line, loc.Character, word)}}, nil
		}
		if loc, ok := s.modportLocation(uri, text, line, character, receiver, receiverStart, word); ok {
			return []protocol.Location{{URI: protocol.DocumentUri(loc.URI), Range: nameRange(loc.Line, loc.Character, word)}}, nil
		}
	}

	qualifier, hasQualifier := sv.QualifierAt(text, line, start)

	locs, ok := resolve(uri, line, character, word, qualifier, hasQualifier)
	if !ok {
		return nil, nil
	}
	return formatLocations(locs, word), nil
}

// structFieldLocation resolves "receiver.word" to the field's own
// declaration inside its struct/union typedef, mirroring structFieldHover
// on the query side.
//
// Without it, goto-definition was the last cursor handler still resolving
// a field access by plain name: hover (structFieldHover) and
// references/rename/highlight (ScopedOccurrencesForStructField) both go
// through the receiver's type, so on "link.addr" where some unrelated
// module also declares a port "addr", hover was right and F12 jumped into
// the unrelated module -- silently wrong rather than simply empty.
func (s *Server) structFieldLocation(uri, text string, line, character int, receiver string, receiverStart int, word string) (sv.Location, bool) {
	qualifier, hasQualifier := sv.QualifierAt(text, line, receiverStart)
	recv, ok := s.index.HoverInfo(uri, line, character, receiver, qualifier, hasQualifier)
	if !ok || recv.TypeName == "" {
		return sv.Location{}, false
	}
	return s.index.StructFieldLocation(recv.TypeName, word)
}

// interfaceMemberLocation resolves "receiver.word" to the member's own
// declaration inside receiver's own interface, mirroring
// structFieldLocation -- gated on the same recv.TypeName != "" check,
// since receiver here is a typed instance, not the interface type itself
// (see modportLocation for that case). Unlike a struct/union field, an
// interface member's own Declaration already carries a real position, so
// this needs no analogue of struct-field goto-definition's missing-target
// problem.
func (s *Server) interfaceMemberLocation(uri, text string, line, character int, receiver string, receiverStart int, word string) (sv.Location, bool) {
	qualifier, hasQualifier := sv.QualifierAt(text, line, receiverStart)
	recv, ok := s.index.HoverInfo(uri, line, character, receiver, qualifier, hasQualifier)
	if !ok || recv.TypeName == "" {
		return sv.Location{}, false
	}
	locs, ok := s.index.FindInterfaceMember(recv.TypeName, word)
	if !ok || len(locs) == 0 {
		return sv.Location{}, false
	}
	return locs[0], true
}

// modportLocation resolves "receiver.word" to a modport declaration
// inside receiver's own interface, mirroring structFieldLocation --
// except gated on recv.Kind == KindInterface rather than recv.TypeName !=
// "", since an interface name IS a type (it has none of its own to point
// at).
func (s *Server) modportLocation(uri, text string, line, character int, receiver string, receiverStart int, word string) (sv.Location, bool) {
	qualifier, hasQualifier := sv.QualifierAt(text, line, receiverStart)
	recv, ok := s.index.HoverInfo(uri, line, character, receiver, qualifier, hasQualifier)
	if !ok || recv.Kind != sv.KindInterface {
		return sv.Location{}, false
	}
	locs, ok := s.index.FindModport(recv.Name, word)
	if !ok || len(locs) == 0 {
		return sv.Location{}, false
	}
	return locs[0], true
}

// includeLocations matches an `include's written path against the URIs the
// file's last scan actually resolved its includes to, by path suffix, and
// points at the start of each match.
//
// Reusing the recorded resolution rather than resolving the path again means
// no filesystem access, and guarantees goto-definition lands on the same file
// the index really read. More than one match (the same basename included from
// two directories) is a fine answer -- LSP takes a list. An include that never
// resolved, because it sits in an `ifdef-excluded region or because the file
// is missing, simply has no entry, and its unresolved-include diagnostic
// already tells the user why.
func includeLocations(resolved []string, path string) []protocol.Location {
	path = strings.TrimPrefix(path, "./")
	if path == "" {
		return nil
	}
	var out []protocol.Location
	for _, uri := range resolved {
		if uri == path || strings.HasSuffix(uri, "/"+path) {
			out = append(out, protocol.Location{URI: protocol.DocumentUri(uri)})
		}
	}
	return out
}

func formatLocations(locs []sv.Location, word string) []protocol.Location {
	results := make([]protocol.Location, 0, len(locs))
	for _, loc := range locs {
		results = append(results, protocol.Location{URI: loc.URI, Range: nameRange(loc.Line, loc.Character, word)})
	}
	return results
}

// maxSymbolCompletions bounds how many general symbol-completion
// candidates get sent back per request, so a short, common prefix in a
// large workspace doesn't dump its entire symbol table on every keystroke.
// A truncated result is returned as a protocol.CompletionList with
// IsIncomplete set (see completionResult) so the client re-queries as the
// user keeps typing, rather than treating the truncated slice as the
// complete, final answer and only ever filtering client-side among what
// was already sent -- which would make anything alphabetically past the
// cut permanently unreachable for that prefix.
const maxSymbolCompletions = 200

// TextDocumentCompletion offers four kinds of completion:
//  1. Named-port-connection completion when the cursor is inside a
//     module/interface/program instantiation's port-list parens -- see
//     sv.InstantiationContextAt.
//  2. Parameter-override completion when the cursor is inside an
//     instantiation's "#( ... )" parameter list instead -- see
//     sv.InstantiationParamContextAt. Reuses portCompletionItems with
//     wantCallSnippet=true (it's generic over the []sv.Port it's given);
//     only "localparam" entries are excluded from the candidates
//     (sv.Index.Params), since those can never legally appear on the
//     right of ".name(value)" here.
//  3. Struct/union member (or interface-instance signal) completion when
//     the cursor directly follows "receiver." and receiver resolves (via
//     sv.Index.HoverInfo, the same resolution goto-definition and hover
//     already use) to a port or variable whose own declared type is
//     itself a struct/union typedef or an interface -- see
//     structMemberCompletionItems. Deliberately narrow:
//     only a *direct* struct/union typedef reference is resolved, not a
//     chain of plain alias typedefs that eventually names one, not nested
//     field-of-field access ("a.b.c" where b is itself struct-typed --
//     sv.Port, reused for sv.Declaration.Fields, has no structured type of
//     its own to recurse into), and not class member access. Those remain
//     future work.
//  4. Otherwise, general prefix-based identifier completion against every
//     declared symbol in the workspace index (types, functions/tasks,
//     classes, packages, enum members, ...) -- see sv.Index.CompleteSymbols.
//     This isn't type- or scope-aware: an enum member from a wholly
//     unrelated type, or a function that isn't actually reachable from
//     here, can still show up as a candidate, since confirming reachability
//     needs the same elaboration this lexical index doesn't do. This is
//     also still what a dot-qualified receiver that ISN'T a resolved
//     struct/union (a plain "logic" variable, an unresolved name, ...)
//     falls back to -- an empty typed prefix there (cursor right after the
//     dot) still means no completions at all, same as any other
//     empty-prefix request here.
//
// No path attempts keyword completion.
func (s *Server) TextDocumentCompletion(context *glsp.Context, params *protocol.CompletionParams) (any, error) {
	text, ok := s.textForURI(params.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	line, character := int(params.Position.Line), int(params.Position.Character)

	toks := sv.Lex(text) // lexed once, shared by both context probes below -- see sv.Tokens
	if moduleName, connected, ok := sv.InstantiationContextIn(toks, line, character); ok {
		var items []protocol.CompletionItem
		if ports, ok := s.index.Ports(moduleName); ok {
			items = s.portCompletionItems(text, line, character, ports, connected, true)
		}
		return completionResult(items, false), nil
	}

	if moduleName, connected, ok := sv.InstantiationParamContextIn(toks, line, character); ok {
		var items []protocol.CompletionItem
		if params, ok := s.index.Params(moduleName); ok {
			items = s.portCompletionItems(text, line, character, params, connected, true)
		}
		return completionResult(items, false), nil
	}

	if items, ok := s.structMemberCompletionItems(text, params.TextDocument.URI, line, character); ok {
		return completionResult(items, false), nil
	}

	items, truncated := s.symbolCompletionItems(text, line, character)
	return completionResult(items, truncated), nil
}

// completionResult returns a true nil any for an empty, non-truncated
// items slice, rather than an any wrapping a nil []protocol.CompletionItem
// -- the latter is non-nil at the interface level even though it carries
// no items, which is a needless footgun for anything (tests included)
// that checks the result against a literal nil. When truncated (see
// maxSymbolCompletions), items is wrapped in a protocol.CompletionList
// with IsIncomplete set instead of returned as a bare slice, which LSP
// clients otherwise treat as a complete result -- the port-completion
// path (truncated always false) never needs this, since it never caps
// its result.
func completionResult(items []protocol.CompletionItem, truncated bool) any {
	if truncated {
		return protocol.CompletionList{IsIncomplete: true, Items: items}
	}
	if len(items) == 0 {
		return nil
	}
	return items
}

// portCompletionItems renders ports as completion items to fill a
// ".name(...)" connection slot at an instantiation site (branches 1 and 2
// of TextDocumentCompletion) or, when wantCallSnippet is false, a bare
// ".name" struct/union field-access position (branch 3) -- the same
// []sv.Port shape covers both a module's port list and a struct's field
// list (see sv.Declaration.Fields), so this rendering is fully generic
// over which one it's given. wantCallSnippet controls only the snippet
// body appended after the name when the client supports snippets: true
// appends "($0)" (there's always a value to fill in a connection), false
// appends nothing at all (a struct field reference has no parens).
func (s *Server) portCompletionItems(text string, line, character int, ports []sv.Port, connected map[string]bool, wantCallSnippet bool) []protocol.CompletionItem {
	// An explicit TextEdit (rather than InsertText) makes the replaced
	// range unambiguous: the client already inserted the "." that
	// triggered completion into the buffer before this request was even
	// sent, and a client that doesn't treat "." as a word character may
	// not replace it -- relying on InsertText there would duplicate the
	// "." instead of reusing it.
	prefixStart, hasDot := sv.CompletionEditRange(text, line, character)
	editRange := completionRange(line, prefixStart, character)
	leadingDot := ""
	if !hasDot {
		leadingDot = "." // completion was invoked without the user typing "." first (e.g. manual invocation)
	}

	snippets := s.SnippetSupport()
	kind := protocol.CompletionItemKindField
	items := make([]protocol.CompletionItem, 0, len(ports))
	for _, p := range ports {
		if connected[p.Name] {
			continue
		}
		item := protocol.CompletionItem{Label: p.Name, Kind: &kind}
		if p.Detail != "" {
			detail := p.Detail
			item.Detail = &detail
		}

		var newText string
		if snippets && wantCallSnippet {
			newText = fmt.Sprintf("%s%s($0)", leadingDot, p.Name)
			format := protocol.InsertTextFormatSnippet
			item.InsertTextFormat = &format
		} else {
			newText = leadingDot + p.Name
		}
		item.TextEdit = &protocol.TextEdit{Range: editRange, NewText: newText}
		items = append(items, item)
	}
	return items
}

// structMemberCompletionItems offers completion for "receiver." where
// receiver is a port or local variable whose own declared type directly
// names a struct or union typedef, or an interface -- e.g. completing
// "st_bundle." to that struct's field names, or "uin_bus." to an
// interface-typed port's own signal names. It resolves receiver the same
// way hover does (sv.WordAt + sv.QualifierAt + sv.Index.HoverInfo, passing
// the ORIGINAL cursor line/character for scope resolution, not receiver's
// own start column -- see hover.go's identical pattern) rather than
// introducing a second resolver, then looks its Declaration.TypeName up as
// a struct/union typedef via sv.Index.StructFields, falling back to
// sv.Index.InterfaceMembers when that type name is an interface instead.
// ok is false whenever neither path applies at all (no dot immediately
// precedes the typed prefix, no identifier immediately precedes the dot,
// receiver doesn't resolve, or its type is neither a struct/union typedef
// nor an interface) -- the caller falls back to general symbol completion
// in that case, not an empty result, since a bare "." after something
// else entirely (a plain "logic" variable, an unresolved name) is still a
// valid, if unscoped, completion request, same as before this existed.
func (s *Server) structMemberCompletionItems(text, uri string, line, character int) ([]protocol.CompletionItem, bool) {
	prefixStart, hasDot := sv.CompletionEditRange(text, line, character)
	if !hasDot {
		return nil, false
	}
	receiver, receiverStart, ok := sv.WordAt(text, line, prefixStart-1)
	if !ok {
		return nil, false
	}
	qualifier, hasQualifier := sv.QualifierAt(text, line, receiverStart)
	decl, ok := s.index.HoverInfo(uri, line, character, receiver, qualifier, hasQualifier)
	if !ok || decl.TypeName == "" {
		return nil, false
	}
	fields, ok := s.index.StructFields(decl.TypeName)
	if !ok {
		fields, ok = s.index.InterfaceMembers(decl.TypeName)
		if !ok {
			return nil, false
		}
	}
	return s.portCompletionItems(text, line, character, fields, nil, false), true
}

// symbolCompletionItems also reports whether the result was truncated to
// maxSymbolCompletions, so the caller can mark the response incomplete
// (see completionResult) rather than let the client mistake a capped
// result for the full symbol table.
func (s *Server) symbolCompletionItems(text string, line, character int) ([]protocol.CompletionItem, bool) {
	prefixStart, _ := sv.CompletionEditRange(text, line, character)
	prefix := sv.LineSlice(text, line, prefixStart, character)
	if prefix == "" {
		// No typed prefix to filter by -- returning the whole workspace's
		// symbol table on every cursor move through blank space isn't
		// useful, so wait for at least one character.
		return nil, false
	}

	symbols, truncated := s.index.CompleteSymbols(prefix, maxSymbolCompletions)
	if len(symbols) == 0 {
		return nil, false
	}

	editRange := completionRange(line, prefixStart, character)
	items := make([]protocol.CompletionItem, 0, len(symbols))
	for _, sym := range symbols {
		kind := completionItemKindFor(sym.Kind)
		items = append(items, protocol.CompletionItem{
			Label:    sym.Name,
			Kind:     &kind,
			TextEdit: &protocol.TextEdit{Range: editRange, NewText: sym.Name},
		})
	}
	return items, truncated
}

func completionRange(line, start, end int) protocol.Range {
	return protocol.Range{
		Start: protocol.Position{Line: protocol.UInteger(line), Character: protocol.UInteger(start)},
		End:   protocol.Position{Line: protocol.UInteger(line), Character: protocol.UInteger(end)},
	}
}

func completionItemKindFor(kind sv.Kind) protocol.CompletionItemKind {
	switch kind {
	case sv.KindModule, sv.KindProgram, sv.KindPackage:
		return protocol.CompletionItemKindModule
	case sv.KindInterface:
		return protocol.CompletionItemKindInterface
	case sv.KindClass:
		return protocol.CompletionItemKindClass
	case sv.KindFunction, sv.KindTask:
		return protocol.CompletionItemKindFunction
	case sv.KindTypedef:
		return protocol.CompletionItemKindStruct
	case sv.KindEnumMember:
		return protocol.CompletionItemKindEnumMember
	case sv.KindVariable:
		return protocol.CompletionItemKindVariable
	case sv.KindPort:
		return protocol.CompletionItemKindProperty
	case sv.KindParameter:
		return protocol.CompletionItemKindConstant
	default:
		return protocol.CompletionItemKindText
	}
}

// textForURI returns the best-known text for uri: the live editor buffer
// if the document is open, otherwise its on-disk contents.
func (s *Server) textForURI(uri string) (string, bool) {
	if doc, ok := s.docs.Get(document.URI(uri)); ok {
		return doc.Text, true
	}
	path, err := uriToPath(uri)
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// initializeSearchStart picks the path FindRoot should start walking
// upward from: the first workspace folder if the client sent any
// (multi-root clients), falling back to the deprecated singular rootUri.
func initializeSearchStart(params *protocol.InitializeParams) string {
	if len(params.WorkspaceFolders) > 0 {
		if path, err := uriToPath(params.WorkspaceFolders[0].URI); err == nil {
			return path
		}
	}
	if params.RootURI != nil {
		if path, err := uriToPath(*params.RootURI); err == nil {
			return path
		}
	}
	return "."
}

func workspaceRoots(params *protocol.InitializeParams) []workspace.Root {
	if len(params.WorkspaceFolders) > 0 {
		roots := make([]workspace.Root, 0, len(params.WorkspaceFolders))
		for _, folder := range params.WorkspaceFolders {
			path, _ := uriToPath(folder.URI)
			roots = append(roots, workspace.Root{URI: folder.URI, Path: path})
		}
		return roots
	}
	if params.RootURI != nil {
		path, _ := uriToPath(*params.RootURI)
		return []workspace.Root{{URI: *params.RootURI, Path: path}}
	}
	return nil
}
