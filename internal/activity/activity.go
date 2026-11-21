package activity

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/redact"
	"github.com/SigmaUno/subspace/internal/trace"
)

const (
	SourceAudit   = "audit"
	SourceMessage = "message"

	maxNote = 120
	maxBody = 240
)

type Ref struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type Item struct {
	ID       string    `json:"id"`
	TS       time.Time `json:"ts"`
	Source   string    `json:"source"`
	Tool     string    `json:"tool,omitempty"`
	Actor    Ref       `json:"actor"`
	Text     string    `json:"text"`
	Targets  []Ref     `json:"targets"`
	Severity string    `json:"severity"`
	Icon     string    `json:"icon"`
	BriefID  string    `json:"brief_id,omitempty"`
	TraceID  string    `json:"trace_id,omitempty"`
}

var placeholder = regexp.MustCompile(`\{([\w.]+)(?:\?([^}|]*)(?:\|([^}]*))?)?\}`)

func FromAudit(e *model.AuditEvent) Item {
	f := auditFields{tool: e.Tool, before: e.Before, after: e.After, actor: actorRef(e.AgentID, e.Role)}
	if e.TaskID != nil {
		f.task = *e.TaskID
	}
	var traceID string
	if e.Note != nil {
		note, id := splitTrace(*e.Note)
		traceID = id
		f.note = clip(redact.String(stripTokenID(note)), maxNote)
	}
	r, ok := rules[e.Tool]
	if !ok {
		r = fallback
	}
	text := r.text
	if f.task != "" && r.onTask != "" {
		text = r.onTask
	}
	if alt, ok := r.byStatus[f.get("status")]; ok {
		text = alt
	}
	return Item{
		ID:       "a:" + strconv.FormatInt(e.ID, 10),
		TS:       e.TS,
		Source:   SourceAudit,
		Tool:     e.Tool,
		Actor:    f.actor,
		Text:     render(text, f),
		Targets:  f.targets(),
		Severity: severity(r, f),
		Icon:     r.icon,
		TraceID:  traceID,
	}
}

func FromMessage(m *model.Message) Item {
	actor := actorRef(m.AuthorID, m.AuthorRole)
	body := clip(redact.String(m.Body), maxBody)
	text := body
	if r, _ := utf8.DecodeRuneInString(body); unicode.IsLower(r) {
		text = actor.ID + " " + body
	}
	sev, icon := messageSeverity(body)
	var targets []Ref
	switch m.ThreadKind {
	case model.ThreadTask, model.ThreadBrief, model.ThreadAgent:
		targets = []Ref{{Kind: m.ThreadKind, ID: m.ThreadID}}
	}
	return Item{
		ID:       "m:" + strconv.FormatInt(m.ID, 10),
		TS:       m.CreatedAt,
		Source:   SourceMessage,
		Actor:    actor,
		Text:     text,
		Targets:  nonNil(targets),
		Severity: sev,
		Icon:     icon,
	}
}

type auditFields struct {
	actor         Ref
	tool          string
	task          string
	note          string
	before, after map[string]any
}

func (f auditFields) get(name string) string {
	switch name {
	case "actor":
		return f.actor.ID
	case "tool":
		return f.tool
	case "task":
		return f.task
	case "note":
		return f.note
	case "status":
		return format(f.after["status"])
	case "pr":
		return prNumber(f.note)
	}
	if k, ok := strings.CutPrefix(name, "a."); ok {
		return clip(redact.String(format(f.after[k])), maxNote)
	}
	if k, ok := strings.CutPrefix(name, "b."); ok {
		return clip(redact.String(format(f.before[k])), maxNote)
	}
	return ""
}

func (f auditFields) targets() []Ref {
	var out []Ref
	add := func(kind, id string) {
		if id == "" {
			return
		}
		r := Ref{Kind: kind, ID: id}
		for _, o := range out {
			if o == r {
				return
			}
		}
		out = append(out, r)
	}
	add("task", f.task)
	add("plan", format(f.after["plan_id"]))
	add("brief", format(f.after["brief_id"]))
	add("question", format(f.after["question_id"]))
	add("spawn", format(f.after["spawn_id"]))
	if id := format(f.after["request_id"]); strings.HasPrefix(id, "HR-") {
		add("help_request", id)
	}
	if strings.HasSuffix(f.tool, "_artifact") {
		add("artifact", firstNonEmpty(format(f.after["id"]), format(f.before["id"])))
	}
	for _, k := range []string{"assignee", "bot", "agent"} {
		add("agent", format(f.after[k]))
		add("agent", format(f.before[k]))
	}
	for _, id := range list(f.after["created"]) {
		add("task", id)
	}
	return nonNil(out)
}

func actorRef(id, role string) Ref {
	switch {
	case role == model.RoleOwner:
		return Ref{Kind: "owner", ID: "owner"}
	case id == "":
		return Ref{Kind: "system", ID: "system"}
	case role == "system":
		return Ref{Kind: "system", ID: id}
	}
	return Ref{Kind: "agent", ID: id}
}

func render(text string, f auditFields) string {
	out := placeholder.ReplaceAllStringFunc(text, func(m string) string {
		p := placeholder.FindStringSubmatch(m)
		v := f.get(p[1])
		if v == "" {
			return ""
		}
		return p[2] + v + p[3]
	})
	return strings.Join(strings.Fields(out), " ")
}

func severity(r rule, f auditFields) string {
	if r.warnIf != "" {
		if v := f.get(r.warnIf); v != "" && v != "0" {
			return SevWarning
		}
	}
	if r.severity != "" {
		return r.severity
	}
	switch f.get("status") {
	case model.StatusDone, model.StatusApproved:
		return SevSuccess
	case model.StatusBlocked, model.StatusNeedsOwner, model.StatusChangesRequested, model.StatusCancelled, "stopped":
		return SevWarning
	case "failed":
		return SevError
	}
	return SevInfo
}

var warnWords = []string{"expired", "blocked", "paused", "budget", "poison", "revoked", "cancelled", "needs_owner"}

func messageSeverity(body string) (string, string) {
	low := strings.ToLower(body)
	if strings.Contains(low, "failed") {
		return SevError, "alert"
	}
	for _, w := range warnWords {
		if strings.Contains(low, w) {
			return SevWarning, "alert"
		}
	}
	return SevInfo, "comment"
}

func Severities() []string { return []string{SevInfo, SevSuccess, SevWarning, SevError} }

func format(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int, int64, bool:
		return fmt.Sprint(x)
	case []string:
		return strings.Join(x, ", ")
	case []any:
		return strings.Join(list(x), ", ")
	}
	return ""
}

func list(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s := format(e); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func prNumber(note string) string {
	url, ok := strings.CutPrefix(note, "PR ")
	if !ok {
		return ""
	}
	fields := strings.Fields(url)
	if len(fields) == 0 {
		return ""
	}
	url = strings.TrimRight(fields[0], "/")
	n := url[strings.LastIndex(url, "/")+1:]
	if _, err := strconv.Atoi(n); err != nil {
		return ""
	}
	return n
}

func splitTrace(note string) (string, string) {
	const tag = "trace="
	i := len(note) - len(tag) - 32
	if i < 0 || note[i:i+len(tag)] != tag || (i > 0 && !strings.HasSuffix(note[:i], "; ")) {
		return note, ""
	}
	id := note[i+len(tag):]
	if _, ok := trace.ParseID(id); !ok {
		return note, ""
	}
	return strings.TrimSuffix(note[:i], "; "), id
}

func stripTokenID(note string) string {
	rest, ok := strings.CutPrefix(note, "token=")
	if !ok {
		return note
	}
	id, tail, _ := strings.Cut(rest, "; ")
	if id == "" || strings.ContainsAny(id, " \t\n") {
		return note
	}
	return tail
}

func clip(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit]) + "…"
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func nonNil(r []Ref) []Ref {
	if r == nil {
		return []Ref{}
	}
	return r
}
