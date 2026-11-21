package repos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/redact"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	HostGitHub  = "github"
	HostForgejo = "forgejo"

	DefaultBranch = "main"

	maxName      = 200
	maxBranch    = 100
	maxPatterns  = 50
	maxCommand   = 32
	maxArg       = 500
	maxWorkDir   = 500
	maxBaseURL   = 2048
	maxListNames = 20
)

var (
	partRe     = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	branchRe   = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	patternRe  = regexp.MustCompile(`^[A-Za-z0-9._/*?-]+$`)
	tokenEnvRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	probeTasks = []string{"sub/t-1-x", "sub/t-10-add-login", "sub/t-999-a-long-task-slug"}
)

type Repo struct {
	Name              string    `json:"name"`
	Host              string    `json:"host"`
	BaseURL           string    `json:"base_url"`
	DefaultBranch     string    `json:"default_branch"`
	ProtectedBranches []string  `json:"protected_branches"`
	MergeTestCommand  []string  `json:"merge_test_command"`
	MergeWorkDir      string    `json:"merge_work_dir"`
	TokenEnv          string    `json:"token_env"`
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type Input struct {
	Name              string   `json:"name"`
	Host              string   `json:"host"`
	BaseURL           string   `json:"base_url"`
	DefaultBranch     string   `json:"default_branch"`
	ProtectedBranches []string `json:"protected_branches"`
	MergeTestCommand  []string `json:"merge_test_command"`
	MergeWorkDir      string   `json:"merge_work_dir"`
	TokenEnv          string   `json:"token_env"`
	Enabled           *bool    `json:"enabled"`
}

type Source interface {
	List(ctx context.Context) ([]Repo, error)
}

func (in Input) Repo() Repo {
	return Repo{Name: in.Name, Host: in.Host, BaseURL: in.BaseURL, DefaultBranch: in.DefaultBranch,
		ProtectedBranches: in.ProtectedBranches, MergeTestCommand: in.MergeTestCommand, MergeWorkDir: in.MergeWorkDir,
		TokenEnv: in.TokenEnv, Enabled: in.Enabled == nil || *in.Enabled}
}

func (r *Repo) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	owner, name, ok := strings.Cut(r.Name, "/")
	if !ok || len(r.Name) > maxName || !partRe.MatchString(owner) || !partRe.MatchString(name) ||
		slices.Contains([]string{".", ".."}, owner) || slices.Contains([]string{".", ".."}, name) {
		return model.BadRequest("name must be owner/name using letters, digits, '.', '_' and '-'")
	}
	if err := r.validateHost(); err != nil {
		return err
	}
	if r.DefaultBranch == "" {
		r.DefaultBranch = DefaultBranch
	}
	if err := validBranch(r.DefaultBranch); err != nil {
		return err
	}
	var err error
	if r.ProtectedBranches, err = cleanPatterns(r.ProtectedBranches); err != nil {
		return err
	}
	for _, branch := range probeTasks {
		if p := r.protectedBy(branch); p != "" {
			return model.BadRequest("protected branch pattern %q would match task branches such as %s", p, branch)
		}
	}
	if err := r.validateMerge(); err != nil {
		return err
	}
	if r.TokenEnv != "" && (!tokenEnvRe.MatchString(r.TokenEnv) || redact.Contains(r.TokenEnv)) {
		return model.BadRequest("token_env must be the name of an environment variable (A-Z, 0-9 and _), never the token itself")
	}
	return nil
}

func (r *Repo) validateHost() error {
	switch r.Host {
	case HostGitHub:
		if r.BaseURL == "" {
			r.BaseURL = "https://github.com"
		}
	case HostForgejo:
		if r.BaseURL == "" {
			return model.BadRequest("base_url is required for forgejo repositories")
		}
	default:
		return model.BadRequest("host must be %s or %s", HostGitHub, HostForgejo)
	}
	u, err := url.Parse(r.BaseURL)
	if err != nil || len(r.BaseURL) > maxBaseURL || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return model.BadRequest("base_url must be an http(s) URL without credentials, query or fragment")
	}
	r.BaseURL = strings.TrimRight(r.BaseURL, "/")
	return nil
}

func (r *Repo) validateMerge() error {
	if len(r.MergeTestCommand) > maxCommand {
		return model.BadRequest("merge_test_command has at most %d arguments", maxCommand)
	}
	for _, a := range r.MergeTestCommand {
		if a == "" || len(a) > maxArg || strings.ContainsAny(a, "\x00\r\n") {
			return model.BadRequest("merge_test_command arguments must be non-empty single lines of at most %d bytes", maxArg)
		}
	}
	r.MergeTestCommand = nz(r.MergeTestCommand)
	if r.MergeWorkDir == "" {
		return nil
	}
	if len(r.MergeWorkDir) > maxWorkDir || !path.IsAbs(r.MergeWorkDir) || strings.ContainsAny(r.MergeWorkDir, "\x00\r\n") {
		return model.BadRequest("merge_work_dir must be an absolute path")
	}
	r.MergeWorkDir = path.Clean(r.MergeWorkDir)
	return nil
}

func validBranch(b string) error {
	if len(b) > maxBranch || !branchRe.MatchString(b) || strings.Contains(b, "..") || strings.HasPrefix(b, "/") ||
		strings.HasSuffix(b, "/") || strings.Contains(b, "//") || strings.HasSuffix(b, ".lock") {
		return model.BadRequest("default_branch is not a valid branch name")
	}
	return nil
}

func cleanPatterns(in []string) ([]string, error) {
	if len(in) > maxPatterns {
		return nil, model.BadRequest("protected_branches has at most %d patterns", maxPatterns)
	}
	out := []string{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || len(p) > maxBranch || !patternRe.MatchString(p) {
			return nil, model.BadRequest("protected branch patterns use letters, digits, '.', '_', '-', '/', '*' and '?' (at most %d characters)", maxBranch)
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out, nil
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func Match(pattern, branch string) bool {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(branch)
}

func (r Repo) protectedBy(branch string) string {
	for _, p := range r.ProtectedBranches {
		if Match(p, branch) {
			return p
		}
	}
	return ""
}

func (r Repo) Protected(branch string) bool { return r.protectedBy(branch) != "" }

func (r Repo) Token() string {
	if r.TokenEnv == "" {
		return ""
	}
	return os.Getenv(r.TokenEnv)
}

func (r Repo) APIURL() string {
	switch {
	case r.Host == HostForgejo:
		return r.BaseURL + "/api/v1"
	case strings.EqualFold(hostOf(r.BaseURL), "github.com"):
		return "https://api.github.com"
	default:
		return r.BaseURL + "/api/v3"
	}
}

func (r Repo) CloneURL() string { return r.BaseURL + "/" + r.Name + ".git" }

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

type Store struct {
	St  store.Store
	Now func() time.Time
}

type sqlTx interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
}

var errNoSQL = errors.New("repos: store transaction does not expose SQL")

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

const cols = `name, host, base_url, default_branch, protected_branches, merge_test_command, merge_work_dir, token_env, enabled, created_at, updated_at`

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Millisecond)
	}
	return time.Now().UTC().Truncate(time.Millisecond)
}

func scan(rows *sql.Rows) ([]Repo, error) {
	defer func() { _ = rows.Close() }()
	out := []Repo{}
	for rows.Next() {
		var r Repo
		var protected, command string
		var enabled int
		var created, updated int64
		if err := rows.Scan(&r.Name, &r.Host, &r.BaseURL, &r.DefaultBranch, &protected, &command, &r.MergeWorkDir, &r.TokenEnv, &enabled, &created, &updated); err != nil {
			return nil, err
		}
		if err := errors.Join(json.Unmarshal([]byte(protected), &r.ProtectedBranches), json.Unmarshal([]byte(command), &r.MergeTestCommand)); err != nil {
			return nil, fmt.Errorf("repos: stored repository %s: %w", r.Name, err)
		}
		r.ProtectedBranches, r.MergeTestCommand = nz(r.ProtectedBranches), nz(r.MergeTestCommand)
		r.Enabled = enabled != 0
		r.CreatedAt, r.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

func query(tx store.Tx, where string, args ...any) ([]Repo, error) {
	q, err := sqlOf(tx)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(`SELECT `+cols+` FROM repos `+where, args...)
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

func load(tx store.Tx, name string) (*Repo, error) {
	rs, err := query(tx, `WHERE lower(name) = ?`, strings.ToLower(name))
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, model.NotFound("repository %s not found", name)
	}
	return &rs[0], nil
}

func encode(r Repo) (protected, command string) {
	p, _ := json.Marshal(nz(r.ProtectedBranches))
	c, _ := json.Marshal(nz(r.MergeTestCommand))
	return string(p), string(c)
}

func flag(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) List(ctx context.Context) ([]Repo, error) {
	var out []Repo
	err := s.St.View(ctx, func(tx store.Tx) error {
		var err error
		out, err = query(tx, `ORDER BY name`)
		return err
	})
	return out, err
}

func (s *Store) Get(ctx context.Context, name string) (Repo, error) {
	var out Repo
	err := s.St.View(ctx, func(tx store.Tx) error {
		r, err := load(tx, name)
		if r != nil {
			out = *r
		}
		return err
	})
	return out, err
}

func snapshot(r *Repo) map[string]any {
	return map[string]any{"name": r.Name, "host": r.Host, "base_url": r.BaseURL, "default_branch": r.DefaultBranch,
		"protected_branches": r.ProtectedBranches, "merge_test_command": r.MergeTestCommand, "merge_work_dir": r.MergeWorkDir,
		"token_env": r.TokenEnv, "enabled": r.Enabled}
}

func (s *Store) audit(ctx context.Context, tx store.Tx, tool string, before, after *Repo) error {
	e := &model.AuditEvent{TS: s.now(), AgentID: model.Owner.ID, Role: model.Owner.Role, Tool: tool}
	name := ""
	if before != nil {
		e.Before, name = snapshot(before), before.Name
	}
	if after != nil {
		e.After, name = snapshot(after), after.Name
	}
	note := auth.AuditNote(ctx, "repo "+name)
	e.Note = &note
	return tx.InsertAudit(e)
}

func (s *Store) Create(ctx context.Context, r Repo) (Repo, error) {
	if err := r.Validate(); err != nil {
		return Repo{}, err
	}
	r.CreatedAt = s.now()
	r.UpdatedAt = r.CreatedAt
	err := s.St.Atomic(ctx, func(tx store.Tx) error {
		if rs, err := query(tx, `WHERE lower(name) = ?`, strings.ToLower(r.Name)); err != nil {
			return err
		} else if len(rs) > 0 {
			return model.Conflict("repository %s already exists", rs[0].Name)
		}
		q, err := sqlOf(tx)
		if err != nil {
			return err
		}
		protected, command := encode(r)
		if _, err := q.Exec(`INSERT INTO repos (`+cols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Name, r.Host, r.BaseURL, r.DefaultBranch, protected, command, r.MergeWorkDir, r.TokenEnv, flag(r.Enabled),
			r.CreatedAt.UnixMilli(), r.UpdatedAt.UnixMilli()); err != nil {
			return err
		}
		return s.audit(ctx, tx, "create_repo", nil, &r)
	})
	return r, err
}

func (s *Store) Update(ctx context.Context, name string, r Repo) (Repo, error) {
	var out Repo
	err := s.St.Atomic(ctx, func(tx store.Tx) error {
		old, err := load(tx, name)
		if err != nil {
			return err
		}
		r.Name = old.Name
		if err := r.Validate(); err != nil {
			return err
		}
		r.CreatedAt, r.UpdatedAt = old.CreatedAt, s.now()
		q, err := sqlOf(tx)
		if err != nil {
			return err
		}
		protected, command := encode(r)
		if _, err := q.Exec(`UPDATE repos SET host = ?, base_url = ?, default_branch = ?, protected_branches = ?, merge_test_command = ?,
			merge_work_dir = ?, token_env = ?, enabled = ?, updated_at = ? WHERE name = ?`,
			r.Host, r.BaseURL, r.DefaultBranch, protected, command, r.MergeWorkDir, r.TokenEnv, flag(r.Enabled), r.UpdatedAt.UnixMilli(), old.Name); err != nil {
			return err
		}
		out = r
		return s.audit(ctx, tx, "update_repo", old, &r)
	})
	return out, err
}

func (s *Store) Delete(ctx context.Context, name string) error {
	return s.St.Atomic(ctx, func(tx store.Tx) error {
		old, err := load(tx, name)
		if err != nil {
			return err
		}
		q, err := sqlOf(tx)
		if err != nil {
			return err
		}
		if _, err := q.Exec(`DELETE FROM repos WHERE name = ?`, old.Name); err != nil {
			return err
		}
		return s.audit(ctx, tx, "delete_repo", old, nil)
	})
}

func (s *Store) Check(tx store.Tx, repo, branch string) error {
	rs, err := query(tx, `ORDER BY name`)
	if err != nil || len(rs) == 0 {
		return err
	}
	i := slices.IndexFunc(rs, func(r Repo) bool { return strings.EqualFold(r.Name, repo) })
	if i < 0 {
		return model.BadRequest("repo %q is not in the repository registry; known repos: %s", repo, known(rs))
	}
	r := rs[i]
	if !r.Enabled {
		return model.BadRequest("repo %s is disabled in the repository registry", r.Name)
	}
	if p := r.protectedBy(branch); branch != "" && p != "" {
		return model.BadRequest("branch %s matches the protected branch pattern %q of %s", branch, p, r.Name)
	}
	return nil
}

func known(rs []Repo) string {
	names := make([]string, 0, len(rs))
	for _, r := range rs {
		names = append(names, r.Name)
	}
	if len(names) > maxListNames {
		return strings.Join(names[:maxListNames], ", ") + fmt.Sprintf(" and %d more", len(names)-maxListNames)
	}
	return strings.Join(names, ", ")
}

func Enabled(ctx context.Context, src Source) ([]Repo, error) {
	if src == nil {
		return nil, nil
	}
	rs, err := src.List(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(rs, func(r Repo) bool { return !r.Enabled }), nil
}

var deprecated sync.Map

type warning struct {
	log    *slog.Logger
	source string
}

func Deprecated(log *slog.Logger, source string) {
	if log == nil {
		return
	}
	if _, dup := deprecated.LoadOrStore(warning{log, source}, struct{}{}); dup {
		return
	}
	log.Warn("deprecated configuration: register repositories through /api/v1/repos instead", "source", source)
}
