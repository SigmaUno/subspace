package server_test

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/spawner"
)

func dockerFrame(stream byte, s string) []byte {
	b := make([]byte, 8, 8+len(s))
	b[0] = stream
	binary.BigEndian.PutUint32(b[4:], uint32(len(s)))
	return append(b, s...)
}

func TestDockerLogCaptureIntoAgentOutput(t *testing.T) {
	var removed atomic.Int32
	var started atomic.Bool
	logs := append(dockerFrame(1, "build ok\n"), dockerFrame(2, "GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123\n")...)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /containers/json", func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		if started.Load() {
			out = append(out, map[string]any{"Id": "c1", "State": "running", "Labels": map[string]string{"subspace.managed": "true", "subspace.agent": "w1"}})
		}
		if r.URL.Query().Get("all") == "1" {
			out = append(out, map[string]any{"Id": "c2", "State": "exited", "Labels": map[string]string{"subspace.managed": "true", "subspace.agent": "gone"}})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("DELETE /containers/c2", func(w http.ResponseWriter, _ *http.Request) {
		removed.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /containers/c1/logs", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(logs)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("GET /networks", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	mux.HandleFunc("GET /volumes", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"Volumes":[]}`)) })

	dir, err := os.MkdirTemp("", "dk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	engine := httptest.NewUnstartedServer(mux)
	engine.Listener = l
	engine.Start()
	t.Cleanup(engine.Close)

	dk, err := spawner.NewDocker(spawner.DockerConfig{Socket: sock, SecretsDir: t.TempDir(), LogPoll: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	e := servertest.Start(t, lifeConfig(dk, servertest.DB(t)))
	e.Register("w1", "worker", "claude-code")
	started.Store(true)

	deadline := time.Now().Add(5 * time.Second)
	for {
		code, out := e.REST("GET", "/api/v1/agents/w1/output", nil)
		if code != 200 {
			t.Fatalf("output: %d %v", code, out)
		}
		var texts []string
		for _, l := range out["lines"].([]any) {
			texts = append(texts, l.(map[string]any)["stream"].(string)+":"+l.(map[string]any)["text"].(string))
		}
		all := strings.Join(texts, "\n")
		if strings.Contains(all, "stdout:build ok") && strings.Contains(all, "stderr:GITHUB_TOKEN=") {
			if strings.Contains(all, "ghp_abcdefghijklmnopqrstuvwxyz0123") {
				t.Fatalf("secret not redacted: %q", all)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("docker logs not captured: %q", all)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if removed.Load() != 1 {
		t.Fatalf("orphaned container removed %d times", removed.Load())
	}
}
