package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS mcp_gateways (
  name TEXT PRIMARY KEY, config TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
`

const pgSchema = `
CREATE TABLE IF NOT EXISTS mcp_gateways (
  name TEXT COLLATE "C" PRIMARY KEY, config TEXT NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL
);
`

func init() {
	sqlite.RegisterSchema(schema)
	postgres.RegisterSchema(pgSchema, "mcp_gateways")
}

type Store struct {
	St       store.Store
	Now      func() time.Time
	Commands []string
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Millisecond)
	}
	return time.Now().UTC().Truncate(time.Millisecond)
}

func query(tx store.Tx, where string, args ...any) ([]Gateway, error) {
	q, err := store.SQLOf(tx)
	if err != nil {
		return nil, err
	}
	return store.QueryAll(q, scanGateway, `SELECT name, config, created_at, updated_at FROM mcp_gateways `+where, args...)
}

func scanGateway(rows *sql.Rows) (Gateway, error) {
	var (
		name, config     string
		created, updated int64
		g                Gateway
	)
	if err := rows.Scan(&name, &config, &created, &updated); err != nil {
		return Gateway{}, err
	}
	if err := json.Unmarshal([]byte(config), &g); err != nil {
		return Gateway{}, fmt.Errorf("gateway: stored gateway %s: %w", name, err)
	}
	g.Name = name
	g.CreatedAt, g.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	return g, nil
}

func load(tx store.Tx, name string) (*Gateway, error) {
	gs, err := query(tx, `WHERE name = ?`, name)
	if err != nil {
		return nil, err
	}
	if len(gs) == 0 {
		return nil, model.NotFound("gateway %s not found", name)
	}
	return &gs[0], nil
}

func (s *Store) List(ctx context.Context) ([]Gateway, error) {
	var out []Gateway
	err := s.St.View(ctx, func(tx store.Tx) error {
		var err error
		out, err = query(tx, `ORDER BY name`)
		return err
	})
	return out, err
}

func (s *Store) Get(ctx context.Context, name string) (Gateway, error) {
	var out Gateway
	err := s.St.View(ctx, func(tx store.Tx) error {
		g, err := load(tx, name)
		if g != nil {
			out = *g
		}
		return err
	})
	return out, err
}

func snapshot(g *Gateway) map[string]any {
	out := map[string]any{}
	config, err := encode(*g)
	if err == nil {
		_ = json.Unmarshal([]byte(config), &out)
	}
	delete(out, "created_at")
	delete(out, "updated_at")
	return out
}

func (s *Store) audit(ctx context.Context, tx store.Tx, tool string, before, after *Gateway) error {
	e := &model.AuditEvent{TS: s.now(), AgentID: model.Owner.ID, Role: model.Owner.Role, Tool: tool}
	name := ""
	if before != nil {
		e.Before, name = snapshot(before), before.Name
	}
	if after != nil {
		e.After, name = snapshot(after), after.Name
	}
	note := auth.AuditNote(ctx, "gateway "+name)
	e.Note = &note
	return tx.InsertAudit(e)
}

func encode(g Gateway) (string, error) {
	g.CreatedAt, g.UpdatedAt = time.Time{}, time.Time{}
	b, err := json.Marshal(g)
	return string(b), err
}

func (s *Store) Create(ctx context.Context, g Gateway) (Gateway, error) {
	if err := g.Validate(s.Commands); err != nil {
		return Gateway{}, err
	}
	g.CreatedAt = s.now()
	g.UpdatedAt = g.CreatedAt
	config, err := encode(g)
	if err != nil {
		return Gateway{}, err
	}
	err = s.St.Atomic(ctx, func(tx store.Tx) error {
		if gs, err := query(tx, `WHERE name = ?`, g.Name); err != nil {
			return err
		} else if len(gs) > 0 {
			return model.Conflict("gateway %s already exists", g.Name)
		}
		q, err := store.SQLOf(tx)
		if err != nil {
			return err
		}
		if _, err := q.Exec(`INSERT INTO mcp_gateways (name, config, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			g.Name, config, g.CreatedAt.UnixMilli(), g.UpdatedAt.UnixMilli()); err != nil {
			return err
		}
		return s.audit(ctx, tx, "create_gateway", nil, &g)
	})
	return g, err
}

func (s *Store) Update(ctx context.Context, name string, g Gateway) (Gateway, error) {
	var out Gateway
	err := s.St.Atomic(ctx, func(tx store.Tx) error {
		old, err := load(tx, name)
		if err != nil {
			return err
		}
		g.Name = old.Name
		if err := g.Validate(s.Commands); err != nil {
			return err
		}
		g.CreatedAt, g.UpdatedAt = old.CreatedAt, s.now()
		if !g.UpdatedAt.After(old.UpdatedAt) {
			g.UpdatedAt = old.UpdatedAt.Add(time.Millisecond)
		}
		config, err := encode(g)
		if err != nil {
			return err
		}
		q, err := store.SQLOf(tx)
		if err != nil {
			return err
		}
		if _, err := q.Exec(`UPDATE mcp_gateways SET config = ?, updated_at = ? WHERE name = ?`, config, g.UpdatedAt.UnixMilli(), old.Name); err != nil {
			return err
		}
		out = g
		return s.audit(ctx, tx, "update_gateway", old, &g)
	})
	return out, err
}

func (s *Store) Delete(ctx context.Context, name string) error {
	return s.St.Atomic(ctx, func(tx store.Tx) error {
		old, err := load(tx, name)
		if err != nil {
			return err
		}
		q, err := store.SQLOf(tx)
		if err != nil {
			return err
		}
		if _, err := q.Exec(`DELETE FROM mcp_gateways WHERE name = ?`, old.Name); err != nil {
			return err
		}
		return s.audit(ctx, tx, "delete_gateway", old, nil)
	})
}
