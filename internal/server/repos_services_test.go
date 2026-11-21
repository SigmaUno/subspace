package server_test

import (
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/repos"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func seedRegistry(t *testing.T, rs ...repos.Repo) string {
	t.Helper()
	path := servertest.DB(t)
	if strings.HasPrefix(path, "postgres") {
		t.Skip("seeding needs the sqlite file")
	}
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if _, err := (&repos.Store{St: st}).Create(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestForgeStatusReadsTheRepositoryRegistry(t *testing.T) {
	const sha = "0a1b2c3d4e5f60718293a4b5c6d7e8f901234567"
	f := newForgeFake(t, "/api/v1", sha, 0)
	t.Setenv("REGISTRY_FORGE_TOKEN", "registry-token")
	t.Setenv("SUBSPACE_PRSYNC_CONFIG", "")
	t.Setenv("SUBSPACE_PRSYNC_INTERVAL", "1h")
	path := seedRegistry(t, repos.Repo{Name: "o/r", Host: repos.HostForgejo, BaseURL: f.srv.URL, TokenEnv: "REGISTRY_FORGE_TOKEN", Enabled: true})
	e := servertest.Start(t, server.Config{DBPath: path, PublicURL: "https://sub.example",
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})

	fl := startFlow(t, e, f.srv.URL)
	fl.submit()
	if got := f.await(t, 1); got[0].State != "pending" || got[0].Context != "subspace/review" {
		t.Fatalf("submitted: %+v", got[0])
	}
	e.Must(fl.rev, "review", map[string]any{"task_id": fl.taskID, "verdict": "approve", "comments": "lgtm"})
	if got := f.await(t, 2); got[1].State != "success" {
		t.Fatalf("approved: %+v", got)
	}
}
