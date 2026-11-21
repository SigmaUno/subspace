package deploy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	historyLimit   = 500
	maxScanLine    = 16 << 20
	genericMinLen  = 20
	genericEntropy = 3.5
)

type secretRule struct {
	name string
	re   *regexp.Regexp
}

var secretRules = []secretRule{
	{"private-key", regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`)},
	{"anthropic-key", regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`)},
	{"openai-key", regexp.MustCompile(`\bsk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{32,}`)},
	{"github-token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,})`)},
	{"slack-token", regexp.MustCompile(`\bxox[abeoprs]-[A-Za-z0-9-]{10,}`)},
	{"aws-access-key", regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA|AIDA|AROA)[0-9A-Z]{16}\b`)},
	{"stripe-key", regexp.MustCompile(`\b(?:[sr]k|whsec)_(?:live_|test_)?[0-9A-Za-z]{16,}`)},
	{"basic-auth-url", regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.-]*://[^\s/:@'"<>{}$]+:[^\s/@'"<>{}$]+@[^\s/'"]+`)},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
}

var genericAssign = regexp.MustCompile(`(?i)\b[\w.-]*(?:token|secret|password|passwd|key)[\w.-]*["']?\s*(?::=|[:=])\s*["']?([A-Za-z0-9+_=-]+)`)

const genericRule = "generic-secret"

var (
	commitID     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	envName      = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	mixedClasses = regexp.MustCompile(`^(?:.*[A-Za-z].*[0-9]|.*[0-9].*[A-Za-z]).*$`)
)

func shannon(s string) float64 {
	if s == "" {
		return 0
	}
	counts := map[rune]int{}
	for _, r := range s {
		counts[r]++
	}
	n := float64(len([]rune(s)))
	var h float64
	for _, c := range counts {
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

func detectLine(line string) []string {
	var found []string
	for _, r := range secretRules {
		if r.re.MatchString(line) {
			found = append(found, r.name)
		}
	}
	for _, m := range genericAssign.FindAllStringSubmatch(line, -1) {
		v := m[1]
		if len(v) >= genericMinLen && !envName.MatchString(v) && mixedClasses.MatchString(v) && shannon(v) >= genericEntropy {
			found = append(found, genericRule)
			break
		}
	}
	return found
}

type allowEntry struct {
	Commit  string `json:"commit,omitempty"`
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
	Reason  string `json:"reason"`
}

type allowlist []allowEntry

func ruleKnown(name string) bool {
	if name == genericRule {
		return true
	}
	for _, r := range secretRules {
		if r.name == name {
			return true
		}
	}
	return false
}

func parseAllowlist(b []byte) (allowlist, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var entries allowlist
	if err := dec.Decode(&entries); err != nil {
		return nil, err
	}
	for i, e := range entries {
		if e.Commit != "" && !commitID.MatchString(e.Commit) {
			return nil, fmt.Errorf("entry %d: commit must be a full 40-character hash", i)
		}
		if e.Path == "" {
			return nil, fmt.Errorf("entry %d: path is required", i)
		}
		if _, err := path.Match(e.Path, ""); err != nil {
			return nil, fmt.Errorf("entry %d: bad path pattern: %w", i, err)
		}
		if !ruleKnown(e.Pattern) {
			return nil, fmt.Errorf("entry %d: unknown pattern %q", i, e.Pattern)
		}
		if strings.TrimSpace(e.Reason) == "" {
			return nil, fmt.Errorf("entry %d (%s %s): reason is required", i, e.Path, e.Pattern)
		}
	}
	return entries, nil
}

func (a allowlist) allows(commit, file, rule string) bool {
	for _, e := range a {
		if e.Pattern != rule || (e.Commit != "" && e.Commit != commit) {
			continue
		}
		if ok, _ := path.Match(e.Path, file); ok || e.Path == file {
			return true
		}
	}
	return false
}

type finding struct {
	commit string
	file   string
	line   int
	rule   string
}

func (f finding) String() string {
	loc := fmt.Sprintf("%s:%d", f.file, f.line)
	if f.commit != "" {
		return fmt.Sprintf("commit %s %s %s", f.commit, loc, f.rule)
	}
	return fmt.Sprintf("%s %s", loc, f.rule)
}

func scanText(file string, text string, allow allowlist) []finding {
	var out []finding
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64<<10), maxScanLine)
	for n := 1; sc.Scan(); n++ {
		for _, rule := range detectLine(sc.Text()) {
			if !allow.allows("", file, rule) {
				out = append(out, finding{file: file, line: n, rule: rule})
			}
		}
	}
	return out
}

func scanDiff(r io.Reader, allow allowlist) ([]finding, error) {
	var (
		out     []finding
		commit  string
		file    string
		inHunk  bool
		newLine int
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxScanLine)
	hunk := regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)`)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "commit:"):
			commit = strings.TrimPrefix(line, "commit:")
			inHunk = false
		case strings.HasPrefix(line, "diff --git "):
			inHunk = false
			file = ""
		case !inHunk && strings.HasPrefix(line, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
		case strings.HasPrefix(line, "@@"):
			inHunk = true
			if m := hunk.FindStringSubmatch(line); m != nil {
				_, _ = fmt.Sscan(m[1], &newLine)
			}
		case inHunk && strings.HasPrefix(line, "+"):
			for _, rule := range detectLine(line[1:]) {
				if !allow.allows(commit, file, rule) {
					out = append(out, finding{commit: commit, file: file, line: newLine, rule: rule})
				}
			}
			newLine++
		case inHunk && strings.HasPrefix(line, " "):
			newLine++
		}
	}
	return out, sc.Err()
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	return strings.TrimSpace(string(root))
}

func gitOutput(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func loadAllowlist(t *testing.T) allowlist {
	t.Helper()
	b, err := os.ReadFile("secretscan.allow")
	if err != nil {
		t.Fatalf("read allowlist: %v", err)
	}
	allow, err := parseAllowlist(b)
	if err != nil {
		t.Fatalf("allowlist: %v", err)
	}
	return allow
}

func TestNoSecretsInTrackedFiles(t *testing.T) {
	root := repoRoot(t)
	allow := loadAllowlist(t)
	files := bytes.Split(bytes.TrimRight(gitOutput(t, root, "ls-files", "-z"), "\x00"), []byte{0})
	var findings []finding
	for _, f := range files {
		name := string(f)
		if name == "" {
			continue
		}
		b, err := os.ReadFile(root + "/" + name)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", name, err)
		}
		if bytes.IndexByte(b, 0) >= 0 {
			continue
		}
		findings = append(findings, scanText(name, string(b), allow)...)
	}
	for _, f := range findings {
		t.Errorf("likely secret: %s", f)
	}
}

func TestNoSecretsInRecentHistory(t *testing.T) {
	root := repoRoot(t)
	allow := loadAllowlist(t)
	cmd := exec.Command("git", "log", "-p", "--no-color", "--no-ext-diff", "--no-renames",
		fmt.Sprintf("-n%d", historyLimit), "--format=commit:%H")
	cmd.Dir = root
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("git log: %v", err)
	}
	findings, scanErr := scanDiff(stdout, allow)
	_, _ = io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("git log: %v", err)
	}
	if scanErr != nil {
		t.Fatalf("scan history: %v", scanErr)
	}
	seen := map[string]bool{}
	for _, f := range findings {
		t.Errorf("likely secret in history: %s", f)
		seen[f.commit] = true
	}
	if len(seen) > 0 {
		t.Errorf("%d of the last %d commits contain likely secrets", len(seen), historyLimit)
	}
}

func TestNoSecretsInStagedChanges(t *testing.T) {
	if os.Getenv("SECRETSCAN_STAGED") == "" {
		t.Skip("set SECRETSCAN_STAGED=1 to scan the staged diff")
	}
	root := repoRoot(t)
	allow := loadAllowlist(t)
	out := gitOutput(t, root, "diff", "--cached", "--no-color", "--no-ext-diff", "--no-renames", "--diff-filter=AM")
	findings, err := scanDiff(bytes.NewReader(out), allow)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Errorf("likely secret staged: %s", f)
	}
}

func plant(parts ...string) string {
	return strings.Join(parts, "")
}

func TestDetectorCatchesPlantedSecrets(t *testing.T) {
	random := "q8Zr3VnT0xYb6LkP2mWd9HsJ4cFg7AeU"
	cases := []struct {
		rule string
		line string
	}{
		{"private-key", plant("-----BEGIN ", "OPENSSH ", "PRIVATE KEY-----")},
		{"private-key", plant("-----BEGIN ", "PRIVATE KEY-----")},
		{"anthropic-key", plant("export X=sk-", "ant-api03-", random)},
		{"openai-key", plant("OPENAI=sk-", "proj-", random)},
		{"github-token", plant("gh", "p_", random, "abcd")},
		{"github-token", plant("gh", "o_", random, "abcd")},
		{"github-token", plant("gh", "s_", random, "abcd")},
		{"github-token", plant("github", "_pat_", random)},
		{"slack-token", plant("xo", "xb-", "1234567890-abcdefghij")},
		{"aws-access-key", plant("AK", "IA", "ABCDEFGH23456723")},
		{"stripe-key", plant("sk", "_live_", random)},
		{"basic-auth-url", plant("https://deploy:", "hunter2hunter2", "@example.com/repo.git")},
		{"jwt", plant("eyJ", "hbGciOiJIUzI1NiJ9", ".eyJ", "zdWIiOiIxMjM0NTY3ODkwIn0", ".dBjftJeZ4CVPmB92K27uhbUJU1p1r")},
		{genericRule, plant("api_", "token = \"", random, "\"")},
		{genericRule, plant("DB_", "PASSWORD=", random)},
		{genericRule, plant(`"client`, `_secret": "`, random, `"`)},
		{genericRule, plant("signing", "Key := \"", random, "\"")},
	}
	for _, c := range cases {
		got := detectLine(c.line)
		if !slices.Contains(got, c.rule) {
			t.Errorf("rule %s not detected (got %v)", c.rule, got)
		}
	}
}

func TestDetectorIgnoresBenignLines(t *testing.T) {
	benign := []string{
		`Authorization: Bearer <token>`,
		`token := os.Getenv("SUBSPACE_TOKEN")`,
		`SUBSPACE_OWNER_TOKEN_FILE=/run/secrets/owner_token`,
		`password = "aaaaaaaaaaaaaaaaaaaaaaaaaaaa"`,
		`secret: ${SUBSPACE_WEBHOOK_SECRET_GITHUB}`,
		`http://localhost:8080/api/v1/tasks`,
		`https://user@example.com/path`,
		`key := "ssp_..."`,
		`const apiKey = process.env.API_KEY`,
		`sk-learn is a library`,
		`-----BEGIN CERTIFICATE-----`,
		`token_expires_at: null`,
	}
	for _, line := range benign {
		if got := detectLine(line); len(got) > 0 {
			t.Errorf("benign line flagged %v: %q", got, line)
		}
	}
}

func TestDiffScanReportsCommitWithoutValue(t *testing.T) {
	secret := plant("gh", "p_", "q8Zr3VnT0xYb6LkP2mWd9HsJ4cFg7AeUabcd")
	diff := strings.Join([]string{
		"commit:aaaa111",
		"",
		"diff --git a/clean.txt b/clean.txt",
		"--- a/clean.txt",
		"+++ b/clean.txt",
		"@@ -1,1 +1,2 @@",
		" keep",
		"+nothing here",
		"commit:bbbb222",
		"",
		"diff --git a/cfg/env.txt b/cfg/env.txt",
		"--- a/cfg/env.txt",
		"+++ b/cfg/env.txt",
		"@@ -3,2 +3,3 @@",
		" a",
		"+GITHUB=" + secret,
		" b",
		"",
	}, "\n")
	findings, err := scanDiff(strings.NewReader(diff), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v", findings)
	}
	f := findings[0]
	if f.commit != "bbbb222" || f.file != "cfg/env.txt" || f.line != 4 || f.rule != "github-token" {
		t.Fatalf("finding = %+v", f)
	}
	if strings.Contains(f.String(), secret) {
		t.Fatal("finding output leaks the secret")
	}
	allowed, err := scanDiff(strings.NewReader(diff), allowlist{{Path: "cfg/*.txt", Pattern: "github-token", Reason: "fixture"}})
	if err != nil || len(allowed) != 0 {
		t.Fatalf("allowlisted findings = %v, err = %v", allowed, err)
	}
	pinned, err := scanDiff(strings.NewReader(diff), allowlist{{Commit: "bbbb222", Path: "cfg/env.txt", Pattern: "github-token", Reason: "fixture"}})
	if err != nil || len(pinned) != 0 {
		t.Fatalf("commit-allowlisted findings = %v, err = %v", pinned, err)
	}
	other, err := scanDiff(strings.NewReader(diff), allowlist{{Commit: "aaaa111", Path: "cfg/env.txt", Pattern: "github-token", Reason: "fixture"}})
	if err != nil || len(other) != 1 {
		t.Fatalf("allowlist for another commit hid findings = %v, err = %v", other, err)
	}
	if tree := scanText("cfg/env.txt", "GITHUB="+secret, allowlist{{Commit: "bbbb222", Path: "cfg/env.txt", Pattern: "github-token", Reason: "fixture"}}); len(tree) != 1 {
		t.Fatalf("commit-scoped entry hid a tracked-file finding: %v", tree)
	}
}

func TestAllowlistRequiresReason(t *testing.T) {
	bad := []string{
		`[{"path":"a.txt","pattern":"jwt"}]`,
		`[{"path":"a.txt","pattern":"jwt","reason":"  "}]`,
		`[{"path":"","pattern":"jwt","reason":"x"}]`,
		`[{"path":"a.txt","pattern":"nope","reason":"x"}]`,
		`[{"path":"a.txt","pattern":"jwt","reason":"x","extra":1}]`,
		`[{"commit":"abc123","path":"a.txt","pattern":"jwt","reason":"x"}]`,
	}
	for _, in := range bad {
		if _, err := parseAllowlist([]byte(in)); err == nil {
			t.Errorf("accepted %s", in)
		}
	}
	good, err := parseAllowlist([]byte(`[{"path":"a/*.txt","pattern":"jwt","reason":"documented sample"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if !good.allows("", "a/b.txt", "jwt") || good.allows("", "a/b.txt", "aws-access-key") || good.allows("", "c/b.txt", "jwt") {
		t.Fatal("allowlist matching is wrong")
	}
}
