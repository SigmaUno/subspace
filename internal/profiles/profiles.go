package profiles

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	seededKey      = "agent_profiles.seeded"
	maxDescription = 500
	maxCallbackURL = 2048
)

var (
	nameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	providerRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	modelRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$`)
	kinds      = []string{model.KindClaudeCode, model.KindCodex, model.KindOpenCode, model.KindOther}
)

type Profile struct {
	Name        string    `json:"name"`
	Role        string    `json:"role"`
	Kind        string    `json:"kind"`
	Model       string    `json:"model"`
	Provider    string    `json:"provider"`
	ExpiresIn   string    `json:"expires_in"`
	Count       int       `json:"count"`
	CallbackURL string    `json:"callback_url"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func Builtin() []Profile {
	return []Profile{
		{Name: "claude-code-worker", Role: model.RoleWorker, Kind: model.KindClaudeCode, Model: "claude-sonnet-5-5", Provider: "anthropic",
			Description: "Claude Code worker on Anthropic"},
		{Name: "codex-worker", Role: model.RoleWorker, Kind: model.KindCodex, Model: "gpt-5-codex", Provider: "openai",
			Description: "Codex worker on OpenAI"},
		{Name: "opencode-deepseek-worker", Role: model.RoleWorker, Kind: model.KindOpenCode, Model: "deepseek/deepseek-chat", Provider: "deepseek",
			Description: "OpenCode worker on DeepSeek"},
	}
}

func (p *Profile) Validate() error {
	p.Description = strings.TrimSpace(p.Description)
	if !nameRe.MatchString(p.Name) {
		return model.BadRequest("name must match [a-z0-9][a-z0-9_-]{0,62}")
	}
	if p.Role == "" {
		p.Role = model.RoleWorker
	}
	if !model.ValidAgentRole(p.Role) {
		return model.BadRequest("role must be leader, worker, tester or reviewer")
	}
	if !contains(kinds, p.Kind) {
		return model.BadRequest("kind must be %s", strings.Join(kinds, ", "))
	}
	if p.Model != "" && !modelRe.MatchString(p.Model) {
		return model.BadRequest("model must match [A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}")
	}
	if p.Provider != "" {
		if !providerRe.MatchString(p.Provider) {
			return model.BadRequest("provider must match [a-z0-9][a-z0-9._-]{0,62}")
		}
		if (p.Kind == model.KindClaudeCode || p.Kind == model.KindCodex) && p.Provider != model.ProviderForKind(p.Kind) {
			return model.BadRequest("provider for kind %s must be %s", p.Kind, model.ProviderForKind(p.Kind))
		}
	}
	if _, err := board.ParseExpiresIn(p.ExpiresIn); err != nil {
		return err
	}
	if p.Count < 0 || p.Count > board.MaxBulk {
		return model.BadRequest("count must be 0..%d", board.MaxBulk)
	}
	if len(p.Description) > maxDescription {
		return model.BadRequest("description must be at most %d characters", maxDescription)
	}
	return validCallback(p.CallbackURL)
}

func validCallback(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || len(raw) > maxCallbackURL || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return model.BadRequest("callback_url must be an http(s) URL without credentials, at most %d bytes", maxCallbackURL)
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func fill(dst *string, src string) {
	if *dst == "" {
		*dst = src
	}
}

func (p Profile) Apply(role, kind, mdl, provider *string) {
	sameKind := *kind == "" || *kind == p.Kind
	fill(role, p.Role)
	fill(kind, p.Kind)
	fill(mdl, p.Model)
	if sameKind {
		fill(provider, p.Provider)
	}
}

func (p Profile) ApplyFields(f Fields, bulk bool) {
	role, kind, mdl, provider := f.Text("role"), f.Text("kind"), f.Text("model"), f.Text("provider")
	p.Apply(&role, &kind, &mdl, &provider)
	f.Fill("role", role)
	f.Fill("kind", kind)
	f.Fill("model", mdl)
	f.Fill("provider", provider)
	f.Fill("expires_in", p.ExpiresIn)
	if bulk && f.zero("count") && p.Count > 0 {
		f["count"] = json.RawMessage(strconv.Itoa(p.Count))
	}
	if !bulk {
		f.Fill("callback_url", p.CallbackURL)
	}
}

type Store struct {
	St  store.Store
	Now func() time.Time
}

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("profiles: store transaction does not expose SQL")

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

const cols = `name, role, kind, model, provider, expires_in, bulk_count, callback_url, description, created_at, updated_at`

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func scan(rows *sql.Rows) ([]Profile, error) {
	defer func() { _ = rows.Close() }()
	out := []Profile{}
	for rows.Next() {
		var p Profile
		var created, updated int64
		if err := rows.Scan(&p.Name, &p.Role, &p.Kind, &p.Model, &p.Provider, &p.ExpiresIn, &p.Count, &p.CallbackURL, &p.Description, &created, &updated); err != nil {
			return nil, err
		}
		p.CreatedAt, p.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

func query(tx store.Tx, where string, args ...any) ([]Profile, error) {
	q, err := sqlOf(tx)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(`SELECT `+cols+` FROM agent_profiles `+where, args...)
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

func load(tx store.Tx, name string) (*Profile, error) {
	ps, err := query(tx, `WHERE name = ?`, name)
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, model.NotFound("profile %s not found", name)
	}
	return &ps[0], nil
}

func insert(tx store.Tx, p Profile) error {
	q, err := sqlOf(tx)
	if err != nil {
		return err
	}
	_, err = q.Exec(`INSERT INTO agent_profiles (`+cols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Role, p.Kind, p.Model, p.Provider, p.ExpiresIn, p.Count, p.CallbackURL, p.Description, p.CreatedAt.UnixMilli(), p.UpdatedAt.UnixMilli())
	return err
}

func (s *Store) List(ctx context.Context) ([]Profile, error) {
	var out []Profile
	err := s.St.View(ctx, func(tx store.Tx) error {
		var err error
		out, err = query(tx, `ORDER BY name`)
		return err
	})
	return out, err
}

func (s *Store) Get(ctx context.Context, name string) (Profile, error) {
	var out Profile
	err := s.St.View(ctx, func(tx store.Tx) error {
		p, err := load(tx, name)
		if p != nil {
			out = *p
		}
		return err
	})
	return out, err
}

func snapshot(p *Profile) map[string]any {
	return map[string]any{"name": p.Name, "role": p.Role, "kind": p.Kind, "model": p.Model, "provider": p.Provider,
		"expires_in": p.ExpiresIn, "count": p.Count, "callback_url": p.CallbackURL != ""}
}

func (s *Store) audit(ctx context.Context, tx store.Tx, tool string, before, after *Profile) error {
	e := &model.AuditEvent{TS: s.now(), AgentID: model.Owner.ID, Role: model.Owner.Role, Tool: tool}
	name := ""
	if before != nil {
		e.Before, name = snapshot(before), before.Name
	}
	if after != nil {
		e.After, name = snapshot(after), after.Name
	}
	note := auth.AuditNote(ctx, "profile "+name)
	e.Note = &note
	return tx.InsertAudit(e)
}

func (s *Store) Create(ctx context.Context, p Profile) (Profile, error) {
	if err := p.Validate(); err != nil {
		return Profile{}, err
	}
	p.CreatedAt = s.now()
	p.UpdatedAt = p.CreatedAt
	err := s.St.Atomic(ctx, func(tx store.Tx) error {
		if ps, err := query(tx, `WHERE name = ?`, p.Name); err != nil {
			return err
		} else if len(ps) > 0 {
			return model.Conflict("profile %s already exists", p.Name)
		}
		if err := insert(tx, p); err != nil {
			return err
		}
		return s.audit(ctx, tx, "create_agent_profile", nil, &p)
	})
	return p, err
}

func (s *Store) Update(ctx context.Context, name string, p Profile) (Profile, error) {
	p.Name = name
	if err := p.Validate(); err != nil {
		return Profile{}, err
	}
	err := s.St.Atomic(ctx, func(tx store.Tx) error {
		old, err := load(tx, name)
		if err != nil {
			return err
		}
		p.CreatedAt, p.UpdatedAt = old.CreatedAt, s.now()
		q, err := sqlOf(tx)
		if err != nil {
			return err
		}
		if _, err := q.Exec(`UPDATE agent_profiles SET role = ?, kind = ?, model = ?, provider = ?, expires_in = ?, bulk_count = ?,
			callback_url = ?, description = ?, updated_at = ? WHERE name = ?`,
			p.Role, p.Kind, p.Model, p.Provider, p.ExpiresIn, p.Count, p.CallbackURL, p.Description, p.UpdatedAt.UnixMilli(), name); err != nil {
			return err
		}
		return s.audit(ctx, tx, "update_agent_profile", old, &p)
	})
	return p, err
}

type InUse func(tx store.Tx, name string) error

func (s *Store) Delete(ctx context.Context, name string, inUse InUse) error {
	return s.St.Atomic(ctx, func(tx store.Tx) error {
		old, err := load(tx, name)
		if err != nil {
			return err
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
		if _, err := q.Exec(`DELETE FROM agent_profiles WHERE name = ?`, name); err != nil {
			return err
		}
		return s.audit(ctx, tx, "delete_agent_profile", old, nil)
	})
}

func (s *Store) Seed(ctx context.Context) error {
	return s.St.Atomic(ctx, func(tx store.Tx) error {
		if _, done, err := tx.GetKV(seededKey); err != nil || done {
			return err
		}
		now := s.now()
		for _, p := range Builtin() {
			p.CreatedAt, p.UpdatedAt = now, now
			if ps, err := query(tx, `WHERE name = ?`, p.Name); err != nil {
				return err
			} else if len(ps) > 0 {
				continue
			}
			if err := insert(tx, p); err != nil {
				return err
			}
		}
		return tx.SetKV(seededKey, "true")
	})
}
