package redteam2

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
)

type violation struct {
	inv    string
	detail string
}

func (v *violation) String() string { return v.inv + ": " + v.detail }

const (
	maxGoroutineGrowth = 400
	maxHeapGrowth      = 768 << 20
)

var markerRe = regexp.MustCompile(`zq[0-9a-f]{20}`)

func usage() (int, uint64) {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return runtime.NumGoroutine(), ms.HeapInuse
}

func isAgent(actor string) bool {
	switch actor {
	case actorOwner, actorViewer, actorAnon:
		return false
	}
	return true
}

func (e *env) roleOf(id string) string {
	for _, a := range agentSpecs {
		if a.id == id {
			return a.role
		}
	}
	return ""
}

func (e *env) checkCall(op Op, req []byte, code int, body []byte) *violation {
	if code >= 500 || code == 0 {
		return &violation{"5xx", fmt.Sprintf("%s -> %d %s", op, code, clip(body))}
	}
	if op.Kind == kindMCP || op.Kind == kindRPC {
		if v := mcpInternal(op, body); v != nil {
			return v
		}
	}
	if v := e.checkAuth(op, code, body); v != nil {
		return v
	}
	for _, n := range e.needles {
		if bytes.Contains(body, []byte(n)) && !bytes.Contains(req, []byte(n)) && !e.ownToken(op.Actor, n) {
			return &violation{"secret_in_response", fmt.Sprintf("%s returned %q%s", op, n, around(body, n))}
		}
	}
	for _, tok := range e.allTokens() {
		h := sha256.Sum256([]byte(tok))
		if bytes.Contains(body, []byte(hex.EncodeToString(h[:]))) {
			return &violation{"token_hash_in_response", op.String()}
		}
	}
	if bytes.Contains(body, []byte(`"token_hash"`)) {
		return &violation{"token_hash_in_response", op.String()}
	}
	if isAgent(op.Actor) && code < 300 {
		return e.checkPrivacy(op, body)
	}
	return nil
}

func (e *env) ownToken(actor, n string) bool {
	return e.tokens[actor] == n
}

func (e *env) allTokens() []string {
	out := []string{e.ownerTok}
	for _, t := range e.tokens {
		out = append(out, t)
	}
	return out
}

func mcpInternal(op Op, body []byte) *violation {
	var res struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
		Result struct {
			SC struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &res) != nil {
		return nil
	}
	if res.Error != nil && res.Error.Code == -32603 {
		return &violation{"5xx", fmt.Sprintf("%s -> rpc internal %s", op, clip(body))}
	}
	if res.Result.SC.Error.Code == "internal" {
		return &violation{"5xx", fmt.Sprintf("%s -> tool internal %s", op, clip(body))}
	}
	return nil
}

func (e *env) checkAuth(op Op, code int, body []byte) *violation {
	ok := code >= 200 && code < 300
	switch {
	case op.Kind == kindREST && strings.HasPrefix(op.Path, "/api/v1") && isAgent(op.Actor) && ok:
		return &violation{"agent_on_rest", fmt.Sprintf("%s -> %d", op, code)}
	case op.Kind == kindREST && strings.HasPrefix(op.Path, "/api/v1") && op.Actor == actorAnon && ok:
		return &violation{"anon_on_rest", fmt.Sprintf("%s -> %d", op, code)}
	case op.Kind == kindREST && op.Actor == actorViewer && op.Method != "GET" && op.Method != "HEAD" && ok:
		return &violation{"viewer_write", fmt.Sprintf("%s -> %d", op, code)}
	case op.Kind != kindREST && !isAgent(op.Actor) && ok && bytes.Contains(body, []byte(`"result"`)):
		return &violation{"non_agent_on_mcp", fmt.Sprintf("%s -> %d %s", op, code, clip(body))}
	}
	return nil
}

func (e *env) checkPrivacy(op Op, body []byte) *violation {
	seen := map[string]bool{}
	for _, m := range markerRe.FindAll(body, -1) {
		mk, ok := e.markers[string(m)]
		if !ok || seen[string(m)] {
			continue
		}
		seen[string(m)] = true
		if !e.visible(op.Actor, mk) {
			return &violation{"cross_role_" + mk.kind, fmt.Sprintf("%s saw %s %s", op, mk.kind, mk.id)}
		}
	}
	var doc any
	if json.Unmarshal(body, &doc) != nil {
		return nil
	}
	if who := foreignQuota(doc, op.Actor); who != "" {
		return &violation{"cross_role_quota", fmt.Sprintf("%s saw quota of %s", op, who)}
	}
	return nil
}

func foreignQuota(v any, self string) string {
	switch x := v.(type) {
	case map[string]any:
		if _, q := x["quota"]; q || x["quota_usage"] != nil {
			id, _ := x["id"].(string)
			if id == "" {
				id, _ = x["agent_id"].(string)
			}
			if id != self {
				return cmp.Or(id, "an unnamed agent")
			}
		}
		for _, c := range x {
			if s, ok := c.(string); ok && strings.HasPrefix(s, "{") {
				var inner any
				if json.Unmarshal([]byte(s), &inner) == nil {
					if w := foreignQuota(inner, self); w != "" {
						return w
					}
				}
			}
			if w := foreignQuota(c, self); w != "" {
				return w
			}
		}
	case []any:
		for _, c := range x {
			if w := foreignQuota(c, self); w != "" {
				return w
			}
		}
	}
	return ""
}

func (e *env) visible(actor string, m marker) bool {
	role := e.roleOf(actor)
	switch m.kind {
	case "thread":
		return m.id == actor || role == "leader"
	case "output":
		return m.id == actor
	case "question":
		agent, task, _ := strings.Cut(m.id, " ")
		return e.visible(actor, marker{"thread", agent}) || e.visible(actor, marker{"task", task})
	case "brief":
		return role == "leader" && e.leads(actor, m.id)
	case "task":
		_, t := e.owner("GET", "/api/v1/tasks/"+m.id, nil)
		if t == nil {
			return false
		}
		brief, _ := t["brief_id"].(string)
		assignee, _ := t["assignee"].(string)
		reviewer, _ := t["reviewer"].(string)
		status, _ := t["status"].(string)
		switch {
		case role == "leader":
			return e.leads(actor, brief)
		case assignee == actor || reviewer == actor:
			return true
		case role == "reviewer" && reviewer == "" && (status == "submitted" || status == "in_review"):
			return true
		}
	}
	return false
}

func (e *env) leads(actor, brief string) bool {
	_, b := e.owner("GET", "/api/v1/briefs/"+brief, nil)
	if b == nil {
		return false
	}
	l, _ := b["leader_id"].(string)
	return l == "" || l == actor
}

func (e *env) checkGlobal() *violation {
	code, out := e.owner("GET", "/api/v1/audit/verify", nil)
	if code != 200 || out["ok"] != true {
		return &violation{"audit_chain", fmt.Sprintf("%d %v", code, out)}
	}
	g, h := usage()
	if g > e.baseGoroutines+maxGoroutineGrowth {
		return &violation{"goroutine_growth", fmt.Sprintf("%d -> %d", e.baseGoroutines, g)}
	}
	if h > e.baseHeap+maxHeapGrowth {
		return &violation{"heap_growth", fmt.Sprintf("%d -> %d", e.baseHeap, h)}
	}
	sources := map[string][]byte{"logs": e.logs.snapshot(), "events": e.sse.snapshot()}
	for _, f := range []string{e.dbPath, e.dbPath + "-wal"} {
		b, err := os.ReadFile(f)
		if err == nil {
			sources["db "+f[strings.LastIndex(f, "/")+1:]] = b
		}
	}
	paths := []string{"/api/v1/audit/export", "/api/v1/activity?limit=1000", "/api/v1/alerts", "/api/v1/decisions",
		"/api/v1/logs?limit=2000", "/api/v1/questions", "/api/v1/help-requests?status=all"}
	for _, b := range []string{"B-1", "B-2", "B-3"} {
		paths = append(paths, "/api/v1/briefs/"+b+"/replay", "/api/v1/briefs/"+b+"/replay/timeline", "/api/v1/briefs/"+b+"/report.md")
	}
	for _, a := range agentSpecs {
		paths = append(paths, "/api/v1/agents/"+a.id+"/output?limit=2000")
	}
	for _, p := range paths {
		code, b := e.do("GET", p, e.ownerTok, nil, nil)
		if code >= 500 {
			return &violation{"5xx", fmt.Sprintf("GET %s -> %d %s", p, code, clip(b))}
		}
		sources[p] = b
	}
	for name, b := range sources {
		for _, n := range e.needles {
			if bytes.Contains(b, []byte(n)) {
				return &violation{"secret_stored", fmt.Sprintf("%q in %s%s", n, name, around(b, n))}
			}
		}
	}
	return nil
}

func around(b []byte, n string) string {
	i := bytes.Index(b, []byte(n))
	lo, hi := max(0, i-200), min(len(b), i+len(n)+40)
	return ": ..." + strings.ToValidUTF8(string(b[lo:hi]), "?") + "..."
}

func clip(b []byte) string {
	if len(b) > 300 {
		b = b[:300]
	}
	return strings.ToValidUTF8(string(b), "?")
}
