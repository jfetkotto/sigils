// Package initcmd implements "sigils init": it writes a .sigils.json for a
// workspace, naming the filelists it finds and spelling out every option the
// server reads, with the lint rules at their default levels, so the file is a
// complete starting point to edit rather than something to write from the docs.
package initcmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jfetkotto/svparse/v2/lint"

	"github.com/jfetkotto/sigils/internal/workspace"
)

// file is the layout of the generated .sigils.json. It lists every field
// workspace.Config reads, without omitting the empty ones, which is what makes
// the file worth generating.
type file struct {
	Filelists           []string `json:"filelists"`
	SemanticDiagnostics bool     `json:"semanticDiagnostics"`
	SemanticTokens      bool     `json:"semanticTokens"`
	SemanticNavigation  bool     `json:"semanticNavigation"`
	Lint                lintPart `json:"lint"`
}

type lintPart struct {
	Enabled bool              `json:"enabled"`
	Rules   map[string]string `json:"rules"`
	Ignore  []string          `json:"ignore"`
}

// Run is "sigils init [flags] [dir]". It returns the process exit status.
func Run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sigils init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	force := fs.Bool("force", false, "overwrite an existing "+workspace.ConfigFileName)
	var lists multi
	fs.Var(&lists, "filelist", "a top-level filelist, relative to the directory (repeatable; default: the ones found)")
	printOnly := fs.Bool("print", false, "print the file instead of writing it")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: sigils init [-force] [-print] [-filelist path]... [dir]\n\n"+
			"Writes %s in dir (default: the current directory) with the filelists it finds\n"+
			"and every option at its default, including the lint rules and their levels.\n\n", workspace.ConfigFileName)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	dir := "."
	if fs.NArg() > 1 {
		fs.Usage()
		return 2
	}
	if fs.NArg() == 1 {
		dir = fs.Arg(0)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "sigils init: %s is not a directory\n", dir)
		return 2
	}

	filelists := []string(lists)
	if len(filelists) == 0 {
		filelists = FindFilelists(dir)
	}
	data, err := Render(filelists)
	if err != nil {
		fmt.Fprintf(stderr, "sigils init: %v\n", err)
		return 1
	}
	if *printOnly {
		stdout.Write(data)
		return 0
	}
	path := filepath.Join(dir, workspace.ConfigFileName)
	if _, err := os.Stat(path); err == nil && !*force {
		fmt.Fprintf(stderr, "sigils init: %s already exists (use -force to overwrite it)\n", path)
		return 1
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fmt.Fprintf(stderr, "sigils init: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s\n", path)
	if len(filelists) == 0 {
		fmt.Fprintf(stdout, "no filelist found: add one under \"filelists\" so the server indexes your sources\n")
	} else {
		fmt.Fprintf(stdout, "filelists: %s\n", strings.Join(filelists, ", "))
	}
	return 0
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(s string) error { *m = append(*m, filepath.ToSlash(s)); return nil }

// Render returns the file's text for the given top-level filelists: every
// option, and every lint rule at the level it runs at by default.
func Render(filelists []string) ([]byte, error) {
	rules := map[string]string{}
	for _, r := range lint.Rules() {
		rules[r.Name] = r.Severity.String()
	}
	f := file{
		Filelists:           append([]string{}, filelists...),
		SemanticDiagnostics: true,
		SemanticTokens:      true,
		SemanticNavigation:  false,
		Lint:                lintPart{Enabled: true, Rules: rules, Ignore: []string{}},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var filelistExts = map[string]bool{".f": true, ".fl": true, ".lst": true}

// maxDepth bounds how far below the directory filelists are looked for.
const maxDepth = 3

// FindFilelists finds the top-level filelists under dir: the ones nothing else
// pulls in with "-f". Hidden directories and node_modules are skipped. Paths
// are relative to dir, with forward slashes, in sorted order.
func FindFilelists(dir string) []string {
	var found []string
	root := filepath.Clean(dir)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			name := d.Name()
			depth := strings.Count(filepath.ToSlash(rel), "/") + 1
			if rel != "." && (strings.HasPrefix(name, ".") || name == "node_modules" || depth > maxDepth) {
				return fs.SkipDir
			}
			return nil
		}
		if filelistExts[strings.ToLower(filepath.Ext(p))] {
			found = append(found, p)
		}
		return nil
	})
	included := map[string]bool{}
	for _, p := range found {
		for _, inc := range includedLists(p) {
			included[filepath.Clean(inc)] = true
		}
	}
	var out []string
	for _, p := range found {
		if included[filepath.Clean(p)] {
			continue
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

// includedLists returns the filelists a filelist names with -f or -F, as paths
// relative to the working directory like path itself.
func includedLists(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		for _, flagName := range []string{"-f ", "-F "} {
			if rest, ok := strings.CutPrefix(line, flagName); ok {
				arg := strings.TrimSpace(rest)
				if i := strings.Index(arg, " //"); i >= 0 {
					arg = strings.TrimSpace(arg[:i])
				}
				if !filepath.IsAbs(arg) {
					arg = filepath.Join(filepath.Dir(path), arg)
				}
				out = append(out, arg)
			}
		}
	}
	return out
}
