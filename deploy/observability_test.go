package deploy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/metrics"
)

var (
	subspaceMetric = regexp.MustCompile(`subspace_[a-z0-9_]+`)
	typeLine       = regexp.MustCompile(`(?m)^# TYPE (\S+) (\S+)$`)
	exprLine       = regexp.MustCompile(`(?m)^\s*expr:\s*(.+)$`)
)

func metricFamilies(t *testing.T) map[string]string {
	t.Helper()
	srv := httptest.NewServer(metrics.New().Registry.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	fams := map[string]string{}
	for _, m := range typeLine.FindAllStringSubmatch(string(body), -1) {
		fams[m[1]] = m[2]
	}
	if len(fams) == 0 {
		t.Fatal("metrics exposition has no families")
	}
	return fams
}

func knownMetric(name string, fams map[string]string) bool {
	if _, ok := fams[name]; ok {
		return true
	}
	for _, suffix := range []string{"_bucket", "_sum", "_count"} {
		if base, ok := strings.CutSuffix(name, suffix); ok && fams[base] == "histogram" {
			return true
		}
	}
	return false
}

func checkMetricRefs(t *testing.T, file, expr string, fams map[string]string) {
	t.Helper()
	for _, ref := range subspaceMetric.FindAllString(expr, -1) {
		if !knownMetric(ref, fams) {
			t.Errorf("%s: %s is not exported by internal/metrics", file, ref)
		}
	}
}

type dashboard struct {
	UID    string `json:"uid"`
	Title  string `json:"title"`
	Panels []struct {
		Title   string `json:"title"`
		Type    string `json:"type"`
		Targets []struct {
			Expr string `json:"expr"`
		} `json:"targets"`
	} `json:"panels"`
}

func TestGrafanaDashboard(t *testing.T) {
	const file = "deploy/observability/grafana-dashboard.json"
	var d dashboard
	if err := json.Unmarshal([]byte(readRepoFile(t, file)), &d); err != nil {
		t.Fatal(err)
	}
	if d.UID == "" || d.Title == "" {
		t.Error("dashboard needs a uid and a title")
	}
	if len(d.Panels) == 0 {
		t.Fatal("dashboard has no panels")
	}
	fams := metricFamilies(t)
	var refs strings.Builder
	for _, p := range d.Panels {
		if p.Title == "" || p.Type == "" || len(p.Targets) == 0 {
			t.Errorf("panel %q is incomplete", p.Title)
		}
		for _, tg := range p.Targets {
			if tg.Expr == "" {
				t.Errorf("panel %q has an empty expr", p.Title)
			}
			checkMetricRefs(t, file, tg.Expr, fams)
			refs.WriteString(tg.Expr)
		}
	}
	for _, want := range []string{
		"subspace_tasks", "subspace_agents", "subspace_mcp_tool_calls_total",
		"subspace_mcp_tool_call_duration_seconds_bucket", "subspace_http_requests_total",
		"subspace_sse_subscribers", "subspace_sse_dropped_events_total",
		"subspace_leases_expired_total", "subspace_backup_last_timestamp_seconds",
	} {
		if !strings.Contains(refs.String(), want) {
			t.Errorf("dashboard does not chart %s", want)
		}
	}
}

func TestPrometheusAlertRules(t *testing.T) {
	const file = "deploy/observability/alerts.yml"
	src := readRepoFile(t, file)
	exprs := exprLine.FindAllStringSubmatch(src, -1)
	if len(exprs) == 0 {
		t.Fatal("no alert expressions found")
	}
	if got := strings.Count(src, "- alert: "); got != len(exprs) {
		t.Errorf("%d alerts but %d expressions", got, len(exprs))
	}
	fams := metricFamilies(t)
	for _, m := range exprs {
		checkMetricRefs(t, file, m[1], fams)
	}
	for _, want := range []string{"probe_success{job=\"subspace-healthz\"}", "subspace_backup_last_timestamp_seconds"} {
		if !strings.Contains(src, want) {
			t.Errorf("alerts do not reference %s", want)
		}
	}
}

func TestPrometheusScrapeNoInlineToken(t *testing.T) {
	const file = "deploy/observability/prometheus.yml"
	src := readRepoFile(t, file)
	if !strings.Contains(src, "bearer_token_file:") {
		t.Error("owner metrics scrape must read its token from bearer_token_file")
	}
	inline := regexp.MustCompile(`(?m)^\s*(-\s*)?(bearer_token|credentials|password)\s*:`)
	if inline.MatchString(src) {
		t.Error("prometheus.yml carries an inline credential")
	}
	if !strings.Contains(src, "/api/v1/metrics") {
		t.Error("scrape job must target /api/v1/metrics")
	}
}

func TestPrometheusRulesWithPromtool(t *testing.T) {
	bin, err := exec.LookPath("promtool")
	if err != nil {
		t.Skip("promtool not available")
	}
	if out, err := exec.Command(bin, "check", "rules", "observability/alerts.yml").CombinedOutput(); err != nil {
		t.Errorf("promtool check rules: %v\n%s", err, out)
	}
}
