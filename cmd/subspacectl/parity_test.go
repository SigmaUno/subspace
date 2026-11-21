package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/SigmaUno/subspace/internal/guardhint"
	"github.com/SigmaUno/subspace/internal/openapi"
)

type use struct{ args, stdin string }

const (
	legacyBots   = "legacy alias of the /agents routes; the agents commands are the supported surface"
	perItem      = "applied through the per-item routes by the existing bulk command"
	rawStream    = "raw event stream; agent output is followed with output tail --follow"
	scrapeTarget = "Prometheus scrape target, not an operator command"
	pending      = "no command yet; the web UI and curl cover it"
)

var covered = map[string]use{
	"GET /api/v1/hillclimb/jobs":                                     {"hillclimb list", ""},
	"POST /api/v1/hillclimb/jobs":                                    {"hillclimb create --file -", `{"brief_id":"B-1"}`},
	"GET /api/v1/hillclimb/jobs/{id}/series":                         {"hillclimb series H-1", ""},
	"GET /api/v1/instructions/{scope}/{kind}/versions":               {"instructions versions --kind rules", ""},
	"GET /api/v1/instructions/{scope}/{kind}/versions/{n}":           {"instructions get --kind rules --version 1", ""},
	"GET /api/v1/instructions/{scope}/{kind}/diff":                   {"instructions diff --kind rules --from 1 --to 2", ""},
	"POST /api/v1/instructions/{scope}/{kind}/rollback":              {"instructions rollback --kind rules --to 1", ""},
	"POST /api/v1/action-links":                                      {"action-links create --decision plan:P-1 --action approve", ""},
	"GET /api/v1/timeseries":                                         {"timeseries --metric tasks_done --bucket 1d", ""},
	"GET /api/v1/activity":                                           {"activity --brief-id B-1 --severity warning", ""},
	"GET /api/v1/briefs/{id}":                                        {"briefs show B-1", ""},
	"GET /api/v1/briefs/{id}/architecture":                           {"briefs architecture B-1", ""},
	"GET /api/v1/help-requests":                                      {"help-requests --status open", ""},
	"GET /api/v1/log-level":                                          {"log-level get", ""},
	"GET /api/v1/prs":                                                {"prs --state open", ""},
	"GET /api/v1/threads/{kind}/{id}/messages":                       {"threads list task T-1", ""},
	"PATCH /api/v1/briefs/{id}":                                      {"briefs update B-1 --file -", `{"cross_review":true}`},
	"POST /api/v1/briefs/{id}/leader":                                {"briefs leader B-1 --agent rex", ""},
	"POST /api/v1/tasks/{id}/gates/override":                         {"tasks override-gates T-1 --reason accepted", ""},
	"POST /api/v1/threads/{kind}/{id}/messages":                      {"threads post task T-1 --body hi", ""},
	"POST /api/v1/token-leaks/{id}/reissue":                          {"token-leaks reissue TL-1", ""},
	"PUT /api/v1/autopilot/briefs/{id}/spawn":                        {"autopilot spawn B-1 --worker-kind claude-code --model m", ""},
	"PUT /api/v1/briefs/{id}/components":                             {"briefs components set B-1 --file -", `{"components":[]}`},
	"PUT /api/v1/log-level":                                          {"log-level set --level warn", ""},
	"DELETE /api/v1/repos/{owner}/{name}":                            {"repos remove o/r", ""},
	"GET /api/v1/repos":                                              {"repos list", ""},
	"GET /api/v1/repos/{owner}/{name}":                               {"repos show o/r", ""},
	"GET /api/v1/repos/{owner}/{name}/health":                        {"repos health o/r", ""},
	"POST /api/v1/repos":                                             {"repos add o/r --host github", ""},
	"PUT /api/v1/repos/{owner}/{name}":                               {"repos update o/r --default-branch trunk", ""},
	"DELETE /api/v1/gateways/{name}":                                 {"gateways remove docs", ""},
	"GET /api/v1/gateways":                                           {"gateways list", ""},
	"GET /api/v1/gateways/{name}":                                    {"gateways show docs", ""},
	"GET /api/v1/gateways/{name}/tools":                              {"gateways tools docs", ""},
	"POST /api/v1/gateways":                                          {"gateways add --file -", `{"name":"docs"}`},
	"PUT /api/v1/gateways/{name}":                                    {"gateways update docs --file -", `{"name":"docs"}`},
	"DELETE /api/v1/agent-profiles/{name}":                           {"profiles delete p1", ""},
	"DELETE /api/v1/announcements/{id}":                              {"announcements delete AN-1", ""},
	"DELETE /api/v1/artifacts/{id}":                                  {"artifacts delete A-1", ""},
	"DELETE /api/v1/knowledge/{id}":                                  {"knowledge delete K-1", ""},
	"DELETE /api/v1/plans/{id}/tasks/{taskId}":                       {"plans drop-task P-1 T-1", ""},
	"DELETE /api/v1/playbooks/{name}":                                {"playbooks delete hotfix-x", ""},
	"DELETE /api/v1/schedules/{id}":                                  {"schedules delete SC-1", ""},
	"DELETE /api/v1/webhooks-out/{id}":                               {"webhooks-out delete W-1", ""},
	"GET /api/v1/agent-output":                                       {"output latest", ""},
	"GET /api/v1/agent-profiles":                                     {"profiles list", ""},
	"GET /api/v1/agent-profiles/{name}":                              {"profiles show p1", ""},
	"GET /api/v1/agent-stats":                                        {"agents performance", ""},
	"GET /api/v1/agents":                                             {"agents", ""},
	"GET /api/v1/agents/{id}":                                        {"agents show a1", ""},
	"GET /api/v1/agents/{id}/connect":                                {"join-snippet a1", ""},
	"GET /api/v1/agents/{id}/insights":                               {"insights a1", ""},
	"GET /api/v1/agents/{id}/output":                                 {"output tail a1", ""},
	"GET /api/v1/agents/{id}/stats":                                  {"agents performance a1", ""},
	"GET /api/v1/alerts":                                             {"alerts --status active", ""},
	"GET /api/v1/announcements":                                      {"announcements list", ""},
	"GET /api/v1/artifacts":                                          {"artifacts list", ""},
	"GET /api/v1/artifacts/{id}":                                     {"artifacts download A-1 --out {tmp}", ""},
	"GET /api/v1/audit":                                              {"audit list", ""},
	"GET /api/v1/audit/export":                                       {"audit export", ""},
	"GET /api/v1/about":                                              {"about", ""},
	"GET /api/v1/audit/verify":                                       {"audit verify", ""},
	"GET /api/v1/autopilot":                                          {"autopilot status", ""},
	"GET /api/v1/backups":                                            {"backups list", ""},
	"GET /api/v1/breakers":                                           {"breakers list", ""},
	"GET /api/v1/brief-templates":                                    {"briefs templates", ""},
	"GET /api/v1/brief-templates/{id}/render":                        {"briefs templates feature", ""},
	"GET /api/v1/briefs":                                             {"briefs", ""},
	"GET /api/v1/briefs/{id}/board":                                  {"board B-1", ""},
	"GET /api/v1/briefs/{id}/estimates":                              {"estimates brief B-1", ""},
	"GET /api/v1/briefs/{id}/forecast":                               {"forecast B-1", ""},
	"GET /api/v1/briefs/{id}/replay":                                 {"replay show B-1 --at 5", ""},
	"GET /api/v1/briefs/{id}/replay/diff":                            {"replay diff B-1 --from 1 --to 5", ""},
	"GET /api/v1/briefs/{id}/replay/timeline":                        {"replay timeline B-1", ""},
	"GET /api/v1/briefs/{id}/report":                                 {"report B-1", ""},
	"GET /api/v1/briefs/{id}/report.md":                              {"report B-1 --format md", ""},
	"GET /api/v1/capacity":                                           {"capacity get", ""},
	"GET /api/v1/charter":                                            {"charter get", ""},
	"GET /api/v1/conversations":                                      {"chat list", ""},
	"GET /api/v1/conversations/{id}":                                 {"chat show C-1", ""},
	"GET /api/v1/costs":                                              {"costs --group-by day", ""},
	"GET /api/v1/decisions":                                          {"decisions list", ""},
	"GET /api/v1/decisions/next":                                     {"decisions next", ""},
	"GET /api/v1/ha":                                                 {"ha status", ""},
	"GET /api/v1/digest/preview":                                     {"digest --window 24h", ""},
	"GET /api/v1/drills":                                             {"drills list", ""},
	"GET /api/v1/incident":                                           {"incident list", ""},
	"GET /api/v1/insights":                                           {"insights --severity high", ""},
	"GET /api/v1/instructions/effective":                             {"instructions get", ""},
	"GET /api/v1/instructions/{scope}/{kind}":                        {"instructions get --kind rules", ""},
	"GET /api/v1/knowledge":                                          {"knowledge list --q x", ""},
	"GET /api/v1/knowledge/{id}":                                     {"knowledge show K-1", ""},
	"GET /api/v1/limits":                                             {"spend-limits get", ""},
	"GET /api/v1/me":                                                 {"me", ""},
	"GET /api/v1/logs":                                               {"logs --level warn", ""},
	"GET /api/v1/mergequeue/candidates":                              {"mergequeue list", ""},
	"GET /api/v1/mergequeue/candidates/{id}":                         {"mergequeue show MQ-1", ""},
	"GET /api/v1/mergequeue/trains":                                  {"mergequeue trains", ""},
	"GET /api/v1/mergequeue/trains/{id}":                             {"mergequeue train MQT-1", ""},
	"GET /api/v1/notify/status":                                      {"notify status", ""},
	"GET /api/v1/openapi.json":                                       {"openapi", ""},
	"GET /api/v1/plans":                                              {"plans list", ""},
	"GET /api/v1/plans/{id}":                                         {"plans show P-1", ""},
	"GET /api/v1/plans/{id}/estimates":                               {"estimates plan P-1", ""},
	"GET /api/v1/playbooks":                                          {"playbooks list", ""},
	"GET /api/v1/playbooks/{name}":                                   {"playbooks show hotfix", ""},
	"GET /api/v1/prompts":                                            {"prompts list", ""},
	"GET /api/v1/prompts/{id}":                                       {"prompts show x", ""},
	"GET /api/v1/prompts/{id}/render":                                {"prompts render x", ""},
	"GET /api/v1/questions":                                          {"questions --status open", ""},
	"GET /api/v1/retention":                                          {"retention status", ""},
	"GET /api/v1/retention/verify":                                   {"retention verify", ""},
	"GET /api/v1/schedules":                                          {"schedules list", ""},
	"GET /api/v1/schedules/preview":                                  {"schedules preview --cron=*/5", ""},
	"GET /api/v1/schedules/{id}":                                     {"schedules show SC-1", ""},
	"GET /api/v1/schedules/{id}/runs":                                {"schedules runs SC-1", ""},
	"GET /api/v1/search/":                                            {"search foo", ""},
	"GET /api/v1/spawns":                                             {"spawns list", ""},
	"GET /api/v1/state":                                              {"state", ""},
	"GET /api/v1/task-templates":                                     {"task-templates", ""},
	"GET /api/v1/tasks":                                              {"tasks list", ""},
	"GET /api/v1/tasks/{id}":                                         {"tasks show T-1", ""},
	"GET /api/v1/tasks/{id}/gates":                                   {"tasks gates T-1", ""},
	"GET /api/v1/tasks/{id}/pr":                                      {"tasks pr T-1", ""},
	"GET /api/v1/tasks/{id}/scope":                                   {"tasks scope T-1", ""},
	"GET /api/v1/tokens":                                             {"tokens list", ""},
	"GET /api/v1/webhooks-out":                                       {"webhooks-out list", ""},
	"GET /api/v1/webhooks-out/{id}":                                  {"webhooks-out show W-1", ""},
	"GET /api/v1/webhooks-out/{id}/deliveries":                       {"webhooks-out deliveries W-1 --state dead", ""},
	"PATCH /api/v1/knowledge/{id}":                                   {"knowledge edit K-1 --title t", ""},
	"PATCH /api/v1/schedules/{id}":                                   {"schedules edit SC-1 --file -", `{"enabled":false}`},
	"PATCH /api/v1/tasks/{id}":                                       {"plans edit-task T-1 --title t", ""},
	"POST /api/v1/agent-profiles":                                    {"profiles create p1 --kind codex", ""},
	"POST /api/v1/agents":                                            {"register-agent a1 --role worker --kind codex --model m", ""},
	"POST /api/v1/agents/bulk":                                       {"register-agents --prefix w --count 2 --role worker --kind codex --model m", ""},
	"POST /api/v1/agents/quota-bulk":                                 {"quotas bulk a1 a2 --max-cost-eur-per-day 5 --max-tasks-per-day 0 --max-concurrent-tasks 1", ""},
	"POST /api/v1/agents/{id}/resume":                                {"agents bulk-action resume a1", ""},
	"POST /api/v1/agents/{id}/revoke":                                {"revoke-agent a1", ""},
	"POST /api/v1/agents/{id}/rotate-token":                          {"agents rotate-token a1", ""},
	"POST /api/v1/announcements":                                     {"announcements publish --text hello", ""},
	"POST /api/v1/artifacts":                                         {"artifacts upload --scope task:T-1 --file - --name n.txt", "data"},
	"POST /api/v1/autopilot/briefs/{id}":                             {"autopilot enable B-1", ""},
	"POST /api/v1/backup":                                            {"backups create", ""},
	"POST /api/v1/breakers/{provider}/reset":                         {"breakers reset anthropic", ""},
	"POST /api/v1/briefs":                                            {"create-brief --title T --body b", ""},
	"POST /api/v1/briefs/import":                                     {"briefs import --file -", "# x"},
	"POST /api/v1/decisions/{id}":                                    {"decisions act q:Q-1 --action skip", ""},
	"POST /api/v1/drills":                                            {"drills run", ""},
	"POST /api/v1/incident":                                          {"incident start --reason leak --actions pause_all", ""},
	"POST /api/v1/incident/{id}/end":                                 {"incident end INC-1", ""},
	"POST /api/v1/knowledge":                                         {"knowledge add --brief B-1 --kind decision --title t --body b", ""},
	"POST /api/v1/knowledge/{id}/approve":                            {"knowledge approve K-1", ""},
	"POST /api/v1/knowledge/{id}/pin":                                {"knowledge pin K-1", ""},
	"POST /api/v1/knowledge/{id}/supersede":                          {"knowledge supersede K-1 --by K-2", ""},
	"POST /api/v1/mergequeue/candidates":                             {"mergequeue enqueue T-1", ""},
	"POST /api/v1/messages":                                          {"chat send --to leader --body hi", ""},
	"POST /api/v1/notify/test":                                       {"notify test", ""},
	"POST /api/v1/pause-all":                                         {"pause-all", ""},
	"POST /api/v1/plans/{id}/approve":                                {"plans approve P-1", ""},
	"POST /api/v1/plans/{id}/reject":                                 {"plans reject P-1 --reason r", ""},
	"POST /api/v1/plans/{id}/tasks":                                  {"plans add-task P-1 --title t --spec s --repo o/r", ""},
	"POST /api/v1/playbooks":                                         {"playbooks create hotfix-x --coverage tester", ""},
	"POST /api/v1/playbooks/{name}/clone":                            {"playbooks clone hotfix hotfix-x", ""},
	"POST /api/v1/questions/{id}/answer":                             {"answer Q-1 --answer yes", ""},
	"POST /api/v1/reload":                                            {"reload", ""},
	"POST /api/v1/resume-all":                                        {"resume-all", ""},
	"POST /api/v1/retention/run":                                     {"retention run", ""},
	"POST /api/v1/retention/segments/{id}/restore":                   {"retention restore 3", ""},
	"POST /api/v1/schedules":                                         {"schedules create --file -", `{"name":"x"}`},
	"POST /api/v1/tasks/{id}/cancel":                                 {"tasks bulk cancel T-1", ""},
	"POST /api/v1/tasks/{id}/done":                                   {"tasks bulk done T-1", ""},
	"POST /api/v1/tasks/{id}/reopen":                                 {"tasks bulk reopen T-1 --status ready", ""},
	"POST /api/v1/tokens":                                            {"tokens create --name ci --scope viewer", ""},
	"POST /api/v1/tokens/{id}/revoke":                                {"tokens revoke K-1", ""},
	"POST /api/v1/webhooks-out":                                      {"webhooks-out create --target-url https://example.com/h --events task.updated", ""},
	"POST /api/v1/webhooks-out/{id}/deliveries/{delivery}/redeliver": {"webhooks-out redeliver W-1 D-1", ""},
	"PUT /api/v1/agent-profiles/{name}":                              {"profiles update p1 --kind codex", ""},
	"PUT /api/v1/agents/{id}/quota":                                  {"quotas set a1 --max-cost-eur-per-day 5 --max-tasks-per-day 0 --max-concurrent-tasks 1", ""},
	"PUT /api/v1/capacity":                                           {"capacity set --file -", `{}`},
	"PUT /api/v1/charter":                                            {"charter set --text rules", ""},
	"PUT /api/v1/instructions/{scope}/{kind}":                        {"instructions set --kind rules --text t", ""},
	"PUT /api/v1/playbooks/{name}":                                   {"playbooks update hotfix-x --description d", ""},
	"PUT /api/v1/limits":                                             {"spend-limits set --daily-eur 10 --weekly-eur 0", ""},
}

var exempt = map[string]string{
	"DELETE /api/v1/push/subscriptions":   "browser push subscriptions are created and removed by the browser itself",
	"GET /api/v1/push/subscriptions":      "browser push subscriptions are created and removed by the browser itself",
	"GET /api/v1/push/vapid-key":          "browser push subscriptions are created and removed by the browser itself",
	"POST /api/v1/push/subscriptions":     "browser push subscriptions are created and removed by the browser itself",
	"DELETE /api/v1/bots/{id}":            legacyBots,
	"GET /api/v1/bots":                    legacyBots,
	"GET /api/v1/bots/{id}":               legacyBots,
	"POST /api/v1/bots":                   legacyBots,
	"POST /api/v1/bots/bulk":              legacyBots,
	"POST /api/v1/bots/spawn":             legacyBots,
	"POST /api/v1/bots/{id}/kill":         legacyBots,
	"POST /api/v1/bots/{id}/pause":        legacyBots,
	"POST /api/v1/bots/{id}/resume":       legacyBots,
	"POST /api/v1/bots/{id}/revoke":       legacyBots,
	"POST /api/v1/bots/{id}/rotate-token": legacyBots,
	"GET /api/v1/events":                  rawStream,
	"GET /api/v1/metrics":                 scrapeTarget,
	"POST /api/v1/agents/bulk-action":     perItem,
	"POST /api/v1/tasks/bulk":             perItem,
	"POST /api/v1/spawns/{id}/stop":       "spawns stop kills the agent through /bots/{id}/kill",
}

var (
	docRoute    = regexp.MustCompile("^\\| ([A-Z/]+) \\| `(/api/v1/[^`?]*)[^`]*` \\|")
	routeParams = regexp.MustCompile(`\{[^}]*\}`)
)

func ownerRoutes(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	for _, op := range openapi.Routes() {
		if strings.HasPrefix(op.Path, "/api/v1/") {
			seen[op.Method+" "+op.Path] = true
		}
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "api.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if m := docRoute.FindStringSubmatch(line); m != nil {
			for _, method := range strings.Split(m[1], "/") {
				seen[method+" "+m[2]] = true
			}
		}
	}
	if len(seen) < 150 {
		t.Fatalf("found only %d owner routes", len(seen))
	}
	routes := make([]string, 0, len(seen))
	for route := range seen {
		routes = append(routes, route)
	}
	slices.Sort(routes)
	return routes
}

func TestEveryOwnerRouteHasACommandOrAnExemption(t *testing.T) {
	guardhint.Watch(t, guardhint.CLIParity)
	routes := ownerRoutes(t)
	for _, route := range routes {
		_, has := covered[route]
		reason, skipped := exempt[route]
		switch {
		case has && skipped:
			t.Errorf("%s is both covered and exempt", route)
		case !has && !skipped:
			t.Errorf("%s has no subspacectl command: add one to the covered table or exempt it with a reason", route)
		case skipped && strings.TrimSpace(reason) == "":
			t.Errorf("%s is exempt without a reason", route)
		}
	}
	for _, table := range []map[string]string{exempt, coveredNames()} {
		for route := range table {
			if !slices.Contains(routes, route) {
				t.Errorf("%s is listed but no such owner route exists", route)
			}
		}
	}
}

func coveredNames() map[string]string {
	out := make(map[string]string, len(covered))
	for route, u := range covered {
		out[route] = u.args
	}
	return out
}

type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *recorder) add(entry string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, entry)
}

func (r *recorder) hit(route string) bool {
	method, template, _ := strings.Cut(route, " ")
	parts := routeParams.Split(template, -1)
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	re := regexp.MustCompile("^" + method + " " + strings.Join(parts, "[^/]+") + "$")
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.ContainsFunc(r.seen, re.MatchString)
}

func TestCoveredCommandsCallTheirRoute(t *testing.T) {
	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r.Method + " " + r.URL.Path)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	env := mapEnv(map[string]string{"SUBSPACE_URL": server.URL, "SUBSPACE_OWNER_TOKEN": "token"})
	download := filepath.Join(t.TempDir(), "artifact")
	routes := make([]string, 0, len(covered))
	for route := range covered {
		routes = append(routes, route)
	}
	slices.Sort(routes)
	for _, route := range routes {
		u := covered[route]
		t.Run(route, func(t *testing.T) {
			args := strings.Fields(strings.ReplaceAll(u.args, "{tmp}", download))
			var out bytes.Buffer
			err := run(t.Context(), append([]string{"--json"}, args...), env, strings.NewReader(u.stdin), &out)
			if !rec.hit(route) {
				t.Fatalf("%q did not call %s (error: %v)", u.args, route, err)
			}
			if err == nil && out.Len() > 0 && !validJSONStream(out.Bytes()) {
				t.Fatalf("%q printed non-JSON under --json: %s", u.args, out.String())
			}
		})
	}
}

func validJSONStream(data []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var v any
		if dec.Decode(&v) != nil {
			return false
		}
	}
	return true
}
