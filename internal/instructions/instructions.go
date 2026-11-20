package instructions

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	KindRules     = "rules"
	KindStyle     = "style"
	KindTemplates = "templates"

	ScopeGlobal   = "global"
	projectPrefix = "project:"

	maxTextBytes = 256 << 10
)

var Kinds = []string{KindRules, KindStyle, KindTemplates}

var projectIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

type Meta struct {
	Scope     string    `json:"scope"`
	Kind      string    `json:"kind"`
	Version   int       `json:"version"`
	Author    string    `json:"author"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type Version struct {
	Meta
	Text string `json:"text"`
}

type Effective struct {
	Text    string `json:"text"`
	Version string `json:"version"`
}

type Event struct {
	Actor   model.Actor
	Tool    string
	Scope   string
	Kind    string
	Before  int
	After   int
	Message string
}

type Auditor interface {
	Record(ctx context.Context, e Event) error
}

type Service struct {
	st  store.Store
	au  Auditor
	now func() time.Time
}

func New(st store.Store, au Auditor, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, au: au, now: now}
}

func ProjectScope(projectID string) string {
	return projectPrefix + projectID
}

func validate(scope, kind string) error {
	if scope != ScopeGlobal {
		id, ok := strings.CutPrefix(scope, projectPrefix)
		if !ok || !projectIDPattern.MatchString(id) {
			return model.BadRequest("scope must be %q or %q<id>", ScopeGlobal, projectPrefix)
		}
	}
	for _, k := range Kinds {
		if k == kind {
			return nil
		}
	}
	return model.BadRequest("kind must be one of %s", strings.Join(Kinds, ", "))
}

func headKey(scope, kind string) string {
	return "instructions/" + scope + "/" + kind
}

func versionKey(scope, kind string, n int) string {
	return headKey(scope, kind) + "/" + strconv.Itoa(n)
}

type stored struct {
	Author    string `json:"author"`
	Message   string `json:"message"`
	CreatedAt int64  `json:"created_at"`
	Text      string `json:"text"`
}

func head(tx store.Tx, scope, kind string) (int, error) {
	s, ok, err := tx.GetKV(headKey(scope, kind))
	if err != nil || !ok {
		return 0, err
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("instructions: corrupt head for %s/%s: %w", scope, kind, err)
	}
	return n, nil
}

func load(tx store.Tx, scope, kind string, n int) (*Version, error) {
	s, ok, err := tx.GetKV(versionKey(scope, kind, n))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, model.NotFound("%s %s has no version %d", scope, kind, n)
	}
	var st stored
	if err := json.Unmarshal([]byte(s), &st); err != nil {
		return nil, fmt.Errorf("instructions: corrupt version %s/%s/%d: %w", scope, kind, n, err)
	}
	return &Version{
		Meta: Meta{Scope: scope, Kind: kind, Version: n, Author: st.Author, Message: st.Message, CreatedAt: time.UnixMilli(st.CreatedAt).UTC()},
		Text: st.Text,
	}, nil
}

func (s *Service) append(tx store.Tx, actor model.Actor, scope, kind, text, message string) (*Version, int, error) {
	prev, err := head(tx, scope, kind)
	if err != nil {
		return nil, 0, err
	}
	v := &Version{
		Meta: Meta{Scope: scope, Kind: kind, Version: prev + 1, Author: actor.ID, Message: message, CreatedAt: s.now().UTC().Truncate(time.Millisecond)},
		Text: text,
	}
	raw, err := json.Marshal(stored{Author: v.Author, Message: v.Message, CreatedAt: v.CreatedAt.UnixMilli(), Text: text})
	if err != nil {
		return nil, 0, err
	}
	if err := tx.SetKV(versionKey(scope, kind, v.Version), string(raw)); err != nil {
		return nil, 0, err
	}
	if err := tx.SetKV(headKey(scope, kind), strconv.Itoa(v.Version)); err != nil {
		return nil, 0, err
	}
	return v, prev, nil
}

func (s *Service) audit(ctx context.Context, actor model.Actor, tool string, v *Version, before int) error {
	if s.au == nil {
		return nil
	}
	return s.au.Record(ctx, Event{Actor: actor, Tool: tool, Scope: v.Scope, Kind: v.Kind, Before: before, After: v.Version, Message: v.Message})
}

func (s *Service) Save(ctx context.Context, actor model.Actor, scope, kind, text, message string) (*Version, error) {
	if err := validate(scope, kind); err != nil {
		return nil, err
	}
	if len(text) > maxTextBytes {
		return nil, model.BadRequest("text exceeds %d bytes", maxTextBytes)
	}
	var v *Version
	var before int
	err := s.st.Atomic(ctx, func(tx store.Tx) error {
		var err error
		v, before, err = s.append(tx, actor, scope, kind, text, message)
		return err
	})
	if err != nil {
		return nil, err
	}
	return v, s.audit(ctx, actor, "instructions_save", v, before)
}

func (s *Service) Rollback(ctx context.Context, actor model.Actor, scope, kind string, to int, message string) (*Version, error) {
	if err := validate(scope, kind); err != nil {
		return nil, err
	}
	var v *Version
	var before int
	err := s.st.Atomic(ctx, func(tx store.Tx) error {
		cur, err := head(tx, scope, kind)
		if err != nil {
			return err
		}
		if to == cur {
			return model.Conflict("%s %s is already at version %d", scope, kind, to)
		}
		target, err := load(tx, scope, kind, to)
		if err != nil {
			return err
		}
		if message == "" {
			message = fmt.Sprintf("rollback to v%d", to)
		}
		v, before, err = s.append(tx, actor, scope, kind, target.Text, message)
		return err
	})
	if err != nil {
		return nil, err
	}
	return v, s.audit(ctx, actor, "instructions_rollback", v, before)
}

func (s *Service) Get(ctx context.Context, scope, kind string) (*Version, error) {
	if err := validate(scope, kind); err != nil {
		return nil, err
	}
	var v *Version
	err := s.st.View(ctx, func(tx store.Tx) error {
		n, err := head(tx, scope, kind)
		if err != nil {
			return err
		}
		if n == 0 {
			return model.NotFound("%s %s has no versions", scope, kind)
		}
		v, err = load(tx, scope, kind, n)
		return err
	})
	return v, err
}

func (s *Service) GetVersion(ctx context.Context, scope, kind string, n int) (*Version, error) {
	if err := validate(scope, kind); err != nil {
		return nil, err
	}
	var v *Version
	err := s.st.View(ctx, func(tx store.Tx) error {
		var err error
		v, err = load(tx, scope, kind, n)
		return err
	})
	return v, err
}

func (s *Service) History(ctx context.Context, scope, kind string) ([]Meta, error) {
	if err := validate(scope, kind); err != nil {
		return nil, err
	}
	var out []Meta
	err := s.st.View(ctx, func(tx store.Tx) error {
		n, err := head(tx, scope, kind)
		if err != nil {
			return err
		}
		for i := n; i >= 1; i-- {
			v, err := load(tx, scope, kind, i)
			if err != nil {
				return err
			}
			out = append(out, v.Meta)
		}
		return nil
	})
	return out, err
}

func (s *Service) Diff(ctx context.Context, scope, kind string, from, to int) ([]DiffLine, error) {
	a, err := s.GetVersion(ctx, scope, kind, from)
	if err != nil {
		return nil, err
	}
	b, err := s.GetVersion(ctx, scope, kind, to)
	if err != nil {
		return nil, err
	}
	return LineDiff(a.Text, b.Text), nil
}

func scopesFor(projectID string) ([]string, error) {
	if projectID == "" {
		return []string{ScopeGlobal}, nil
	}
	if !projectIDPattern.MatchString(projectID) {
		return nil, model.BadRequest("invalid project id %q", projectID)
	}
	return []string{ScopeGlobal, ProjectScope(projectID)}, nil
}

func (s *Service) Effective(ctx context.Context, projectID string) (*Effective, error) {
	scopes, err := scopesFor(projectID)
	if err != nil {
		return nil, err
	}
	texts := make([][]string, len(Kinds))
	versions := make([][]int, len(scopes))
	err = s.st.View(ctx, func(tx store.Tx) error {
		for si, scope := range scopes {
			versions[si] = make([]int, len(Kinds))
			for ki, kind := range Kinds {
				n, err := head(tx, scope, kind)
				if err != nil {
					return err
				}
				versions[si][ki] = n
				if n == 0 {
					continue
				}
				v, err := load(tx, scope, kind, n)
				if err != nil {
					return err
				}
				texts[ki] = append(texts[ki], v.Text)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var parts []string
	for ki, kind := range Kinds {
		if merged := Merge(texts[ki]...); merged != "" {
			parts = append(parts, "# "+strings.ToUpper(kind[:1])+kind[1:]+"\n\n"+merged)
		}
	}
	return &Effective{Text: strings.Join(parts, "\n\n"), Version: versionString(versions, len(scopes) > 1)}, nil
}

func (s *Service) Version(ctx context.Context, projectID string) (string, error) {
	scopes, err := scopesFor(projectID)
	if err != nil {
		return "", err
	}
	versions := make([][]int, len(scopes))
	err = s.st.View(ctx, func(tx store.Tx) error {
		for si, scope := range scopes {
			versions[si] = make([]int, len(Kinds))
			for ki, kind := range Kinds {
				n, err := head(tx, scope, kind)
				if err != nil {
					return err
				}
				versions[si][ki] = n
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return versionString(versions, len(scopes) > 1), nil
}

func versionString(versions [][]int, withProject bool) string {
	join := func(prefix string, ns []int) string {
		p := make([]string, len(ns))
		for i, n := range ns {
			p[i] = strconv.Itoa(n)
		}
		return prefix + strings.Join(p, ".")
	}
	out := join("g", versions[0])
	if withProject {
		out += "/" + join("p", versions[1])
	}
	return out
}
