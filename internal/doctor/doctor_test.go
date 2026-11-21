package doctor

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

var now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

type route struct {
	status  int
	body    string
	headers map[string]string
	err     error
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testEnv(vars map[string]string, routes map[string]route) Env {
	return Env{
		Getenv: func(k string) string { return vars[k] },
		Now:    func() time.Time { return now },
		HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			rt, ok := routes[r.Method+" "+r.URL.Host+r.URL.Path]
			if !ok {
				return nil, errors.New("connection refused")
			}
			if rt.err != nil {
				return nil, rt.err
			}
			h := http.Header{}
			for k, v := range rt.headers {
				h.Set(k, v)
			}
			return &http.Response{StatusCode: rt.status, Header: h, Body: io.NopCloser(strings.NewReader(rt.body)), Request: r}, nil
		})},
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
		Output:   func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("not run") },
		LookupHost: func(context.Context, string) ([]string, error) {
			return nil, errors.New("no such host")
		},
		DiskFree: func(string) (uint64, error) { return 100 << 30, nil },
	}
}

func worst(res []Result) Severity {
	rank := map[Severity]int{OK: 0, Info: 1, Warn: 2, Error: 3}
	w := OK
	for _, r := range res {
		if rank[r.Severity] > rank[w] {
			w = r.Severity
		}
	}
	return w
}

func writeFile(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodToken = "Zk3qT9vLx2WmB7nR" + "c4HdYe8PaUf1GjS6"

func TestConfigChecks(t *testing.T) {
	dir := t.TempDir()
	tokenFile := writeFile(t, dir, "token", goodToken+"\n", 0o600)
	openToken := writeFile(t, dir, "token-open", goodToken, 0o644)
	badCapacity := writeFile(t, dir, "cap.json", `{"global_max_agents":-1}`, 0o600)
	goodCapacity := writeFile(t, dir, "cap-ok.json", `{"global_max_agents":10,"limits":[{"provider":"*","model":"*","max_concurrency":5}]}`, 0o600)
	unreadable := filepath.Join(dir, "missing")

	for _, tc := range []struct {
		name string
		fn   check
		vars map[string]string
		want Severity
	}{
		{"token missing", checkOwnerToken, nil, Error},
		{"token short", checkOwnerToken, map[string]string{"SUBSPACE_OWNER_TOKEN": "short"}, Error},
		{"token medium", checkOwnerToken, map[string]string{"SUBSPACE_OWNER_TOKEN": goodToken[:26]}, Warn},
		{"token repetitive", checkOwnerToken, map[string]string{"SUBSPACE_OWNER_TOKEN": strings.Repeat("ab", 20)}, Warn},
		{"token env good", checkOwnerToken, map[string]string{"SUBSPACE_OWNER_TOKEN": goodToken}, OK},
		{"token file good", checkOwnerToken, map[string]string{"SUBSPACE_OWNER_TOKEN_FILE": tokenFile}, OK},
		{"token file open", checkOwnerToken, map[string]string{"SUBSPACE_OWNER_TOKEN_FILE": openToken}, Warn},
		{"token file unreadable", checkOwnerToken, map[string]string{"SUBSPACE_OWNER_TOKEN_FILE": unreadable}, Error},
		{"url default", checkPublicURL, nil, OK},
		{"url https", checkPublicURL, map[string]string{"SUBSPACE_PUBLIC_URL": "https://subspace.example.com"}, OK},
		{"url http remote", checkPublicURL, map[string]string{"SUBSPACE_PUBLIC_URL": "http://subspace.example.com"}, Warn},
		{"url scheme", checkPublicURL, map[string]string{"SUBSPACE_PUBLIC_URL": "ftp://subspace.example.com"}, Error},
		{"url relative", checkPublicURL, map[string]string{"SUBSPACE_PUBLIC_URL": "subspace.example.com"}, Error},
		{"url credentials", checkPublicURL, map[string]string{"SUBSPACE_PUBLIC_URL": "https://u:p" + "@subspace.example.com"}, Error},
		{"addr all interfaces", checkListenAddr, nil, Warn},
		{"addr all interfaces allowlisted", checkListenAddr, map[string]string{"SUBSPACE_OWNER_ALLOW_CIDRS": "10.0.0.0/8"}, Info},
		{"addr loopback", checkListenAddr, map[string]string{"SUBSPACE_ADDR": "127.0.0.1:8080"}, OK},
		{"addr private", checkListenAddr, map[string]string{"SUBSPACE_ADDR": "10.0.0.5:8080"}, Info},
		{"addr malformed", checkListenAddr, map[string]string{"SUBSPACE_ADDR": "8080"}, Error},
		{"addr port", checkListenAddr, map[string]string{"SUBSPACE_ADDR": ":99999"}, Error},
		{"capacity unset", checkCapacityFile, nil, Info},
		{"capacity good", checkCapacityFile, map[string]string{"SUBSPACE_CAPACITY_FILE": goodCapacity}, OK},
		{"capacity invalid", checkCapacityFile, map[string]string{"SUBSPACE_CAPACITY_FILE": badCapacity}, Error},
		{"capacity missing", checkCapacityFile, map[string]string{"SUBSPACE_CAPACITY_FILE": unreadable}, Error},
		{"notify none", checkNotify, nil, Info},
		{"notify webhook", checkNotify, map[string]string{"SUBSPACE_NOTIFY_WEBHOOK_URL": "https://hooks.example.com/x"}, OK},
		{"notify private target", checkNotify, map[string]string{"SUBSPACE_NOTIFY_WEBHOOK_URL": "http://127.0.0.1/x"}, Error},
		{"notify telegram half", checkNotify, map[string]string{"SUBSPACE_NOTIFY_TELEGRAM_BOT_TOKEN": "123:abc"}, Error},
		{"notify smtp incomplete", checkNotify, map[string]string{"SUBSPACE_NOTIFY_SMTP_URL": "smtp://mail.example.com"}, Error},
		{"spawner default", checkSpawner, nil, OK},
		{"spawner hc", checkSpawner, map[string]string{"SUBSPACE_SPAWNER": "hc"}, OK},
		{"spawner hc bad timeout", checkSpawner, map[string]string{"SUBSPACE_SPAWNER": "hc", "SUBSPACE_HC_TIMEOUT": "soon"}, Error},
		{"spawner unknown", checkSpawner, map[string]string{"SUBSPACE_SPAWNER": "k8s"}, Error},
		{"spawner opencode root", checkSpawner, map[string]string{"SUBSPACE_SPAWNER": "opencode"}, Error},
		{"spawner docker", checkSpawner, map[string]string{"SUBSPACE_SPAWNER": "docker"}, Warn},
		{"rates default", checkRateLimits, nil, OK},
		{"rates valid", checkRateLimits, map[string]string{"SUBSPACE_RATE_MCP": "30/s:200", "SUBSPACE_RATE_AUTHFAIL": "off", "SUBSPACE_RATE_ACTIONLINK": "12/m"}, OK},
		{"rates invalid", checkRateLimits, map[string]string{"SUBSPACE_RATE_MCP": "fast"}, Warn},
		{"rates bad burst", checkRateLimits, map[string]string{"SUBSPACE_RATE_AUTHFAIL": "5/s:0"}, Warn},
		{"rates bad unit", checkRateLimits, map[string]string{"SUBSPACE_RATE_ACTIONLINK": "5/h"}, Warn},
		{"env valid", checkEnvValues, map[string]string{"SUBSPACE_LEASE": "20m", "SUBSPACE_BACKUP_KEEP": "7"}, OK},
		{"env bad duration", checkEnvValues, map[string]string{"SUBSPACE_LEASE": "20"}, Error},
		{"env bad count", checkEnvValues, map[string]string{"SUBSPACE_POISON_EXPIRIES": "-1"}, Error},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.fn(context.Background(), testEnv(tc.vars, nil))
			if got := worst(res); got != tc.want {
				t.Fatalf("severity %s, want %s: %+v", got, tc.want, res)
			}
			for _, r := range res {
				if r.Severity != OK && r.Severity != Info && r.Fix == "" && tc.name != "env bad count" && tc.name != "addr port" && tc.name != "capacity good" {
					t.Errorf("%s has no fix hint: %+v", r.Check, r)
				}
			}
		})
	}
}

func seedDB(t *testing.T) (dir, path string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, "subspace.db")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		ev := &model.AuditEvent{TS: now.Add(time.Duration(i) * time.Second), AgentID: "w-1", Role: "worker", Tool: "heartbeat", After: map[string]any{"n": i}}
		if err := st.Atomic(context.Background(), func(tx store.Tx) error { return tx.InsertAudit(ev) }); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func execSQL(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func TestDatabaseChecks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(t *testing.T, dir, path string)
		check  string
		want   Severity
	}{
		{"healthy", func(*testing.T, string, string) {}, "", OK},
		{"file mode", func(t *testing.T, _, path string) {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}, "db.permissions", Warn},
		{"directory mode", func(t *testing.T, dir, _ string) {
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}, "db.permissions", Warn},
		{"not a database", func(t *testing.T, _, path string) {
			writeFile(t, filepath.Dir(path), "subspace.db", strings.Repeat("garbage!", 1024), 0o600)
		}, "db.integrity", Error},
		{"foreign key", func(t *testing.T, _, path string) {
			execSQL(t, path, `CREATE TABLE fk_parent (id INTEGER PRIMARY KEY)`,
				`CREATE TABLE fk_child (pid INTEGER REFERENCES fk_parent(id))`, `INSERT INTO fk_child VALUES (7)`)
		}, "db.foreign_keys", Error},
		{"missing column", func(t *testing.T, _, path string) {
			execSQL(t, path, `ALTER TABLE tasks DROP COLUMN paths`)
		}, "db.schema", Error},
		{"missing table", func(t *testing.T, _, path string) {
			execSQL(t, path, `DROP TABLE kv`)
		}, "db.schema", Error},
		{"audit tampered", func(t *testing.T, _, path string) {
			execSQL(t, path, `DROP TRIGGER audit_no_update`, `UPDATE audit SET note = 'edited' WHERE id = 2`)
		}, "db.audit_chain", Error},
		{"audit event removed", func(t *testing.T, _, path string) {
			execSQL(t, path, `DROP TRIGGER audit_no_delete`, `DELETE FROM audit WHERE id = 1`)
		}, "db.audit_chain", Error},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, path := seedDB(t)
			tc.break_(t, dir, path)
			res := checkDatabase(context.Background(), testEnv(map[string]string{"SUBSPACE_DB": path}, nil))
			if tc.check == "" {
				if worst(res) != OK || len(res) != 6 {
					t.Fatalf("healthy database: %+v", res)
				}
				return
			}
			var hit *Result
			for i := range res {
				if res[i].Check == tc.check && res[i].Severity == tc.want {
					hit = &res[i]
				}
			}
			if hit == nil {
				t.Fatalf("no %s %s result: %+v", tc.check, tc.want, res)
			}
			for _, r := range res {
				if r.Check != tc.check && r.Severity == Error && tc.check != "db.integrity" {
					t.Errorf("unrelated error %+v", r)
				}
			}
		})
	}
}

func TestDatabaseMissingAndPostgres(t *testing.T) {
	res := checkDatabase(context.Background(), testEnv(map[string]string{"SUBSPACE_DB": filepath.Join(t.TempDir(), "none.db")}, nil))
	if len(res) != 1 || res[0].Severity != Warn || res[0].Check != "db.path" {
		t.Fatalf("missing database: %+v", res)
	}
	res = checkDatabase(context.Background(), testEnv(map[string]string{"SUBSPACE_DB": "postgres://u:secret" + "@db/x"}, nil))
	if len(res) != 1 || res[0].Severity != Info || strings.Contains(res[0].Message, "secret") {
		t.Fatalf("postgres: %+v", res)
	}
}

func TestWALSize(t *testing.T) {
	for _, tc := range []struct {
		size int64
		want Severity
	}{{0, OK}, {walWarnBytes, Warn}, {walErrorBytes, Error}} {
		path := filepath.Join(t.TempDir(), "subspace.db")
		if tc.size > 0 {
			f, err := os.Create(path + "-wal")
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Truncate(tc.size); err != nil {
				t.Skip("sparse files unsupported:", err)
			}
			_ = f.Close()
		}
		if got := worst(checkWAL(path)); got != tc.want {
			t.Errorf("wal %d: %s, want %s", tc.size, got, tc.want)
		}
	}
}

func TestDiskSpace(t *testing.T) {
	_, path := seedDB(t)
	backups := filepath.Join(t.TempDir(), "not", "created", "yet")
	free := map[string]uint64{}
	e := testEnv(map[string]string{"SUBSPACE_DB": path, "SUBSPACE_BACKUP_DIR": backups}, nil)
	e.DiskFree = func(dir string) (uint64, error) {
		v, ok := free[dir]
		if !ok {
			return 0, errors.New("unknown volume")
		}
		return v, nil
	}
	existing := filepath.Dir(path)
	for _, tc := range []struct {
		name         string
		data, backup uint64
		wantData     Severity
		wantBackup   Severity
	}{
		{"plenty", 10 << 30, 10 << 30, OK, OK},
		{"low", 100 << 20, 10 << 30, Warn, OK},
		{"full backup volume", 10 << 30, 1 << 10, OK, Error},
	} {
		t.Run(tc.name, func(t *testing.T) {
			free[existing] = tc.data
			free[filepath.Dir(filepath.Dir(filepath.Dir(backups)))] = tc.backup
			res := checkDisk(context.Background(), e)
			if len(res) != 2 || res[0].Severity != tc.wantData || res[1].Severity != tc.wantBackup {
				t.Fatalf("%+v", res)
			}
		})
	}
	clear(free)
	if res := checkDisk(context.Background(), e); worst(res) != Info {
		t.Fatalf("unknown free space: %+v", res)
	}
}

func TestBackups(t *testing.T) {
	_, path := seedDB(t)
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	put := func(dir string, at time.Time) string {
		p := filepath.Join(dir, "subspace-"+at.Format(backupTimeLayout)+".db")
		if err := st.Backup(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, tc := range []struct {
		name     string
		interval string
		setup    func(dir string)
		want     Severity
	}{
		{"directory missing", "", func(string) {}, Warn},
		{"fresh", "", func(dir string) { put(dir, now.Add(-time.Hour)) }, OK},
		{"stale", "", func(dir string) { put(dir, now.Add(-60*time.Hour)) }, Warn},
		{"very stale", "", func(dir string) { put(dir, now.Add(-10*24*time.Hour)) }, Error},
		{"interval relative", "1h", func(dir string) { put(dir, now.Add(-3*time.Hour)) }, Warn},
		{"disabled schedule", "0", func(dir string) { put(dir, now.Add(-time.Hour)) }, OK},
		{"newest corrupt", "", func(dir string) {
			put(dir, now.Add(-2*time.Hour))
			writeFile(t, dir, "subspace-"+now.Add(-time.Hour).Format(backupTimeLayout)+".db", strings.Repeat("junk", 2048), 0o600)
		}, Error},
		{"older corrupt is ignored", "", func(dir string) {
			writeFile(t, dir, "subspace-"+now.Add(-5*time.Hour).Format(backupTimeLayout)+".db", strings.Repeat("junk", 2048), 0o600)
			put(dir, now.Add(-time.Hour))
		}, OK},
		{"unrelated files ignored", "", func(dir string) {
			writeFile(t, dir, "notes.txt", "x", 0o600)
			put(dir, now.Add(-time.Hour))
		}, OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "backups")
			if tc.name != "directory missing" {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			tc.setup(dir)
			vars := map[string]string{"SUBSPACE_DB": path, "SUBSPACE_BACKUP_DIR": dir, "SUBSPACE_BACKUP_INTERVAL": tc.interval}
			res := checkBackups(context.Background(), testEnv(vars, nil))
			if len(res) != 1 || res[0].Severity != tc.want {
				t.Fatalf("%+v, want %s", res, tc.want)
			}
		})
	}
}

func TestTimeSkew(t *testing.T) {
	date := func(d time.Duration) map[string]string {
		return map[string]string{"Date": now.Add(d).Format(http.TimeFormat)}
	}
	url := map[string]string{"SUBSPACE_PUBLIC_URL": "https://subspace.example.com"}
	const key = "HEAD subspace.example.com/healthz"
	for _, tc := range []struct {
		name   string
		vars   map[string]string
		routes map[string]route
		want   Severity
	}{
		{"in sync", url, map[string]route{key: {status: 200, headers: date(time.Second)}}, OK},
		{"ahead", url, map[string]route{key: {status: 200, headers: date(30 * time.Second)}}, Warn},
		{"behind", url, map[string]route{key: {status: 200, headers: date(-10 * time.Minute)}}, Error},
		{"no date", url, map[string]route{key: {status: 200}}, Info},
		{"unreachable", url, nil, Info},
		{"local", nil, nil, Info},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := checkTimeSkew(context.Background(), testEnv(tc.vars, tc.routes))
			if len(res) != 1 || res[0].Severity != tc.want {
				t.Fatalf("%+v, want %s", res, tc.want)
			}
		})
	}
}

func TestHC(t *testing.T) {
	hc := map[string]string{"SUBSPACE_SPAWNER": "hc"}
	found := func(e *Env) { e.LookPath = func(n string) (string, error) { return "/usr/bin/" + n, nil } }
	for _, tc := range []struct {
		name  string
		vars  map[string]string
		setup func(e *Env)
		want  Severity
	}{
		{"other spawner", nil, nil, Info},
		{"missing binary", hc, nil, Error},
		{"version", hc, func(e *Env) {
			found(e)
			e.Output = func(context.Context, string, ...string) ([]byte, error) {
				return []byte("hc 1.4.2\nbuilt today\n"), nil
			}
		}, OK},
		{"version fails", hc, func(e *Env) { found(e) }, Warn},
		{"remote without ssh", map[string]string{"SUBSPACE_SPAWNER": "hc", "SUBSPACE_HC_HOST": "box"}, nil, Error},
		{"remote", map[string]string{"SUBSPACE_SPAWNER": "hc", "SUBSPACE_HC_HOST": "box"}, found, Info},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := testEnv(tc.vars, nil)
			if tc.setup != nil {
				tc.setup(&e)
			}
			res := checkHC(context.Background(), e)
			if len(res) != 1 || res[0].Severity != tc.want {
				t.Fatalf("%+v, want %s", res, tc.want)
			}
		})
	}
}

func TestForgejoMirror(t *testing.T) {
	vars := map[string]string{
		"SUBSPACE_MIRROR_FORGEJO_URL": "https://forge.example.com", "SUBSPACE_MIRROR_FORGEJO_TOKEN": "tok-abcdef123456",
		"SUBSPACE_MIRROR_FORGEJO_REPO_MAP": "SigmaUno/subspace=bot/issues",
	}
	user := "GET forge.example.com/api/v1/user"
	repo := "GET forge.example.com/api/v1/repos/bot/issues"
	for _, tc := range []struct {
		name   string
		vars   map[string]string
		routes map[string]route
		want   Severity
	}{
		{"not configured", nil, nil, Info},
		{"incomplete", map[string]string{"SUBSPACE_MIRROR_FORGEJO_URL": "https://forge.example.com"}, nil, Error},
		{"bad map", map[string]string{"SUBSPACE_MIRROR_FORGEJO_URL": "https://f", "SUBSPACE_MIRROR_FORGEJO_TOKEN": "t", "SUBSPACE_MIRROR_FORGEJO_REPO_MAP": "oops"}, nil, Error},
		{"healthy", vars, map[string]route{user: {status: 200}, repo: {status: 200, body: `{"permissions":{"push":true}}`}}, OK},
		{"token rejected", vars, map[string]route{user: {status: 401}}, Error},
		{"unreachable", vars, nil, Warn},
		{"repo hidden", vars, map[string]route{user: {status: 200}, repo: {status: 404}}, Error},
		{"read only", vars, map[string]route{user: {status: 200}, repo: {status: 200, body: `{"permissions":{"push":false}}`}}, Warn},
		{"server error", vars, map[string]route{user: {status: 502}}, Warn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := checkForgejoMirror(context.Background(), testEnv(tc.vars, tc.routes))
			if got := worst(res); got != tc.want {
				t.Fatalf("%s, want %s: %+v", got, tc.want, res)
			}
		})
	}
}

func TestPRSyncHosts(t *testing.T) {
	dir := t.TempDir()
	cfg := writeFile(t, dir, "prsync.json", `{"hosts":{
		"gh":{"kind":"github","token_env":"GH_TOKEN"},
		"forge":{"kind":"forgejo","url":"https://forge.example.com","token_env":"FORGE_TOKEN"}}}`, 0o600)
	bad := writeFile(t, dir, "bad.json", `{"hosts":{}}`, 0o600)
	ghUser := "GET api.github.com/user"
	forgeUser := "GET forge.example.com/api/v1/user"
	forgeRepos := "GET forge.example.com/api/v1/user/repos"
	tokens := map[string]string{"SUBSPACE_PRSYNC_CONFIG": cfg, "GH_TOKEN": "ghp_secretsecret", "FORGE_TOKEN": "forge-secretsecret"}
	healthy := map[string]route{
		ghUser:    {status: 200, headers: map[string]string{"X-OAuth-Scopes": "read:org, repo"}},
		forgeUser: {status: 200}, forgeRepos: {status: 200},
	}
	with := func(base map[string]route, k string, r route) map[string]route {
		out := map[string]route{}
		for kk, v := range base {
			out[kk] = v
		}
		out[k] = r
		return out
	}
	for _, tc := range []struct {
		name   string
		vars   map[string]string
		routes map[string]route
		want   Severity
	}{
		{"not configured", nil, nil, Info},
		{"invalid config", map[string]string{"SUBSPACE_PRSYNC_CONFIG": bad}, nil, Error},
		{"healthy", tokens, healthy, OK},
		{"no tokens", map[string]string{"SUBSPACE_PRSYNC_CONFIG": cfg}, healthy, Warn},
		{"github token rejected", tokens, with(healthy, ghUser, route{status: 401}), Error},
		{"github scope missing", tokens, with(healthy, ghUser, route{status: 200, headers: map[string]string{"X-OAuth-Scopes": "read:org"}}), Warn},
		{"github fine grained", tokens, with(healthy, ghUser, route{status: 200}), OK},
		{"forgejo token rejected", tokens, with(healthy, forgeUser, route{status: 401}), Error},
		{"forgejo scope missing", tokens, with(healthy, forgeRepos, route{status: 403}), Warn},
		{"hosts unreachable", tokens, nil, Warn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := checkPRSyncHosts(context.Background(), testEnv(tc.vars, tc.routes))
			if got := worst(res); got != tc.want {
				t.Fatalf("%s, want %s: %+v", got, tc.want, res)
			}
		})
	}
}

func TestNotifyDNS(t *testing.T) {
	smtp := map[string]string{"SUBSPACE_NOTIFY_SMTP_URL": "smtp://mail.example.com:587"}
	both := map[string]string{"SUBSPACE_NOTIFY_SMTP_URL": "smtp://mail.example.com", "SUBSPACE_NOTIFY_TELEGRAM_BOT_TOKEN": "1:abc"}
	resolving := func(hosts ...string) func(*Env) {
		return func(e *Env) {
			e.LookupHost = func(_ context.Context, h string) ([]string, error) {
				for _, ok := range hosts {
					if h == ok {
						return []string{"192.0.2.1"}, nil
					}
				}
				return nil, errors.New("no such host")
			}
		}
	}
	for _, tc := range []struct {
		name  string
		vars  map[string]string
		setup func(*Env)
		want  Severity
		count int
	}{
		{"nothing configured", nil, nil, Info, 1},
		{"smtp resolves", smtp, resolving("mail.example.com"), OK, 1},
		{"smtp fails", smtp, nil, Warn, 1},
		{"telegram only fails", both, resolving("mail.example.com"), Warn, 1},
		{"both fail", both, nil, Warn, 2},
		{"ip literal", map[string]string{"SUBSPACE_NOTIFY_SMTP_URL": "smtp://192.0.2.9"}, nil, OK, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := testEnv(tc.vars, nil)
			if tc.setup != nil {
				tc.setup(&e)
			}
			res := checkNotifyDNS(context.Background(), e)
			if len(res) != tc.count || worst(res) != tc.want {
				t.Fatalf("%+v, want %d x %s", res, tc.count, tc.want)
			}
		})
	}
}

func TestReportNeverContainsSecrets(t *testing.T) {
	dir, path := seedDB(t)
	secrets := []string{"owner-secret-AAAA1111bbbb2222cccc3333", "hooks-secret-path-77", "smtp-pass-88", "forge-token-99", "file-token-ZZZZ0000yyyy1111xxxx2222"}
	tokenFile := writeFile(t, dir, "owner", secrets[4]+"\n", 0o600)
	vars := map[string]string{
		"SUBSPACE_DB":                      path,
		"SUBSPACE_OWNER_TOKEN":             secrets[0],
		"SUBSPACE_OWNER_TOKEN_FILE":        tokenFile,
		"SUBSPACE_NOTIFY_WEBHOOK_URL":      "http://[::1/" + secrets[1],
		"SUBSPACE_NOTIFY_SMTP_URL":         "smtp://user:" + secrets[2] + "@mail.example.com",
		"SUBSPACE_MIRROR_FORGEJO_URL":      "https://forge.example.com",
		"SUBSPACE_MIRROR_FORGEJO_TOKEN":    secrets[3],
		"SUBSPACE_MIRROR_FORGEJO_REPO_MAP": "a/b=bot/issues",
		"SUBSPACE_PUBLIC_URL":              "https://subspace.example.com",
	}
	e := testEnv(vars, map[string]route{"GET forge.example.com/api/v1/user": {status: 401}})
	rep := Run(context.Background(), e)
	if rep.Errors == 0 {
		t.Fatal("expected errors in this configuration")
	}
	var text, js bytes.Buffer
	if err := rep.WriteText(&text); err != nil {
		t.Fatal(err)
	}
	if err := rep.WriteJSON(&js); err != nil {
		t.Fatal(err)
	}
	for _, s := range secrets {
		if strings.Contains(text.String(), s) || strings.Contains(js.String(), s) {
			t.Errorf("secret %q leaked into the report", s)
		}
	}
	if !strings.Contains(text.String(), "owner token from SUBSPACE_OWNER_TOKEN_FILE") {
		t.Errorf("token source missing from report:\n%s", text.String())
	}
}
