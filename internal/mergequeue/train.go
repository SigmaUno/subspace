package mergequeue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	TrainBuilding = "building"
	TrainTesting  = "testing"
	TrainMerging  = "merging"
	TrainMerged   = "merged"
	TrainFailed   = "failed"
	TrainAborted  = "aborted"
)

const (
	MemberPending  = "pending"
	MemberMerging  = "merging"
	MemberMerged   = "merged"
	MemberEjected  = "ejected"
	MemberRequeued = "requeued"
)

type Train struct {
	ID        string        `json:"id"`
	Repo      string        `json:"repo"`
	Base      string        `json:"base"`
	State     string        `json:"state"`
	BaseSHA   string        `json:"base_sha"`
	Tip       string        `json:"tip"`
	Members   []TrainMember `json:"members"`
	Runs      int           `json:"runs"`
	Log       string        `json:"log,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

type TrainMember struct {
	CandidateID string `json:"candidate_id"`
	TaskID      string `json:"task_id"`
	PR          int    `json:"pr"`
	Head        string `json:"head"`
	SHA         string `json:"sha"`
	Result      string `json:"result"`
}

func (t *Train) Active() bool {
	return t.State == TrainBuilding || t.State == TrainTesting || t.State == TrainMerging
}

type TrainStore interface {
	InsertTrain(ctx context.Context, t *Train) error
	UpdateTrain(ctx context.Context, t *Train) error
	GetTrain(ctx context.Context, id string) (*Train, error)
	ListTrains(ctx context.Context, repo string) ([]*Train, error)
}

type TaskInfo struct {
	Priority  int
	DependsOn []string
	Done      bool
}

type Deps interface {
	Task(ctx context.Context, taskID string) (TaskInfo, error)
}

type CheckStatus struct {
	Head     string
	Contexts map[string]string
}

type Checks interface {
	Statuses(ctx context.Context, c Candidate) (*CheckStatus, error)
}

type trainRun struct {
	q        *Queue
	rc       RepoConfig
	t        *Train
	cands    map[string]*Candidate
	info     map[string]TaskInfo
	checks   map[string]*CheckStatus
	dir      string
	log      strings.Builder
	progress bool
}

func (q *Queue) ProcessTrain(ctx context.Context, repo string) (*Train, error) {
	rc, ok := q.cfg.Repos[repo]
	if !ok || rc.MaxTrain <= 0 {
		return nil, fmt.Errorf("mergequeue: repo %q does not use merge trains", repo)
	}
	if !q.acquire(repo) {
		return nil, nil
	}
	defer q.release(repo)
	return q.processTrain(ctx, repo, rc)
}

func (q *Queue) processTrain(ctx context.Context, repo string, rc RepoConfig) (*Train, error) {
	if t, err := q.recoverTrain(ctx, repo, rc); t != nil || err != nil {
		return t, err
	}
	r, err := q.pickTrain(ctx, repo, rc)
	if err != nil || r == nil {
		return nil, err
	}
	if err := q.cfg.Trains.InsertTrain(context.WithoutCancel(ctx), r.t); err != nil {
		return nil, err
	}
	for _, m := range r.t.Members {
		if err := q.save(ctx, r.cands[m.CandidateID], StateRunning); err != nil {
			return r.t, r.abort(ctx, err)
		}
	}
	if err := r.run(ctx); err != nil {
		return r.t, r.abort(ctx, err)
	}
	if !r.progress {
		return nil, nil
	}
	return r.t, nil
}

func (q *Queue) newRun(rc RepoConfig, t *Train) *trainRun {
	return &trainRun{q: q, rc: rc, t: t, cands: map[string]*Candidate{}, info: map[string]TaskInfo{},
		checks: map[string]*CheckStatus{}, dir: filepath.Join(q.cfg.WorkDir, dirName(t.Repo))}
}

func (q *Queue) pickTrain(ctx context.Context, repo string, rc RepoConfig) (*trainRun, error) {
	list, err := q.cfg.Store.List(ctx, repo)
	if err != nil {
		return nil, err
	}
	checks := map[string]*CheckStatus{}
	var pool []*Candidate
	for _, c := range list {
		if c.finished() {
			continue
		}
		if !rc.Auto {
			ok, err := q.cfg.Gate.Approved(ctx, *c)
			if err != nil {
				return nil, err
			}
			if !ok {
				if c.State != StateWaiting {
					err := errors.Join(q.save(ctx, c, StateWaiting),
						q.notify(ctx, c, fmt.Sprintf("merge queue: PR #%d is waiting for owner approval before it joins a merge train.", c.PR)))
					if err != nil {
						return nil, err
					}
				}
				continue
			}
		}
		cs, hold, reason, err := q.protection(ctx, rc, c)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			if err := q.eject(ctx, c, reason, ""); err != nil {
				return nil, err
			}
			continue
		}
		if !hold {
			checks[c.ID] = cs
			pool = append(pool, c)
		}
	}
	if len(pool) == 0 {
		return nil, nil
	}
	info := map[string]TaskInfo{}
	for _, c := range pool {
		if err := q.lookup(ctx, info, c.TaskID); err != nil {
			return nil, err
		}
		for _, d := range info[c.TaskID].DependsOn {
			if err := q.lookup(ctx, info, d); err != nil {
				return nil, err
			}
		}
	}
	slices.SortStableFunc(pool, func(a, b *Candidate) int { return info[b.TaskID].Priority - info[a.TaskID].Priority })
	ordered := order(pool, info)
	if len(ordered) == 0 {
		return nil, nil
	}
	base := ordered[0].Base
	ordered = order(slices.DeleteFunc(pool, func(c *Candidate) bool { return c.Base != base }), info)
	ordered = ordered[:min(len(ordered), rc.MaxTrain)]
	now := q.cfg.Now()
	r := q.newRun(rc, &Train{Repo: repo, Base: base, State: TrainBuilding, CreatedAt: now, UpdatedAt: now})
	r.info = info
	for _, c := range ordered {
		r.cands[c.ID] = c
		r.checks[c.ID] = checks[c.ID]
		r.t.Members = append(r.t.Members, TrainMember{CandidateID: c.ID, TaskID: c.TaskID, PR: c.PR, Head: c.Head, Result: MemberPending})
	}
	return r, nil
}

func (q *Queue) lookup(ctx context.Context, info map[string]TaskInfo, taskID string) error {
	if _, ok := info[taskID]; ok || q.cfg.Deps == nil {
		return nil
	}
	ti, err := q.cfg.Deps.Task(ctx, taskID)
	if err != nil {
		return err
	}
	info[taskID] = ti
	return nil
}

func order(pool []*Candidate, info map[string]TaskInfo) []*Candidate {
	placed := map[string]bool{}
	remaining := slices.Clone(pool)
	var out []*Candidate
	for {
		i := slices.IndexFunc(remaining, func(c *Candidate) bool { return depsMet(info, c.TaskID, placed) })
		if i < 0 {
			return out
		}
		out = append(out, remaining[i])
		placed[remaining[i].TaskID] = true
		remaining = slices.Delete(remaining, i, i+1)
	}
}

func depsMet(info map[string]TaskInfo, taskID string, placed map[string]bool) bool {
	for _, d := range info[taskID].DependsOn {
		if !info[d].Done && !placed[d] {
			return false
		}
	}
	return true
}

func (q *Queue) protection(ctx context.Context, rc RepoConfig, c *Candidate) (*CheckStatus, bool, string, error) {
	if len(rc.Required) == 0 || q.cfg.Checks == nil {
		return nil, false, "", nil
	}
	cs, err := q.cfg.Checks.Statuses(ctx, *c)
	if err != nil || cs == nil {
		return nil, false, "", err
	}
	hold := false
	for _, name := range rc.Required {
		switch st := cs.Contexts[name]; st {
		case "success":
		case "failure", "error":
			return cs, false, fmt.Sprintf("required status check %q is %s", name, st), nil
		default:
			hold = true
		}
	}
	return cs, hold, "", nil
}

func (q *Queue) eject(ctx context.Context, c *Candidate, reason, detail string) error {
	ctx = context.WithoutCancel(ctx)
	body := fmt.Sprintf("merge queue: PR #%d ejected: %s.", c.PR, reason)
	if detail != "" {
		body += fmt.Sprintf("\n\n```\n%s\n```", tailN(detail, 4000))
	}
	return errors.Join(
		q.save(ctx, c, StateEjected),
		q.notify(ctx, c, body),
		q.cfg.Tasks.RequestChanges(ctx, c.TaskID, "merge queue: "+reason),
	)
}

func (r *trainRun) run(ctx context.Context) error {
	q, t := r.q, r.t
	if err := q.ensureRepo(ctx, r.dir, r.rc, &r.log); err != nil {
		return err
	}
	baseRef := "refs/remotes/origin/" + t.Base
	specs := []string{"+refs/heads/" + t.Base + ":" + baseRef}
	for _, m := range t.Members {
		if s := "+refs/heads/" + m.Head + ":refs/remotes/origin/" + m.Head; !slices.Contains(specs, s) {
			specs = append(specs, s)
		}
	}
	if _, err := q.git(ctx, r.dir, &r.log, append([]string{"fetch", "--no-tags", "--prune", "origin"}, specs...)...); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(r.dir, ".git", "MERGE_HEAD")); err == nil {
		if _, err := q.git(ctx, r.dir, &r.log, "merge", "--abort"); err != nil {
			return err
		}
	}
	sha, err := q.git(ctx, r.dir, &r.log, "rev-parse", "--verify", baseRef+"^{commit}")
	if err != nil {
		return err
	}
	t.BaseSHA = sha
	if _, err := q.git(ctx, r.dir, &r.log, "checkout", "-q", "-f", "-B", "mergequeue", baseRef); err != nil {
		return err
	}
	if _, err := q.git(ctx, r.dir, &r.log, "clean", "-q", "-fdx"); err != nil {
		return err
	}
	if err := r.build(ctx); err != nil {
		return err
	}
	var idx []int
	for i, m := range t.Members {
		if m.Result == MemberPending {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return r.save(ctx, TrainFailed)
	}
	if err := r.save(ctx, TrainTesting); err != nil {
		return err
	}
	pass, out, err := r.test(ctx, t.Members[idx[len(idx)-1]].SHA)
	if err != nil {
		return err
	}
	if pass {
		return r.merge(ctx, idx)
	}
	lo, hi := 0, len(idx)
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		ok, o, err := r.test(ctx, t.Members[idx[mid-1]].SHA)
		if err != nil {
			return err
		}
		if ok {
			lo = mid
		} else {
			hi, out = mid, o
		}
	}
	culprit := &t.Members[idx[hi-1]]
	verify := strings.Join(r.rc.Verify, " ")
	if err := r.eject(ctx, culprit, fmt.Sprintf("merge train %s failed `%s` and bisection isolated this PR after %d test runs", t.ID, verify, t.Runs), out); err != nil {
		return err
	}
	for _, i := range idx[hi:] {
		msg := fmt.Sprintf("merge queue: PR #%d returned to the queue: merge train %s failed because of PR #%d; it will be retested in the next train.", t.Members[i].PR, t.ID, culprit.PR)
		if err := r.requeue(ctx, &t.Members[i], msg); err != nil {
			return err
		}
	}
	if lo == 0 {
		return r.save(ctx, TrainFailed)
	}
	return r.merge(ctx, idx[:lo])
}

func (r *trainRun) build(ctx context.Context) error {
	q, t := r.q, r.t
	placed := map[string]bool{}
	var ahead []string
	for i := range t.Members {
		m := &t.Members[i]
		c := r.cands[m.CandidateID]
		if !depsMet(r.info, c.TaskID, placed) {
			if err := r.requeue(ctx, m, ""); err != nil {
				return err
			}
			continue
		}
		head, err := q.git(ctx, r.dir, &r.log, "rev-parse", "--verify", "refs/remotes/origin/"+c.Head+"^{commit}")
		if err != nil {
			return err
		}
		if cs := r.checks[c.ID]; cs != nil && cs.Head != "" && cs.Head != head {
			if err := r.requeue(ctx, m, ""); err != nil {
				return err
			}
			continue
		}
		_, err = q.git(ctx, r.dir, &r.log, "-c", "user.name=subspace-mergequeue", "-c", "user.email=mergequeue@subspace.invalid",
			"merge", "--no-ff", "--no-edit", "-m", fmt.Sprintf("Merge PR #%d (%s) via merge train %s", c.PR, c.TaskID, t.ID),
			"refs/remotes/origin/"+c.Head)
		if err != nil {
			if ctx.Err() != nil {
				return err
			}
			if _, err := q.git(ctx, r.dir, &r.log, "merge", "--abort"); err != nil {
				return err
			}
			reason := fmt.Sprintf("merge conflict merging %s into %s", c.Head, c.Base)
			if len(ahead) > 0 {
				reason += " on top of " + strings.Join(ahead, ", ")
			}
			if err := r.eject(ctx, m, fmt.Sprintf("merge train %s: %s", t.ID, reason), r.log.String()); err != nil {
				return err
			}
			continue
		}
		if m.SHA, err = q.git(ctx, r.dir, &r.log, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
			return err
		}
		placed[c.TaskID] = true
		ahead = append(ahead, "#"+strconv.Itoa(c.PR))
	}
	return nil
}

func (r *trainRun) test(ctx context.Context, sha string) (bool, string, error) {
	q := r.q
	if _, err := q.git(ctx, r.dir, &r.log, "checkout", "-q", "-f", "--detach", sha); err != nil {
		return false, "", err
	}
	if _, err := q.git(ctx, r.dir, &r.log, "clean", "-q", "-fdx"); err != nil {
		return false, "", err
	}
	r.t.Runs++
	fmt.Fprintf(&r.log, "$ %s  # %s\n", strings.Join(r.rc.Verify, " "), short(sha))
	out, err := q.cfg.Runner.Run(ctx, r.dir, r.rc.Verify[0], r.rc.Verify[1:]...)
	r.log.WriteString(out)
	if err != nil {
		fmt.Fprintf(&r.log, "%v\n", err)
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}
	}
	return err == nil, out, r.save(ctx, "")
}

func (r *trainRun) merge(ctx context.Context, idx []int) error {
	q, t := r.q, r.t
	if !r.rc.Auto {
		for _, i := range idx {
			c := r.cands[t.Members[i].CandidateID]
			ok, err := q.cfg.Gate.Approved(ctx, *c)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("mergequeue: %s is no longer approved; merge train %s aborted", c.TaskID, t.ID)
			}
		}
	}
	for _, i := range idx {
		t.Members[i].Result = MemberMerging
	}
	t.Tip = t.Members[idx[len(idx)-1]].SHA
	if err := r.save(ctx, TrainMerging); err != nil {
		return err
	}
	if _, err := q.git(ctx, r.dir, &r.log, "push", "--atomic", "--porcelain",
		"--force-with-lease=refs/heads/"+t.Base+":"+t.BaseSHA,
		"origin", t.Tip+":refs/heads/"+t.Base); err != nil {
		return fmt.Errorf("mergequeue: push merge train %s: %w", t.ID, err)
	}
	return r.finalize(ctx)
}

func (r *trainRun) finalize(ctx context.Context) error {
	q, t := r.q, r.t
	final := TrainMerged
	var prs []string
	for _, m := range t.Members {
		switch m.Result {
		case MemberMerging, MemberMerged:
			prs = append(prs, "#"+strconv.Itoa(m.PR))
		default:
			final = TrainFailed
		}
	}
	verify := strings.Join(r.rc.Verify, " ")
	for i := range t.Members {
		m := &t.Members[i]
		if m.Result != MemberMerging {
			continue
		}
		c := r.cands[m.CandidateID]
		if c.State != StateMerged {
			c.MergedSHA = m.SHA
			err := errors.Join(q.save(ctx, c, StateMerged),
				q.notify(ctx, c, fmt.Sprintf("merge queue: PR #%d merged into %s as %s by merge train %s (%s), `%s` passed.",
					c.PR, t.Base, short(m.SHA), t.ID, strings.Join(prs, ", "), verify)))
			if err != nil {
				return err
			}
		}
		m.Result = MemberMerged
		r.progress = true
	}
	return r.save(ctx, final)
}

func (r *trainRun) eject(ctx context.Context, m *TrainMember, reason, detail string) error {
	m.Result = MemberEjected
	r.progress = true
	c := r.cands[m.CandidateID]
	c.Log = tail(r.log.String())
	return r.q.eject(ctx, c, reason, detail)
}

func (r *trainRun) requeue(ctx context.Context, m *TrainMember, msg string) error {
	m.Result = MemberRequeued
	c := r.cands[m.CandidateID]
	if err := r.q.save(ctx, c, StateQueued); err != nil {
		return err
	}
	if msg == "" {
		return nil
	}
	return r.q.notify(ctx, c, msg)
}

func (r *trainRun) abort(ctx context.Context, cause error) error {
	ctx = context.WithoutCancel(ctx)
	if cause != nil {
		fmt.Fprintf(&r.log, "merge train aborted: %v\n", cause)
	}
	errs := []error{cause}
	for i := range r.t.Members {
		m := &r.t.Members[i]
		if m.Result != MemberPending && m.Result != MemberMerging {
			continue
		}
		m.Result = MemberRequeued
		if c := r.cands[m.CandidateID]; c != nil && c.State == StateRunning {
			c.Log = tail(r.log.String())
			errs = append(errs, r.q.save(ctx, c, StateQueued))
			if cause == nil {
				errs = append(errs, r.q.notify(ctx, c, fmt.Sprintf("merge queue: merge train %s was interrupted; PR #%d returned to the queue.", r.t.ID, c.PR)))
			}
		}
	}
	return errors.Join(append(errs, r.save(ctx, TrainAborted))...)
}

func (r *trainRun) save(ctx context.Context, state string) error {
	if state != "" {
		r.t.State = state
	}
	r.t.UpdatedAt = r.q.cfg.Now()
	r.t.Log = tail(r.log.String())
	return r.q.cfg.Trains.UpdateTrain(context.WithoutCancel(ctx), r.t)
}

func (q *Queue) recoverTrain(ctx context.Context, repo string, rc RepoConfig) (*Train, error) {
	ts, err := q.cfg.Trains.ListTrains(ctx, repo)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(ts, (*Train).Active)
	if i < 0 {
		return nil, nil
	}
	t := ts[i]
	r := q.newRun(rc, t)
	r.log.WriteString(t.Log)
	fmt.Fprintf(&r.log, "recovering merge train %s interrupted while %s\n", t.ID, t.State)
	for _, m := range t.Members {
		c, err := q.cfg.Store.Get(ctx, m.CandidateID)
		if err != nil {
			return nil, err
		}
		r.cands[m.CandidateID] = c
	}
	if t.State == TrainMerging && t.Tip != "" {
		landed, err := r.landed(ctx)
		if err != nil {
			return nil, err
		}
		if landed {
			return t, r.finalize(ctx)
		}
	}
	return t, r.abort(ctx, nil)
}

func (r *trainRun) landed(ctx context.Context) (bool, error) {
	q, t := r.q, r.t
	if err := q.ensureRepo(ctx, r.dir, r.rc, &r.log); err != nil {
		return false, err
	}
	baseRef := "refs/remotes/origin/" + t.Base
	if _, err := q.git(ctx, r.dir, &r.log, "fetch", "--no-tags", "origin", "+refs/heads/"+t.Base+":"+baseRef); err != nil {
		return false, err
	}
	_, err := q.git(ctx, r.dir, &r.log, "merge-base", "--is-ancestor", t.Tip, baseRef)
	return err == nil, nil
}
