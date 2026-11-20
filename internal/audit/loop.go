package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

type key struct{ agent, tool, args string }

type guard struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[key][]time.Time
	sweep  time.Time
}

// NewLoopGuard returns a guard that trips on the (max+1)th identical call within window.
func NewLoopGuard(max int, window time.Duration) LoopGuard {
	return &guard{max: max, window: window, hits: map[key][]time.Time{}}
}

func (g *guard) Hit(agentID, tool, argsHash string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Sub(g.sweep) > g.window {
		g.sweep = now
		for k, ts := range g.hits {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) > g.window {
				delete(g.hits, k)
			}
		}
	}
	k := key{agentID, tool, argsHash}
	ts := g.hits[k]
	cut := 0
	for cut < len(ts) && now.Sub(ts[cut]) > g.window {
		cut++
	}
	ts = append(ts[cut:], now)
	g.hits[k] = ts
	return len(ts) > g.max
}

// ArgsHash canonicalises JSON (sorted keys) and hashes it.
func ArgsHash(raw json.RawMessage) string {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		h := sha256.Sum256(raw)
		return hex.EncodeToString(h[:8])
	}
	b, _ := json.Marshal(canon(v)) // map keys are sorted by encoding/json
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
