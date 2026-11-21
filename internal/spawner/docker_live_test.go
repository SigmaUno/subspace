package spawner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDockerLive(t *testing.T) {
	if os.Getenv("SUBSPACE_TEST_DOCKER") != "1" {
		t.Skip("set SUBSPACE_TEST_DOCKER=1 to run against a real docker engine")
	}
	image := os.Getenv("SUBSPACE_TEST_DOCKER_IMAGE")
	if image == "" {
		image = "alpine:3.24"
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	prefix := "sstest-" + hex.EncodeToString(b[:])
	user := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	if os.Getuid() == 0 {
		user = DefaultDockerUser
	}
	script := `id -u; test -r ` + AgentTokenPath + ` && echo token-ok; ` +
		`[ -z "$SUBSPACE_TOKEN" ] && echo no-env-token; ` +
		`touch /rootfs 2>/dev/null || echo rootfs-ro; ` +
		`touch /work/x && echo work-rw; ` +
		`wget -q -T 2 -O /dev/null http://example.com 2>/dev/null || echo isolated; sleep 60`
	d, err := NewDocker(DockerConfig{
		Socket:      os.Getenv("SUBSPACE_TEST_DOCKER_SOCKET"),
		Images:      map[string][]string{"claude-code": {image}},
		Limits:      Limits{CPUs: 0.5, MemoryBytes: 128 << 20, Pids: 64},
		User:        user,
		Cmd:         []string{"sh", "-c", script},
		LabelPrefix: prefix,
		Network:     prefix + "-net",
		SecretsDir:  t.TempDir(),
		LogPoll:     200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	t.Cleanup(func() { purgeLive(t, d) })

	req := testRequest()
	req.AgentID = "live-1"
	if err := d.Spawn(ctx, req); err != nil {
		t.Fatal(err)
	}
	if st, err := d.Status(ctx, req.AgentID); err != nil || st != StatusRunning {
		t.Fatalf("status %v %v", st, err)
	}

	var info struct {
		HostConfig hostConfig `json:"HostConfig"`
	}
	if err := d.do(ctx, "", http.MethodGet, "/containers/"+d.agentName(req.AgentID)+"/json", nil, &info); err != nil {
		t.Fatal(err)
	}
	hc := info.HostConfig
	if !hc.ReadonlyRootfs || !slices.Equal(hc.CapDrop, []string{"ALL"}) || hc.PidsLimit != 64 || hc.Memory != 128<<20 || hc.NetworkMode != prefix+"-net" {
		t.Fatalf("hostconfig %+v", hc)
	}

	logCtx, stopLogs := context.WithCancel(ctx)
	var rec sinkRec
	done := make(chan struct{})
	go func() {
		d.WatchLogs(logCtx, rec.sink)
		close(done)
	}()
	want := []string{"token-ok", "no-env-token", "rootfs-ro", "work-rw", "isolated", strings.Split(user, ":")[0]}
	deadline := time.Now().Add(30 * time.Second)
	for {
		rec.mu.Lock()
		all := strings.Join(rec.got, "\n")
		rec.mu.Unlock()
		missing := ""
		for _, w := range want {
			if !strings.Contains(all, w) {
				missing = w
			}
		}
		if missing == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("log line %q not seen in %q", missing, all)
		}
		time.Sleep(200 * time.Millisecond)
	}
	stopLogs()
	<-done

	if err := d.Stop(ctx, req.AgentID); err != nil {
		t.Fatal(err)
	}
	if st, err := d.Status(ctx, req.AgentID); err != nil || st != StatusExited {
		t.Fatalf("status after stop %v %v", st, err)
	}
	if _, err := d.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
}

func purgeLive(t *testing.T, d *Docker) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := d.list(ctx, d.label("managed")+"=true", true)
	if err != nil {
		t.Errorf("purge list: %v", err)
	}
	for _, c := range list {
		if err := d.do(ctx, "", http.MethodDelete, "/containers/"+c.ID+"?force=true", nil, nil); err != nil && !isStatus(err, http.StatusNotFound) && !isStatus(err, http.StatusConflict) {
			t.Errorf("purge container %s: %v", c.ID, err)
		}
	}
	filters, _ := json.Marshal(map[string][]string{"label": {d.label("managed") + "=true"}})
	var nets []struct {
		ID string `json:"Id"`
	}
	if err := d.do(ctx, "", http.MethodGet, "/networks?"+url.Values{"filters": {string(filters)}}.Encode(), nil, &nets); err != nil {
		t.Errorf("purge networks: %v", err)
	}
	for _, n := range nets {
		for range 20 {
			if err := d.do(ctx, "", http.MethodDelete, "/networks/"+n.ID, nil, nil); err == nil || isStatus(err, http.StatusNotFound) {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
}
