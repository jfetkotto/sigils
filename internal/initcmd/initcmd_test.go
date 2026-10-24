package initcmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jfetkotto/svparse/v2/lint"

	"github.com/jfetkotto/sigils/internal/workspace"
)

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(args ...string) (code int, out, errOut string) {
	var o, e bytes.Buffer
	code = Run(args, &o, &e)
	return code, o.String(), e.String()
}

func TestInitWritesAConfigTheServerAccepts(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "filelists/top.f", "-f sub.f\n../rtl/a.sv\n")
	write(t, dir, "filelists/sub.f", "../rtl/b.sv\n")
	write(t, dir, "rtl/a.sv", "module a; endmodule\n")
	if code, out, errOut := run(dir); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	cfg, err := workspace.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	// sub.f is pulled in by top.f, so only top.f is an entry point.
	if len(cfg.Filelists) != 1 || cfg.Filelists[0] != "filelists/top.f" {
		t.Errorf("filelists: %v", cfg.Filelists)
	}
	if !cfg.SemanticDiagnosticsEnabled() || !cfg.SemanticTokensEnabled() || cfg.SemanticNavigationEnabled() || !cfg.LintEnabled() {
		t.Errorf("defaults: %+v", cfg)
	}
	// Every lint rule is spelt out at its default level, and the settings
	// validate against svparse's own checker.
	lc := lint.Config{Rules: cfg.Lint.Rules, Ignore: cfg.Lint.Ignore}
	if err := lc.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, r := range lint.Rules() {
		if got := cfg.Lint.Rules[r.Name]; got != r.Severity.String() {
			t.Errorf("rule %s: %q, want %q", r.Name, got, r.Severity)
		}
	}
	if len(cfg.Lint.Rules) != len(lint.Rules()) {
		t.Errorf("%d rules written, %d exist", len(cfg.Lint.Rules), len(lint.Rules()))
	}
}

func TestInitDoesNotOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, workspace.ConfigFileName, `{"filelists": ["mine.f"]}`)
	code, _, errOut := run(dir)
	if code != 1 || !strings.Contains(errOut, "already exists") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, workspace.ConfigFileName)); !strings.Contains(string(data), "mine.f") {
		t.Fatal("the existing file was changed")
	}
	if code, _, errOut := run("-force", dir); code != 0 {
		t.Fatalf("-force: %d %s", code, errOut)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, workspace.ConfigFileName)); strings.Contains(string(data), "mine.f") {
		t.Fatal("-force did not overwrite")
	}
}

func TestInitFilelistFlagAndPrint(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := run("-print", "-filelist", "a/b.f", "-filelist", "c.f", dir)
	if code != 0 {
		t.Fatal(code)
	}
	var got file
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Filelists, ",") != "a/b.f,c.f" {
		t.Errorf("filelists %v", got.Filelists)
	}
	if _, err := os.Stat(filepath.Join(dir, workspace.ConfigFileName)); err == nil {
		t.Fatal("-print wrote a file")
	}
}

func TestInitWithoutFilelistsSaysSo(t *testing.T) {
	dir := t.TempDir()
	code, out, _ := run(dir)
	if code != 0 || !strings.Contains(out, "no filelist found") {
		t.Fatalf("%d %q", code, out)
	}
	cfg, err := workspace.LoadConfig(dir)
	if err != nil || len(cfg.Filelists) != 0 {
		t.Fatalf("%v %v", cfg, err)
	}
}

func TestFindFilelistsSkipsHiddenAndDeepDirectories(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.f", "x.sv\n")
	write(t, dir, ".git/hooks/ignored.f", "x\n")
	write(t, dir, "node_modules/p/ignored.f", "x\n")
	write(t, dir, "l1/l2/l3/l4/too_deep.f", "x\n")
	write(t, dir, "l1/l2/ok.fl", "x\n")
	got := strings.Join(FindFilelists(dir), ",")
	if got != "a.f,l1/l2/ok.fl" {
		t.Fatalf("got %s", got)
	}
}

func TestInitRejectsAMissingDirectory(t *testing.T) {
	if code, _, errOut := run(filepath.Join(t.TempDir(), "nope")); code != 2 || !strings.Contains(errOut, "not a directory") {
		t.Fatalf("%d %s", code, errOut)
	}
}
