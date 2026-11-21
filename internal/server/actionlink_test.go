package server_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/notify"
	"github.com/SigmaUno/subspace/internal/server"
	"github.com/SigmaUno/subspace/internal/servertest"
)

const linkBase = "https://sub.test"

var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

type linkEnv struct {
	*servertest.Env
	lead string
}

type confirmPage struct {
	code   int
	body   string
	csrf   string
	cookie *http.Cookie
}

func newLinkEnv(t *testing.T) *linkEnv {
	t.Helper()
	if _, ok := os.LookupEnv("SUBSPACE_RATE_ACTIONLINK"); !ok {
		t.Setenv("SUBSPACE_RATE_ACTIONLINK", "off")
	}
	e := servertest.Start(t, server.Config{PublicURL: linkBase,
		FlushEvery: 50 * time.Millisecond, SweepEvery: 50 * time.Millisecond, Lease: time.Minute})
	return &linkEnv{Env: e, lead: e.Register("rex", "leader", "other")}
}

func (e *linkEnv) okREST(want int, method, path string, body any) map[string]any {
	e.T.Helper()
	code, out := e.REST(method, path, body)
	if code != want {
		e.T.Fatalf("%s %s: %d %v", method, path, code, out)
	}
	return out
}

func (e *linkEnv) plan() string {
	e.T.Helper()
	br := e.okREST(201, "POST", "/api/v1/briefs", map[string]any{"title": "Demo", "body": "b", "repos": []string{"o/r"}})
	task := map[string]any{"title": "t", "spec": "s", "repo": "o/r", "acceptance_criteria": []string{"ok"}}
	p := e.Must(e.lead, "propose_plan", map[string]any{"brief_id": br["id"], "tasks": []any{task}})["plan"].(map[string]any)
	return p["id"].(string)
}

func (e *linkEnv) planStatus(id string) string {
	e.T.Helper()
	return e.okREST(200, "GET", "/api/v1/plans/"+id, nil)["status"].(string)
}

func (e *linkEnv) mint(decision, action string, option int) string {
	e.T.Helper()
	out := e.okREST(201, "POST", "/api/v1/action-links", map[string]any{"decision_id": decision, "action": action, "option": option})
	u := out["url"].(string)
	if !strings.HasPrefix(u, linkBase+"/a/") {
		e.T.Fatalf("url %q", u)
	}
	return strings.TrimPrefix(u, linkBase)
}

func (e *linkEnv) send(req *http.Request) (int, string, *http.Response) {
	e.T.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.T.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp
}

func (e *linkEnv) open(path string) confirmPage {
	e.T.Helper()
	req, _ := http.NewRequest("GET", e.TS.URL+path, nil)
	code, body, resp := e.send(req)
	p := confirmPage{code: code, body: body}
	if m := csrfField.FindStringSubmatch(body); m != nil {
		p.csrf = m[1]
	}
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, "ssa_") {
			p.cookie = c
		}
	}
	return p
}

func (e *linkEnv) confirm(path string, p confirmPage) (int, string) {
	e.T.Helper()
	form := url.Values{}
	if p.csrf != "" {
		form.Set("csrf", p.csrf)
	}
	req, _ := http.NewRequest("POST", e.TS.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if p.cookie != nil {
		req.AddCookie(&http.Cookie{Name: p.cookie.Name, Value: p.cookie.Value})
	}
	code, body, _ := e.send(req)
	return code, body
}

func (e *linkEnv) audit(tool string) []map[string]any {
	e.T.Helper()
	var out []map[string]any
	for _, ev := range e.okREST(200, "GET", "/api/v1/audit?tool="+tool, nil)["events"].([]any) {
		out = append(out, ev.(map[string]any))
	}
	return out
}

func TestActionLinkApprovesPlanOnlyOnConfirmedPost(t *testing.T) {
	e := newLinkEnv(t)
	pid := e.plan()
	path := e.mint("plan:"+pid, "approve", 0)

	page := e.open(path)
	if page.code != 200 || page.csrf == "" || page.cookie == nil || !strings.Contains(page.body, "Approve plan") {
		t.Fatalf("confirm page: %d %q", page.code, page.body)
	}
	if !page.cookie.HttpOnly || !page.cookie.Secure || page.cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie flags %+v", page.cookie)
	}
	if e.open(path).code != 200 || e.planStatus(pid) != "proposed" {
		t.Fatal("GET must not perform the action")
	}

	if code, body := e.confirm(path, page); code != 200 || !strings.Contains(body, "done") {
		t.Fatalf("confirm: %d %s", code, body)
	}
	if st := e.planStatus(pid); st != "approved" {
		t.Fatalf("plan %s", st)
	}
	evs := e.audit("approve_plan")
	if len(evs) != 1 || evs[0]["role"] != "owner" || evs[0]["note"] != "action-link" {
		t.Fatalf("audit %v", evs)
	}

	if code, _ := e.confirm(path, page); code != http.StatusGone {
		t.Fatalf("replayed POST: %d", code)
	}
	if p := e.open(path); p.code != http.StatusGone || p.csrf != "" {
		t.Fatalf("replayed GET: %d", p.code)
	}
	if n := len(e.audit("approve_plan")); n != 1 {
		t.Fatalf("approve_plan audited %d times", n)
	}
}

func TestActionLinkTamperMatrix(t *testing.T) {
	e := newLinkEnv(t)
	pid := e.plan()
	other := e.plan()
	path := e.mint("plan:"+pid, "reject", 0)
	tok := strings.TrimPrefix(path, "/a/")
	body, sig, _ := strings.Cut(tok, ".")
	otherBody, otherSig, _ := strings.Cut(strings.TrimPrefix(e.mint("plan:"+other, "approve", 0), "/a/"), ".")

	forge := func(edit func(map[string]any)) string {
		raw, err := base64.RawURLEncoding.DecodeString(body)
		if err != nil {
			t.Fatal(err)
		}
		var c map[string]any
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		edit(c)
		b, _ := json.Marshal(c)
		return base64.RawURLEncoding.EncodeToString(b) + "." + sig
	}
	flip := func(s string, i int) string {
		b := []byte(s)
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		return string(b)
	}

	cases := map[string]string{
		"payload bit":       flip(body, len(body)/2) + "." + sig,
		"signature bit":     body + "." + flip(sig, 3),
		"no signature":      body,
		"empty signature":   body + ".",
		"other signature":   body + "." + otherSig,
		"other payload":     otherBody + "." + sig,
		"action swapped":    forge(func(c map[string]any) { c["a"] = "approve" }),
		"decision swapped":  forge(func(c map[string]any) { c["d"] = "plan:" + other }),
		"expiry extended":   forge(func(c map[string]any) { c["e"] = c["e"].(float64) + 3600 }),
		"extra field":       forge(func(c map[string]any) { c["x"] = 1 }),
		"garbage":           "not-a-token",
		"oversized":         strings.Repeat("A", 2000) + "." + sig,
		"signature only":    "." + sig,
		"double separator":  body + ".." + sig,
		"trailing segment":  tok + ".x",
		"padded base64 sig": body + "." + sig + "==",
	}
	for name, forged := range cases {
		t.Run(name, func(t *testing.T) {
			p := e.open("/a/" + url.PathEscape(forged))
			if p.code != http.StatusNotFound || p.csrf != "" || strings.Contains(p.body, pid) || strings.Contains(p.body, other) {
				t.Fatalf("GET: %d %s", p.code, p.body)
			}
			if code, _ := e.confirm("/a/"+url.PathEscape(forged), confirmPage{csrf: "x.y"}); code != http.StatusNotFound {
				t.Fatalf("POST: %d", code)
			}
		})
	}
	if e.planStatus(pid) != "proposed" || e.planStatus(other) != "proposed" {
		t.Fatal("tampered link changed a plan")
	}

	if p := e.open(path); p.code != 200 || !strings.Contains(p.body, pid) || strings.Contains(p.body, other) {
		t.Fatalf("genuine link: %d %s", p.code, p.body)
	}
}

func TestActionLinkRequiresRenderedConfirmation(t *testing.T) {
	e := newLinkEnv(t)
	pid := e.plan()
	path := e.mint("plan:"+pid, "approve", 0)
	page := e.open(path)
	otherPage := e.open(e.mint("plan:"+e.plan(), "approve", 0))

	for name, p := range map[string]confirmPage{
		"no csrf":          {cookie: page.cookie},
		"no cookie":        {csrf: page.csrf},
		"foreign csrf":     {csrf: otherPage.csrf, cookie: page.cookie},
		"foreign pair":     {csrf: otherPage.csrf, cookie: &http.Cookie{Name: page.cookie.Name, Value: otherPage.cookie.Value}},
		"mismatched nonce": {csrf: page.csrf, cookie: &http.Cookie{Name: page.cookie.Name, Value: otherPage.cookie.Value}},
	} {
		if code, _ := e.confirm(path, p); code != http.StatusForbidden {
			t.Fatalf("%s: %d", name, code)
		}
	}
	if e.planStatus(pid) != "proposed" {
		t.Fatal("unconfirmed POST changed the plan")
	}
	if code, _ := e.confirm(path, page); code != 200 {
		t.Fatalf("confirm after rejected attempts: %d", code)
	}
}

func TestActionLinkWrongActionAndStaleDecision(t *testing.T) {
	e := newLinkEnv(t)
	pid := e.plan()

	for name, body := range map[string]map[string]any{
		"action of another kind": {"decision_id": "plan:" + pid, "action": "merge"},
		"option on approve":      {"decision_id": "plan:" + pid, "action": "approve", "option": 1},
		"unknown decision":       {"decision_id": "plan:P-999", "action": "approve"},
		"ttl too long":           {"decision_id": "plan:" + pid, "action": "approve", "ttl": "48h"},
		"missing action":         {"decision_id": "plan:" + pid},
	} {
		if code, out := e.REST("POST", "/api/v1/action-links", body); code < 400 || code >= 500 {
			t.Fatalf("%s: %d %v", name, code, out)
		}
	}

	path := e.mint("plan:"+pid, "approve", 0)
	page := e.open(path)
	e.okREST(200, "POST", "/api/v1/plans/"+pid+"/reject", map[string]any{"reason": "no"})
	if p := e.open(path); p.code != http.StatusConflict || p.csrf != "" {
		t.Fatalf("GET on decided plan: %d", p.code)
	}
	if code, _ := e.confirm(path, page); code != http.StatusConflict {
		t.Fatalf("POST on decided plan: %d", code)
	}
	if st := e.planStatus(pid); st != "rejected" {
		t.Fatalf("plan %s", st)
	}
	if n := len(e.audit("approve_plan")); n != 0 {
		t.Fatalf("approve_plan audited %d times", n)
	}
}

func TestActionLinkConcurrentDoubleSubmitExecutesOnce(t *testing.T) {
	e := newLinkEnv(t)
	pid := e.plan()
	path := e.mint("plan:"+pid, "approve", 0)
	page := e.open(path)

	const n = 8
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			code, _ := e.confirm(path, page)
			codes <- code
		})
	}
	wg.Wait()
	close(codes)
	got := map[int]int{}
	for c := range codes {
		got[c]++
	}
	if got[200] != 1 || got[http.StatusGone] != n-1 {
		t.Fatalf("status counts %v", got)
	}
	if k := len(e.audit("approve_plan")); k != 1 {
		t.Fatalf("approve_plan audited %d times", k)
	}
}

func TestActionLinkAnswersChosenOptionAndSnoozes(t *testing.T) {
	e := newLinkEnv(t)
	wk := e.Register("w1", "worker", "claude-code")
	q := e.Must(wk, "ask_owner", map[string]any{"question": "which db?", "options": []string{"sqlite", "postgres"}})["question"].(map[string]any)
	qid := q["id"].(string)

	if code, _ := e.REST("POST", "/api/v1/action-links", map[string]any{"decision_id": "q:" + qid, "action": "answer", "option": 2}); code != http.StatusConflict {
		t.Fatalf("out of range option: %d", code)
	}
	first := e.mint("q:"+qid, "answer", 0)
	second := e.mint("q:"+qid, "answer", 1)
	firstPage := e.open(first)
	page := e.open(second)
	if page.code != 200 || !strings.Contains(page.body, "Answer: postgres") {
		t.Fatalf("page %d %s", page.code, page.body)
	}
	if code, _ := e.confirm(second, page); code != 200 {
		t.Fatalf("confirm %d", code)
	}
	qs := e.okREST(200, "GET", "/api/v1/questions?status=answered", nil)["questions"].([]any)
	if len(qs) != 1 || qs[0].(map[string]any)["answer"] != "postgres" {
		t.Fatalf("questions %v", qs)
	}
	if code, _ := e.confirm(first, firstPage); code != http.StatusConflict {
		t.Fatalf("sibling link after answer: %d", code)
	}

	pid := e.plan()
	snooze := e.mint("plan:"+pid, "skip", 0)
	if code, _ := e.confirm(snooze, e.open(snooze)); code != 200 {
		t.Fatalf("snooze %d", code)
	}
	for _, d := range e.okREST(200, "GET", "/api/v1/decisions", nil)["decisions"].([]any) {
		if d.(map[string]any)["id"] == "plan:"+pid {
			t.Fatal("snoozed plan still in the inbox")
		}
	}
	if e.planStatus(pid) != "proposed" {
		t.Fatal("snooze changed the plan")
	}
}

func TestActionLinkRateLimited(t *testing.T) {
	t.Setenv("SUBSPACE_RATE_ACTIONLINK", "2/m:2")
	e := newLinkEnv(t)
	for i, want := range []int{404, 404, 429} {
		if p := e.open("/a/bogus.token"); p.code != want {
			t.Fatalf("request %d: %d want %d", i, p.code, want)
		}
	}
}

func TestNotificationsCarryWorkingActionLinks(t *testing.T) {
	var mu sync.Mutex
	got := map[string]notify.Notification{}
	sink := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var n notify.Notification
		_ = json.NewDecoder(r.Body).Decode(&n)
		mu.Lock()
		got[n.ID] = n
		mu.Unlock()
	}))
	defer sink.Close()
	t.Setenv(notify.EnvWebhookURL, sink.URL)
	t.Setenv(notify.EnvAllowPrivate, "true")
	e := newLinkEnv(t)

	pid := e.plan()
	wk := e.Register("w1", "worker", "claude-code")
	qid := e.Must(wk, "ask_owner", map[string]any{"question": "which?", "options": []string{"a", "b"}})["question"].(map[string]any)["id"].(string)
	plain := e.Must(wk, "ask_owner", map[string]any{"question": "free text?"})["question"].(map[string]any)["id"].(string)

	wait := func(id string) notify.Notification {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n, ok := got[id]
			mu.Unlock()
			if ok {
				return n
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("no notification for %s", id)
		return notify.Notification{}
	}
	labels := func(n notify.Notification) string {
		var ls []string
		for _, a := range n.Actions {
			if !strings.HasPrefix(a.URL, linkBase+"/a/") {
				t.Fatalf("action url %q", a.URL)
			}
			ls = append(ls, a.Label)
		}
		return strings.Join(ls, ",")
	}

	pn := wait(pid)
	if l := labels(pn); l != "Approve,Reject,Snooze 1h" {
		t.Fatalf("plan actions %q", l)
	}
	if l := labels(wait(qid)); l != "Option 1,Option 2,Snooze 1h" {
		t.Fatalf("question actions %q", l)
	}
	if l := labels(wait(plain)); l != "" {
		t.Fatalf("free-text question actions %q", l)
	}

	approve := strings.TrimPrefix(pn.Actions[0].URL, linkBase)
	if code, _ := e.confirm(approve, e.open(approve)); code != 200 {
		t.Fatalf("confirm %d", code)
	}
	if st := e.planStatus(pid); st != "approved" {
		t.Fatalf("plan %s", st)
	}
	reject := strings.TrimPrefix(pn.Actions[1].URL, linkBase)
	if p := e.open(reject); p.code != http.StatusConflict {
		t.Fatalf("reject after approve: %d", p.code)
	}
}
