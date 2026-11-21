package policy

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	ActAssign       = "assign"
	ActClaim        = "claim_task"
	ActSubmit       = "submit"
	ActReview       = "review"
	ActSpawn        = "request_spawn"
	ActCreateTask   = "create_task"
	ActUpdateTask   = "update_task"
	ActApprovePlan  = "approve_plan"
	ActMergeReady   = "merge_ready"
	maxFactString   = 1024
	maxScannedTasks = 100000
)

var Actions = []string{ActAssign, ActClaim, ActSubmit, ActReview, ActSpawn, ActCreateTask, ActUpdateTask, ActApprovePlan, ActMergeReady}

var taskActions = []string{ActAssign, ActClaim, ActSubmit, ActReview, ActCreateTask, ActUpdateTask, ActMergeReady}

type FactInfo struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Actions     []string `json:"actions"`

	typ Type
}

func fact(name string, t Type, desc string, actions ...string) FactInfo {
	if actions == nil {
		actions = Actions
	}
	return FactInfo{Name: name, Type: t.String(), Description: desc, Actions: actions, typ: t}
}

var factList = []FactInfo{
	fact("action", TStr, "the action being evaluated"),
	fact("actor.id", TStr, "agent id of the caller, owner for owner actions"),
	fact("actor.role", TStr, "role of the caller: owner, leader, worker, tester or reviewer"),
	fact("actor.kind", TStr, "agent kind of the caller"),
	fact("actor.provider", TStr, "model provider of the caller"),
	fact("actor.model", TStr, "model of the caller"),
	fact("subject.id", TStr, "agent that will do the work: the target of assign, otherwise the caller"),
	fact("subject.provider", TStr, "provider of the subject agent; the spawned provider for request_spawn"),
	fact("target.id", TStr, "agent being assigned", ActAssign),
	fact("target.role", TStr, "role of the agent being assigned", ActAssign),
	fact("target.kind", TStr, "kind of the agent being assigned", ActAssign),
	fact("target.provider", TStr, "provider of the agent being assigned", ActAssign),
	fact("target.model", TStr, "model of the agent being assigned", ActAssign),
	fact("target.tester_tasks", TNum, "open tester tasks assigned to the target agent", ActAssign),
	fact("request.as", TStr, "assignee or reviewer", ActAssign),
	fact("task.id", TStr, "task id", taskActions...),
	fact("task.title", TStr, "task title", taskActions...),
	fact("task.repo", TStr, "task repository owner/name", taskActions...),
	fact("task.paths", TStrList, "path globs the task touches", taskActions...),
	fact("task.component", TStr, "component id of the task", taskActions...),
	fact("task.priority", TNum, "task priority", taskActions...),
	fact("task.labels", TStrList, "labels from #tag words and [tag] markers in the task title and spec", taskActions...),
	fact("task.role", TStr, "worker or tester", taskActions...),
	fact("task.status", TStr, "task status before the action", taskActions...),
	fact("task.has_tester", TBool, "a live tester task depends on this task, or it is a tester task", taskActions...),
	fact("brief.id", TStr, "brief id"),
	fact("brief.title", TStr, "brief title"),
	fact("brief.playbook", TStr, "playbook of the brief"),
	fact("brief.repos", TStrList, "repositories of the brief"),
	fact("plan.id", TStr, "plan id", ActApprovePlan),
	fact("plan.tasks", TNum, "number of tasks in the plan", ActApprovePlan),
	fact("spawn.kind", TStr, "requested worker kind", ActSpawn),
	fact("spawn.provider", TStr, "requested provider", ActSpawn),
	fact("spawn.model", TStr, "requested model", ActSpawn),
	fact("spawn.role", TStr, "requested role", ActSpawn),
	fact("spawn.n", TNum, "number of agents requested", ActSpawn),
	fact("time.hour", TNum, "hour 0-23 in the policy timezone"),
	fact("time.minute", TNum, "minute 0-59 in the policy timezone"),
	fact("time.weekday", TStr, "mon, tue, wed, thu, fri, sat or sun in the policy timezone"),
	fact("time.date", TStr, "YYYY-MM-DD in the policy timezone"),
	fact("cost.task_used", TNum, "EUR used by the task", taskActions...),
	fact("cost.task_budget", TNum, "task max_cost_eur, 0 = none", taskActions...),
	fact("cost.task_minutes_used", TNum, "minutes used by the task", taskActions...),
	fact("cost.task_minutes_budget", TNum, "task max_minutes, 0 = none", taskActions...),
	fact("cost.brief_used", TNum, "EUR used by the brief"),
	fact("cost.brief_budget", TNum, "brief max_cost_eur, 0 = none"),
	fact("cost.component_used", TNum, "EUR used by tasks of the same brief and component", taskActions...),
	fact("count.in_progress_agent", TNum, "in_progress tasks held by the subject agent"),
	fact("count.in_progress_provider", TNum, "in_progress tasks held by agents of the subject provider"),
	fact("count.in_progress_brief", TNum, "in_progress tasks of the brief"),
	fact("count.in_progress_total", TNum, "in_progress tasks overall"),
}

var factIndex = func() map[string]FactInfo {
	m := make(map[string]FactInfo, len(factList))
	for _, f := range factList {
		m[f.Name] = f
	}
	return m
}()

func FactList() []FactInfo { return factList }

func lookupFact(name string) (Type, bool) {
	f, ok := factIndex[name]
	return f.typ, ok
}

type Spawn struct {
	Kind     string `json:"worker_kind"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Role     string `json:"role"`
	N        int    `json:"n"`
}

type Request struct {
	Action string
	Actor  model.Actor
	Task   *model.Task
	Brief  *model.Brief
	Plan   *model.Plan
	Target *model.Agent
	As     string
	Spawn  *Spawn
	Args   map[string]string
	Now    time.Time
}

func (r *Request) ref() string {
	switch {
	case r.Action == ActCreateTask && r.Task != nil:
		return r.Task.BriefID + ":" + r.Task.Title
	case r.Task != nil:
		return r.Task.ID
	case r.Plan != nil:
		return r.Plan.ID
	case r.Brief != nil:
		return r.Brief.ID
	}
	return ""
}

func (r *Request) target() string {
	switch {
	case r.Target != nil:
		return r.Target.ID
	case r.Spawn != nil:
		return r.Spawn.Kind + ":" + r.Spawn.Provider + ":" + r.Spawn.Model
	}
	return ""
}

type resolver struct {
	tx    store.Tx
	req   *Request
	cache map[string]Value
	actor *model.Agent
	err   error

	inProgress []*model.Task
	providers  map[string]string
	scanned    bool
}

func newResolver(tx store.Tx, req *Request) *resolver {
	return &resolver{tx: tx, req: req, cache: map[string]Value{}}
}

func (r *resolver) fail(err error) {
	if err != nil && r.err == nil {
		r.err = err
	}
}

func (r *resolver) actorAgent() *model.Agent {
	if r.actor != nil || r.req.Actor.Role == model.RoleOwner || r.tx == nil {
		return r.actor
	}
	a, err := r.tx.GetAgent(r.req.Actor.ID)
	if err != nil {
		r.actor = &model.Agent{ID: r.req.Actor.ID, Role: r.req.Actor.Role}
		return r.actor
	}
	r.actor = a
	return a
}

func (r *resolver) subject() (id, provider string) {
	switch {
	case r.req.Target != nil:
		return r.req.Target.ID, r.req.Target.Provider
	case r.req.Spawn != nil:
		return "", r.req.Spawn.Provider
	}
	if a := r.actorAgent(); a != nil {
		return a.ID, a.Provider
	}
	return r.req.Actor.ID, ""
}

func (r *resolver) scan() {
	if r.scanned || r.tx == nil {
		return
	}
	r.scanned = true
	ts, err := r.tx.ListTasks(store.TaskFilter{Status: []string{model.StatusInProgress}, Limit: maxScannedTasks})
	if err != nil {
		r.fail(err)
		return
	}
	r.inProgress = ts
	r.providers = map[string]string{}
	for _, t := range ts {
		if t.Assignee == nil {
			continue
		}
		if _, ok := r.providers[*t.Assignee]; ok {
			continue
		}
		a, err := r.tx.GetAgent(*t.Assignee)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			r.fail(err)
			return
		}
		r.providers[a.ID] = a.Provider
	}
}

func (r *resolver) countInProgress(match func(t *model.Task) bool) Value {
	r.scan()
	n := 0
	for _, t := range r.inProgress {
		if match(t) {
			n++
		}
	}
	return Num(float64(n))
}

func (r *resolver) Fact(name string) Value {
	if v, ok := r.cache[name]; ok {
		return v
	}
	v := r.resolve(name)
	if v.t == TStr && len(v.s) > maxFactString {
		v.s = v.s[:maxFactString]
	}
	r.cache[name] = v
	return v
}

func (r *resolver) resolve(name string) Value {
	req := r.req
	group, field, _ := strings.Cut(name, ".")
	switch group {
	case "action":
		return Str(req.Action)
	case "actor":
		a := r.actorAgent()
		switch field {
		case "id":
			return Str(req.Actor.ID)
		case "role":
			return Str(req.Actor.Role)
		}
		if a == nil {
			return Str("")
		}
		return agentField(a, field)
	case "subject":
		id, provider := r.subject()
		if field == "id" {
			return Str(id)
		}
		return Str(provider)
	case "target":
		if req.Target == nil {
			return Zero(factIndex[name].typ)
		}
		if field == "tester_tasks" {
			return r.testerTasks(req.Target.ID)
		}
		return agentField(req.Target, field)
	case "request":
		return Str(req.As)
	case "task":
		if req.Task == nil {
			return Zero(factIndex[name].typ)
		}
		return r.taskFact(field)
	case "brief":
		br := req.Brief
		if br == nil {
			return Zero(factIndex[name].typ)
		}
		switch field {
		case "id":
			return Str(br.ID)
		case "title":
			return Str(br.Title)
		case "playbook":
			return Str(br.Playbook)
		case "repos":
			return Strs(br.Repos)
		}
	case "plan":
		if req.Plan == nil {
			return Zero(factIndex[name].typ)
		}
		if field == "id" {
			return Str(req.Plan.ID)
		}
		return Num(float64(len(req.Plan.Tasks)))
	case "spawn":
		sp := req.Spawn
		if sp == nil {
			return Zero(factIndex[name].typ)
		}
		switch field {
		case "kind":
			return Str(sp.Kind)
		case "provider":
			return Str(sp.Provider)
		case "model":
			return Str(sp.Model)
		case "role":
			return Str(sp.Role)
		case "n":
			return Num(float64(sp.N))
		}
	case "cost":
		return r.costFact(field)
	case "count":
		return r.countFact(field)
	}
	return Zero(factIndex[name].typ)
}

func agentField(a *model.Agent, field string) Value {
	switch field {
	case "id":
		return Str(a.ID)
	case "role":
		return Str(a.Role)
	case "kind":
		return Str(a.Kind)
	case "provider":
		return Str(a.Provider)
	case "model":
		return Str(a.Model)
	}
	return Str("")
}

func (r *resolver) testerTasks(agentID string) Value {
	if r.tx == nil {
		return Num(0)
	}
	ts, err := r.tx.ListTasks(store.TaskFilter{Assignee: agentID, Role: model.RoleTester, Limit: maxScannedTasks})
	if err != nil {
		r.fail(err)
		return Num(0)
	}
	n := 0
	for _, t := range ts {
		if t.Role == model.RoleTester && t.Status != model.StatusDone && t.Status != model.StatusCancelled {
			n++
		}
	}
	return Num(float64(n))
}

func (r *resolver) taskFact(field string) Value {
	t := r.req.Task
	switch field {
	case "id":
		return Str(t.ID)
	case "title":
		return Str(t.Title)
	case "repo":
		return Str(t.Repo)
	case "paths":
		return Strs(t.Paths)
	case "component":
		if t.ComponentID == nil {
			return Str("")
		}
		return Str(*t.ComponentID)
	case "priority":
		return Num(float64(t.Priority))
	case "labels":
		return Strs(Labels(t.Title, t.Spec))
	case "role":
		return Str(t.Role)
	case "status":
		return Str(t.Status)
	case "has_tester":
		return Bool(r.hasTester(t))
	}
	return Str("")
}

func (r *resolver) briefTasks() []*model.Task {
	if r.tx == nil || r.req.Task == nil {
		return nil
	}
	ts, err := r.tx.ListTasks(store.TaskFilter{BriefID: r.req.Task.BriefID, Limit: maxScannedTasks})
	r.fail(err)
	return ts
}

func (r *resolver) hasTester(t *model.Task) bool {
	if t.Role == model.RoleTester {
		return true
	}
	for _, o := range r.briefTasks() {
		if o.Role == model.RoleTester && o.Status != model.StatusCancelled && containsStr(o.DependsOn, t.ID) {
			return true
		}
	}
	return false
}

func (r *resolver) costFact(field string) Value {
	t, br := r.req.Task, r.req.Brief
	switch field {
	case "brief_used":
		if br == nil || r.tx == nil {
			return Num(0)
		}
		c, err := r.tx.BriefCost(br.ID)
		r.fail(err)
		return Num(c)
	case "brief_budget":
		if br == nil {
			return Num(0)
		}
		return Num(br.Budget.MaxCostEUR)
	}
	if t == nil {
		return Num(0)
	}
	switch field {
	case "task_used":
		return Num(t.CostUsed)
	case "task_budget":
		return Num(t.Budget.MaxCostEUR)
	case "task_minutes_used":
		return Num(float64(t.MinutesUsed))
	case "task_minutes_budget":
		return Num(float64(t.Budget.MaxMinutes))
	case "component_used":
		comp := ""
		if t.ComponentID != nil {
			comp = *t.ComponentID
		}
		if comp == "" {
			return Num(t.CostUsed)
		}
		sum, self := 0.0, false
		for _, o := range r.briefTasks() {
			if o.ComponentID != nil && *o.ComponentID == comp {
				if o.ID == t.ID {
					self = true
					sum += t.CostUsed
					continue
				}
				sum += o.CostUsed
			}
		}
		if !self {
			sum += t.CostUsed
		}
		return Num(sum)
	}
	return Num(0)
}

func (r *resolver) countFact(field string) Value {
	switch field {
	case "in_progress_agent":
		id, _ := r.subject()
		return r.countInProgress(func(t *model.Task) bool { return id != "" && t.Assignee != nil && *t.Assignee == id })
	case "in_progress_provider":
		_, p := r.subject()
		return r.countInProgress(func(t *model.Task) bool {
			return p != "" && t.Assignee != nil && r.providers[*t.Assignee] == p
		})
	case "in_progress_brief":
		if r.req.Brief == nil {
			return Num(0)
		}
		id := r.req.Brief.ID
		return r.countInProgress(func(t *model.Task) bool { return t.BriefID == id })
	case "in_progress_total":
		return r.countInProgress(func(*model.Task) bool { return true })
	}
	return Num(0)
}

var weekdays = [...]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

type located struct {
	base      Facts
	loc       *time.Location
	now       time.Time
	overrides map[string]Value
}

func (l located) Fact(name string) Value {
	if v, ok := l.overrides[name]; ok {
		return v
	}
	t := l.now.In(l.loc)
	switch name {
	case "time.hour":
		return Num(float64(t.Hour()))
	case "time.minute":
		return Num(float64(t.Minute()))
	case "time.weekday":
		return Str(weekdays[t.Weekday()])
	case "time.date":
		return Str(t.Format(time.DateOnly))
	}
	return l.base.Fact(name)
}

var labelRe = regexp.MustCompile(`(?:^|[\s(])#([A-Za-z0-9][A-Za-z0-9_-]{0,40})|\[([A-Za-z0-9][A-Za-z0-9_ -]{0,40})\]`)

func Labels(title, spec string) []string {
	out := []string{}
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" && !containsStr(out, s) && len(out) < MaxListLength {
			out = append(out, s)
		}
	}
	for _, m := range labelRe.FindAllStringSubmatch(title, -1) {
		add(m[1] + m[2])
	}
	if len(spec) > 4096 {
		spec = spec[:4096]
	}
	for _, m := range labelRe.FindAllStringSubmatch(spec, -1) {
		if m[1] != "" {
			add(m[1])
		}
	}
	return out
}
