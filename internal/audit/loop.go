package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

const maxKeysPerAgent = 256

type callKey struct{ tool, args string }

type guard struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	agents map[string]map[callKey][]time.Time
	sweep  time.Time
}

func NewLoopGuard(max int, window time.Duration) LoopGuard {
	return &guard{max: max, window: window, agents: map[string]map[callKey][]time.Time{}}
}

func (g *guard) Hit(agentID, tool, argsHash string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Sub(g.sweep) > g.window {
		g.sweep = now
		g.sweepLocked(now)
	}
	keys := g.agents[agentID]
	if keys == nil {
		keys = map[callKey][]time.Time{}
		g.agents[agentID] = keys
	}
	k := callKey{tool, argsHash}
	ts := keys[k]
	if _, known := keys[k]; !known && len(keys) >= maxKeysPerAgent {
		g.evictLocked(keys, now)
	}
	cut := 0
	for cut < len(ts) && now.Sub(ts[cut]) > g.window {
		cut++
	}
	ts = append(ts[cut:], now)

	if len(ts) > g.max+1 {
		ts = append([]time.Time(nil), ts[len(ts)-(g.max+1):]...)
	}
	keys[k] = ts
	return len(ts) > g.max
}

func (g *guard) sweepLocked(now time.Time) {
	for a, keys := range g.agents {
		for k, ts := range keys {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) > g.window {
				delete(keys, k)
			}
		}
		if len(keys) == 0 {
			delete(g.agents, a)
		}
	}
}

func (g *guard) evictLocked(keys map[callKey][]time.Time, now time.Time) {
	var oldest callKey
	var oldestAt time.Time
	first := true
	for k, ts := range keys {
		last := ts[len(ts)-1]
		if now.Sub(last) > g.window {
			delete(keys, k)
			continue
		}
		if first || last.Before(oldestAt) {
			oldest, oldestAt, first = k, last, false
		}
	}
	if len(keys) >= maxKeysPerAgent && !first {
		delete(keys, oldest)
	}
}

func ArgsHash(raw json.RawMessage) string {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if len(raw) == 0 || dec.Decode(&v) != nil || dec.More() {
		h := sha256.Sum256(raw)
		return hex.EncodeToString(h[:8])
	}
	b, _ := json.Marshal(canon(v))
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

func canon(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(x))
		for _, k := range keys {
			out[k] = canon(x[k])
		}
		return out
	case []any:
		for i := range x {
			x[i] = canon(x[i])
		}
	}
	return v
}
