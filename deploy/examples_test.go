package deploy

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/breaker"
	"github.com/SigmaUno/subspace/internal/briefs"
	"github.com/SigmaUno/subspace/internal/capacity"
	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/playbooks"
	"github.com/SigmaUno/subspace/internal/profiles"
	"github.com/SigmaUno/subspace/internal/servertest"
)

const maxCharterBytes = 8 << 10

var (
	placeholderRe = regexp.MustCompile(`\{\{\s*([a-z][a-z0-9_]*)\s*\}\}`)
	envKeyRe      = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

func exampleFiles(t *testing.T, dir, pattern string, min int) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("examples", dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < min {
		t.Fatalf("examples/%s: found %d files matching %s, want at least %d", dir, len(files), pattern, min)
	}
	return files
}

func readExample(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeStrict(t *testing.T, path string, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(readExample(t, path)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if dec.More() {
		t.Fatalf("%s: trailing data after the JSON value", path)
	}
}

func parseEnvFile(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for i, line := range strings.Split(string(readExample(t, path)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !envKeyRe.MatchString(k) {
			t.Fatalf("%s:%d: not a KEY=value line", path, i+1)
		}
		if _, dup := out[k]; dup {
			t.Fatalf("%s:%d: duplicate key %s", path, i+1, k)
		}
		out[k] = v
	}
	return out
}

func TestExampleChartersFitAndReachAgents(t *testing.T) {
	e := servertest.New(t)
	tok := e.Register("w1", "worker", "claude-code")
	for _, path := range exampleFiles(t, "charters", "*.md", 5) {
		text := string(readExample(t, path))
		if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || len(text) > maxCharterBytes {
			t.Errorf("%s: charter must be non-empty UTF-8 of at most %d bytes, got %d", path, maxCharterBytes, len(text))
			continue
		}
		if strings.Contains(text, "{{") {
			t.Errorf("%s: charters are plain text, found a template action", path)
		}
		if code, out := e.REST("PUT", "/api/v1/charter", map[string]any{"text": text}); code != 200 {
			t.Errorf("%s: PUT charter: %d %v", path, code, out)
			continue
		}
		first, _ := json.Marshal(strings.SplitN(text, "\n", 2)[0])
		got, _ := json.Marshal(e.Must(tok, "get_instructions", nil))
		if !bytes.Contains(got, bytes.Trim(first, `"`)) {
			t.Errorf("%s: get_instructions does not include the charter", path)
		}
	}
}

func TestExampleCapacityFilesLoad(t *testing.T) {
	for _, f := range exampleFiles(t, "capacity", "*.json", 2) {
		var strict capacity.Config
		decodeStrict(t, f, &strict)
		cfg, err := capacity.Load(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if cfg.GlobalMaxAgents < 1 || len(cfg.Limits) == 0 {
			t.Errorf("%s: needs global_max_agents and at least one limit", f)
		}
		providers := map[string]bool{}
		for _, l := range cfg.Limits {
			providers[l.Provider] = true
		}
		if strings.Contains(f, "mixed") && len(providers) < 3 {
			t.Errorf("%s: mixed example needs at least three providers, has %d", f, len(providers))
		}
	}
}

func TestExampleBreakerEnvLoads(t *testing.T) {
	env := parseEnvFile(t, filepath.Join("examples", "env", "breakers.env"))
	for k := range env {
		if !strings.HasPrefix(k, "SUBSPACE_BREAKER_") {
			t.Errorf("unexpected key %s", k)
		}
	}
	cfg, err := breaker.ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Threshold != 0.4 || cfg.Window != 10*time.Minute || cfg.MinSamples != 15 || cfg.Cooldown != 2*time.Minute {
		t.Errorf("breaker settings not applied: %+v", cfg)
	}
}

func TestExampleProfilesValidate(t *testing.T) {
	seen := map[string]string{}
	for _, p := range profiles.Builtin() {
		seen[p.Name] = "builtin"
	}
	for _, f := range exampleFiles(t, "profiles", "*.json", 3) {
		var ps []profiles.Profile
		decodeStrict(t, f, &ps)
		roles := map[string]bool{}
		for _, p := range ps {
			if err := p.Validate(); err != nil {
				t.Errorf("%s: profile %s: %v", f, p.Name, err)
			}
			if other, dup := seen[p.Name]; dup {
				t.Errorf("%s: profile name %s already used by %s", f, p.Name, other)
			}
			seen[p.Name] = f
			roles[p.Role] = true
		}
		if !roles["worker"] || !roles["reviewer"] {
			t.Errorf("%s: a usable set needs a worker and a reviewer profile", f)
		}
	}
}

func examplePlaybooks(t *testing.T) []playbooks.Playbook {
	t.Helper()
	var all []playbooks.Playbook
	for _, f := range exampleFiles(t, "playbooks", "*.json", 2) {
		var ps []playbooks.Playbook
		decodeStrict(t, f, &ps)
		if len(ps) == 0 {
			t.Errorf("%s: empty playbook set", f)
		}
		all = append(all, ps...)
	}
	return all
}

func TestExamplePlaybooksValidate(t *testing.T) {
	seen := map[string]bool{}
	for _, b := range playbooks.Builtin() {
		seen[b.Name] = true
	}
	for _, p := range examplePlaybooks(t) {
		if p.Builtin || !p.CreatedAt.IsZero() || !p.UpdatedAt.IsZero() {
			t.Errorf("playbook %s: builtin and timestamps are server-managed", p.Name)
		}
		if err := p.Validate(); err != nil {
			t.Errorf("playbook %s: %v", p.Name, err)
		}
		if seen[p.Name] {
			t.Errorf("playbook name %s collides with a builtin or another example", p.Name)
		}
		seen[p.Name] = true
	}
}

func TestExamplePlaybooksImportThroughAPI(t *testing.T) {
	e := servertest.New(t)
	for _, p := range examplePlaybooks(t) {
		if code, out := e.REST("POST", "/api/v1/playbooks", p); code != 201 {
			t.Errorf("import playbook %s: %d %v", p.Name, code, out)
		}
	}
}

func TestExampleBriefTemplatesParse(t *testing.T) {
	known := map[string]bool{}
	for _, b := range playbooks.Builtin() {
		known[b.Name] = true
	}
	for _, p := range examplePlaybooks(t) {
		known[p.Name] = true
	}
	for _, f := range exampleFiles(t, "briefs", "*.md", 5) {
		src := placeholderRe.ReplaceAllString(string(readExample(t, f)), "sample")
		doc, err := briefs.Parse([]byte(src))
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if doc.Playbook != "" && !known[doc.Playbook] {
			t.Errorf("%s: playbook %q does not exist", f, doc.Playbook)
		}
		if len(doc.Repos) == 0 || doc.MaxCostEUR <= 0 || strings.TrimSpace(doc.Body) == "" {
			t.Errorf("%s: needs repos, a cost cap and a body", f)
		}
	}
}

func TestExampleNotifySinksLoad(t *testing.T) {
	urlKeys := []string{notify.EnvWebhookURL, notify.EnvNtfyURL, notify.EnvSlackURL, notify.EnvSMTPURL}
	for _, f := range exampleFiles(t, "notify", "*.env", 4) {
		env := parseEnvFile(t, f)
		want := 0
		for k, v := range env {
			if !slices.Contains(notify.ReloadKeys, k) {
				t.Errorf("%s: unknown notify key %s", f, k)
			}
			if slices.Contains(urlKeys, k) && v != "" {
				want++
			}
		}
		cfg, err := notify.ConfigFromEnv(func(k string) string { return env[k] })
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if want == 0 || len(cfg.Sinks) != want {
			t.Errorf("%s: %d sinks configured, want %d", f, len(cfg.Sinks), want)
		}
	}
}

func composeVariables(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`\$\{(SUBSPACE_[A-Z0-9_]+)`).FindAllStringSubmatch(readRepoFile(t, "docker-compose.yml"), -1) {
		out[m[1]] = true
	}
	return out
}

func TestExampleComposeEnvFiles(t *testing.T) {
	vars := composeVariables(t)
	for _, f := range exampleFiles(t, "env", "*.env", 4) {
		if filepath.Base(f) == "breakers.env" {
			continue
		}
		env := parseEnvFile(t, f)
		for k, v := range env {
			if !vars[k] {
				t.Errorf("%s: %s is not read by docker-compose.yml", f, k)
			}
			if strings.Contains(k, "TOKEN") || strings.Contains(k, "SECRET") || strings.Contains(k, "PASSWORD") {
				t.Errorf("%s: %s must not be set in an env file", f, k)
			}
			if v == "" {
				t.Errorf("%s: %s is empty; drop it so the compose default applies", f, k)
			}
		}
		if u, err := url.Parse(env["SUBSPACE_PUBLIC_URL"]); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			t.Errorf("%s: SUBSPACE_PUBLIC_URL must be an http(s) URL", f)
		}
		if d, err := time.ParseDuration(env["SUBSPACE_LEASE"]); err != nil || d < time.Minute {
			t.Errorf("%s: SUBSPACE_LEASE must be a duration of at least 1m", f)
		}
		if !slices.Contains([]string{"noop", "log", "hc", "docker"}, env["SUBSPACE_SPAWNER"]) {
			t.Errorf("%s: unknown SUBSPACE_SPAWNER %q", f, env["SUBSPACE_SPAWNER"])
		}
		if env["SUBSPACE_SPAWNER"] == "hc" && (env["SUBSPACE_HC_BIN"] == "" || !strings.Contains(env["SUBSPACE_HC_COMMAND"], "{join_line}")) {
			t.Errorf("%s: hc spawner needs SUBSPACE_HC_BIN and a command with {join_line}", f)
		}
		if !slices.Contains([]string{"debug", "info", "warn", "error"}, env["SUBSPACE_LOG_LEVEL"]) {
			t.Errorf("%s: unknown SUBSPACE_LOG_LEVEL %q", f, env["SUBSPACE_LOG_LEVEL"])
		}
		for _, k := range []string{"SUBSPACE_BACKUP_KEEP", "SUBSPACE_BACKUP_EVERY_SECONDS"} {
			if n, err := strconv.Atoi(env[k]); err != nil || n < 1 {
				t.Errorf("%s: %s must be a positive integer", f, k)
			}
		}
		if p := env["SUBSPACE_CAPACITY_FILE"]; p != "" && filepath.Dir(p) != "/data/config" {
			t.Errorf("%s: SUBSPACE_CAPACITY_FILE must live in the mounted /data/config", f)
		}
	}
}
