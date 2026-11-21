package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/webhookout"
)

type outReceiver struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	hits   []outHit
}

type outHit struct {
	header http.Header
	body   []byte
}

func newOutReceiver(t *testing.T) *outReceiver {
	t.Helper()
	r := &outReceiver{status: http.StatusOK}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.hits = append(r.hits, outHit{req.Header.Clone(), b})
		status := r.status
		r.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *outReceiver) setStatus(code int) {
	r.mu.Lock()
	r.status = code
	r.mu.Unlock()
}

func (r *outReceiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.hits)
}

func (r *outReceiver) hit(i int) outHit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[i]
}

func waitOut(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func outJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func outDeliveries(e *servertest.Env, id, state string) []map[string]any {
	e.T.Helper()
	code, out := e.REST("GET", "/api/v1/webhooks-out/"+id+"/deliveries?state="+state, nil)
	if code != 200 {
		e.T.Fatalf("deliveries: %d %v", code, out)
	}
	var ds []map[string]any
	for _, x := range out["deliveries"].([]any) {
		ds = append(ds, x.(map[string]any))
	}
	return ds
}

func TestOutboundWebhookFlow(t *testing.T) {
	t.Setenv("SUBSPACE_WEBHOOKS_OUT_ALLOW_CIDRS", "127.0.0.0/8")
	e, clk := newTokenEnv(t)
	rcv := newOutReceiver(t)

	code, out := e.REST("POST", "/api/v1/webhooks-out", map[string]any{"url": rcv.URL + "/in", "events": []string{"agent.updated"}})
	if code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	secret := out["secret"].(string)
	hook := out["webhook"].(map[string]any)
	id := hook["id"].(string)
	if !strings.HasPrefix(secret, "whsec_") || id != "WH-1" {
		t.Fatalf("secret %q id %q", secret, id)
	}
	if _, leaked := hook["secret"]; leaked {
		t.Fatal("webhook object exposes the secret")
	}

	code, out = e.REST("GET", "/api/v1/webhooks-out", nil)
	if code != 200 || len(out["webhooks"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, out)
	}
	if strings.Contains(out["webhooks"].([]any)[0].(map[string]any)["url"].(string), secret) || strings.Contains(outJSON(out), secret) {
		t.Fatal("list exposes the secret")
	}

	e.Register("w1", "worker", "claude-code")
	waitOut(t, "first delivery", func() bool { return rcv.count() == 1 })
	h := rcv.hit(0)
	if !webhookout.Verify(secret, h.header.Get("X-Subspace-Timestamp"), h.body, h.header.Get("X-Subspace-Signature")) {
		t.Fatalf("bad signature on %s", h.body)
	}
	if h.header.Get("X-Subspace-Event") != "agent.updated" || h.header.Get("X-Subspace-Delivery") == "" {
		t.Fatalf("headers %v", h.header)
	}
	if strings.Contains(string(h.body), "ssp_") {
		t.Fatalf("payload holds a token: %s", h.body)
	}
	waitOut(t, "delivered state", func() bool { return len(outDeliveries(e, id, "delivered")) == 1 })

	rcv.setStatus(http.StatusInternalServerError)
	e.Register("w2", "worker", "claude-code")
	waitOut(t, "failed attempt", func() bool {
		ds := outDeliveries(e, id, "pending")
		return len(ds) == 1 && ds[0]["attempts"].(float64) == 1
	})
	clk.Advance(time.Hour)
	waitOut(t, "retry", func() bool {
		ds := outDeliveries(e, id, "pending")
		return len(ds) == 1 && ds[0]["attempts"].(float64) == 2
	})
	clk.Advance(25 * time.Hour)
	waitOut(t, "dead letter", func() bool { return len(outDeliveries(e, id, "dead")) == 1 })
	dead := outDeliveries(e, id, "dead")[0]
	if dead["last_status"].(float64) != 500 || dead["next_attempt_at"] != nil {
		t.Fatalf("dead %v", dead)
	}
	_, hook = e.REST("GET", "/api/v1/webhooks-out/"+id, nil)
	if hook["dead"].(float64) != 1 || hook["delivered"].(float64) != 1 || hook["pending"].(float64) != 0 {
		t.Fatalf("counts %v", hook)
	}

	did := dead["id"].(string)
	if code, _ := e.REST("POST", "/api/v1/webhooks-out/"+id+"/deliveries/"+did+"/redeliver", map[string]any{}); code != 200 {
		t.Fatalf("redeliver: %d", code)
	}
	rcv.setStatus(http.StatusOK)
	waitOut(t, "redelivery", func() bool {
		return len(outDeliveries(e, id, "dead")) == 0 && len(outDeliveries(e, id, "delivered")) == 2
	})
	if code, _ := e.REST("POST", "/api/v1/webhooks-out/"+id+"/deliveries/"+did+"/redeliver", map[string]any{}); code != 409 {
		t.Fatalf("redeliver of a delivered entry: %d", code)
	}

	_, audit := e.REST("GET", "/api/v1/audit?tool=create_webhook_out", nil)
	if evs := audit["events"].([]any); len(evs) != 1 || strings.Contains(outJSON(evs), secret) {
		t.Fatalf("audit %v", evs)
	}

	if code, _ := e.REST("DELETE", "/api/v1/webhooks-out/"+id, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := e.REST("GET", "/api/v1/webhooks-out/"+id, nil); code != 404 {
		t.Fatalf("get after delete: %d", code)
	}
	before := rcv.count()
	e.Register("w3", "worker", "claude-code")
	time.Sleep(700 * time.Millisecond)
	if rcv.count() != before {
		t.Fatal("deleted webhook still receives events")
	}
}

func TestOutboundWebhookBriefFilter(t *testing.T) {
	t.Setenv("SUBSPACE_WEBHOOKS_OUT_ALLOW_CIDRS", "127.0.0.0/8")
	e := servertest.New(t)
	rcv := newOutReceiver(t)
	_, b1 := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "one", "body": "b", "repos": []string{"o/r"}})
	_, b2 := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "two", "body": "b", "repos": []string{"o/r"}})
	lead := e.Register("rex", "leader", "other")
	for _, brief := range []string{b1["id"].(string), b2["id"].(string)} {
		e.Must(lead, "propose_plan", map[string]any{"brief_id": brief, "tasks": []any{map[string]any{
			"title": "T for " + brief, "spec": "do it", "acceptance_criteria": []string{"ok"}, "repo": "o/r"}}})
	}
	e.REST("POST", "/api/v1/webhooks-out", map[string]any{"url": rcv.URL, "events": []string{"plan.proposed"}, "brief_id": b2["id"]})
	e.Must(lead, "propose_plan", map[string]any{"brief_id": b1["id"], "tasks": []any{map[string]any{
		"title": "again", "spec": "do it", "acceptance_criteria": []string{"ok"}, "repo": "o/r"}}})
	e.Must(lead, "propose_plan", map[string]any{"brief_id": b2["id"], "tasks": []any{map[string]any{
		"title": "Visible title", "spec": "SPEC-NOT-IN-PAYLOAD", "acceptance_criteria": []string{"ok"}, "repo": "o/r"}}})
	waitOut(t, "filtered delivery", func() bool { return rcv.count() >= 1 })
	time.Sleep(300 * time.Millisecond)
	if rcv.count() != 1 {
		t.Fatalf("receiver got %d events, want only the filtered brief's", rcv.count())
	}
	body := string(rcv.hit(0).body)
	if !strings.Contains(body, b2["id"].(string)) || !strings.Contains(body, "Visible title") || strings.Contains(body, "SPEC-NOT-IN-PAYLOAD") {
		t.Fatalf("payload %s", body)
	}
}

func TestOutboundWebhookRefusesPrivateReceivers(t *testing.T) {
	e := servertest.New(t)
	for _, u := range []string{"http://127.0.0.1:9/x", "http://10.1.2.3/x", "http://169.254.169.254/latest", "http://[::1]/x", "ftp://example.com/x", "https://u:p@example.com/"} {
		if code, out := e.REST("POST", "/api/v1/webhooks-out", map[string]any{"url": u, "events": []string{"task.updated"}}); code != 400 {
			t.Errorf("%s: %d %v", u, code, out)
		}
	}
	if code, out := e.REST("POST", "/api/v1/webhooks-out", map[string]any{"url": "https://example.com/", "events": []string{}}); code != 400 {
		t.Errorf("empty events: %d %v", code, out)
	}
	if code, out := e.REST("POST", "/api/v1/webhooks-out", map[string]any{"url": "https://example.com/", "events": []string{"task.updated"}, "extra": 1}); code != 400 {
		t.Errorf("unknown field: %d %v", code, out)
	}

	code, out := e.REST("POST", "/api/v1/webhooks-out", map[string]any{"url": "http://localhost:1/x", "events": []string{"agent.updated"}})
	if code != 201 {
		t.Fatalf("hostname receiver: %d %v", code, out)
	}
	id := out["webhook"].(map[string]any)["id"].(string)
	e.Register("w1", "worker", "claude-code")
	waitOut(t, "blocked attempt", func() bool {
		ds := outDeliveries(e, id, "pending")
		return len(ds) == 1 && ds[0]["attempts"].(float64) >= 1
	})
	d := outDeliveries(e, id, "pending")[0]
	if d["last_error"] != webhookout.ErrBlocked.Error() || d["last_status"] != nil {
		t.Fatalf("delivery %v", d)
	}
}

func TestOutboundWebhookViewerIsReadOnly(t *testing.T) {
	e := servertest.New(t)
	_, secret := createToken(e, "viewer", "viewer", nil)
	body := map[string]any{"url": "https://example.com/", "events": []string{"task.updated"}}
	if code, _ := e.RESTAs(secret, "POST", "/api/v1/webhooks-out", body); code != 403 {
		t.Fatalf("viewer create: %d", code)
	}
	if code, _ := e.RESTAs(secret, "GET", "/api/v1/webhooks-out", nil); code != 200 {
		t.Fatalf("viewer list: %d", code)
	}
}
