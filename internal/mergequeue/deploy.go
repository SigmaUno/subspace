package mergequeue

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	DeployRunning   = "running"
	DeploySucceeded = "succeeded"
	DeployFailed    = "failed"
)

const (
	DefaultDeployTimeout = 10 * time.Minute
	maxDeployLog         = 16 << 10
	deployNoteTail       = 4000
)

type Deployment struct {
	Repo       string    `json:"repo"`
	Base       string    `json:"base"`
	SHA        string    `json:"sha"`
	TrainID    string    `json:"train_id,omitempty"`
	Candidates []string  `json:"candidates"`
	Tasks      []string  `json:"tasks"`
	PRs        []int     `json:"prs"`
	State      string    `json:"state"`
	Log        string    `json:"log,omitempty"`
	At         time.Time `json:"at"`
}

type Deploys interface {
	Deployed(ctx context.Context, d Deployment) error
}

func (q *Queue) deploy(ctx context.Context, dir string, rc RepoConfig, d Deployment) Deployment {
	timeout := rc.DeployTimeout
	if timeout == 0 {
		timeout = DefaultDeployTimeout
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var log strings.Builder
	cmd := strings.Join(rc.Deploy, " ")
	_, err := q.git(dctx, dir, &log, "checkout", "-q", "-f", "--detach", d.SHA)
	if err == nil {
		_, err = q.git(dctx, dir, &log, "clean", "-q", "-fdx")
	}
	if err == nil {
		fmt.Fprintf(&log, "$ %s  # %s\n", cmd, short(d.SHA))
		var out string
		out, err = q.cfg.Runner.Run(dctx, dir, rc.Deploy[0], rc.Deploy[1:]...)
		log.WriteString(redactCredentials(out))
	}
	switch {
	case errors.Is(dctx.Err(), context.DeadlineExceeded):
		fmt.Fprintf(&log, "deploy timed out after %s\n", timeout)
	case ctx.Err() != nil:
		fmt.Fprintf(&log, "deploy interrupted: %v\n", ctx.Err())
	case err != nil:
		fmt.Fprintf(&log, "%v\n", err)
	}
	d.State = DeploySucceeded
	if err != nil || dctx.Err() != nil {
		d.State = DeployFailed
	}
	d.Log = tailN(log.String(), maxDeployLog)
	d.At = q.cfg.Now()
	return d
}

func (q *Queue) deployed(ctx context.Context, d Deployment, cands []*Candidate) error {
	ctx = context.WithoutCancel(ctx)
	prs := make([]string, len(d.PRs))
	for i, pr := range d.PRs {
		prs[i] = "#" + strconv.Itoa(pr)
	}
	what := strings.Join(prs, ", ")
	if d.TrainID != "" {
		what = "merge train " + d.TrainID + " (" + what + ")"
	}
	body := fmt.Sprintf("merge queue: deployed %s at %s.", what, short(d.SHA))
	if d.State == DeployFailed {
		body = fmt.Sprintf("merge queue: deploy of %s at %s failed; the merge stays.\n\n```\n%s\n```", what, short(d.SHA), tailN(d.Log, deployNoteTail))
	}
	var errs []error
	for _, c := range cands {
		errs = append(errs, q.notify(ctx, c, body))
	}
	if q.cfg.Deploys != nil {
		errs = append(errs, q.cfg.Deploys.Deployed(ctx, d))
	}
	return errors.Join(errs...)
}
