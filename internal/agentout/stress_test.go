package agentout_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/testmode"
)

var stressAgents = testmode.Size(100, 200)

const (
	stressFollowed = 20
	stressChunks   = 20
	stressMemory   = 256 << 10
	stressLines    = stressChunks*2 + stressChunks/3
)

var sgr = regexp.MustCompile(`\x1b\[[0-9;]*m`)

type collector struct {
	mu     sync.Mutex
	chunks map[string][]map[string]any
	lagged int
	hello  chan struct{}
}

func follow(t *testing.T, e *servertest.Env, topics string) *collector {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", e.TS.URL+"/api/v1/events?topics="+topics, nil)
	req.Header.Set("Authorization", "Bearer "+servertest.OwnerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != 200 {
		t.Fatalf("follow %s: %d", topics, resp.StatusCode)
	}
	c := &collector{chunks: map[string][]map[string]any{}, hello: make(chan struct{})}
	go func() {
		br := bufio.NewReader(resp.Body)
		var typ string
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				return
			}
			l = strings.TrimRight(l, "\n")
			if v, ok := strings.CutPrefix(l, "event: "); ok {
				typ = v
				continue
			}
			data, ok := strings.CutPrefix(l, "data: ")
			if !ok {
				continue
			}
			switch typ {
			case "hello":
				close(c.hello)
			case "lagged":
				c.mu.Lock()
				c.lagged++
				c.mu.Unlock()
			case "agent.output":
				var env struct{ Data map[string]any }
				if json.Unmarshal([]byte(data), &env) == nil {
					id, _ := env.Data["agent_id"].(string)
					c.mu.Lock()
					c.chunks[id] = append(c.chunks[id], env.Data)
					c.mu.Unlock()
				}
			}
		}
	}()
	select {
	case <-c.hello:
	case <-time.After(5 * time.Second):
		t.Fatalf("follow %s: no hello", topics)
	}
	return c
}

func (c *collector) seqs(agent string) (last int64, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range c.chunks[agent] {
		for _, l := range ch["lines"].([]any) {
			last = int64(l.(map[string]any)["seq"].(float64))
			n++
		}
	}
	return last, n
}

func agentTokens(t *testing.T, e *servertest.Env) (ids, toks []string) {
	t.Helper()
	code, out := e.REST("POST", "/api/v1/agents/bulk", map[string]any{"prefix": "ag", "count": stressAgents, "role": "worker", "kind": "claude-code", "model": "m"})
	if code != 201 {
		t.Fatalf("bulk: %d %v", code, out)
	}
	for _, r := range out["agents"].([]any) {
		m := r.(map[string]any)
		ids = append(ids, m["agent"].(map[string]any)["id"].(string))
		toks = append(toks, m["connect"].(map[string]any)["token"].(string))
	}
	return ids, toks
}

func rpcCall(c *http.Client, url, tok string, args map[string]any) (map[string]any, error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "log_output", "arguments": args}})
	req, _ := http.NewRequest("POST", url+"/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Result struct {
			IsError bool           `json:"isError"`
			Struct  map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Result.IsError || out.Result.Struct == nil {
		return nil, fmt.Errorf("log_output failed: status %d %v", resp.StatusCode, out.Result.Struct)
	}
	return out.Result.Struct, nil
}

func plantedSecrets(rng *rand.Rand) []string {
	const set = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	alnum := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = set[rng.IntN(len(set))]
		}
		return string(b)
	}
	return []string{"ghp_" + alnum(36), "sk-ant-" + alnum(40), "AKIA" + strings.ToUpper(alnum(16)), "password=" + alnum(20)}
}

func TestStressConcurrentAgentsFollowedOverSSE(t *testing.T) {
	t.Setenv("SUBSPACE_OUTPUT_AGENT_BYTES", "16384")
	t.Setenv("SUBSPACE_OUTPUT_MEMORY", fmt.Sprint(stressMemory))
	e := servertest.New(t)
	ids, toks := agentTokens(t, e)

	followed := ids[:stressFollowed]
	var subs []*collector
	for i := 0; i < stressFollowed; i += 5 {
		topics := make([]string, 0, 5)
		for _, id := range followed[i : i+5] {
			topics = append(topics, "output:"+id)
		}
		subs = append(subs, follow(t, e, strings.Join(topics, ",")))
	}

	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64}, Timeout: 30 * time.Second}
	var (
		mu       sync.Mutex
		planted  []string
		sent     int
		failures []error
		wg       sync.WaitGroup
	)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(i), 42))
			var secrets []string
			var chars int
			for c := 1; c <= stressChunks; c++ {
				s := plantedSecrets(rng)
				secrets = append(secrets, s...)
				text := fmt.Sprintf("\x1b]0;%s\x07\x1b[32mstep %d\x1b[0m waiting for lock...\n\x1b[1;31m%s\x1b[0m key=%s\x1b[2K\n",
					ids[i], c, s[0], s[1])
				if c%3 == 0 {
					text += "export " + s[3] + " " + s[2] + "\n"
				}
				res, err := rpcCall(client, e.TS.URL, toks[i], map[string]any{"stream": "stdout", "text": text, "seq": c})
				if err == nil && (res["duplicate"] == true || res["lines"] == nil || res["lines"].(float64) < 2) {
					err = fmt.Errorf("%s chunk %d: unexpected result %v", ids[i], c, res)
				}
				if err == nil && c%10 == 0 {
					res, err = rpcCall(client, e.TS.URL, toks[i], map[string]any{"stream": "stdout", "text": text, "seq": c})
					if err == nil && res["duplicate"] != true {
						err = fmt.Errorf("%s chunk %d retry not ignored: %v", ids[i], c, res)
					}
				}
				if err != nil {
					mu.Lock()
					failures = append(failures, err)
					mu.Unlock()
					return
				}
				chars += len(text)
			}
			mu.Lock()
			planted = append(planted, secrets...)
			sent += chars
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(failures) > 0 {
		t.Fatalf("%d agents failed, first: %v", len(failures), failures[0])
	}

	deadline := time.Now().Add(10 * time.Second)
	for k, id := range followed {
		for {
			last, n := subs[k/5].seqs(id)
			if last == stressLines && n == stressLines {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: followed %d lines, last seq %d, want %d", id, n, last, stressLines)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	for k, id := range followed {
		sub := subs[k/5]
		sub.mu.Lock()
		next := int64(1)
		for _, ch := range sub.chunks[id] {
			for _, l := range ch["lines"].([]any) {
				m := l.(map[string]any)
				if got := int64(m["seq"].(float64)); got != next {
					sub.mu.Unlock()
					t.Fatalf("%s: seq %d, want %d: dropped or reordered chunk", id, got, next)
				}
				next++
			}
		}
		lagged := sub.lagged
		sub.mu.Unlock()
		if lagged != 0 {
			t.Fatalf("%s: subscriber reported %d lagged events under the rate limit", id, lagged)
		}
		if next-1 != stressLines {
			t.Fatalf("%s: followed %d lines, want %d", id, next-1, stressLines)
		}
	}

	var every []string
	for i := range subs {
		subs[i].mu.Lock()
		for _, chs := range subs[i].chunks {
			for _, ch := range chs {
				for _, l := range ch["lines"].([]any) {
					every = append(every, l.(map[string]any)["text"].(string))
				}
			}
		}
		subs[i].mu.Unlock()
	}

	retained := 0
	for _, id := range ids {
		_, out := e.REST("GET", "/api/v1/agents/"+id+"/output?limit=2000", nil)
		for _, l := range lines(t, out) {
			txt := l["text"].(string)
			every = append(every, txt)
			retained += len(txt)
		}
	}
	if retained == 0 || retained > stressMemory {
		t.Fatalf("retained %d text bytes, global cap %d", retained, stressMemory)
	}
	if retained >= sent/2 {
		t.Fatalf("retained %d of %d bytes sent: global cap did not evict", retained, sent)
	}

	for _, txt := range every {
		if strings.ContainsAny(sgr.ReplaceAllString(txt, ""), "\x1b\x07\x00") || strings.Contains(txt, "ag-") {
			t.Fatalf("terminal control sequence leaked: %q", txt)
		}
		for _, s := range planted {
			if tail := s[len(s)-12:]; strings.Contains(txt, tail) {
				t.Fatalf("secret %q leaked in %q", tail, txt)
			}
		}
	}

	if code, out := e.REST("GET", "/api/v1/agents?status=paused", nil); code != 200 || len(out["agents"].([]any)) != 0 {
		t.Fatalf("agents paused by repeated output: %d %v", code, out)
	}
}
