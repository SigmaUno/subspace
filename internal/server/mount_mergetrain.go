package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/httpx"
	"github.com/SigmaUno/subspace/internal/mergequeue"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/prsync"
	"github.com/SigmaUno/subspace/internal/store"
)

const mqTrainSchema = `
CREATE TABLE IF NOT EXISTS mq_trains (
  id TEXT PRIMARY KEY, repo TEXT NOT NULL, base TEXT NOT NULL, state TEXT NOT NULL,
  base_sha TEXT NOT NULL DEFAULT '', tip TEXT NOT NULL DEFAULT '', members TEXT NOT NULL DEFAULT '[]',
  runs INTEGER NOT NULL DEFAULT 0, log TEXT NOT NULL DEFAULT '', created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS mq_trains_repo ON mq_trains(repo, state);
`

const mqTrainCols = `id,repo,base,state,base_sha,tip,members,runs,log,created_at,updated_at`

func init() {
	registerSchema(mqTrainSchema, "mq_trains")
	registerMount(mountMergeTrains)
}

func mountMergeTrains(d Deps) {
	st := mqStore{st: d.Store}
	d.API.Mount(d.Mux, "GET /api/v1/mergequeue/trains", httpx.Handle(http.StatusOK, func(r *http.Request) (any, error) {
		limit, err := httpx.PositiveLimit(r, 50, 500)
		if err != nil {
			return nil, err
		}
		ts, err := st.ListTrains(r.Context(), r.URL.Query().Get("repo"), limit)
		return map[string]any{"trains": ts}, err
	}))
	d.API.Mount(d.Mux, "GET /api/v1/mergequeue/trains/{id}", httpx.Handle(http.StatusOK, func(r *http.Request) (any, error) {
		return st.GetTrain(r.Context(), r.PathValue("id"))
	}))
}

func (s mqStore) InsertTrain(ctx context.Context, t *mergequeue.Train) error {
	members, err := json.Marshal(t.Members)
	if err != nil {
		return err
	}
	return s.st.Atomic(ctx, withSQL(func(tx store.Tx, q sqlTx) error {
		id, err := tx.NextID("MQT")
		if err != nil {
			return err
		}
		if _, err := q.Exec(`INSERT INTO mq_trains(`+mqTrainCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			id, t.Repo, t.Base, t.State, t.BaseSHA, t.Tip, string(members), t.Runs, t.Log,
			t.CreatedAt.UnixMilli(), t.UpdatedAt.UnixMilli()); err != nil {
			return err
		}
		t.ID = id
		return nil
	}))
}

func (s mqStore) UpdateTrain(ctx context.Context, t *mergequeue.Train) error {
	members, err := json.Marshal(t.Members)
	if err != nil {
		return err
	}
	return s.st.Atomic(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		res, err := q.Exec(`UPDATE mq_trains SET state=?, base_sha=?, tip=?, members=?, runs=?, log=?, updated_at=? WHERE id=?`,
			t.State, t.BaseSHA, t.Tip, string(members), t.Runs, t.Log, t.UpdatedAt.UnixMilli(), t.ID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return errors.Join(err, store.ErrNotFound)
		}
		return nil
	}))
}

func (s mqStore) GetTrain(ctx context.Context, id string) (*mergequeue.Train, error) {
	ts, err := s.trains(ctx, `WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	if len(ts) == 0 {
		return nil, model.NotFound("train %s not found", id)
	}
	return ts[0], nil
}

func (s mqStore) ListTrains(ctx context.Context, repo string, limit int) ([]*mergequeue.Train, error) {
	where, args := `WHERE 1=1`, []any{}
	if repo != "" {
		where, args = `WHERE repo=?`, append(args, repo)
	}
	order := ` ORDER BY length(id) DESC, id DESC`
	if limit > 0 {
		order += ` LIMIT ` + strconv.Itoa(limit)
	}
	return s.trains(ctx, where+order, args...)
}

func (s mqStore) ActiveTrains(ctx context.Context, repo string) ([]*mergequeue.Train, error) {
	return s.trains(ctx, `WHERE repo=? AND state IN (?,?,?) ORDER BY length(id), id`,
		repo, mergequeue.TrainBuilding, mergequeue.TrainTesting, mergequeue.TrainMerging)
}

func (s mqStore) trains(ctx context.Context, tail string, args ...any) ([]*mergequeue.Train, error) {
	out := []*mergequeue.Train{}
	err := s.st.View(ctx, withSQL(func(_ store.Tx, q sqlTx) error {
		rows, err := q.Query(`SELECT `+mqTrainCols+` FROM mq_trains `+tail, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var t mergequeue.Train
			var members string
			var created, updated int64
			if err := rows.Scan(&t.ID, &t.Repo, &t.Base, &t.State, &t.BaseSHA, &t.Tip, &members, &t.Runs, &t.Log, &created, &updated); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(members), &t.Members); err != nil {
				return err
			}
			t.CreatedAt, t.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
			out = append(out, &t)
		}
		return rows.Err()
	}))
	return out, err
}

type mqDeps struct{ b *board.Board }

func (m mqDeps) Task(ctx context.Context, taskID string) (mergequeue.TaskInfo, error) {
	t, err := m.b.GetTask(ctx, model.Owner, taskID)
	if err != nil {
		if model.AsError(err).Code == "not_found" {
			return mergequeue.TaskInfo{}, nil
		}
		return mergequeue.TaskInfo{}, err
	}
	return mergequeue.TaskInfo{Priority: t.Priority, DependsOn: t.DependsOn, Done: t.Status == model.StatusDone}, nil
}

type prStatusReader interface {
	PRStatus(ctx context.Context, taskID string) ([]byte, bool, error)
}

type mqChecks struct{ st prStatusReader }

func (m mqChecks) Statuses(ctx context.Context, c mergequeue.Candidate) (*mergequeue.CheckStatus, error) {
	raw, ok, err := m.st.PRStatus(ctx, c.TaskID)
	if err != nil || !ok {
		return nil, err
	}
	var s prsync.Status
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if s.State == "" {
		return nil, nil
	}
	return &mergequeue.CheckStatus{Head: s.HeadSHA, Contexts: map[string]string{"checks": s.Checks}}, nil
}
