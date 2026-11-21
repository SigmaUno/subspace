package main

import (
	"math"
	"net/http"
	"strings"
	"time"
)

func runawayAgent(r *Run) error {
	st, err := r.Opt.NewStack()
	if err != nil {
		return err
	}
	defer st.Close()
	c := st.Client()
	w, err := c.Seed(r.Ctx, 2)
	if err != nil {
		return err
	}
	defer w.Cleanup()
	a, b := w.Workers[0], w.Workers[1]
	args := map[string]any{"task_id": w.Tasks[0], "body": "same note again"}

	start := time.Now()
	var accepted int
	var code string
	for range 6 {
		_, code, err = c.Tool(r.Ctx, a.Token, "add_note", args)
		if err != nil {
			return err
		}
		if code != "" {
			break
		}
		accepted++
	}
	if !r.Expect("identical calls accepted up to the limit", accepted == 5, "%d accepted", accepted) {
		return nil
	}
	r.Expect("sixth identical call refused", code == "agent_paused", "code %q", code)
	if !r.Detected(time.Since(start)) {
		return nil
	}
	got, err := c.Must(r.Ctx, http.MethodGet, "/agents/"+a.ID, nil, http.StatusOK)
	if err != nil {
		return err
	}
	r.Expect("agent paused for looping", str(got, "status") == "paused" && strings.HasPrefix(str(got, "pause_reason"), "loop"), "status %s, reason %q", str(got, "status"), str(got, "pause_reason"))
	msgs, err := c.Must(r.Ctx, http.MethodGet, "/threads/agent/"+a.ID+"/messages?limit=1000", nil, http.StatusOK)
	if err != nil {
		return err
	}
	var told bool
	for _, m := range list(msgs, "messages") {
		told = told || str(m, "kind") == "system"
	}
	r.Expect("system message in the agent thread", told, "owner can see why")
	_, other, err := c.Tool(r.Ctx, b.Token, "add_note", map[string]any{"task_id": w.Tasks[1], "body": args["body"]})
	if err != nil {
		return err
	}
	r.Expect("other agents unaffected", other == "", "code %q", other)

	resumed := time.Now()
	if _, err := c.Must(r.Ctx, http.MethodPost, "/agents/"+a.ID+"/resume", map[string]any{}, http.StatusOK); err != nil {
		return err
	}
	_, after, err := c.Tool(r.Ctx, a.Token, "add_note", map[string]any{"task_id": w.Tasks[0], "body": "back to normal"})
	if err != nil {
		return err
	}
	if r.Expect("resumed agent works", after == "", "code %q", after) {
		r.Recovered(time.Since(resumed))
	}
	return nil
}

func leakedToken(r *Run) error {
	st, err := r.Opt.NewStack()
	if err != nil {
		return err
	}
	defer st.Close()
	c := st.Client()
	w, err := c.Seed(r.Ctx, 2)
	if err != nil {
		return err
	}
	defer w.Cleanup()
	victim, writer := w.Workers[0], w.Workers[1]

	start := time.Now()
	if _, err := c.MustTool(r.Ctx, writer.Token, "post_message", map[string]any{"thread": "task:" + w.Tasks[1], "body": "debug dump " + victim.Token}); err != nil {
		return err
	}
	_, ok := r.Until(r.Opt.Detect, func() (bool, error) {
		_, code, err := c.Tool(r.Ctx, victim.Token, "whoami", nil)
		return err == nil && code == "http_401", nil
	})
	if !r.Expect("leaked token rejected", ok, "whoami with the leaked token fails") || !r.Detected(time.Since(start)) {
		return nil
	}
	got, err := c.Must(r.Ctx, http.MethodGet, "/agents/"+victim.ID, nil, http.StatusOK)
	if err != nil {
		return err
	}
	r.Expect("agent revoked with a leak reason", str(got, "status") == "revoked" && strings.HasPrefix(str(got, "pause_reason"), "token leaked"), "status %s, reason %q", str(got, "status"), str(got, "pause_reason"))
	msgs, err := c.Must(r.Ctx, http.MethodGet, "/threads/task/"+w.Tasks[1]+"/messages?limit=1000", nil, http.StatusOK)
	if err != nil {
		return err
	}
	var stored, redacted bool
	for _, m := range list(msgs, "messages") {
		body := str(m, "body")
		stored = stored || strings.Contains(body, victim.Token)
		redacted = redacted || strings.Contains(body, "[redacted:token]")
	}
	r.Expect("token never stored", !stored && redacted, "stored=%t redacted=%t", stored, redacted)
	_, wcode, err := c.Tool(r.Ctx, writer.Token, "whoami", nil)
	if err != nil {
		return err
	}
	r.Expect("author keeps working", wcode == "", "code %q", wcode)
	leak := leakDecision(r, c, victim.ID)
	if !r.Expect("owner decision raised", leak != "", "leak decision for %s", victim.ID) {
		return nil
	}

	reissued := time.Now()
	out, err := c.Must(r.Ctx, http.MethodPost, "/token-leaks/"+leak+"/reissue", map[string]any{}, http.StatusOK)
	if err != nil {
		return err
	}
	fresh := str(out, "connect", "token")
	_, code, err := c.Tool(r.Ctx, fresh, "whoami", nil)
	if err != nil {
		return err
	}
	if r.Expect("reissued token works", fresh != "" && code == "", "code %q", code) {
		r.Recovered(time.Since(reissued))
	}
	_, old, err := c.Tool(r.Ctx, victim.Token, "whoami", nil)
	if err != nil {
		return err
	}
	r.Expect("leaked token stays dead", old == "http_401", "code %q", old)
	return nil
}

func leakDecision(r *Run, c *Client, agent string) string {
	var id string
	r.Until(r.Opt.Detect, func() (bool, error) {
		out, err := c.Must(r.Ctx, http.MethodGet, "/decisions?limit=1000", nil, http.StatusOK)
		if err != nil {
			return false, err
		}
		for _, d := range list(out, "decisions") {
			if str(d, "kind") == "token_leak" && str(d, "agent_id") == agent && strings.HasPrefix(str(d, "id"), "leak:") {
				id = strings.TrimPrefix(str(d, "id"), "leak:")
			}
		}
		return id != "", nil
	})
	return id
}

func budgetExhaustion(r *Run) error {
	st, err := r.Opt.NewStack()
	if err != nil {
		return err
	}
	defer st.Close()
	c := st.Client()
	w, err := c.Seed(r.Ctx, 2)
	if err != nil {
		return err
	}
	defer w.Cleanup()
	prev, err := c.Must(r.Ctx, http.MethodGet, "/limits", nil, http.StatusOK)
	if err != nil {
		return err
	}
	var spent float64
	for _, win := range list(prev, "windows") {
		if str(win, "window") == "daily" {
			spent = num(win, "spent_eur")
		}
	}
	restore := func() {
		_, _, _ = c.REST(r.Ctx, http.MethodPut, "/limits", map[string]any{"daily_cost_eur": num(prev, "daily_cost_eur"), "weekly_cost_eur": num(prev, "weekly_cost_eur")})
		if _, state, err := c.REST(r.Ctx, http.MethodGet, "/state", nil); err == nil && state["pause_reason"] == "org_budget" {
			_, _, _ = c.REST(r.Ctx, http.MethodPost, "/resume-all", map[string]any{})
		}
	}
	defer restore()

	if _, err := c.MustTool(r.Ctx, w.Workers[0].Token, "claim_task", map[string]any{"task_id": w.Tasks[0]}); err != nil {
		return err
	}
	cap := math.Ceil(spent) + 5
	if _, err := c.Must(r.Ctx, http.MethodPut, "/limits", map[string]any{"daily_cost_eur": cap, "weekly_cost_eur": num(prev, "weekly_cost_eur")}, http.StatusOK); err != nil {
		return err
	}
	start := time.Now()
	if _, err := c.MustTool(r.Ctx, w.Workers[0].Token, "update_status", map[string]any{"task_id": w.Tasks[0], "status": "in_progress", "cost_eur": cap - spent + 1}); err != nil {
		return err
	}
	_, ok := r.Until(r.Opt.Detect, func() (bool, error) {
		out, err := c.Must(r.Ctx, http.MethodGet, "/state", nil, http.StatusOK)
		return err == nil && out["paused"] == true && out["pause_reason"] == "org_budget", err
	})
	if !r.Expect("spend guard paused the organisation", ok, "pause_reason org_budget") || !r.Detected(time.Since(start)) {
		return nil
	}
	_, code, err := c.Tool(r.Ctx, w.Workers[1].Token, "claim_task", map[string]any{"task_id": w.Tasks[1]})
	if err != nil {
		return err
	}
	r.Expect("claims refused while paused", code == "paused", "code %q", code)
	dec, err := c.Must(r.Ctx, http.MethodGet, "/decisions?limit=1000", nil, http.StatusOK)
	if err != nil {
		return err
	}
	var raised bool
	for _, x := range list(dec, "decisions") {
		raised = raised || str(x, "id") == "budget:org-daily"
	}
	r.Expect("budget decision raised", raised, "budget:org-daily")
	task, err := w.Task(r.Ctx, 0)
	if err != nil {
		return err
	}
	r.Expect("spend recorded", num(task, "cost_used") >= cap-spent, "cost_used %v", task["cost_used"])

	raisedAt := time.Now()
	if _, err := c.Must(r.Ctx, http.MethodPut, "/limits", map[string]any{"daily_cost_eur": cap * 100, "weekly_cost_eur": num(prev, "weekly_cost_eur") * 100}, http.StatusOK); err != nil {
		return err
	}
	_, ok = r.Until(r.Opt.Recover, func() (bool, error) {
		_, code, err := c.Tool(r.Ctx, w.Workers[1].Token, "claim_task", map[string]any{"task_id": w.Tasks[1]})
		return err == nil && code == "", nil
	})
	if r.Expect("claims work after the cap was raised", ok, "pause lifted") {
		r.Recovered(time.Since(raisedAt))
	}
	return nil
}
