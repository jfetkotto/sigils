# sigils

A SystemVerilog language server, built on [`svparse` v2](https://github.com/jfetkotto/svparse) (lexer, preprocessor, parser, semantic model, elaborator and linter) and [`glsp`](https://github.com/tliron/glsp) (LSP transport). svparse v2 is currently pinned at a pre-release (`v2.0.0-alpha.1`).

The name is a backronym (**S**till **I**ndexing, **G**ive **I**t **L**onger, **S**orry), or maybe it's that sigils are magical symbols, who knows?

## Install

```sh
go install github.com/jfetkotto/sigils@latest
```

Produces a `sigils` binary that speaks LSP over stdio. Flags: `-log-file <path>` (log there instead of stderr) and `-verbosity <n>` (-4 none to 2 debug). `sigils init` writes a config file (see below).

## Features

- Go to definition / declaration, including on an `` `include`` path, which jumps to the file the include actually resolved to (through the filelist's `+incdir+` set, not a relative-path guess)
- Semantic tokens: every name the semantic model resolves is coloured by what it is (package, type, parameter, port, signal, function, enum member, ...); `"semanticTokens": false` in `.sigils.json` turns it off
- Outline: modules, ports and declarations, plus `always`/`initial`/`final` processes (with their sensitivity) and generate constructs, with the declarations inside nested under them
- Hover (the declaration, plus what the name or expression means: resolved type, width, and constant value, including the width an assignment evaluates it at)
- Completion: symbol names, and named port/parameter-connection completion (`.name(`) at instantiation sites, snippet-aware
- Folding ranges
- Workspace symbol search
- Find references / rename, correctly scoped workspace-wide, including named instantiation-connection sites, resolved from either the port's declaration or any of its connection sites
- Document highlight
- Diagnostics (push model): svparse's preprocessor/parser errors plus semantic and lint findings (see Configuration), republished on open, edit, and file-watcher-triggered reindex (including a cascade to every file that `` `include``s a changed one)
- Workspace/file discovery: finds the root by walking upward for a `.sigils.json` marker, then recursively expands its filelists into the full source set (symlink-aware, so a read-only network-cache repo checkout still indexes correctly)

## Configuration

`sigils init [dir]` writes a `.sigils.json` for you (in `dir`, default the current directory). It spells out every option, with each lint rule at the level it runs at by default, so there is something complete to edit; replace the `path/to/filelist.f` placeholder with your top-level filelist (or pass `-filelist path`, repeatable, to name it up front). It will not overwrite an existing file without `-force`, and `-print` shows the file instead of writing it.

A `.sigils.json` file at the workspace root marks the root (the same way `.git` does) and names one or more top-level filelists:

```json
{
  "filelists": ["path/to/top/filelist.f"]
}
```

Semantic diagnostics (unresolved modules, illegal assignments and drivers, duplicate definitions, and the other checks svparse's semantic model makes) are published a moment after the last edit, in addition to the immediate syntax errors. A file that does not parse shows only its syntax errors. To keep syntax errors only, set `"semanticDiagnostics": false`.

On a large workspace (where the whole pass takes more than about 0.3 s) a change is analysed for the changed file only, so its errors and lint findings appear in about a second on a million-line design; the whole workspace is re-checked about two seconds after you stop typing, which refreshes other files the change affected. When a big workspace is first opened, the open files are answered before the rest.

svparse's lint rules (unused and undriven signals, latch inference, blocking assignments in sequential blocks, width truncation, overlapping drivers, and so on) are published with them, as warnings or hints. Tune them in the same file, using the rule names from `svparse lint --list-rules`:

```json
{
  "lint": {
    "rules": {"unused-signal": "off", "implicit-net": "error"},
    "ignore": ["third_party/**"]
  }
}
```

`"lint": {"enabled": false}` turns them off. A single finding can be silenced in the source with `// svparse: disable=<rule>` (this line), `disable-next-line=<rule>` or `disable-file=<rule>`. Lint stops with the semantic diagnostics when `"semanticDiagnostics": false`.

Go-to-definition and find-references can be answered from svparse's semantic model, which resolves names through scopes, imports, packages, instance ports and parameters and class members instead of by spelling, and tells apart same-named symbols (a local shadowing a port, one signal name in two modules). It is off by default; turn it on with `"semanticNavigation": true`. Where the model has no answer (a file that does not parse, an `import` line) the lexical lookup answers as before. The first request after an edit rebuilds the index of the whole workspace (about 11 ms on the benchmark workspace). Rename stays lexical.

Each filelist entry may itself point at other filelists (nested, resolved recursively) or at source files directly. If no `.sigils.json` is found above the files you open, `sigils` still runs. It just has nothing indexed beyond whatever's currently open in the editor.

## Limitations

- svparse v2 is pre-release; its known limits apply: one `` `default_nettype`` per design, `defparam` paths with indices, type parameters are not substituted in types, and arrays of interfaces are not resolved
- Class, constraint, covergroup, assertion and clocking-block bodies are parsed but not yet looked into by the undeclared-name check
- Rename is lexical, not semantic, even with `"semanticNavigation": true`
- Diagnostics are push-only (no LSP 3.17 pull)
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
