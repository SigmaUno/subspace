package offsite_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/backup"
	"github.com/SigmaUno/subspace/internal/backup/offsite"
	"github.com/SigmaUno/subspace/internal/backup/s3"
	"github.com/SigmaUno/subspace/internal/backup/s3/s3test"
	"github.com/SigmaUno/subspace/internal/backup/seal"
	"github.com/SigmaUno/subspace/internal/netguard"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

var creds = s3.Credentials{AccessKey: "test-access-key-id", SecretKey: "test-secret-access-key"}

var t0 = time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

type localBackups struct {
	dir  string
	list []backup.Info
}

func (l *localBackups) add(t *testing.T, at time.Time, data []byte) backup.Info {
	t.Helper()
	name := "subspace-" + at.UTC().Format("20060102T150405Z") + ".db"
	path := filepath.Join(l.dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	info := backup.Info{Name: name, Path: path, Bytes: int64(len(data)), Verified: true, CreatedAt: at}
	l.list = append([]backup.Info{info}, l.list...)
	return info
}

func (l *localBackups) List(context.Context) ([]backup.Info, error) {
	return append([]backup.Info(nil), l.list...), nil
}

func keyFile(t *testing.T, fill byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "backup.key")
	if err := os.WriteFile(p, []byte(strings.Repeat(fmt.Sprintf("%02x", fill), seal.KeySize)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func payload(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

type rig struct {
	t      *testing.T
	cfg    offsite.Config
	deps   offsite.Deps
	local  *localBackups
	clk    *clock
	svc    *offsite.Service
	remote *offsite.Remote
	st     *sqlite.Store
	s3     *s3test.Server
	dir    string
}

type rigOpt func(*rig)

func newFSRig(t *testing.T, opts ...rigOpt) *rig {
	t.Helper()
	dest := t.TempDir()
	r := &rig{t: t, dir: t.TempDir(), clk: &clock{t: t0}}
	r.cfg = offsite.Config{
		Kind: offsite.KindFS, Path: dest, KeyFile: keyFile(t, 7), KeyID: "k1",
		Retention: offsite.DefaultPolicy, Interval: time.Minute,
	}
	return r.start(opts)
}

func newS3Rig(t *testing.T, opts ...rigOpt) *rig {
	t.Helper()
	srv := s3test.New(t, "offsite", "eu-west-1", creds)
	t.Setenv("TEST_OFFSITE_ACCESS", creds.AccessKey)
	t.Setenv("TEST_OFFSITE_SECRET", creds.SecretKey)
	r := &rig{t: t, dir: t.TempDir(), clk: &clock{t: t0}, s3: srv}
	r.cfg = offsite.Config{
		Kind: offsite.KindS3, KeyFile: keyFile(t, 7), KeyID: "k1", Retention: offsite.DefaultPolicy, Interval: time.Minute,
		S3: offsite.S3{
			Endpoint: srv.Endpoint(), Region: "eu-west-1", Bucket: "offsite", Prefix: "subspace/prod", AllowPrivate: true,
			AccessKey: offsite.Source{Env: "TEST_OFFSITE_ACCESS"}, SecretKey: offsite.Source{Env: "TEST_OFFSITE_SECRET"},
		},
	}
	r.deps.HTTP = srv.Client()
	r.deps.Sleep = func(context.Context, time.Duration) error { return nil }
	return r.start(opts)
}

func (r *rig) start(opts []rigOpt) *rig {
	t := r.t
	for _, o := range opts {
		o(r)
	}
	r.deps.Getenv = os.Getenv
	r.deps.Now = r.clk.Now
	var err error
	if r.remote, err = offsite.Open(r.cfg, r.deps); err != nil {
		t.Fatal(err)
	}
	if r.st, err = sqlite.Open(filepath.Join(t.TempDir(), "live.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.st.Close() })
	r.local = &localBackups{dir: r.dir}
	r.svc, err = offsite.NewService(offsite.Options{
		Remote: r.remote, Store: r.st, Backups: r.local, StageDir: filepath.Join(r.dir, "stage"),
		Retention: r.cfg.Retention, Interval: r.cfg.Interval, Now: r.clk.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) state() offsite.State {
	r.t.Helper()
	st, err := offsite.LoadState(context.Background(), r.st)
	if err != nil {
		r.t.Fatal(err)
	}
	return st
}

func (r *rig) objects() []string {
	r.t.Helper()
	list, err := r.remote.List(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name)
	}
	return names
}

func (r *rig) restoreLatest() ([]byte, string) {
	r.t.Helper()
	var buf bytes.Buffer
	name, _, err := r.remote.Fetch(context.Background(), &buf)
	if err != nil {
		r.t.Fatal(err)
	}
	return buf.Bytes(), name
}

func TestUploadEncryptsVerifiesAndRestores(t *testing.T) {
	for name, mk := range map[string]func(*testing.T, ...rigOpt) *rig{"fs": newFSRig, "s3": newS3Rig} {
		t.Run(name, func(t *testing.T) {
			r := mk(t)
			data := append([]byte("SQLite format 3\x00"), payload(300_000, 1)...)
			info := r.local.add(t, t0, data)
			res, err := r.svc.Sync(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !res.Uploaded || res.Object != offsite.ObjectName(t0) {
				t.Fatalf("result %+v", res)
			}
			st := r.state()
			rec := st.Records[info.Name]
			if !rec.Verified || rec.Object != res.Object || rec.KeyID != "k1" || rec.Error != "" || st.LastError != "" || st.LastSuccessAt.IsZero() {
				t.Fatalf("state %+v", st)
			}
			got, name := r.restoreLatest()
			if name != res.Object || !bytes.Equal(got, data) {
				t.Fatal("restored backup differs")
			}
			if r.s3 != nil {
				raw, ok := r.s3.Get("subspace/prod/" + res.Object)
				if !ok || bytes.Contains(raw, []byte("SQLite format")) || bytes.Contains(raw, data[100:140]) {
					t.Fatal("object at the destination is not encrypted")
				}
				if r.s3.Rejected != 0 {
					t.Fatalf("%d requests rejected", r.s3.Rejected)
				}
			}
			again, err := r.svc.Sync(context.Background())
			if err != nil || again.Uploaded {
				t.Fatalf("second sync: %+v %v", again, err)
			}
		})
	}
}

func TestS3MultipartUploadRoundTrip(t *testing.T) {
	r := newS3Rig(t, func(r *rig) { r.cfg.S3.PartSize = s3.MinPartSize })
	data := payload(11<<20, 2)
	r.local.add(t, t0, data)
	if _, err := r.svc.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	var parts int
	for _, q := range r.s3.Requests() {
		if strings.Contains(q, "partNumber=") {
			parts++
		}
	}
	if parts < 3 {
		t.Fatalf("expected a multipart upload, saw %d parts", parts)
	}
	got, _ := r.restoreLatest()
	if !bytes.Equal(got, data) {
		t.Fatal("restored backup differs")
	}
}

func TestVerifyCatchesCorruptionAndRetries(t *testing.T) {
	r := newS3Rig(t)
	r.local.add(t, t0, payload(200_000, 3))
	var corrupted bool
	r.s3.Inject(func(req *http.Request) int {
		if req.Method == http.MethodHead && !corrupted {
			corrupted = true
			r.s3.Mutate("subspace/prod/"+offsite.ObjectName(t0), func(b []byte) { b[len(b)-1] ^= 0xff })
		}
		return 0
	})
	if _, err := r.svc.Sync(context.Background()); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("corrupted upload accepted: %v", err)
	}
	st := r.state()
	rec := st.Records[r.local.list[0].Name]
	if rec.Verified || rec.Error == "" || st.LastError == "" {
		t.Fatalf("failure not recorded: %+v", st)
	}
	if _, _, ok := st.Newest(); ok {
		t.Fatal("unverified upload counts as the newest remote backup")
	}
	r.clk.Set(t0.Add(time.Minute))
	if res, err := r.svc.Sync(context.Background()); err != nil || !res.Uploaded {
		t.Fatalf("retry: %+v %v", res, err)
	}
	if st := r.state(); !st.Records[r.local.list[0].Name].Verified || st.LastError != "" {
		t.Fatalf("recovered state %+v", st)
	}
}

func TestRetentionKeepsLastDailyAndWeekly(t *testing.T) {
	r := newFSRig(t, func(r *rig) { r.cfg.Retention = offsite.Policy{Last: 2, Daily: 3, Weekly: 2} })
	for day := range 40 {
		at := t0.AddDate(0, 0, day)
		r.clk.Set(at)
		r.local.add(t, at, payload(2000, int64(day)))
		if _, err := r.svc.Sync(context.Background()); err != nil {
			t.Fatalf("day %d: %v", day, err)
		}
	}
	got := r.objects()
	want := map[string]bool{}
	for _, d := range []int{39, 38, 37} {
		want[offsite.ObjectName(t0.AddDate(0, 0, d))] = true
	}
	for _, d := range []int{35} {
		want[offsite.ObjectName(t0.AddDate(0, 0, d))] = true
	}
	if len(got) != len(want) {
		t.Fatalf("kept %v, want %d objects", got, len(want))
	}
	for _, n := range got {
		if !want[n] {
			t.Fatalf("kept %s unexpectedly: %v", n, got)
		}
	}
	for name, rec := range r.state().Records {
		if !want[rec.Object] {
			t.Fatalf("record %s for pruned object %s survived", name, rec.Object)
		}
	}
}

func TestFutureDatedObjectsDoNotPushOutRealBackups(t *testing.T) {
	r := newFSRig(t, func(r *rig) { r.cfg.Retention = offsite.Policy{Last: 2, Daily: 2, Weekly: 1} })
	for day := range 3 {
		at := t0.AddDate(0, 0, day)
		r.clk.Set(at)
		r.local.add(t, at, payload(2000, int64(day)))
		if _, err := r.svc.Sync(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for week := range 3 {
		junk := filepath.Join(r.cfg.Path, offsite.ObjectName(time.Date(2099, 1, 1+7*week, 0, 0, 0, 0, time.UTC)))
		if err := os.WriteFile(junk, []byte("junk"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.svc.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(r.objects(), " ")
	for _, day := range []int{1, 2} {
		if name := offsite.ObjectName(t0.AddDate(0, 0, day)); !strings.Contains(got, name) {
			t.Fatalf("planted future objects pruned real backup %s: %s", name, got)
		}
	}
	if _, name := r.restoreLatest(); name != offsite.ObjectName(t0.AddDate(0, 0, 2)) {
		t.Fatalf("latest is %s", name)
	}
}

func TestPolicyPrune(t *testing.T) {
	at := func(d, h int) offsite.Entry {
		c := t0.AddDate(0, 0, d).Add(time.Duration(h) * time.Hour)
		return offsite.Entry{Name: offsite.ObjectName(c), Created: c}
	}
	entries := []offsite.Entry{at(0, 0), at(0, 12), at(1, 0), at(2, 0), at(9, 0), at(16, 0)}
	names := func(es []offsite.Entry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.Name)
		}
		return out
	}
	got := names(offsite.Policy{Last: 1, Daily: 2}.Prune(entries, ""))
	want := names([]offsite.Entry{at(2, 0), at(1, 0), at(0, 12), at(0, 0)})
	if !slices.Equal(got, want) {
		t.Fatalf("dropped %v, want %v", got, want)
	}
	got = names(offsite.Policy{Last: 1}.Prune(entries, at(0, 0).Name))
	want = names([]offsite.Entry{at(9, 0), at(2, 0), at(1, 0), at(0, 12)})
	if !slices.Equal(got, want) {
		t.Fatalf("with a protected object dropped %v, want %v", got, want)
	}
}

func TestRestoreFailsOnTamperedTruncatedOrWrongKey(t *testing.T) {
	r := newFSRig(t)
	data := payload(3_000_000, 4)
	r.local.add(t, t0, data)
	if _, err := r.svc.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.cfg.Path, offsite.ObjectName(t0))
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fetch := func() error {
		var buf bytes.Buffer
		_, _, err := r.remote.Fetch(context.Background(), &buf)
		return err
	}
	mut := bytes.Clone(orig)
	mut[len(mut)/2] ^= 1
	if err := os.WriteFile(path, mut, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fetch(); !errors.Is(err, seal.ErrAuth) {
		t.Fatalf("tampered object: %v", err)
	}
	if err := os.WriteFile(path, orig[:47+2*(1<<20+16)], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fetch(); !errors.Is(err, seal.ErrTruncated) {
		t.Fatalf("truncated object: %v", err)
	}
	if err := os.WriteFile(path, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	other := r.cfg
	other.KeyFile, other.KeyID = keyFile(t, 9), "k1"
	wrong, err := offsite.Open(other, r.deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := wrong.Fetch(context.Background(), &bytes.Buffer{}); !errors.Is(err, seal.ErrAuth) {
		t.Fatalf("wrong key: %v", err)
	}
	other.KeyID = "other"
	stranger, err := offsite.Open(other, r.deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := stranger.Fetch(context.Background(), &bytes.Buffer{}); !errors.Is(err, seal.ErrUnknownID) {
		t.Fatalf("unknown key id: %v", err)
	}
}

func TestKeyRotationKeepsOldBackupsReadable(t *testing.T) {
	r := newFSRig(t)
	data := payload(5000, 5)
	r.local.add(t, t0, data)
	if _, err := r.svc.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	rotated := r.cfg
	rotated.OldKeys = []offsite.KeyRef{{ID: "k1", File: r.cfg.KeyFile}}
	rotated.KeyFile, rotated.KeyID = keyFile(t, 8), "k2"
	remote, err := offsite.Open(rotated, r.deps)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, _, err := remote.Fetch(context.Background(), &buf); err != nil || !bytes.Equal(buf.Bytes(), data) {
		t.Fatalf("old backup with rotated key ring: %v", err)
	}
}

func TestUnmountedDestinationIsNotCreated(t *testing.T) {
	r := newFSRig(t)
	gone := filepath.Join(t.TempDir(), "mnt", "backup")
	r.cfg.Path = gone
	r.start(nil)
	r.local.add(t, t0, payload(1000, 6))
	if _, err := r.svc.Sync(context.Background()); err == nil {
		t.Fatal("sync into a missing destination succeeded")
	}
	if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination was created: %v", err)
	}
	if st := r.state(); st.LastError == "" || st.LastAttemptAt.IsZero() {
		t.Fatalf("failure not recorded: %+v", st)
	}
}

func TestUnverifiedLocalBackupIsNotUploaded(t *testing.T) {
	r := newFSRig(t)
	r.local.add(t, t0, payload(1000, 7))
	r.local.list[0].Verified = false
	if res, err := r.svc.Sync(context.Background()); err != nil || res.Backup != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if len(r.objects()) != 0 {
		t.Fatal("unverified backup uploaded")
	}
}

func TestEndpointPolicy(t *testing.T) {
	cfg := offsite.Config{
		Kind: offsite.KindS3, KeyFile: keyFile(t, 7), Retention: offsite.DefaultPolicy, Interval: time.Minute,
		S3: offsite.S3{
			Endpoint: "https://s3.example.test", Region: "eu-west-1", Bucket: "offsite",
			AccessKey: offsite.Source{Env: "A"}, SecretKey: offsite.Source{Env: "B"},
		},
	}
	t.Setenv("A", creds.AccessKey)
	t.Setenv("B", creds.SecretKey)
	remote, err := offsite.Open(cfg, offsite.Deps{
		Getenv:   os.Getenv,
		Resolver: staticResolver{"s3.example.test": "10.1.2.3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.List(context.Background()); !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("hostname resolving to a private address: %v", err)
	}
	for _, endpoint := range []string{"https://10.0.0.5", "https://127.0.0.1:9000", "https://localhost", "https://169.254.169.254"} {
		bad := cfg
		bad.S3.Endpoint = endpoint
		if err := bad.Validate(); err == nil {
			t.Errorf("%s accepted without %s", endpoint, offsite.EnvS3AllowPrivate)
		}
	}
	private := cfg
	private.S3.Endpoint, private.S3.AllowPrivate = "http://10.0.0.5:9000", true
	if err := private.Validate(); err != nil {
		t.Fatalf("explicitly allowed private endpoint: %v", err)
	}
	private.S3.Endpoint = "http://169.254.169.254"
	if err := private.Validate(); err == nil {
		t.Fatal("allow-private must not unblock the metadata service")
	}
}

type staticResolver map[string]string

func (s staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	ip, ok := s[host]
	if !ok {
		return nil, fmt.Errorf("no such host %s", host)
	}
	return []netip.Addr{netip.MustParseAddr(ip)}, nil
}

func TestConfigFromEnv(t *testing.T) {
	keyPath := keyFile(t, 7)
	base := map[string]string{
		"SUBSPACE_OFFSITE_KIND":               "s3",
		"SUBSPACE_OFFSITE_KEY_FILE":           keyPath,
		"SUBSPACE_OFFSITE_S3_ENDPOINT":        "https://s3.eu-west-1.amazonaws.com",
		"SUBSPACE_OFFSITE_S3_REGION":          "eu-west-1",
		"SUBSPACE_OFFSITE_S3_BUCKET":          "my-backups",
		"SUBSPACE_OFFSITE_S3_ACCESS_KEY_ENV":  "AWS_ACCESS_KEY_ID",
		"SUBSPACE_OFFSITE_S3_SECRET_KEY_FILE": "/run/secrets/s3_secret",
		"SUBSPACE_OFFSITE_S3_PREFIX":          "prod",
		"SUBSPACE_OFFSITE_KEEP_LAST":          "3",
	}
	with := func(kv ...string) func(string) string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			if kv[i+1] == "" {
				delete(m, kv[i])
			} else {
				m[kv[i]] = kv[i+1]
			}
		}
		return func(k string) string { return m[k] }
	}
	cfg, err := offsite.ConfigFromEnv(with())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Retention != (offsite.Policy{Last: 3, Daily: 7, Weekly: 4}) || cfg.S3.SecretKey.File != "/run/secrets/s3_secret" || cfg.Interval != offsite.DefaultInterval {
		t.Fatalf("%+v", cfg)
	}
	if cfg, err := offsite.ConfigFromEnv(func(string) string { return "" }); err != nil || cfg.Enabled() {
		t.Fatalf("unset configuration must be off: %+v %v", cfg, err)
	}
	fsKey := "SUBSPACE_OFFSITE_KIND"
	for name, tc := range map[string]struct {
		env  func(string) string
		want string
	}{
		"unknown kind":       {with(fsKey, "ftp"), "SUBSPACE_OFFSITE_KIND"},
		"no key file":        {with("SUBSPACE_OFFSITE_KEY_FILE", ""), "SUBSPACE_OFFSITE_KEY_FILE"},
		"secret as value":    {with("SUBSPACE_OFFSITE_S3_ACCESS_KEY_ENV", "AKIA0123456789/abc+def"), "must name an environment variable"},
		"both sources":       {with("SUBSPACE_OFFSITE_S3_ACCESS_KEY_FILE", "/run/secrets/a"), "set only one"},
		"no secret":          {with("SUBSPACE_OFFSITE_S3_SECRET_KEY_FILE", ""), "secret key"},
		"relative cred file": {with("SUBSPACE_OFFSITE_S3_SECRET_KEY_FILE", "secret"), "absolute path"},
		"http endpoint":      {with("SUBSPACE_OFFSITE_S3_ENDPOINT", "http://s3.example.com"), "scheme must be https"},
		"endpoint path":      {with("SUBSPACE_OFFSITE_S3_ENDPOINT", "https://s3.example.com/bucket"), "path or query"},
		"bad bucket":         {with("SUBSPACE_OFFSITE_S3_BUCKET", "Bad_Bucket"), "bucket"},
		"no region":          {with("SUBSPACE_OFFSITE_S3_REGION", ""), "REGION"},
		"prefix escape":      {with("SUBSPACE_OFFSITE_S3_PREFIX", "../x"), "PREFIX"},
		"tiny part":          {with("SUBSPACE_OFFSITE_S3_PART_SIZE", "1024"), "PART_SIZE"},
		"keep nothing":       {with("SUBSPACE_OFFSITE_KEEP_LAST", "0", "SUBSPACE_OFFSITE_KEEP_DAILY", "0", "SUBSPACE_OFFSITE_KEEP_WEEKLY", "0"), "must not all be zero"},
		"negative keep":      {with("SUBSPACE_OFFSITE_KEEP_DAILY", "-1"), "KEEP_DAILY"},
		"bad bool":           {with("SUBSPACE_OFFSITE_S3_ALLOW_PRIVATE", "maybe"), "ALLOW_PRIVATE"},
		"bad interval":       {with("SUBSPACE_OFFSITE_INTERVAL", "soon"), "INTERVAL"},
		"bad key id":         {with("SUBSPACE_OFFSITE_KEY_ID", "has space"), "KEY_ID"},
		"fs relative path":   {with(fsKey, "fs", "SUBSPACE_OFFSITE_PATH", "mnt/backup"), "absolute path"},
	} {
		_, err := offsite.ConfigFromEnv(tc.env)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want error containing %q", name, err, tc.want)
		}
	}
}

func TestCredentialsOnlyFromReferencedSources(t *testing.T) {
	srv := s3test.New(t, "offsite", "eu-west-1", creds)
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretFile, []byte(creds.SecretKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OFFSITE_AK", creds.AccessKey)
	cfg := offsite.Config{
		Kind: offsite.KindS3, KeyFile: keyFile(t, 7), Retention: offsite.DefaultPolicy, Interval: time.Minute,
		S3: offsite.S3{
			Endpoint: srv.Endpoint(), Region: "eu-west-1", Bucket: "offsite", AllowPrivate: true,
			AccessKey: offsite.Source{Env: "OFFSITE_AK"}, SecretKey: offsite.Source{File: secretFile},
		},
	}
	remote, err := offsite.Open(cfg, offsite.Deps{Getenv: os.Getenv, HTTP: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.List(context.Background()); err != nil {
		t.Fatalf("credentials from a variable name and a file: %v", err)
	}
	t.Setenv("OFFSITE_AK", "")
	if _, err := offsite.Open(cfg, offsite.Deps{Getenv: os.Getenv, HTTP: srv.Client()}); err == nil {
		t.Fatal("empty credential variable accepted")
	}
}
