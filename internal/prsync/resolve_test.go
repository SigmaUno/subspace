package prsync_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/prsync"
	"github.com/SigmaUno/subspace/internal/repos"
)

type fakeSource []repos.Repo

func (f fakeSource) List(context.Context) ([]repos.Repo, error) { return f, nil }

func TestResolvePrefersRegistryAndFallsBackToFileWithOneWarning(t *testing.T) {
	t.Setenv("RESOLVE_GH_TOKEN", "gh-registry-token")
	file := filepath.Join(t.TempDir(), "prsync.json")
	if err := os.WriteFile(file, []byte(`{"hosts":{"legacy":{"kind":"github","token":"x"}},"repos":{"o/legacy":"legacy"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(prsync.ConfigEnv, file)
	t.Setenv(prsync.IntervalEnv, "5s")
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	registry := fakeSource{
		{Name: "Acme/Web", Host: "github", BaseURL: "https://github.com", TokenEnv: "RESOLVE_GH_TOKEN", Enabled: true},
		{Name: "acme/api", Host: "github", BaseURL: "https://github.com", TokenEnv: "RESOLVE_GH_TOKEN", Enabled: true},
		{Name: "acme/docs", Host: "github", BaseURL: "https://github.com", Enabled: true},
		{Name: "forge/svc", Host: "forgejo", BaseURL: "https://forge.example.com", TokenEnv: "RESOLVE_FJ_TOKEN", Enabled: true},
		{Name: "acme/off", Host: "github", BaseURL: "https://github.com", Enabled: false},
	}
	cfg, ok, err := prsync.Resolve(context.Background(), registry, log)
	if err != nil || !ok {
		t.Fatalf("registry: ok=%v err=%v", ok, err)
	}
	if cfg.Interval != 5*time.Second || len(cfg.Hosts) != 3 || len(cfg.Repos) != 4 {
		t.Fatalf("registry config = %+v", cfg)
	}
	web, docs := cfg.Hosts[cfg.Repos["acme/web"]], cfg.Hosts[cfg.Repos["acme/docs"]]
	if cfg.Repos["acme/web"] != cfg.Repos["acme/api"] || cfg.Repos["acme/web"] == cfg.Repos["acme/docs"] || web.Token != "gh-registry-token" || docs.Token != "" {
		t.Fatalf("hosts must be shared per base URL and token variable: %+v", cfg)
	}
	if fj := cfg.Hosts[cfg.Repos["forge/svc"]]; fj.Kind != "forgejo" || fj.URL != "https://forge.example.com" || fj.APIURL != "https://forge.example.com/api/v1" || web.APIURL != "https://api.github.com" {
		t.Fatalf("api urls: %+v %+v", fj, web)
	}
	if _, legacy := cfg.Repos["o/legacy"]; legacy || logs.Len() != 0 {
		t.Fatalf("the file must be ignored while the registry has repositories: %v %q", cfg.Repos, logs.String())
	}

	for range 3 {
		cfg, ok, err = prsync.Resolve(context.Background(), fakeSource{{Name: "acme/off", Host: "github", Enabled: false}}, log)
		if err != nil || !ok || cfg.Repos["o/legacy"] != "legacy" || cfg.Interval != 5*time.Second {
			t.Fatalf("fallback: ok=%v err=%v cfg=%+v", ok, err, cfg)
		}
	}
	if n := strings.Count(logs.String(), "deprecated"); n != 1 || !strings.Contains(logs.String(), prsync.ConfigEnv) {
		t.Fatalf("deprecation warnings = %d: %q", n, logs.String())
	}

	if _, ok, err = prsync.Resolve(context.Background(), nil, log); err != nil || !ok {
		t.Fatalf("a store without a registry falls back to the file: ok=%v err=%v", ok, err)
	}
	t.Setenv(prsync.ConfigEnv, "")
	if _, ok, err = prsync.Resolve(context.Background(), fakeSource{}, log); err != nil || ok {
		t.Fatalf("nothing configured: ok=%v err=%v", ok, err)
	}
	t.Setenv(prsync.ConfigEnv, filepath.Join(t.TempDir(), "missing.json"))
	if _, _, err = prsync.Resolve(context.Background(), fakeSource{}, log); err == nil {
		t.Fatal("an unreadable fallback file must be an error")
	}
}
