package agentout

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS agent_output (
  agent_id TEXT NOT NULL, seq INTEGER NOT NULL, task_id TEXT NOT NULL DEFAULT '', stream TEXT NOT NULL,
  text TEXT NOT NULL, ts INTEGER NOT NULL, PRIMARY KEY (agent_id, seq)
);
`

const pgSchema = `
CREATE TABLE IF NOT EXISTS agent_output (
  agent_id TEXT COLLATE "C" NOT NULL, seq BIGINT NOT NULL, task_id TEXT NOT NULL DEFAULT '', stream TEXT NOT NULL,
  text TEXT NOT NULL, ts BIGINT NOT NULL, PRIMARY KEY (agent_id, seq)
);
`

func init() {
	sqlite.RegisterSchema(schema)
	postgres.RegisterSchema(pgSchema, "agent_output")
}

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("agentout: store transaction does not expose SQL")

func withSQL(fn func(sqlTx) error) func(store.Tx) error {
	return func(t store.Tx) error {
		q, ok := t.(sqlTx)
		if !ok {
			return errNoSQL
		}
		return fn(q)
	}
}

func persist(ctx context.Context, st store.Store, batches []batch, keep int) error {
	return st.Atomic(ctx, withSQL(func(q sqlTx) error {
		for _, bt := range batches {
			for _, l := range bt.lines {
				task := ""
				if l.TaskID != nil {
					task = *l.TaskID
				}
				if _, err := q.Exec(`INSERT INTO agent_output(agent_id,seq,task_id,stream,text,ts) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
					bt.agent, l.Seq, task, l.Stream, l.Text, l.TS.UnixMilli()); err != nil {
					return err
				}
			}
			if _, err := q.Exec(`DELETE FROM agent_output WHERE agent_id=? AND seq<=?`, bt.agent, bt.seq-int64(keep)); err != nil {
				return err
			}
		}
		return nil
	}))
}

func load(ctx context.Context, st store.Store) ([]Line, error) {
	var out []Line
	err := st.View(ctx, withSQL(func(q sqlTx) error {
		rows, err := q.Query(`SELECT agent_id,seq,task_id,stream,text,ts FROM agent_output ORDER BY agent_id,seq`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				l    Line
				task string
				ts   int64
			)
			if err := rows.Scan(&l.AgentID, &l.Seq, &task, &l.Stream, &l.Text, &ts); err != nil {
				return err
			}
			if task != "" {
				l.TaskID = &task
			}
			l.TS = time.UnixMilli(ts).UTC()
			out = append(out, l)
		}
		return rows.Err()
	}))
	return out, err
}
