package server

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestActivityFeedRedactsAuditFieldValues(t *testing.T) {
	t.Setenv("SUBSPACE_DATA_DIR", t.TempDir())
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	bid := e.brief(map[string]any{})
	ids := e.approvedPlan(lead, bid, planTask("a", nil))
	e.must(lead, "assign", map[string]any{"task_id": ids[0], "agent_id": "w1"})

	secret := "ghp_" + strings.Repeat("A1b2C3d4E5", 4)
	e.must(wk, "put_artifact", map[string]any{
		"scope": "task:" + ids[0], "name": secret + ".log",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("x")),
	})

	out := e.restWant(200, "GET", "/api/v1/activity?limit=1000", nil)
	raw := toJSON(out)
	if !strings.Contains(raw, "uploaded artifact") {
		t.Fatalf("no artifact activity: %s", raw)
	}
	if strings.Contains(raw, secret) {
		t.Fatalf("activity feed leaks secret: %s", raw)
	}
}
