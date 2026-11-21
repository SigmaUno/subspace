package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/mergequeue"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/repos"
	"github.com/SigmaUno/subspace/internal/store"
)

const mqSchema = `
CREATE TABLE IF NOT EXISTS mq_candidates (
  id TEXT PRIMARY KEY, repo TEXT NOT NULL, pr INTEGER NOT NULL, head TEXT NOT NULL, base TEXT NOT NULL,
  task_id TEXT NOT NULL, state TEXT NOT NULL, merged_sha TEXT NOT NULL DEFAULT '', log TEXT NOT NULL DEFAULT '',
  enqueued_at BIGINT NOT NULL, updated_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS mq_candidates_repo ON mq_candidates(repo, state);
`

const mqCols = `id,repo,pr,head,base,task_id,state,merged_sha,log,enqueued_at,updated_at`

var mqRunner mergequeue.Runner

var mqSystem = model.Actor{ID: "mergequeue", Role: "system"}

func init() {
	registerSchema(mqSchema, "mq_candidates")
	registerMount(mountMergeQueue)
}

type mqRepo struct {
	Remote   string   `json:"remote"`
	Verify   []string `json:"verify"`
	Base     string   `json:"base"`
	Train    int      `json:"train"`
	Required []string `json:"required"`
	WorkDir  string   `json:"workdir"`
}

type mqFile struct {
	WorkDir  string            `json:"workdir"`
	Interval string            `json:"interval"`
	Repos    map[string]mqRepo `json:"repos"`
}

const (
	mqConfigEnv   = "SUBSPACE_MQ_CONFIG"
	mqIntervalEnv = "SUBSPACE_MQ_INTERVAL"
)

func mountMergeQueue(d Deps) {
	f, ok, err := mqConfig(d)
	if err != nil {
		d.Log.Error("mergequeue disabled", "err", err)
		return
	}
	if !ok {
		return
	}
	if err := startMergeQueue(d, f); err != nil {
		d.Log.Error("mergequeue disabled", "err", err)
	}
}

func mqConfig(d Deps) (mqFile, bool, error) {
	rs, err := repos.Enabled(d.Ctx, repoSource(d))
	if err != nil {
		return mqFile{}, false, err
	}
	f := mqFile{Repos: map[string]mqRepo{}, Interval: os.Getenv(mqIntervalEnv)}
	for _, r := range rs {
		if len(r.MergeTestCommand) > 0 {
			f.Repos[r.Name] = mqRepo{Remote: r.CloneURL(), Verify: r.MergeTestCommand, Base: r.DefaultBranch, WorkDir: r.MergeWorkDir}
		}
	}
	if len(f.Repos) > 0 {
		return f, true, nil
	}
	path := os.Getenv(mqConfigEnv)
	if path == "" {
		return mqFile{}, false, nil
	}
	repos.Deprecated(d.Log, mqConfigEnv)
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return mqFile{}, false, err
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return mqFile{}, false, err
	}
	return f, true, nil
}

func startMergeQueue(d Deps, f mqFile) error {
	var err error
	interval := 30 * time.Second
	if f.Interval != "" {
		if interval, err = time.ParseDuration(f.Interval); err != nil || interval <= 0 {
			return fmt.Errorf("invalid interval %q", f.Interval)
		}
	}
	if isPostgres(d.Cfg.DBPath) && f.WorkDir == "" {
		for name, r := range f.Repos {
			if r.WorkDir == "" {
				return fmt.Errorf("workdir is required with the postgres backend (repo %s)", name)
			}
		}
	}
	if f.WorkDir == "" {
		f.WorkDir = filepath.Join(filepath.Dir(d.Cfg.DBPath), "mergequeue")
	}
	life, ok := d.Store.(interface{ Done() <-chan struct{} })
	if !ok {
		return errors.New("store does not support the merge queue")
	}
	repos := make(map[string]mergequeue.RepoConfig, len(f.Repos))
	bases := make(map[string]string, len(f.Repos))
	for name, r := range f.Repos {
		repos[name] = mergequeue.RepoConfig{Remote: r.Remote, Verify: r.Verify, MaxTrain: r.Train, Required: r.Required, WorkDir: r.WorkDir}
		bases[name] = r.Base
		if r.Base == "" {
			bases[name] = "main"
		}
	}
	gate := mqGate{d.Board}
	var checks mergequeue.Checks
	if ps, ok := d.Store.(prStatusReader); ok {
		checks = mqChecks{ps}
	}
	q, err := mergequeue.New(mergequeue.Config{
		Repos:    repos,
		WorkDir:  f.WorkDir,
		Store:    mqStore{st: d.Store, merged: mqTasks{d.Board}.done},
		Runner:   mqRunner,
		Notifier: mqNotifier{d.Board},
		Gate:     gate,
		Tasks:    mqTasks{d.Board},
		Deps:     mqDeps{d.Board},
		Checks:   checks,
		Logger:   d.Log,
	})
	if err != nil {
		return err
	}
	h := &mqHandler{q: q, st: mqStore{st: d.Store}, gate: gate, bases: bases, log: d.Log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /mergequeue/candidates", h.list)
	mux.HandleFunc("GET /mergequeue/candidates/{id}", h.get)
	mux.HandleFunc("POST /mergequeue/candidates", h.enqueue)
	d.API.Mount(d.Mux, "/api/v1/mergequeue/", mux)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-life.Done()
		cancel()
	}()
	go q.Run(ctx, interval)
	return nil
}

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("mergequeue: store transaction does not expose SQL")

type mqStore struct {
	st     store.Store
	merged func(ctx context.Context, c *mergequeue.Candidate) error
}

func withSQL(fn func(store.Tx, sqlTx) error) func(store.Tx) error {
	return func(t store.Tx) error {
		q, ok := t.(sqlTx)
		if !ok {
			return errNoSQL
		}
		return fn(t, q)
	}
}

func (s mqStore) Insert(ctx context.Context, c *mergequeue.Candidate) error {
	return s.st.Atomic(ctx, withSQL(func(t store.Tx, q sqlTx) error {
		id, err := t.NextID("MQ")
		if err != nil {
			return err
		}
		if _, err := q.Exec(`INSERT INTO mq_candidates(`+mqCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			id, c.Repo, c.PR, c.Head, c.Base, c.TaskID, c.State, c.MergedSHA, c.Log,
			c.EnqueuedAt.UnixMilli(), c.UpdatedAt.UnixMilli()); err != nil {
			return err
		}
		c.ID = id
		return nil
	}))
}

func (s mqStore) Update(ctx context.Context, c *mergequeue.Candidate) error {
	err := s.st.Atomic(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		res, err := q.Exec(`UPDATE mq_candidates SET state=?, merged_sha=?, log=?, updated_at=? WHERE id=?`,
			c.State, c.MergedSHA, c.Log, c.UpdatedAt.UnixMilli(), c.ID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return errors.Join(err, store.ErrNotFound)
		}
		return nil
	}))
	if err != nil || c.State != mergequeue.StateMerged || s.merged == nil {
		return err
	}
	return s.merged(ctx, c)
}

func (s mqStore) Get(ctx context.Context, id string) (*mergequeue.Candidate, error) {
	cs, err := s.query(ctx, `WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	if len(cs) == 0 {
		return nil, model.NotFound("candidate %s not found", id)
	}
	return cs[0], nil
}

func (s mqStore) List(ctx context.Context, repo string) ([]*mergequeue.Candidate, error) {
	if repo == "" {
		return s.query(ctx, ``)
	}
	return s.query(ctx, `WHERE repo=?`, repo)
}

func (s mqStore) query(ctx context.Context, where string, args ...any) ([]*mergequeue.Candidate, error) {
	out := []*mergequeue.Candidate{}
	err := s.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		rows, err := q.Query(`SELECT `+mqCols+` FROM mq_candidates `+where+` ORDER BY length(id), id`, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c mergequeue.Candidate
			var enq, upd int64
			if err := rows.Scan(&c.ID, &c.Repo, &c.PR, &c.Head, &c.Base, &c.TaskID, &c.State, &c.MergedSHA, &c.Log, &enq, &upd); err != nil {
				return err
			}
			c.EnqueuedAt, c.UpdatedAt = time.UnixMilli(enq).UTC(), time.UnixMilli(upd).UTC()
			out = append(out, &c)
		}
		return rows.Err()
	}))
	return out, err
}

type mqGate struct{ b *board.Board }

func (g mqGate) Approved(ctx context.Context, c mergequeue.Candidate) (bool, error) {
	_, err := g.check(ctx, c.TaskID)
	var me *model.Error
	if errors.As(err, &me) && me.Code == "conflict" {
		return false, nil
	}
	return err == nil, err
}

func (g mqGate) check(ctx context.Context, taskID string) (*model.Task, error) {
	if paused, _ := g.b.Paused(); paused {
		return nil, model.Conflict("subspace is paused")
	}
	t, err := g.b.GetTask(ctx, model.Owner, taskID)
	if err != nil {
		return nil, err
	}
	if t.Status != model.StatusApproved {
		return nil, model.Conflict("task %s is %s, not approved", t.ID, t.Status)
	}
	if t.Tests != nil && t.Tests.Failed > 0 {
		return nil, model.Conflict("task %s has %d failing tests", t.ID, t.Tests.Failed)
	}
	if t.Reviewer == nil || t.Assignee == nil {
		return nil, model.Conflict("task %s was not approved by a reviewer", t.ID)
	}
	rv, err := g.b.GetAgent(ctx, *t.Reviewer)
	if err != nil {
		return nil, err
	}
	wk, err := g.b.GetAgent(ctx, *t.Assignee)
	if err != nil {
		return nil, err
	}
	if rv.Provider == wk.Provider {
		return nil, model.Conflict("task %s was reviewed by %s on the worker's provider %s", t.ID, rv.ID, rv.Provider)
	}
	if err := g.b.CheckQuality(ctx, t.ID); err != nil {
		if me := model.AsError(err); me.Code == board.CodeGatesFailed {
			return nil, model.Conflict("task %s: %s", t.ID, me.Message)
		}
		return nil, err
	}
	return t, nil
}

type mqTasks struct{ b *board.Board }

func (m mqTasks) RequestChanges(ctx context.Context, taskID, reason string) error {
	t, err := m.b.GetTask(ctx, model.Owner, taskID)
	if err != nil || t.Assignee == nil {
		return err
	}
	_, err = m.b.AppendMessage(ctx, model.ThreadAgent, *t.Assignee, model.Message{
		AuthorID: mqSystem.ID, AuthorRole: mqSystem.Role, Kind: model.MsgSystem,
		Body: fmt.Sprintf("%s was not merged: %s. Fix the branch; the owner re-enqueues it.", taskID, reason),
	})
	return err
}

func (m mqTasks) done(ctx context.Context, c *mergequeue.Candidate) error {
	_, err := m.b.MarkDone(ctx, c.TaskID, fmt.Sprintf("merged by merge queue: %s#%d as %s", c.Repo, c.PR, c.MergedSHA))
	return err
}

type mqNotifier struct{ b *board.Board }

func (n mqNotifier) Notify(ctx context.Context, taskID, body string) error {
	_, err := n.b.AppendMessage(ctx, model.ThreadTask, taskID, model.Message{
		AuthorID: mqSystem.ID, AuthorRole: mqSystem.Role, Kind: model.MsgSystem, Body: body,
	})
	return err
}

type mqHandler struct {
	q     *mergequeue.Queue
	st    mqStore
	gate  mqGate
	bases map[string]string
	log   *slog.Logger
}

func (h *mqHandler) list(w http.ResponseWriter, r *http.Request) {
	cs, err := h.st.List(r.Context(), r.URL.Query().Get("repo"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeMQ(w, http.StatusOK, map[string]any{"candidates": cs})
}

func (h *mqHandler) get(w http.ResponseWriter, r *http.Request) {
	c, err := h.st.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeMQ(w, http.StatusOK, c)
}

func (h *mqHandler) enqueue(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		h.fail(w, model.BadRequest("request body too large or unreadable"))
		return
	}
	var in struct {
		TaskID string `json:"task_id"`
		PR     int    `json:"pr"`
		Base   string `json:"base"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		h.fail(w, model.BadRequest("invalid JSON body"))
		return
	}
	if in.TaskID == "" {
		h.fail(w, model.BadRequest("task_id is required"))
		return
	}
	t, err := h.gate.check(r.Context(), in.TaskID)
	if err != nil {
		h.fail(w, err)
		return
	}
	if in.PR == 0 {
		in.PR = prNumber(t.PRURL)
	}
	if in.Base == "" {
		in.Base = h.bases[t.Repo]
	}
	c, err := h.q.Enqueue(r.Context(), mergequeue.Candidate{Repo: t.Repo, PR: in.PR, Head: t.Branch, Base: in.Base, TaskID: t.ID})
	if err != nil {
		h.fail(w, err)
		return
	}
	writeMQ(w, http.StatusCreated, c)
}

func (h *mqHandler) fail(w http.ResponseWriter, err error) {
	e := model.AsError(err)
	if e.Code == "internal" {
		h.log.Error("mergequeue error", "err", err)
	}
	writeMQ(w, e.HTTPStatus(), map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message}})
}

func writeMQ(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func prNumber(u *string) int {
	if u == nil {
		return 0
	}
	s := strings.TrimRight(*u, "/")
	n, err := strconv.Atoi(s[strings.LastIndex(s, "/")+1:])
	if err != nil {
		return 0
	}
	return n
}
