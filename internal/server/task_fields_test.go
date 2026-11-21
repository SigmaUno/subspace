package server_test

import (
	"slices"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
)

func TestPlanPriorityAndComponentReachBoardAndArchitecture(t *testing.T) {
	e := servertest.New(t)
	lead := e.Register("rex", "leader", "other")
	_, br := e.REST("POST", "/api/v1/briefs", map[string]any{"title": "B", "repos": []string{"o/r"}})
	bid := br["id"].(string)

	task := func(title, comp string, prio int) map[string]any {
		return map[string]any{"title": title, "spec": "s", "repo": "o/r", "priority": prio, "component_id": comp}
	}
	plan := e.Must(lead, "propose_plan", map[string]any{"brief_id": bid, "tasks": []any{
		task("low", "C-1", 0), task("urgent", "C-2", 9), task("mid", "C-1", 4),
	}})["plan"].(map[string]any)
	for _, tk := range plan["tasks"].([]any) {
		m := tk.(map[string]any)
		if m["component_id"] == nil {
			t.Fatalf("plan task %v lost component_id", m["id"])
		}
	}

	code, _ := e.REST("PUT", "/api/v1/briefs/"+bid+"/components", map[string]any{"components": []any{
		map[string]any{"name": "api"}, map[string]any{"name": "web", "depends_on": []string{"api"}},
	}})
	if code != 200 {
		t.Fatalf("components: %d", code)
	}
	if code, _ := e.REST("POST", "/api/v1/plans/"+plan["id"].(string)+"/approve", map[string]any{}); code != 200 {
		t.Fatalf("approve: %d", code)
	}

	extra := e.Must(lead, "create_task", map[string]any{"brief_id": bid, "title": "extra", "spec": "s", "repo": "o/r",
		"priority": 6, "component_id": "C-2"})["task"].(map[string]any)
	if extra["priority"] != float64(6) || extra["component_id"] != "C-2" {
		t.Fatalf("create_task fields: %v", extra)
	}

	_, board := e.REST("GET", "/api/v1/briefs/"+bid+"/board", nil)
	var titles []string
	for _, tk := range board["columns"].(map[string]any)["ready"].([]any) {
		titles = append(titles, tk.(map[string]any)["title"].(string))
	}
	if want := []string{"urgent", "extra", "mid", "low"}; !slices.Equal(titles, want) {
		t.Fatalf("ready order %v want %v", titles, want)
	}

	_, arch := e.REST("GET", "/api/v1/briefs/"+bid+"/architecture", nil)
	totals := map[string]float64{}
	for _, n := range arch["nodes"].([]any) {
		m := n.(map[string]any)
		totals[m["id"].(string)] = m["status"].(map[string]any)["total"].(float64)
	}
	if totals["C-1"] != 2 || totals["C-2"] != 2 {
		t.Fatalf("tasks per component: %v", totals)
	}
}
