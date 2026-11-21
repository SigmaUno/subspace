package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/notify"
)

type binaries struct{ leader, reviewer string }

func buildBinaries(t *testing.T) binaries {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	_, file, _, _ := runtime.Caller(0)
	dir := t.TempDir()
	cmd := exec.Command(goBin, "build", "-o", dir, "./cmd/subspace-leader", "./cmd/subspace-reviewer")
	cmd.Dir = filepath.Join(filepath.Dir(file), "..", "..")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build agents: %v\n%s", err, out)
	}
	return binaries{leader: filepath.Join(dir, "subspace-leader"), reviewer: filepath.Join(dir, "subspace-reviewer")}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type proc struct {
	cmd    *exec.Cmd
	stdout syncBuffer
	stderr syncBuffer
}

func startProc(ctx context.Context, bin, token string, args ...string) (*proc, error) {
	p := &proc{}
	p.cmd = exec.CommandContext(ctx, bin, args...)
	p.cmd.Cancel = func() error { return p.cmd.Process.Signal(os.Interrupt) }
	p.cmd.WaitDelay = 5 * time.Second
	p.cmd.Env = append(os.Environ(), "SUBSPACE_TOKEN="+token)
	p.cmd.Stdout, p.cmd.Stderr = &p.stdout, &p.stderr
	return p, p.cmd.Start()
}

func (p *proc) wait() error { return p.cmd.Wait() }

type sink struct {
	srv  *httptest.Server
	bus  *bus
	mu   sync.Mutex
	seen []notify.Notification
}

func newSink(b *bus) *sink {
	s := &sink{bus: b}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n notify.Notification
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.seen = append(s.seen, n)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		s.bus.fire()
	}))
	return s
}

func (s *sink) close() { s.srv.Close() }

func (s *sink) all() []notify.Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]notify.Notification(nil), s.seen...)
}

func (s *sink) has(id, kind string) bool {
	for _, n := range s.all() {
		if n.ID == id && n.Kind == kind {
			return true
		}
	}
	return false
}

func (s *sink) first(kind string) string {
	for _, n := range s.all() {
		if n.Kind == kind {
			return n.ID
		}
	}
	return ""
}
