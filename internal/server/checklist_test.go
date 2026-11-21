package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/guardhint"
)

var (
	checklistPath  = regexp.MustCompile(`(?:\./)?(?:cmd|internal|deploy|docs|web|sdk)/[A-Za-z0-9_./-]*[A-Za-z0-9_]`)
	checklistMake  = regexp.MustCompile(`^make (\S+)$`)
	checklistSplit = regexp.MustCompile(`\s*(?:&&|\|)\s*`)
)

type checklistRecorder struct {
	cleanups []func()
	failed   bool
	logged   []string
}

func (r *checklistRecorder) Helper()          {}
func (r *checklistRecorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }
func (r *checklistRecorder) Failed() bool     { return r.failed }
func (r *checklistRecorder) Log(args ...any) {
	for _, a := range args {
		r.logged = append(r.logged, a.(string))
	}
}

func (r *checklistRecorder) finish() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

func checklistRead(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	return string(b)
}

func TestGuardHintsPointAtRealFiles(t *testing.T) {
	steps := guardhint.Steps()
	if len(steps) < 7 {
		t.Fatalf("only %d guard steps", len(steps))
	}
	for _, s := range steps {
		for _, p := range checklistPath.FindAllString(s.Command+" "+s.Manual, -1) {
			if _, err := os.Stat(filepath.Join("..", "..", p)); err != nil {
				t.Errorf("%s: %q does not exist: fix internal/guardhint", s.Name, p)
			}
		}
	}
}

func TestSyncRunsEveryGuardCommand(t *testing.T) {
	info, err := os.Stat(filepath.Join("..", "..", "scripts", "sync.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("scripts/sync.sh is not executable")
	}
	makefile := checklistRead(t, "Makefile")
	if !regexp.MustCompile(`(?m)^sync:\n\t\./scripts/sync\.sh$`).MatchString(makefile) {
		t.Error("Makefile has no sync target running scripts/sync.sh")
	}
	script := strings.ReplaceAll(checklistRead(t, "scripts/sync.sh"), `"$GO"`, "go")
	for _, s := range guardhint.Steps() {
		if s.Command == "" {
			continue
		}
		command := s.Command
		if m := checklistMake.FindStringSubmatch(command); m != nil {
			re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(m[1]) + `:.*\n\t(.+)$`)
			recipe := re.FindStringSubmatch(strings.ReplaceAll(makefile, "$(GO)", "go"))
			if recipe == nil {
				t.Errorf("%s: Makefile has no target %s", s.Name, m[1])
				continue
			}
			command = recipe[1]
		}
		for _, part := range checklistSplit.Split(command, -1) {
			if !strings.Contains(script, part) {
				t.Errorf("%s: scripts/sync.sh does not run %q", s.Name, part)
			}
		}
	}
	if !strings.Contains(script, "gofmt -w .") {
		t.Error("scripts/sync.sh does not run gofmt")
	}
}

func TestGuardChecklistIsPrintedOnlyOnFailure(t *testing.T) {
	for _, g := range []guardhint.Guard{guardhint.OpenAPIGolden, guardhint.ToolLists, guardhint.ActivityTemplates, guardhint.CLIParity, guardhint.TSTypes, guardhint.SDK, guardhint.SecretScan} {
		passing := &checklistRecorder{}
		guardhint.Watch(passing, g)
		passing.finish()
		if len(passing.logged) != 0 {
			t.Fatalf("%s: passing guard printed %q", guardhint.Of(g).Name, passing.logged)
		}

		failing := &checklistRecorder{failed: true}
		guardhint.Watch(failing, g)
		failing.finish()
		if len(failing.logged) != 1 {
			t.Fatalf("%s: logged %d messages, want one consolidated hint", guardhint.Of(g).Name, len(failing.logged))
		}
		hint := failing.logged[0]
		if !strings.Contains(hint, "make sync") || !strings.Contains(hint, "> "+guardhint.Of(g).Name) {
			t.Errorf("%s: hint does not point at make sync and the failed guard:\n%s", guardhint.Of(g).Name, hint)
		}
		for _, s := range guardhint.Steps() {
			if !strings.Contains(hint, s.Name) || !strings.Contains(hint, s.Command) || !strings.Contains(hint, s.Manual) {
				t.Errorf("%s: hint is missing the step %q:\n%s", guardhint.Of(g).Name, s.Name, hint)
			}
		}
	}
}
