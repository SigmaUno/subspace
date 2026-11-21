package webhookout

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/redact"
	"github.com/SigmaUno/subspace/internal/trace"
)

const maxTitle = 200

var eventPattern = regexp.MustCompile(`^[a-z_]+\.([a-z_]+|\*)$`)

var scalarFields = []string{"id", "brief_id", "plan_id", "task_id", "agent_id", "title", "status", "role", "kind", "assignee", "reviewer", "current_task_id"}

func matches(patterns []string, typ string) bool {
	for _, p := range patterns {
		if p == typ {
			return true
		}
		if prefix, ok := strings.CutSuffix(p, "*"); ok && strings.HasPrefix(typ, prefix) {
			return true
		}
	}
	return false
}

func shorten(s string) string {
	s = redact.String(s)
	if r := []rune(s); len(r) > maxTitle {
		return string(r[:maxTitle])
	}
	return s
}

func text(k string, v any) (string, bool) {
	s, ok := v.(string)
	if ok && k == "title" {
		s = shorten(s)
	}
	return s, ok
}

func extract(data any) (map[string]any, string) {
	out := map[string]any{}
	raw, err := json.Marshal(data)
	if err != nil {
		return out, ""
	}
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return out, ""
	}
	for _, k := range scalarFields {
		if v, ok := text(k, in[k]); ok {
			out[k] = v
		}
	}
	if list, ok := in["tasks"].([]any); ok {
		tasks := make([]map[string]any, 0, len(list))
		for _, x := range list {
			m, _ := x.(map[string]any)
			t := map[string]any{}
			for _, k := range []string{"id", "title"} {
				if v, ok := text(k, m[k]); ok {
					t[k] = v
				}
			}
			tasks = append(tasks, t)
		}
		out["tasks"] = tasks
	}
	brief, _ := out["brief_id"].(string)
	return out, brief
}

func buildBody(id, event string, at time.Time, data map[string]any, tc trace.SpanContext) string {
	m := map[string]any{"id": id, "event": event, "created_at": at.UTC().Format(time.RFC3339), "data": data}
	if !tc.TraceID.IsZero() {
		m["trace_id"] = tc.TraceID.String()
		m["trace_flags"] = hex.EncodeToString([]byte{tc.Flags})
	}
	b, _ := json.Marshal(m)
	return string(b)
}

var (
	traceKey = []byte(`"trace_id":"`)
	flagsKey = []byte(`"trace_flags":"`)
)

func payloadTrace(payload []byte) (string, byte) {
	id, ok := keyed(payload, traceKey, 32)
	if !ok {
		return "", 0
	}
	if _, ok := trace.ParseID(id); !ok {
		return "", 0
	}
	f, ok := keyed(payload, flagsKey, 2)
	if !ok {
		return id, 0
	}
	b, err := hex.DecodeString(f)
	if err != nil {
		return id, 0
	}
	return id, b[0]
}

func keyed(payload, key []byte, n int) (string, bool) {
	i := bytes.LastIndex(payload, key)
	if i < 0 || len(payload) < i+len(key)+n {
		return "", false
	}
	return string(payload[i+len(key) : i+len(key)+n]), true
}
