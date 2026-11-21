package servertest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/server"
)

const (
	NodeHeader    = "X-Subspace-Test-Node"
	leaderTimeout = 5 * time.Second
	drainTimeout  = 3 * time.Second
)

type Cluster struct {
	T     *testing.T
	Nodes []*Env
	Proxy *httptest.Server

	mu      sync.Mutex
	down    map[int]bool
	next    int
	flight  []int
	streams []int
	proxies []*httputil.ReverseProxy
}

func RequirePostgres(t *testing.T) {
	t.Helper()
	if !strings.HasPrefix(os.Getenv("SUBSPACE_TEST_PG_DSN"), "postgres") {
		t.Skip("SUBSPACE_TEST_PG_DSN not set")
	}
}

func StartCluster(t *testing.T, n int, cfg server.Config) *Cluster {
	t.Helper()
	RequirePostgres(t)
	cfg.DBPath = DB(t)
	if cfg.LeaderInterval == 0 {
		cfg.LeaderInterval = 100 * time.Millisecond
	}
	if cfg.LeaderTTL == 0 {
		cfg.LeaderTTL = 500 * time.Millisecond
	}
	c := &Cluster{T: t, down: map[int]bool{}, flight: make([]int, n), streams: make([]int, n)}
	for i := range n {
		cfg.InstanceID = "node-" + strconv.Itoa(i)
		env := Start(t, cfg)
		u, err := url.Parse(env.TS.URL)
		if err != nil {
			t.Fatal(err)
		}
		p := httputil.NewSingleHostReverseProxy(u)
		p.FlushInterval = -1
		p.ErrorLog = log.New(io.Discard, "", 0)
		c.Nodes = append(c.Nodes, env)
		c.proxies = append(c.proxies, p)
	}
	c.Proxy = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.Stop)
	return c
}

func (c *Cluster) Stop() {
	c.Proxy.CloseClientConnections()
	c.Proxy.Close()
	for _, n := range c.Nodes {
		n.Stop()
	}
}

func (c *Cluster) pick(r *http.Request) int {
	if v, err := strconv.Atoi(r.Header.Get(NodeHeader)); err == nil && v >= 0 && v < len(c.Nodes) && !c.down[v] {
		return v
	}
	for range c.Nodes {
		i := c.next % len(c.Nodes)
		c.next++
		if !c.down[i] {
			return i
		}
	}
	return -1
}

func (c *Cluster) serve(w http.ResponseWriter, r *http.Request) {
	stream := r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	c.mu.Lock()
	i := c.pick(r)
	switch {
	case i < 0:
	case stream:
		c.streams[i]++
	default:
		c.flight[i]++
	}
	c.mu.Unlock()
	if i < 0 {
		http.Error(w, "no instance is up", http.StatusServiceUnavailable)
		return
	}
	if !stream {
		defer func() {
			c.mu.Lock()
			c.flight[i]--
			c.mu.Unlock()
		}()
	}
	c.proxies[i].ServeHTTP(w, r)
}

func (c *Cluster) Streams(i int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streams[i]
}

func (c *Cluster) Alive() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []int
	for i := range c.Nodes {
		if !c.down[i] {
			out = append(out, i)
		}
	}
	return out
}

type HAStatus struct {
	Leader         bool    `json:"leader"`
	Instance       string  `json:"instance"`
	LeaderInstance *string `json:"leader_instance"`
	Backend        string  `json:"backend"`
}

func (c *Cluster) Status(ctx context.Context, i int) (HAStatus, error) {
	var st HAStatus
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Nodes[i].TS.URL+"/api/v1/ha", nil)
	if err != nil {
		return st, err
	}
	req.Header.Set("Authorization", "Bearer "+OwnerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return st, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return st, fmt.Errorf("ha status of node %d: %d", i, resp.StatusCode)
	}
	return st, json.NewDecoder(resp.Body).Decode(&st)
}

func (c *Cluster) Leader(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, leaderTimeout)
	defer cancel()
	for {
		var leaders []int
		agreed := true
		for _, i := range c.Alive() {
			st, err := c.Status(ctx, i)
			if err != nil {
				agreed = false
				continue
			}
			if st.Leader {
				leaders = append(leaders, i)
			}
			if st.LeaderInstance == nil {
				agreed = false
			}
		}
		if len(leaders) > 1 {
			return -1, fmt.Errorf("split brain: %v", leaders)
		}
		if len(leaders) == 1 && agreed {
			return leaders[0], nil
		}
		select {
		case <-ctx.Done():
			return -1, fmt.Errorf("no leader elected within %s", leaderTimeout)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (c *Cluster) Kill(i int) {
	c.mu.Lock()
	c.down[i] = true
	c.mu.Unlock()
	for end := time.Now().Add(drainTimeout); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		c.mu.Lock()
		busy := c.flight[i] > 0
		c.mu.Unlock()
		if !busy {
			break
		}
	}
	c.Nodes[i].Stop()
}
