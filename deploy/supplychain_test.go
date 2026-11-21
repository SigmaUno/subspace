package deploy

import (
	"bytes"
	"encoding/json"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var allowedLicenses = []string{
	"MIT",
	"BSD-2-Clause",
	"BSD-3-Clause",
	"Apache-2.0",
	"ISC",
	"MPL-2.0",
}

var baselineDirectDeps = []string{
	"github.com/jackc/pgx/v5",
	"modernc.org/sqlite",
}

var licenseMarkers = []struct {
	id   string
	all  []string
	none []string
}{
	{id: "MIT", all: []string{"permission is hereby granted, free of charge"}},
	{id: "ISC", all: []string{"permission to use, copy, modify, and/or distribute this software for any purpose with or without fee"}},
	{id: "Apache-2.0", all: []string{"apache license", "version 2.0"}},
	{id: "MPL-2.0", all: []string{"mozilla public license", "version 2.0"}},
	{id: "BSD-3-Clause", all: []string{"redistribution and use in source and binary forms", "neither the name"}},
	{id: "BSD-2-Clause", all: []string{"redistribution and use in source and binary forms"}, none: []string{"neither the name"}},
	{id: "AGPL", all: []string{"gnu affero general public license"}, none: []string{"mozilla public license"}},
	{id: "LGPL", all: []string{"gnu lesser general public license"}, none: []string{"mozilla public license"}},
	{id: "GPL", all: []string{"gnu general public license"}, none: []string{"mozilla public license"}},
}

var (
	licenseFiles   = []string{"LICENSE", "LICENSE.md", "LICENSE.txt", "LICENCE", "COPYING", "COPYING.md", "COPYING.txt"}
	dockerGoImage  = regexp.MustCompile(`(?m)^FROM golang:(\d+\.\d+)(?:\.\d+)?[\w.-]*@sha256:[0-9a-f]{64}\b`)
	goDirectiveRe  = regexp.MustCompile(`^\d+\.\d+(?:\.\d+)?$`)
	toolchainRe    = regexp.MustCompile(`^go\d+\.\d+\.\d+$`)
	whitespaceRuns = regexp.MustCompile(`\s+`)
)

type goModFile struct {
	Go        string
	Toolchain string
	Require   []struct {
		Path     string
		Version  string
		Indirect bool
	}
	Replace []struct {
		Old struct{ Path string }
		New struct{ Path, Version string }
	}
}

type listedModule struct {
	Path string
	Dir  string
	Main bool
}

func readGoMod(t *testing.T, root string) goModFile {
	t.Helper()
	cmd := exec.Command("go", "mod", "edit", "-json")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go mod edit -json: %v", err)
	}
	var mod goModFile
	if err := json.Unmarshal(out, &mod); err != nil {
		t.Fatal(err)
	}
	return mod
}

func cachedModules(t *testing.T, root string, mod goModFile) map[string]string {
	t.Helper()
	cmd := exec.Command("go", "list", "-m", "-json", "all")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOFLAGS=-mod=mod")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("module cache unavailable: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	dirs := map[string]string{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var m listedModule
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		if !m.Main {
			dirs[m.Path] = m.Dir
		}
	}
	for _, r := range mod.Require {
		dir := dirs[r.Path]
		if dir == "" {
			t.Skipf("module cache unavailable: %s not downloaded", r.Path)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Skipf("module cache unavailable: %v", err)
		}
	}
	return dirs
}

func detectLicenses(text string) []string {
	text = strings.ToLower(whitespaceRuns.ReplaceAllString(text, " "))
	var found []string
	for _, m := range licenseMarkers {
		if !slices.ContainsFunc(m.all, func(s string) bool { return !strings.Contains(text, s) }) &&
			!slices.ContainsFunc(m.none, func(s string) bool { return strings.Contains(text, s) }) {
			found = append(found, m.id)
		}
	}
	return found
}

func TestGoModPinned(t *testing.T) {
	root := repoRoot(t)
	mod := readGoMod(t, root)

	if !goDirectiveRe.MatchString(mod.Go) {
		t.Fatalf("go directive %q is not a version", mod.Go)
	}
	if mod.Toolchain != "" {
		if !toolchainRe.MatchString(mod.Toolchain) {
			t.Errorf("toolchain %q is not an exact release", mod.Toolchain)
		}
		if version.Compare("go"+mod.Go, mod.Toolchain) > 0 {
			t.Errorf("toolchain %s is older than the go directive %s", mod.Toolchain, mod.Go)
		}
	}
	if len(mod.Replace) > 0 {
		t.Errorf("go.mod has %d replace directives, want none: %+v", len(mod.Replace), mod.Replace)
	}

	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	image := dockerGoImage.FindSubmatch(dockerfile)
	if image == nil {
		t.Fatal("Dockerfile does not build on a golang image pinned by tag and sha256 digest")
	}
	if want := version.Lang("go" + mod.Go); "go"+string(image[1]) != want {
		t.Errorf("Dockerfile golang image %s does not match go directive %s", image[1], want)
	}
}

func TestGoSumCoversRequirements(t *testing.T) {
	root := repoRoot(t)
	mod := readGoMod(t, root)
	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]bool{}
	for _, line := range strings.Split(string(sum), "\n") {
		if f := strings.Fields(line); len(f) == 3 && strings.HasPrefix(f[2], "h1:") {
			lines[f[0]+" "+f[1]] = true
		}
	}
	for _, r := range mod.Require {
		if !lines[r.Path+" "+r.Version+"/go.mod"] {
			t.Errorf("go.sum lacks the go.mod hash of %s %s", r.Path, r.Version)
		}
	}
}

func TestGoModVerify(t *testing.T) {
	root := repoRoot(t)
	cachedModules(t, root, readGoMod(t, root))
	cmd := exec.Command("go", "mod", "verify")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOPROXY=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go mod verify: %v\n%s", err, out)
	}
}

func TestDependencyLicensesAllowed(t *testing.T) {
	root := repoRoot(t)
	mod := readGoMod(t, root)
	dirs := cachedModules(t, root, mod)

	for path, dir := range dirs {
		if _, err := os.Stat(dir); dir == "" || err != nil {
			continue
		}
		var text []byte
		for _, name := range licenseFiles {
			if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
				text = b
				break
			}
		}
		if text == nil {
			t.Errorf("%s: no license file", path)
			continue
		}
		found := detectLicenses(string(text))
		if len(found) == 0 {
			t.Errorf("%s: license not recognized", path)
		}
		for _, id := range found {
			if !slices.Contains(allowedLicenses, id) {
				t.Errorf("%s: license %s is not in the allowlist %v", path, id, allowedLicenses)
			}
		}
	}
}

func TestDirectDependencyBaseline(t *testing.T) {
	mod := readGoMod(t, repoRoot(t))
	var direct []string
	for _, r := range mod.Require {
		if !r.Indirect {
			direct = append(direct, r.Path)
		}
	}
	slices.Sort(direct)
	if len(direct) > len(baselineDirectDeps) {
		t.Errorf("%d direct dependencies exceed the baseline of %d (%v): %v; justify the addition and extend baselineDirectDeps",
			len(direct), len(baselineDirectDeps), baselineDirectDeps, direct)
	}
	for _, path := range direct {
		if !slices.Contains(baselineDirectDeps, path) {
			t.Errorf("direct dependency %s is not in the baseline %v", path, baselineDirectDeps)
		}
	}
}
