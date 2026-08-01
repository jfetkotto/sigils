package workspace

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// filelistExtensions mark a filelist entry as a reference to another
// filelist to expand recursively, rather than a source file. Everything
// else -- after the VCS/EDA-style flag handling in expand -- is a source
// file entry.
var filelistExtensions = map[string]bool{
	".f":   true,
	".fl":  true,
	".lst": true,
}

// FilelistDiscoverer resolves the recursive filelist-of-filelists
// structure rooted at Config.Filelists. It is symlink-aware:
// SourceFile.ResolvedPath always points at the real underlying file, even
// when a repo folder in the workspace is a symlink into a read-only
// network-cached checkout, while LogicalPath keeps the path as referenced
// in the workspace (the form an editor would report for an open file).
type FilelistDiscoverer struct {
	root Root
	cfg  Config
	warn func(string)

	mu          sync.Mutex
	visited     []string          // resolved paths of every filelist opened during the last Files() call
	includeDirs []string          // +incdir+ directories captured during the last Files() call, in first-seen order
	defines     map[string]string // +define+ names captured during the last Files() call ("" value = defined with no replacement text)
}

// NewFilelistDiscoverer builds a Discoverer that expands cfg.Filelists
// (each a path relative to root.Path) recursively. warn, if non-nil, is
// called with a human-readable message for non-fatal problems (a missing
// or cyclic filelist reference, an unreadable source file) so discovery
// can keep going rather than fail outright -- a single bad reference in a
// large workspace shouldn't take down the whole index.
func NewFilelistDiscoverer(root Root, cfg Config, warn func(string)) *FilelistDiscoverer {
	return &FilelistDiscoverer{root: root, cfg: cfg, warn: warn}
}

func (d *FilelistDiscoverer) Roots(ctx context.Context) ([]Root, error) {
	return []Root{d.root}, nil
}

func (d *FilelistDiscoverer) Files(ctx context.Context, root Root) ([]SourceFile, error) {
	st := &discoveryState{
		seenFilelists:   make(map[string]bool),
		seenSources:     make(map[string]bool),
		seenIncludeDirs: make(map[string]bool),
		defines:         make(map[string]string),
	}

	for _, rel := range d.cfg.Filelists {
		d.expand(filepath.Join(root.Path, rel), st)
	}

	visited := make([]string, 0, len(st.seenFilelists))
	for p := range st.seenFilelists {
		visited = append(visited, p)
	}
	d.mu.Lock()
	d.visited = visited
	d.includeDirs = st.includeDirs
	d.defines = st.defines
	d.mu.Unlock()

	return st.files, nil
}

// IncludeDirs returns the +incdir+ directories captured during the last
// Files() call, in first-seen order across the recursive filelist walk.
func (d *FilelistDiscoverer) IncludeDirs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.includeDirs...)
}

// Defines returns the +define+ names (and, where given, values) captured
// during the last Files() call. A value of "" means the name was defined
// with no replacement text ("+define+FOO", as opposed to "+define+FOO=1").
func (d *FilelistDiscoverer) Defines() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]string, len(d.defines))
	for k, v := range d.defines {
		out[k] = v
	}
	return out
}

// discoveryState accumulates everything one Files() call discovers across
// its recursive expand calls -- bundled into one struct rather than passed
// as several separate parameters, now that expand needs to thread through
// +incdir+/+define+ capture alongside the file/filelist dedup sets it
// already tracked.
type discoveryState struct {
	seenFilelists   map[string]bool
	seenSources     map[string]bool
	seenIncludeDirs map[string]bool
	includeDirs     []string
	defines         map[string]string
	files           []SourceFile
}

// VisitedFilelists returns the resolved paths of every filelist opened
// during the last Files() call (implements workspace.FilelistProvider),
// so a caller can watch them for changes and trigger a rescan.
func (d *FilelistDiscoverer) VisitedFilelists() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.visited...)
}

func (d *FilelistDiscoverer) expand(path string, st *discoveryState) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		d.warnf("could not resolve filelist %s: %s", path, err)
		return
	}
	if st.seenFilelists[resolved] {
		d.warnf("skipping already-visited filelist %s (cyclic or duplicate reference)", path)
		return
	}
	st.seenFilelists[resolved] = true

	f, err := os.Open(resolved)
	if err != nil {
		d.warnf("could not open filelist %s: %s", path, err)
		return
	}
	defer f.Close()

	dir := filepath.Dir(path)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		// A trailing "// comment" on an otherwise real entry (e.g.
		// "rtl/top.sv  // main product top", a common VCS-style filelist
		// convention) -- left unstripped, it glues onto the path, fails
		// symlink resolution below, and silently drops the file from the
		// workspace view (a log warning is the only trace). Stripped
		// before $VAR expansion, so a comment mentioning one doesn't get
		// expanded pointlessly.
		line = stripTrailingComment(line)
		if line == "" {
			continue
		}
		// Real EDA filelists routinely reference $WORKSPACE-style variables.
		line = os.ExpandEnv(line)

		entry := line
		treatAsFilelist := false
		if arg, ok := flagArg(line, "-f"); ok {
			entry, treatAsFilelist = arg, true
		} else if arg, ok := flagArg(line, "-F"); ok {
			// VCS's -F resolves relative paths against the filelist's own
			// directory where -f uses the tool's working directory; a
			// language server has no meaningful working directory, so both
			// resolve filelist-relative here, like every other entry.
			entry, treatAsFilelist = arg, true
		} else if arg, ok := flagArg(line, "-v"); ok {
			entry = arg // a library file is still Verilog source: index it
		} else if arg, ok := plusArg(line, "+incdir+"); ok {
			d.recordIncludeDirs(arg, dir, st)
			continue
		} else if arg, ok := plusArg(line, "+define+"); ok {
			recordDefines(arg, st)
			continue
		} else if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			// Other VCS/EDA-style options (-y, -sverilog, -timescale=..., ...)
			// configure a simulator, not the source view -- expected
			// content, skipped without warning.
			continue
		}

		if !filepath.IsAbs(entry) {
			entry = filepath.Join(dir, entry)
		}

		if treatAsFilelist || filelistExtensions[strings.ToLower(filepath.Ext(entry))] {
			d.expand(entry, st)
			continue
		}

		entryResolved, err := filepath.EvalSymlinks(entry)
		if err != nil {
			d.warnf("could not resolve source file %s (referenced from %s): %s", entry, path, err)
			continue
		}
		if st.seenSources[entryResolved] {
			continue
		}
		st.seenSources[entryResolved] = true
		st.files = append(st.files, SourceFile{LogicalPath: entry, ResolvedPath: entryResolved})
	}
	if err := scanner.Err(); err != nil {
		d.warnf("error reading filelist %s: %s", path, err)
	}
}

// stripTrailingComment removes a trailing "// comment" from line, if
// present, returning the (still whitespace-trimmed) remainder. The "//"
// must be preceded by whitespace to count -- a bare "//" glued directly
// onto other content is left alone, since a filesystem path can
// technically contain "//" (e.g. a redundant path separator) and this
// function has no way to tell that apart from a comment marker without
// the whitespace signal. A full-line comment ("line" itself starting
// with "//") is handled separately, before this is ever called.
func stripTrailingComment(line string) string {
	for i := 1; i+1 < len(line); i++ {
		if line[i] == '/' && line[i+1] == '/' && (line[i-1] == ' ' || line[i-1] == '\t') {
			return strings.TrimSpace(line[:i])
		}
	}
	return line
}

// flagArg returns the argument of a "-x <arg>"-style line if line starts
// with exactly the given flag followed by whitespace and a non-empty
// argument.
func flagArg(line, flag string) (string, bool) {
	rest, ok := strings.CutPrefix(line, flag)
	if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return "", false
	}
	arg := strings.TrimSpace(rest)
	if arg == "" {
		return "", false
	}
	return arg, true
}

// plusArg returns the argument of a "+flag+arg"-style line (VCS/EDA
// convention: no space, the flag and argument share one token) if line
// starts with exactly the given flag, e.g. plusArg("+incdir+/x", "+incdir+")
// returns ("/x", true).
func plusArg(line, flag string) (string, bool) {
	rest, ok := strings.CutPrefix(line, flag)
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

// recordIncludeDirs adds every '+'-separated directory in arg (VCS allows
// "+incdir+dir1+dir2+dir3" as well as one directory per line) to st,
// resolving a relative one against baseDir like every other filelist entry
// and deduplicating across the whole recursive walk.
func (d *FilelistDiscoverer) recordIncludeDirs(arg, baseDir string, st *discoveryState) {
	for _, dir := range strings.Split(arg, "+") {
		if dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(baseDir, dir)
		}
		if st.seenIncludeDirs[dir] {
			continue
		}
		st.seenIncludeDirs[dir] = true
		st.includeDirs = append(st.includeDirs, dir)
	}
}

// recordDefines adds every '+'-separated NAME or NAME=VALUE in arg (VCS
// allows "+define+FOO+BAR=1+BAZ" as well as one define per line) to st.
func recordDefines(arg string, st *discoveryState) {
	for _, def := range strings.Split(arg, "+") {
		if def == "" {
			continue
		}
		name, value, _ := strings.Cut(def, "=")
		if name == "" {
			continue
		}
		st.defines[name] = value
	}
}

func (d *FilelistDiscoverer) warnf(format string, args ...any) {
	if d.warn != nil {
		d.warn(fmt.Sprintf(format, args...))
	}
}
