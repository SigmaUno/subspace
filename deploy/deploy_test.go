package deploy

import (
	"encoding/json"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

type composeService struct {
	Image    string    `json:"image"`
	User     string    `json:"user"`
	Profiles []string  `json:"profiles"`
	Hostname string    `json:"hostname"`
	Build    *struct{} `json:"build"`
	Secrets  []struct {
		Source string `json:"source"`
		Target string `json:"target"`
	} `json:"secrets"`
	Volumes []struct {
		Type   string `json:"type"`
		Source string `json:"source"`
		Target string `json:"target"`
	} `json:"volumes"`
	DependsOn map[string]struct {
		Condition string `json:"condition"`
	} `json:"depends_on"`
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
	Secrets map[string]struct {
		File string `json:"file"`
	} `json:"secrets"`
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
	cmd := exec.Command("docker", "compose", "--env-file", os.DevNull, "--profile", "standalone", "--profile", "ha", "config", "--format", "json")
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
			case "caddy", "ha-caddy":
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

var haServices = []string{"ha-postgres", "ha-server-a", "ha-server-b", "ha-web", "ha-caddy", "ha-backup"}

func haProject(t *testing.T) composeProject {
	t.Helper()
	p := loadCompose(t)
	for _, name := range haServices {
		s, ok := p.Services[name]
		if !ok {
			t.Fatalf("docker-compose.yml: service %s missing from the ha profile", name)
		}
		if !slices.Equal(s.Profiles, []string{"ha"}) {
			t.Fatalf("docker-compose.yml: %s must belong to the ha profile only, has %v", name, s.Profiles)
		}
	}
	return p
}

func TestHAProfileHardening(t *testing.T) {
	p := haProject(t)
	for _, name := range haServices {
		s := p.Services[name]
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
		if s.Image != "" {
			if !regexp.MustCompile(`@sha256:[0-9a-f]{64}$`).MatchString(s.Image) {
				t.Errorf("docker-compose.yml: %s: image %s is not pinned by digest", name, s.Image)
			}
			uid, _, _ := strings.Cut(s.User, ":")
			if uid == "" || uid == "0" || uid == "root" {
				t.Errorf("docker-compose.yml: %s: runs as root; set a non-root user", name)
			}
		} else if s.Build == nil {
			t.Errorf("docker-compose.yml: %s: neither pinned image nor build", name)
		}
	}
	for _, f := range []string{"Dockerfile", "web/Dockerfile"} {
		if !regexp.MustCompile(`(?m)^USER (65532:65532|node)$`).MatchString(readRepoFile(t, f)) {
			t.Errorf("%s: final stage must run as a non-root user", f)
		}
	}
}

func TestHAProfileOnlyCaddyPublishesPorts(t *testing.T) {
	p := haProject(t)
	for _, name := range haServices {
		s := p.Services[name]
		if name != "ha-caddy" && len(s.Ports) > 0 {
			t.Errorf("docker-compose.yml: %s must not publish ports; remove its ports section", name)
		}
		if s.NetworkMode == "host" {
			t.Errorf("docker-compose.yml: %s uses host networking", name)
		}
	}
	if !p.Networks["ha-backend"].Internal {
		t.Error("docker-compose.yml: ha-backend must set internal: true")
	}
	for _, name := range []string{"ha-postgres", "ha-web", "ha-backup"} {
		if got := slices.Collect(maps.Keys(p.Services[name].Networks)); !slices.Equal(got, []string{"ha-backend"}) {
			t.Errorf("docker-compose.yml: %s must only join ha-backend, joins %v", name, got)
		}
	}
	for _, name := range []string{"ha-server-a", "ha-server-b", "ha-caddy"} {
		if _, ok := p.Services[name].Networks["ha-backend"]; !ok {
			t.Errorf("docker-compose.yml: %s must join ha-backend", name)
		}
	}
	for _, port := range p.Services["ha-caddy"].Ports {
		if port.Published != "80" && port.Published != "443" {
			t.Errorf("docker-compose.yml: ha-caddy publishes unexpected port %s", port.Published)
		}
	}
}

func TestHAProfileSecretsViaFiles(t *testing.T) {
	p := haProject(t)
	for name, s := range p.Services {
		if !slices.Contains(haServices, name) {
			continue
		}
		for k, v := range s.Environment {
			if regexp.MustCompile(`(?i)(TOKEN|SECRET|PASSWORD|KEY)$`).MatchString(k) && v != "" {
				t.Errorf("docker-compose.yml: %s: %s carries an inline value", name, k)
			}
		}
	}
	mounted := func(name string) map[string]bool {
		m := map[string]bool{}
		for _, sec := range p.Services[name].Secrets {
			m[sec.Target] = true
		}
		return m
	}
	pg := p.Services["ha-postgres"]
	if f := pg.Environment["POSTGRES_PASSWORD_FILE"]; f == "" || !mounted("ha-postgres")[f] {
		t.Errorf("docker-compose.yml: ha-postgres must read its password from a mounted secret file, got %q", f)
	}
	for _, name := range []string{"ha-server-a", "ha-server-b"} {
		s := p.Services[name]
		if f := s.Environment["SUBSPACE_OWNER_TOKEN_FILE"]; f == "" || !mounted(name)[f] {
			t.Errorf("docker-compose.yml: %s: SUBSPACE_OWNER_TOKEN_FILE %q is not a mounted secret", name, f)
		}
		u, err := url.Parse(s.Environment["SUBSPACE_DB"])
		if err != nil || u.Scheme != "postgres" {
			t.Fatalf("docker-compose.yml: %s: SUBSPACE_DB must be a postgres DSN", name)
		}
		if _, has := u.User.Password(); has {
			t.Errorf("docker-compose.yml: %s: the DSN carries a password; use passfile", name)
		}
		if f := u.Query().Get("passfile"); f == "" || !mounted(name)[f] {
			t.Errorf("docker-compose.yml: %s: DSN passfile %q is not a mounted secret", name, f)
		}
	}
	bk := p.Services["ha-backup"]
	if f := bk.Environment["PGPASSFILE"]; f == "" || !mounted("ha-backup")[f] {
		t.Errorf("docker-compose.yml: ha-backup: PGPASSFILE %q is not a mounted secret", f)
	}
	for name, sec := range p.Secrets {
		if !strings.Contains(sec.File, "/secrets/") {
			t.Errorf("docker-compose.yml: secret %s file %s must live in the secrets directory", name, sec.File)
		}
	}
}

func TestHAPostgresStateIsOnDataVolume(t *testing.T) {
	pg := haProject(t).Services["ha-postgres"]
	found := false
	for _, v := range pg.Volumes {
		if v.Target == "/var/lib/postgresql/data" {
			found = v.Type == "bind" && strings.HasSuffix(v.Source, "/pg")
		}
	}
	if !found {
		t.Error("docker-compose.yml: ha-postgres data must be a bind mount of ${SUBSPACE_DATA}/pg")
	}
	if !strings.Contains(strings.Join(pg.Healthcheck.Test, " "), "pg_isready") {
		t.Error("docker-compose.yml: ha-postgres healthcheck must use pg_isready")
	}
}

func TestHAReplicasShareDatabaseSoMCPSessionsNeedNoAffinity(t *testing.T) {
	p := haProject(t)
	a, b := p.Services["ha-server-a"], p.Services["ha-server-b"]
	if a.Environment["SUBSPACE_DB"] == "" || a.Environment["SUBSPACE_DB"] != b.Environment["SUBSPACE_DB"] {
		t.Error("docker-compose.yml: replicas must share one postgres DSN")
	}
	if a.Hostname == "" || a.Hostname == b.Hostname {
		t.Error("docker-compose.yml: replicas need distinct hostnames for their instance ids")
	}
	for _, name := range []string{"ha-server-a", "ha-server-b"} {
		if p.Services["ha-web"].DependsOn[name].Condition != "service_healthy" || p.Services["ha-caddy"].DependsOn[name].Condition != "service_healthy" {
			t.Errorf("docker-compose.yml: ha-web and ha-caddy must wait for %s to be healthy", name)
		}
		if p.Services[name].DependsOn["ha-postgres"].Condition != "service_healthy" {
			t.Errorf("docker-compose.yml: %s must wait for ha-postgres to be healthy", name)
		}
	}
	if !strings.Contains(readRepoFile(t, "internal/server/mcp_sessions.go"), "CREATE TABLE IF NOT EXISTS mcp_sessions") {
		t.Error("internal/server/mcp_sessions.go: MCP sessions are no longer stored in the database; the ha Caddyfile needs session affinity")
	}
}

func TestHACaddyfile(t *testing.T) {
	src := readRepoFile(t, "deploy/ha/Caddyfile")
	for _, want := range []string{
		"Strict-Transport-Security",
		"X-Content-Type-Options nosniff",
		"X-Frame-Options DENY",
		"-Server",
		"max_size",
		"read_header",
		"dial_timeout",
		"response_header_timeout",
		"@agent path /mcp* /healthz",
		"reverse_proxy ha-server-a:8080 ha-server-b:8080",
		"lb_policy round_robin",
		"lb_try_duration",
		"health_uri /healthz",
		"health_interval",
		"health_status 200",
		"fail_duration",
		"http://:8081",
		"reverse_proxy ha-web:3000",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("deploy/ha/Caddyfile: missing %q", want)
		}
	}
	if regexp.MustCompile(`lb_policy\s+(cookie|header|ip_hash|client_ip_hash|uri_hash|first)`).MatchString(src) {
		t.Error("deploy/ha/Caddyfile: sticky or ordered balancing is not needed and would skew load")
	}
	if got := strings.Count(src, "ha-server-a:8080"); got != 1 {
		t.Errorf("deploy/ha/Caddyfile: %d replica upstream lists, want one shared snippet", got)
	}
	web := haProject(t).Services["ha-web"]
	if web.Environment["SUBSPACE_API_URL"] != "http://ha-caddy:8081" {
		t.Errorf("docker-compose.yml: ha-web must reach the API through the balanced internal listener, got %q", web.Environment["SUBSPACE_API_URL"])
	}
}

func TestPGBackupScriptRetentionAndAtomicWrite(t *testing.T) {
	src := readRepoFile(t, "deploy/pg-backup.sh")
	for _, want := range []string{"umask 077", "chmod 700", `"$#" -gt "$keep"`} {
		if !strings.Contains(src, want) {
			t.Errorf("deploy/pg-backup.sh: missing %q", want)
		}
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	bin, dest := t.TempDir(), t.TempDir()
	fake := map[string]string{
		"pg_dump":    "#!/bin/sh\nfor a; do case \"$a\" in --file=*) echo dump >\"${a#--file=}\" ;; esac; done\n",
		"pg_restore": "#!/bin/sh\n[ -s \"${2}\" ]\n",
	}
	for name, body := range fake {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil { //nolint:gosec // test executable
			t.Fatal(err)
		}
	}
	for _, day := range []string{"20200101", "20200102", "20200103"} {
		if err := os.WriteFile(filepath.Join(dest, "pg-daily-"+day+".dump"), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", filepath.Join("..", "deploy", "pg-backup.sh"), "--once")
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "SUBSPACE_BACKUP_DIR="+dest, "SUBSPACE_BACKUP_KEEP=2")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pg-backup.sh --once: %v\n%s", err, out)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 || names[0] != "pg-daily-20200103.dump" || !regexp.MustCompile(`^pg-daily-\d{8}\.dump$`).MatchString(names[1]) {
		t.Fatalf("backup dir after run = %v, want the newest old dump and today's dump only", names)
	}
}
