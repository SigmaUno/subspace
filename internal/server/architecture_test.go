package server_test

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/testdb"
)

func TestArchitectureSurvivesRestartAndPushesUpdates(t *testing.T) {
	cfg := server.Config{DBPath: testdb.Path(t, "arch.db"), PublicURL: "http://sub.test", FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute}
	e := servertest.Start(t, cfg)
	lead := e.Register("rex", "leader", "other")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{"o/r"}})
	bid := br["id"].(string)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.TS.URL+"/api/v1/events?topics=brief:"+bid, nil)
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	events := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if typ, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				events <- typ
			}
		}
	}()
	waitEvent := func(want string) {
		t.Helper()
		for {
			select {
			case typ := <-events:
				if typ == want {
					return
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("no %s event", want)
			}
		}
	}
	waitEvent("hello")

	e.Must(lead, "set_components", map[string]any{"brief_id": bid, "components": []any{
		map[string]any{"name": "store", "paths": []string{"internal/store"}},
		map[string]any{"name": "api", "depends_on": []string{"store"}},
	}})
	waitEvent("architecture.updated")
	e.Must(lead, "create_task", map[string]any{"brief_id": bid, "title": "api", "spec": "s", "repo": "o/r", "component_id": "C-2"})

	cancel()
	e.TS.CloseClientConnections()
	e.TS.Close()
	_ = e.Srv.Close()
	e2 := servertest.Start(t, cfg)
	code, arch := e2.REST("GET", "/api/v1/briefs/"+bid+"/architecture", nil)
	if code != 200 {
		t.Fatalf("architecture: %d %v", code, arch)
	}
	nodes := arch["nodes"].([]any)
	if len(nodes) != 2 || len(arch["edges"].([]any)) != 1 {
		t.Fatalf("architecture lost across restart: %v", arch)
	}
	api := nodes[1].(map[string]any)
	if api["name"] != "api" || api["status"].(map[string]any)["total"] != float64(1) {
		t.Fatalf("task link lost: %v", api)
	}
}
