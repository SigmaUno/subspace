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
		t.Fatalf("cannot read %s: %v", name, err)
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
		t.Fatalf("docker-compose.yml: cannot parse `docker compose config` output: %v", err)
	}
	return p
}

func TestComposeHardening(t *testing.T) {
	p := loadCompose(t)
	for _, name := range []string{"server", "web", "caddy", "backup"} {
		s, ok := p.Services[name]
		if !ok {
			t.Fatalf("docker-compose.yml: service %s missing", name)
		}
		if !s.ReadOnly {
			t.Errorf("docker-compose.yml: %s: rootfs is writable; set read_only: true", name)
		}
		if !slices.Contains(s.CapDrop, "ALL") {
			t.Errorf("docker-compose.yml: %s: capabilities not dropped (%v); add cap_drop: [ALL]", name, s.CapDrop)
		}
		if !slices.Contains(s.SecurityOpt, "no-new-privileges:true") {
			t.Errorf("docker-compose.yml: %s: no-new-privileges missing; add it to security_opt", name)
		}
		if s.Healthcheck == nil || len(s.Healthcheck.Test) == 0 {
			t.Errorf("docker-compose.yml: %s: no healthcheck; add one", name)
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
					t.Errorf("docker-compose.yml: %s publishes %s on %q; bind it to 127.0.0.1", name, port.Published, port.HostIP)
				}
			case "caddy":
				if port.Published != "80" && port.Published != "443" {
					t.Errorf("docker-compose.yml: caddy publishes unexpected port %s; only 80 and 443 are allowed", port.Published)
				}
			default:
				t.Errorf("docker-compose.yml: %s must not publish ports; remove its ports section", name)
			}
		}
		if s.NetworkMode == "host" {
			t.Errorf("docker-compose.yml: %s uses host networking; remove network_mode: host", name)
		}
	}
	if p.Services["backup"].NetworkMode != "none" {
		t.Error("docker-compose.yml: backup must run with network_mode: none")
	}
	if !p.Networks["internal"].Internal {
		t.Error("docker-compose.yml: the internal network must set internal: true")
	}
	for _, name := range []string{"server", "web"} {
		if _, ok := p.Services[name].Networks["internal"]; !ok {
			t.Errorf("docker-compose.yml: %s must join the internal network", name)
		}
	}
	if _, ok := p.Services["caddy"].Networks["internal"]; ok {
		t.Error("docker-compose.yml: caddy must not join the internal network")
	}
}

func TestComposeNoInlineSecrets(t *testing.T) {
	p := loadCompose(t)
	secret := regexp.MustCompile(`(?i)(TOKEN|SECRET|PASSWORD|KEY)$`)
	for name, s := range p.Services {
		for k, v := range s.Environment {
			if secret.MatchString(k) && v != "" {
				t.Errorf("docker-compose.yml: %s: %s carries an inline value; read it from a file or the environment of the host instead", name, k)
			}
		}
	}
	if p.Services["server"].Environment["SUBSPACE_OWNER_TOKEN_FILE"] == "" {
		t.Error("docker-compose.yml: server must set SUBSPACE_OWNER_TOKEN_FILE")
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
				t.Errorf("%s: %s is not pinned by digest; append @sha256:<digest> in %s", name, f[1], name)
			}
			if len(f) >= 4 {
				stages[f[3]] = true
			}
		}
	}
	for name, s := range loadCompose(t).Services {
		if s.Image != "" && !pinned.MatchString(s.Image) {
			t.Errorf("docker-compose.yml: service %s image %s is not pinned by digest; append @sha256:<digest>", name, s.Image)
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
				t.Errorf("%s: missing %q; add it to %s", name, want, name)
			}
		}
		if got := strings.Count(src, "subspace-server:8080"); got != 1 {
			t.Errorf("%s: %d routes to the Go server, want only the agent route; edit %s", name, got, name)
		}
	}
	if !strings.Contains(readRepoFile(t, "Caddyfile"), "read_header") {
		t.Error("Caddyfile: server timeouts missing; add read_header to Caddyfile")
	}
}

func TestBackupScriptPermissionsAndRetention(t *testing.T) {
	src := readRepoFile(t, "deploy/backup.sh")
	for _, want := range []string{"umask 077", "chmod 700", `"$#" -gt "$keep"`} {
		if !strings.Contains(src, want) {
			t.Errorf("deploy/backup.sh: missing %q; add it to deploy/backup.sh", want)
		}
	}
}

func TestWorkflowLeastPrivilege(t *testing.T) {
	src := readRepoFile(t, ".github/workflows/ci.yml")
	if !regexp.MustCompile(`(?m)^permissions:\n\s+contents: read$`).MatchString(src) {
		t.Error(".github/workflows/ci.yml: workflow must default to contents: read; propose the change in ci-proposals/ci.yml, the leader installs it")
	}
	if regexp.MustCompile(`(?m)^\s+[a-z-]+: write$`).MatchString(src) {
		t.Error(".github/workflows/ci.yml: workflow grants a write permission; propose the change in ci-proposals/ci.yml, the leader installs it")
	}
	sha := regexp.MustCompile(`@[0-9a-f]{40}( |$)`)
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "uses: "); i >= 0 && !sha.MatchString(line[i:]) {
			t.Errorf(".github/workflows/ci.yml: action not pinned to a commit: %s; propose the change in ci-proposals/ci.yml, the leader installs it", strings.TrimSpace(line))
		}
	}
	if strings.Contains(src, "version: latest") {
		t.Error(".github/workflows/ci.yml: tool versions must be pinned, not `latest`; propose the change in ci-proposals/ci.yml, the leader installs it")
	}
}
