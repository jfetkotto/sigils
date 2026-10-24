// Package initcmd implements "sigils init": it writes a .sigils.json for a
// workspace that spells out every option the server reads, with the lint rules
// at their default levels and a placeholder for the filelist, so the file is a
// complete starting point to edit rather than something to write from the docs.
package initcmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// PlaceholderFilelist stands in for the filelist until the user names theirs.
const PlaceholderFilelist = "path/to/filelist.f"

// Run is "sigils init [flags] [dir]". It returns the process exit status.
func Run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sigils init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	force := fs.Bool("force", false, "overwrite an existing "+workspace.ConfigFileName)
	var lists multi
	fs.Var(&lists, "filelist", "a top-level filelist, relative to the directory (repeatable; default: a placeholder)")
	printOnly := fs.Bool("print", false, "print the file instead of writing it")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: sigils init [-force] [-print] [-filelist path]... [dir]\n\n"+
			"Writes %s in dir (default: the current directory) with every option at\n"+
			"its default, including the lint rules and their levels, and a placeholder\n"+
			"for the filelist to replace.\n\n", workspace.ConfigFileName)
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
	placeholder := len(filelists) == 0
	if placeholder {
		filelists = []string{PlaceholderFilelist}
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
	if placeholder {
		fmt.Fprintf(stdout, "replace %q under \"filelists\" with your top-level filelist so the server indexes your sources\n", PlaceholderFilelist)
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
