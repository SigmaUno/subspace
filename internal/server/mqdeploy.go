package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/decisions"
	"github.com/SigmaUno/subspace/internal/events"
	"github.com/SigmaUno/subspace/internal/mergequeue"
	"github.com/SigmaUno/subspace/internal/model"
)

const (
	evTrainDeployed     = "train.deployed"
	evTrainDeployFailed = "train.deploy_failed"
)

type deployTask struct {
	ID string `json:"id"`
}

type deployEvent struct {
	ID      string       `json:"id"`
	Kind    string       `json:"kind"`
	Status  string       `json:"status"`
	Title   string       `json:"title"`
	TaskID  string       `json:"task_id,omitempty"`
	Tasks   []deployTask `json:"tasks"`
	Repo    string       `json:"repo"`
	Base    string       `json:"base"`
	SHA     string       `json:"sha"`
	TrainID string       `json:"train_id,omitempty"`
	PRs     []int        `json:"prs"`
	Log     string       `json:"log,omitempty"`
	At      time.Time    `json:"at"`
}

type mqDeploys struct{ hub events.Hub }

func (m mqDeploys) Deployed(_ context.Context, d mergequeue.Deployment) error {
	id := d.TrainID
	if id == "" && len(d.Candidates) > 0 {
		id = d.Candidates[0]
	}
	ev := deployEvent{ID: id, Kind: "deploy", Status: d.State, Repo: d.Repo, Base: d.Base, SHA: d.SHA, TrainID: d.TrainID,
		PRs: d.PRs, Log: d.Log, At: d.At, Title: deployTitle(d.Repo, d.SHA, d.State)}
	topics := []string{"board"}
	for _, t := range d.Tasks {
		ev.Tasks = append(ev.Tasks, deployTask{ID: t})
		topics = append(topics, "task:"+t)
	}
	if len(d.Tasks) > 0 {
		ev.TaskID = d.Tasks[0]
	}
	typ := evTrainDeployed
	if d.State == mergequeue.DeployFailed {
		typ = evTrainDeployFailed
	}
	if m.hub != nil {
		m.hub.Publish(topics, typ, ev)
	}
	return nil
}

func deployTitle(repo, sha, state string) string {
	if len(sha) > 12 {
		sha = sha[:12]
	}
	if state == mergequeue.DeployFailed {
		return fmt.Sprintf("Deploy of %s at %s failed", repo, sha)
	}
	return fmt.Sprintf("Deployed %s at %s", repo, sha)
}

type mqDeployAlerts struct{ st mqStore }

type deployRun struct {
	id, repo, state, log, task, sha string
	at                              time.Time
}

func (a mqDeployAlerts) Decisions(ctx context.Context) ([]*decisions.Decision, error) {
	done := []any{mergequeue.DeploySucceeded, mergequeue.DeployFailed}
	trains, err := a.st.trains(ctx, `WHERE deploy_state IN (?,?)`, done...)
	if err != nil {
		return nil, err
	}
	cands, err := a.st.query(ctx, `WHERE deploy_state IN (?,?)`, done...)
	if err != nil {
		return nil, err
	}
	latest := map[string]deployRun{}
	keep := func(r deployRun) {
		if cur, ok := latest[r.repo]; !ok || r.at.After(cur.at) {
			latest[r.repo] = r
		}
	}
	for _, t := range trains {
		r := deployRun{id: t.ID, repo: t.Repo, state: t.DeployState, log: t.DeployLog, sha: t.Tip, at: t.UpdatedAt}
		for _, m := range t.Members {
			if m.Result == mergequeue.MemberMerged {
				r.task = m.TaskID
				break
			}
		}
		keep(r)
	}
	for _, c := range cands {
		keep(deployRun{id: c.ID, repo: c.Repo, state: c.DeployState, log: c.DeployLog, task: c.TaskID, sha: c.MergedSHA, at: c.UpdatedAt})
	}
	var out []*decisions.Decision
	for _, r := range latest {
		if r.state != mergequeue.DeployFailed {
			continue
		}
		d := &decisions.Decision{ID: "deploy:" + r.id, Kind: decisions.KindAlert, Title: deployTitle(r.repo, r.sha, r.state),
			Summary: lastLine(r.log) + "; the merge stays, fix and redeploy by hand or merge a fix", TaskID: r.task, CreatedAt: r.at}
		if r.task != "" {
			d.Links.Thread = model.ThreadTask + ":" + r.task
		}
		out = append(out, d)
	}
	return out, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if r := []rune(l); len(r) > 200 {
		l = string(r[:200])
	}
	return l
}
