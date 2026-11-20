package hillclimb

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS hillclimb_jobs (
  id TEXT PRIMARY KEY, brief_id TEXT NOT NULL, repo TEXT NOT NULL, metric_cmd TEXT NOT NULL, direction TEXT NOT NULL,
  target REAL, budget INTEGER NOT NULL, plateau_n INTEGER NOT NULL, status TEXT NOT NULL, stop_reason TEXT,
  best REAL, best_attempt_id INTEGER, attempts INTEGER NOT NULL, since_best INTEGER NOT NULL, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS hillclimb_attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT, job_id TEXT NOT NULL REFERENCES hillclimb_jobs(id), agent_id TEXT NOT NULL,
  branch TEXT NOT NULL, value REAL, status TEXT NOT NULL, measured_at INTEGER NOT NULL, log TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS hillclimb_attempts_job ON hillclimb_attempts(job_id, id);
`

const jobCols = `id,brief_id,repo,metric_cmd,direction,target,budget,plateau_n,status,stop_reason,best,best_attempt_id,attempts,since_best,created_at`

func init() { sqlite.RegisterSchema(schema) }

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("hillclimb: store transaction does not expose SQL")

type SQLStore struct{ st store.Store }

func NewSQLStore(st store.Store) *SQLStore { return &SQLStore{st: st} }

func withSQL(fn func(store.Tx, sqlTx) error) func(store.Tx) error {
	return func(t store.Tx) error {
		q, ok := t.(sqlTx)
		if !ok {
			return errNoSQL
		}
		return fn(t, q)
	}
}

func (s *SQLStore) CreateJob(j *Job) error {
	return s.st.Atomic(context.Background(), withSQL(func(t store.Tx, q sqlTx) error {
		id, err := t.NextID("H")
		if err != nil {
			return err
		}
		c := *j
		c.ID, c.Status = id, JobRunning
		if _, err := q.Exec(`INSERT INTO hillclimb_jobs(`+jobCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, jobArgs(&c)...); err != nil {
			return err
		}
		*j = c
		return nil
	}))
}

func (s *SQLStore) GetJob(id string) (*Job, error) {
	js, err := s.jobs(`WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	if len(js) == 0 {
		return nil, model.NotFound("hillclimb job %s not found", id)
	}
	return &js[0], nil
}

func (s *SQLStore) ListJobs() ([]Job, error) { return s.jobs(`ORDER BY rowid`) }

func (s *SQLStore) UpdateJob(j *Job) error {
	return s.st.Atomic(context.Background(), withSQL(func(_ store.Tx, q sqlTx) error {
		res, err := q.Exec(`UPDATE hillclimb_jobs SET status=?, stop_reason=?, best=?, best_attempt_id=?, attempts=?, since_best=? WHERE id=?`,
			j.Status, j.StopReason, j.Best, j.BestAttemptID, j.Attempts, j.SinceBest, j.ID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return errors.Join(err, model.NotFound("hillclimb job %s not found", j.ID))
		}
		return nil
	}))
}

func (s *SQLStore) AddAttempt(a *Attempt) error {
	if _, err := s.GetJob(a.JobID); err != nil {
		return err
	}
	return s.st.Atomic(context.Background(), withSQL(func(_ store.Tx, q sqlTx) error {
		res, err := q.Exec(`INSERT INTO hillclimb_attempts(job_id,agent_id,branch,value,status,measured_at,log) VALUES(?,?,?,?,?,?,?)`,
			a.JobID, a.AgentID, a.Branch, a.Value, a.Status, a.MeasuredAt.UnixMilli(), a.Log)
		if err != nil {
			return err
		}
		a.ID, err = res.LastInsertId()
		return err
	}))
}

func (s *SQLStore) ListAttempts(jobID string) ([]Attempt, error) {
	if _, err := s.GetJob(jobID); err != nil {
		return nil, err
	}
	out := []Attempt{}
	err := s.st.View(context.Background(), withSQL(func(_ store.Tx, q sqlTx) error {
		rows, err := q.Query(`SELECT id,job_id,agent_id,branch,value,status,measured_at,log FROM hillclimb_attempts WHERE job_id=? ORDER BY id`, jobID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var a Attempt
			var v sql.NullFloat64
			var at int64
			if err := rows.Scan(&a.ID, &a.JobID, &a.AgentID, &a.Branch, &v, &a.Status, &at, &a.Log); err != nil {
				return err
			}
			if v.Valid {
				a.Value = &v.Float64
			}
			a.MeasuredAt = time.UnixMilli(at).UTC()
			out = append(out, a)
		}
		return rows.Err()
	}))
	return out, err
}

func jobArgs(j *Job) []any {
	return []any{j.ID, j.BriefID, j.Repo, j.MetricCmd, j.Direction, j.Target, j.Budget, j.PlateauN, j.Status,
		j.StopReason, j.Best, j.BestAttemptID, j.Attempts, j.SinceBest, j.CreatedAt.UnixMilli()}
}

func (s *SQLStore) jobs(where string, args ...any) ([]Job, error) {
	out := []Job{}
	err := s.st.View(context.Background(), withSQL(func(_ store.Tx, q sqlTx) error {
		rows, err := q.Query(`SELECT `+jobCols+` FROM hillclimb_jobs `+where, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var j Job
			var target, best sql.NullFloat64
			var reason sql.NullString
			var bestID sql.NullInt64
			var at int64
			if err := rows.Scan(&j.ID, &j.BriefID, &j.Repo, &j.MetricCmd, &j.Direction, &target, &j.Budget, &j.PlateauN,
				&j.Status, &reason, &best, &bestID, &j.Attempts, &j.SinceBest, &at); err != nil {
				return err
			}
			if target.Valid {
				j.Target = &target.Float64
			}
			if best.Valid {
				j.Best = &best.Float64
			}
			if reason.Valid {
				j.StopReason = &reason.String
			}
			if bestID.Valid {
				j.BestAttemptID = &bestID.Int64
			}
			j.CreatedAt = time.UnixMilli(at).UTC()
			out = append(out, j)
		}
		return rows.Err()
	}))
	return out, err
}
