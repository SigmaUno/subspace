package playbooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/planlint"
	"github.com/SigmaUno/subspace/internal/store"
)

const maxDescription = 500

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

type Playbook struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Builtin     bool   `json:"builtin"`
	planlint.Rules
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (p *Playbook) Validate() error {
	p.Description = strings.TrimSpace(p.Description)
	if !nameRe.MatchString(p.Name) {
		return model.BadRequest("name must match [a-z0-9][a-z0-9_-]{0,62}")
	}
	if len(p.Description) > maxDescription {
		return model.BadRequest("description must be at most %d characters", maxDescription)
	}
	return p.Rules.Validate()
}

type Store struct {
	St  store.Store
	Now func() time.Time
}

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("playbooks: store transaction does not expose SQL")

func sqlOf(tx store.Tx) (sqlTx, error) {
	q, ok := tx.(sqlTx)
	if !ok {
		return nil, errNoSQL
	}
	return q, nil
}

func SupportsSQL(ctx context.Context, st store.Store) bool {
	ok := false
	_ = st.View(ctx, func(tx store.Tx) error {
		_, ok = tx.(sqlTx)
		return nil
	})
	return ok
}

const cols = `name, description, builtin, rules, created_at, updated_at`

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func scan(rows *sql.Rows) ([]Playbook, error) {
	defer func() { _ = rows.Close() }()
	out := []Playbook{}
	for rows.Next() {
		var p Playbook
		var builtin int
		var rules string
		var created, updated int64
		if err := rows.Scan(&p.Name, &p.Description, &builtin, &rules, &created, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(rules), &p.Rules); err != nil {
			return nil, err
		}
		p.Normalize()
		p.Builtin = builtin != 0
		p.CreatedAt, p.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func query(tx store.Tx, where string, args ...any) ([]Playbook, error) {
	q, err := sqlOf(tx)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(`SELECT `+cols+` FROM playbooks `+where, args...)
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

func Load(tx store.Tx, name string) (*Playbook, error) {
	ps, err := query(tx, `WHERE name = ?`, name)
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, model.NotFound("playbook %s not found", name)
	}
	return &ps[0], nil
}

func Lookup(tx store.Tx, name string) (*planlint.Rules, error) {
	p, err := Load(tx, name)
	if err != nil {
		return nil, err
	}
	return &p.Rules, nil
}

func insert(tx store.Tx, p Playbook) error {
	q, err := sqlOf(tx)
	if err != nil {
		return err
	}
	rules, err := json.Marshal(p.Rules)
	if err != nil {
		return err
	}
	builtin := 0
	if p.Builtin {
		builtin = 1
	}
	_, err = q.Exec(`INSERT INTO playbooks (`+cols+`) VALUES (?, ?, ?, ?, ?, ?)`,
		p.Name, p.Description, builtin, string(rules), p.CreatedAt.UnixMilli(), p.UpdatedAt.UnixMilli())
	return err
}

func (s *Store) List(ctx context.Context) ([]Playbook, error) {
	var out []Playbook
	err := s.St.View(ctx, func(tx store.Tx) error {
		var err error
		out, err = query(tx, `ORDER BY name`)
		return err
	})
	return out, err
}

func (s *Store) Get(ctx context.Context, name string) (Playbook, error) {
	var out Playbook
	err := s.St.View(ctx, func(tx store.Tx) error {
		p, err := Load(tx, name)
		if p != nil {
			out = *p
		}
		return err
	})
	return out, err
}

func snapshot(p *Playbook) map[string]any {
	return map[string]any{"name": p.Name, "builtin": p.Builtin, "coverage": p.Coverage, "cross_review": p.CrossReview,
		"required_tasks": len(p.RequiredTasks), "criteria_rules": len(p.CriteriaRules), "allowed_sizes": p.AllowedSizes}
}

func (s *Store) audit(ctx context.Context, tx store.Tx, tool, note string, before, after *Playbook) error {
	e := &model.AuditEvent{TS: s.now(), AgentID: model.Owner.ID, Role: model.Owner.Role, Tool: tool}
	name := ""
	if before != nil {
		e.Before, name = snapshot(before), before.Name
	}
	if after != nil {
		e.After, name = snapshot(after), after.Name
	}
	if note == "" {
		note = "playbook " + name
	}
	note = auth.AuditNote(ctx, note)
	e.Note = &note
	return tx.InsertAudit(e)
}

func (s *Store) create(ctx context.Context, p Playbook, note string) (Playbook, error) {
	if err := p.Validate(); err != nil {
		return Playbook{}, err
	}
	p.Builtin = false
	p.CreatedAt = s.now()
	p.UpdatedAt = p.CreatedAt
	err := s.St.Atomic(ctx, func(tx store.Tx) error {
		if ps, err := query(tx, `WHERE name = ?`, p.Name); err != nil {
			return err
		} else if len(ps) > 0 {
			return model.Conflict("playbook %s already exists", p.Name)
		}
		if err := insert(tx, p); err != nil {
			return err
		}
		return s.audit(ctx, tx, "create_playbook", note, nil, &p)
	})
	return p, err
}

func (s *Store) Create(ctx context.Context, p Playbook) (Playbook, error) {
	return s.create(ctx, p, "")
}

func (s *Store) Clone(ctx context.Context, from, name, description string) (Playbook, error) {
	src, err := s.Get(ctx, from)
	if err != nil {
		return Playbook{}, err
	}
	src.Name = name
	if description != "" {
		src.Description = description
	}
	return s.create(ctx, src, "playbook "+name+" cloned from "+from)
}

func (s *Store) Update(ctx context.Context, name string, p Playbook) (Playbook, error) {
	p.Name = name
	if err := p.Validate(); err != nil {
		return Playbook{}, err
	}
	err := s.St.Atomic(ctx, func(tx store.Tx) error {
		old, err := Load(tx, name)
		if err != nil {
			return err
		}
		p.Builtin, p.CreatedAt, p.UpdatedAt = old.Builtin, old.CreatedAt, s.now()
		q, err := sqlOf(tx)
		if err != nil {
			return err
		}
		rules, err := json.Marshal(p.Rules)
		if err != nil {
			return err
		}
		if _, err := q.Exec(`UPDATE playbooks SET description = ?, rules = ?, updated_at = ? WHERE name = ?`,
			p.Description, string(rules), p.UpdatedAt.UnixMilli(), name); err != nil {
			return err
		}
		return s.audit(ctx, tx, "update_playbook", "", old, &p)
	})
	return p, err
}

type InUse func(tx store.Tx, name string) error

func (s *Store) Delete(ctx context.Context, name string, inUse InUse) error {
	return s.St.Atomic(ctx, func(tx store.Tx) error {
		old, err := Load(tx, name)
		if err != nil {
			return err
		}
		if old.Builtin {
			return model.Conflict("built-in playbook %s cannot be deleted; clone it to make a variant", name)
		}
		if inUse != nil {
			if err := inUse(tx, name); err != nil {
				return err
			}
		}
		q, err := sqlOf(tx)
		if err != nil {
			return err
		}
		if _, err := q.Exec(`DELETE FROM playbooks WHERE name = ?`, name); err != nil {
			return err
		}
		return s.audit(ctx, tx, "delete_playbook", "", old, nil)
	})
}

func (s *Store) Seed(ctx context.Context) error {
	return s.St.Atomic(ctx, func(tx store.Tx) error {
		now := s.now()
		for _, p := range Builtin() {
			if ps, err := query(tx, `WHERE name = ?`, p.Name); err != nil {
				return err
			} else if len(ps) > 0 {
				continue
			}
			p.Builtin = true
			p.CreatedAt, p.UpdatedAt = now, now
			p.Normalize()
			if err := insert(tx, p); err != nil {
				return err
			}
		}
		return nil
	})
}
