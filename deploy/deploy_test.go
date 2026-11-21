package deploy

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

type composeService struct {
	Image       string            `json:"image"`
	ReadOnly    bool              `json:"read_only"`
	CapDrop     []string          `json:"cap_drop"`
	SecurityOpt []string          `json:"security_opt"`
	NetworkMode string            `json:"network_mode"`
	Networks    map[string]any    `json:"networks"`
	Environment map[string]string `json:"environment"`
	Healthcheck *struct {
		Test []string `json:"test"`
	} `json:"healthcheck"`
	Ports []struct {
		HostIP    string `json:"host_ip"`
		Published string `json:"published"`
	} `json:"ports"`
}

type composeProject struct {
	Services map[string]composeService `json:"services"`
	Networks map[string]struct {
		Internal bool `json:"internal"`
	} `json:"networks"`
}

func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func loadCompose(t *testing.T) composeProject {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	cmd := exec.Command("docker", "compose", "--env-file", os.DevNull, "--profile", "standalone", "config", "--format", "json")
	cmd.Dir = ".."
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("docker compose config unavailable: %v", err)
	}
	var p composeProject
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestComposeHardening(t *testing.T) {
	p := loadCompose(t)
	for _, name := range []string{"server", "web", "caddy", "backup"} {
		s, ok := p.Services[name]
		if !ok {
			t.Fatalf("service %s missing", name)
		}
		if !s.ReadOnly {
			t.Errorf("%s: rootfs is writable", name)
		}
		if !slices.Contains(s.CapDrop, "ALL") {
			t.Errorf("%s: capabilities not dropped: %v", name, s.CapDrop)
		}
		if !slices.Contains(s.SecurityOpt, "no-new-privileges:true") {
			t.Errorf("%s: no-new-privileges missing", name)
		}
		if s.Healthcheck == nil || len(s.Healthcheck.Test) == 0 {
			t.Errorf("%s: no healthcheck", name)
		}
	}
}

func TestComposeExposure(t *testing.T) {
	p := loadCompose(t)
	for name, s := range p.Services {
		for _, port := range s.Ports {
			switch name {
			case "server", "web":
				if port.HostIP != "127.0.0.1" {
					t.Errorf("%s publishes %s on %q", name, port.Published, port.HostIP)
				}
			case "caddy":
				if port.Published != "80" && port.Published != "443" {
					t.Errorf("caddy publishes unexpected port %s", port.Published)
				}
			default:
				t.Errorf("%s must not publish ports", name)
			}
		}
		if s.NetworkMode == "host" {
			t.Errorf("%s uses host networking", name)
		}
	}
	if p.Services["backup"].NetworkMode != "none" {
		t.Error("backup must run with network_mode none")
	}
	if !p.Networks["internal"].Internal {
		t.Error("internal network must be internal")
	}
	for _, name := range []string{"server", "web"} {
		if _, ok := p.Services[name].Networks["internal"]; !ok {
			t.Errorf("%s is not on the internal network", name)
		}
	}
	if _, ok := p.Services["caddy"].Networks["internal"]; ok {
		t.Error("caddy must not join the internal network")
	}
}

func TestComposeNoInlineSecrets(t *testing.T) {
	p := loadCompose(t)
	secret := regexp.MustCompile(`(?i)(TOKEN|SECRET|PASSWORD|KEY)$`)
	for name, s := range p.Services {
		for k, v := range s.Environment {
			if secret.MatchString(k) && v != "" {
				t.Errorf("%s: %s carries an inline value", name, k)
			}
		}
	}
	if p.Services["server"].Environment["SUBSPACE_OWNER_TOKEN_FILE"] == "" {
		t.Error("server must read the owner token from a file")
	}
}

func TestImagesPinned(t *testing.T) {
	pinned := regexp.MustCompile(`@sha256:[0-9a-f]{64}`)
	for _, name := range []string{"Dockerfile", "web/Dockerfile"} {
		stages := map[string]bool{}
		for _, line := range strings.Split(readRepoFile(t, name), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 || !strings.EqualFold(f[0], "FROM") {
				continue
			}
			if !stages[f[1]] && !pinned.MatchString(f[1]) {
				t.Errorf("%s: %s is not pinned by digest", name, f[1])
			}
			if len(f) >= 4 {
				stages[f[3]] = true
			}
		}
	}
	for name, s := range loadCompose(t).Services {
		if s.Image != "" && !pinned.MatchString(s.Image) {
			t.Errorf("service %s image %s is not pinned by digest", name, s.Image)
		}
	}
}

func TestCaddyfiles(t *testing.T) {
	for _, name := range []string{"Caddyfile", "deploy/caddy/subspace.caddy"} {
		src := readRepoFile(t, name)
		for _, want := range []string{
			"Strict-Transport-Security",
			"X-Content-Type-Options nosniff",
			"X-Frame-Options DENY",
			"-Server",
			"max_size",
			"dial_timeout",
			"response_header_timeout",
			"@agent path /mcp* /healthz",
		} {
			if !strings.Contains(src, want) {
				t.Errorf("%s: missing %q", name, want)
			}
		}
		if got := strings.Count(src, "subspace-server:8080"); got != 1 {
			t.Errorf("%s: %d routes to the Go server, want only the agent route", name, got)
		}
	}
	if !strings.Contains(readRepoFile(t, "Caddyfile"), "read_header") {
		t.Error("Caddyfile: server timeouts missing")
	}
}

func TestBackupScriptPermissionsAndRetention(t *testing.T) {
	src := readRepoFile(t, "deploy/backup.sh")
	for _, want := range []string{"umask 077", "chmod 700", `"$#" -gt "$keep"`} {
		if !strings.Contains(src, want) {
			t.Errorf("backup.sh: missing %q", want)
		}
	}
}

func TestWorkflowLeastPrivilege(t *testing.T) {
	src := readRepoFile(t, ".github/workflows/ci.yml")
	if !regexp.MustCompile(`(?m)^permissions:\n\s+contents: read$`).MatchString(src) {
		t.Error("workflow must default to contents: read")
	}
	if regexp.MustCompile(`(?m)^\s+[a-z-]+: write$`).MatchString(src) {
		t.Error("workflow grants a write permission")
	}
	sha := regexp.MustCompile(`@[0-9a-f]{40}( |$)`)
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "uses: "); i >= 0 && !sha.MatchString(line[i:]) {
			t.Errorf("action not pinned to a commit: %s", strings.TrimSpace(line))
		}
	}
	if strings.Contains(src, "version: latest") {
		t.Error("tool versions must be pinned")
	}
}
