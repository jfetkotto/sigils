# sigils

A SystemVerilog language server, built on [`svparse`](https://github.com/jfetkotto/svparse) (lexer/preprocessor/parser) and [`glsp`](https://github.com/tliron/glsp) (LSP transport).

The name is a backronym (**S**till **I**ndexing, **G**ive **I**t **L**onger, **S**orry), or maybe it's that sigils are magical symbols, who knows?

## Install

```sh
go install github.com/jfetkotto/sigils@latest
```

Produces a `sigils` binary that speaks LSP over stdio.

## Features

- Go to definition / declaration, including on an `` `include`` path, which jumps to the file the include actually resolved to (through the filelist's `+incdir+` set, not a relative-path guess)
- Hover (the declaration, plus what the name or expression means: resolved type, width, and constant value, including the width an assignment evaluates it at)
- Completion: symbol names, and named port/parameter-connection completion (`.name(`) at instantiation sites, snippet-aware
- Document symbols (outline)
- Workspace symbol search
- Find references / rename, correctly scoped workspace-wide, including named instantiation-connection sites, resolved from either the port's declaration or any of its connection sites
- Document highlight
- Diagnostics (push model): svparse's own preprocessor/parser errors, republished on open, edit, and file-watcher-triggered reindex (including a cascade to every file that `` `include``s a changed one)
- Workspace/file discovery: finds the root by walking upward for a `.sigils.json` marker, then recursively expands its filelists into the full source set (symlink-aware, so a read-only network-cache repo checkout still indexes correctly)

## Configuration

A `.sigils.json` file at the workspace root marks the root (the same way `.git` does) and names one or more top-level filelists:

```json
{
  "filelists": ["path/to/top/filelist.f"]
}
```

Semantic diagnostics (unresolved modules, illegal assignments and drivers, duplicate definitions, and the other checks svparse's semantic model makes) are published a moment after the last edit, in addition to the immediate syntax errors. A file that does not parse shows only its syntax errors. To keep syntax errors only, set `"semanticDiagnostics": false`.

Each filelist entry may itself point at other filelists (nested, resolved recursively) or at source files directly. If no `.sigils.json` is found above the files you open, `sigils` still runs. It just has nothing indexed beyond whatever's currently open in the editor.

## Limitations

`sigils` indexes via svparse, which is **declaration-grade**, not a full elaborator:

- Expression/statement grammar and procedural bodies (`always`, function/task bodies, constraints) are recognized and skipped, not parsed
- Diagnostics are syntax errors (immediate) plus semantic and elaboration errors (debounced, whole workspace; the design is elaborated with every uninstantiated module as a top), push-only (no LSP 3.17 pull); lint rules are not published yet
- Unsupported constructs: non-ANSI port declarations, `specparam`, user-defined nettypes, class parameterization (`#(...)` on a class type, `extends`, or instantiation), `` `begin_keywords``/`` `end_keywords``, and a bare (non-typedef'd) enum/struct/union used inline as a variable's type
- A couple of narrow preprocessor edge cases: the escaped-quote-within-stringify macro operator, and an `` `include`` argument built via macro expansion
- POSIX paths only: `file://` URIs with a Windows drive letter aren't converted to native paths, so Windows isn't supported

## Helix example

Helix already ships a built-in `systemverilog`/`verilog` language definition; point it at `sigils` instead of its defaults and set `roots` so Helix's own project detection finds the right root:

```toml
[language-server.sigils]
command = "sigils"

[[language]]
name = "systemverilog"
language-servers = ["sigils"]
roots = [".sigils.json"]

[[language]]
name = "verilog"
language-servers = ["sigils"]
roots = [".sigils.json"]
```

`roots = [".sigils.json"]` matters: without it, Helix's own root detection can lock onto the nearest `.git` (e.g. an IP block's own repo) instead of the outer workspace root. `sigils` re-derives its own root independently either way, but this controls what Helix itself treats as the project root for its own UI purposes.
