package schedule

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS schedules (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, cron TEXT NOT NULL, timezone TEXT NOT NULL, action TEXT NOT NULL,
  spec TEXT NOT NULL, enabled INTEGER NOT NULL, max_active INTEGER NOT NULL, catch_up TEXT NOT NULL,
  cursor_at INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS schedule_runs (
  schedule_id TEXT NOT NULL, instant INTEGER NOT NULL, status TEXT NOT NULL, missed INTEGER NOT NULL DEFAULT 0,
  detail TEXT NOT NULL DEFAULT '', brief_id TEXT NOT NULL DEFAULT '', task_ids TEXT NOT NULL DEFAULT '[]',
  started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (schedule_id, instant)
);
`

const pgSchema = `
CREATE TABLE IF NOT EXISTS schedules (
  id TEXT COLLATE "C" PRIMARY KEY, name TEXT NOT NULL, cron TEXT NOT NULL, timezone TEXT NOT NULL, action TEXT NOT NULL,
  spec TEXT NOT NULL, enabled INTEGER NOT NULL, max_active INTEGER NOT NULL, catch_up TEXT NOT NULL,
  cursor_at BIGINT NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS schedule_runs (
  schedule_id TEXT COLLATE "C" NOT NULL, instant BIGINT NOT NULL, status TEXT NOT NULL, missed INTEGER NOT NULL DEFAULT 0,
  detail TEXT NOT NULL DEFAULT '', brief_id TEXT NOT NULL DEFAULT '', task_ids TEXT NOT NULL DEFAULT '[]',
  started_at BIGINT NOT NULL, finished_at BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (schedule_id, instant)
);
`

const (
	scheduleCols = `s.id,s.name,s.cron,s.timezone,s.action,s.spec,s.enabled,s.max_active,s.catch_up,s.cursor_at,s.created_at,s.updated_at,
  COALESCE((SELECT MAX(r.instant) FROM schedule_runs r WHERE r.schedule_id=s.id AND r.status<>'skipped'),0)`
	runCols = `schedule_id,instant,status,missed,detail,brief_id,task_ids,started_at,finished_at`
)

func init() {
	sqlite.RegisterSchema(schema)
	postgres.RegisterSchema(pgSchema, "schedules", "schedule_runs")
}

type specJSON struct {
	Brief   *BriefSpec `json:"brief"`
	BriefID string     `json:"brief_id"`
	Tasks   []TaskSpec `json:"tasks"`
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func flag(b bool) int {
	if b {
		return 1
	}
	return 0
}

func insert(t store.Tx, q store.SQL, s *Schedule) error {
	id, err := t.NextID("SC")
	if err != nil {
		return err
	}
	spec, err := json.Marshal(specJSON{Brief: s.Brief, BriefID: s.BriefID, Tasks: s.Tasks})
	if err != nil {
		return err
	}
	if _, err := q.Exec(`INSERT INTO schedules(id,name,cron,timezone,action,spec,enabled,max_active,catch_up,cursor_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, s.Name, s.Cron, s.Timezone, s.Action, string(spec), flag(s.Enabled), s.MaxActive, s.CatchUp,
		millis(s.cursor), millis(s.CreatedAt), millis(s.UpdatedAt)); err != nil {
		return err
	}
	s.ID = id
	return nil
}

func update(q store.SQL, s *Schedule) error {
	spec, err := json.Marshal(specJSON{Brief: s.Brief, BriefID: s.BriefID, Tasks: s.Tasks})
	if err != nil {
		return err
	}
	res, err := q.Exec(`UPDATE schedules SET name=?,cron=?,timezone=?,action=?,spec=?,enabled=?,max_active=?,catch_up=?,cursor_at=?,updated_at=? WHERE id=?`,
		s.Name, s.Cron, s.Timezone, s.Action, string(spec), flag(s.Enabled), s.MaxActive, s.CatchUp, millis(s.cursor), millis(s.UpdatedAt), s.ID)
	if err != nil {
		return err
	}
	return requireRow(res, s.ID)
}

func requireRow(res sql.Result, id string) error {
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return errors.Join(err, model.NotFound("schedule %s not found", id))
	}
	return nil
}

func remove(q store.SQL, id string) error {
	if _, err := q.Exec(`DELETE FROM schedule_runs WHERE schedule_id=?`, id); err != nil {
		return err
	}
	res, err := q.Exec(`DELETE FROM schedules WHERE id=?`, id)
	if err != nil {
		return err
	}
	return requireRow(res, id)
}

func count(q store.SQL) (int, error) {
	rows, err := q.Query(`SELECT COUNT(*) FROM schedules`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	var n int
	if rows.Next() {
		err = rows.Scan(&n)
	}
	return n, errors.Join(err, rows.Err())
}

func query(q store.SQL, where string, args ...any) ([]*Schedule, error) {
	rows, err := q.Query(`SELECT `+scheduleCols+` FROM schedules s `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*Schedule{}
	for rows.Next() {
		var (
			s                                     Schedule
			spec                                  string
			enabled                               int
			cursor, created, updated, lastRunTime int64
		)
		if err := rows.Scan(&s.ID, &s.Name, &s.Cron, &s.Timezone, &s.Action, &spec, &enabled, &s.MaxActive, &s.CatchUp,
			&cursor, &created, &updated, &lastRunTime); err != nil {
			return nil, err
		}
		var sp specJSON
		if err := json.Unmarshal([]byte(spec), &sp); err != nil {
			return nil, err
		}
		s.Brief, s.BriefID, s.Tasks = sp.Brief, sp.BriefID, sp.Tasks
		if s.Tasks == nil {
			s.Tasks = []TaskSpec{}
		}
		s.Enabled = enabled != 0
		s.cursor, s.CreatedAt, s.UpdatedAt = fromMillis(cursor), fromMillis(created), fromMillis(updated)
		if lastRunTime > 0 {
			t := fromMillis(lastRunTime)
			s.LastRunAt = &t
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

func list(q store.SQL) ([]*Schedule, error) {
	return query(q, `ORDER BY s.created_at, length(s.id), s.id`)
}

func get(q store.SQL, id string) (*Schedule, error) {
	out, err := query(q, `WHERE s.id=?`, id)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, model.NotFound("schedule %s not found", id)
	}
	return out[0], nil
}

func queryRuns(q store.SQL, where string, args ...any) ([]Run, error) {
	rows, err := q.Query(`SELECT `+runCols+` FROM schedule_runs `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Run{}
	for rows.Next() {
		var (
			r                     Run
			instant, start, ended int64
			ids                   string
		)
		if err := rows.Scan(&r.ScheduleID, &instant, &r.Status, &r.Missed, &r.Detail, &r.BriefID, &ids, &start, &ended); err != nil {
			return nil, err
		}
		r.ScheduledFor, r.StartedAt = fromMillis(instant), fromMillis(start)
		if ended > 0 {
			t := fromMillis(ended)
			r.FinishedAt = &t
		}
		if err := json.Unmarshal([]byte(ids), &r.TaskIDs); err != nil || r.TaskIDs == nil {
			r.TaskIDs = []string{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func runs(q store.SQL, id string, limit int) ([]Run, error) {
	return queryRuns(q, `WHERE schedule_id=? ORDER BY instant DESC LIMIT ?`, id, limit)
}

func claim(q store.SQL, s *Schedule, instant, now time.Time, status string, missed int, detail string) (bool, error) {
	var finished int64
	if status != RunRunning {
		finished = millis(now)
	}
	res, err := q.Exec(`INSERT INTO schedule_runs(`+runCols+`) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(schedule_id, instant) DO NOTHING`,
		s.ID, millis(instant), status, missed, detail, "", "[]", millis(now), finished)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	_, err = q.Exec(`UPDATE schedules SET cursor_at=? WHERE id=? AND cursor_at<?`, millis(instant), s.ID, millis(instant))
	return n > 0, err
}

func finish(q store.SQL, id string, instant, now time.Time, status, detail, briefID string, taskIDs []string) error {
	ids, err := json.Marshal(append([]string{}, taskIDs...))
	if err != nil {
		return err
	}
	_, err = q.Exec(`UPDATE schedule_runs SET status=?,detail=?,brief_id=?,task_ids=?,finished_at=? WHERE schedule_id=? AND instant=?`,
		status, detail, briefID, string(ids), millis(now), id, millis(instant))
	return err
}

func interrupt(q store.SQL, now time.Time) (int64, error) {
	res, err := q.Exec(`UPDATE schedule_runs SET status=?,detail=?,finished_at=? WHERE status=?`,
		RunInterrupted, "the server stopped before the run finished", millis(now), RunRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
