package artifacts

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	DefaultRoot         = "/data/artifacts"
	DefaultMaxFileSize  = 100 << 20
	DefaultProjectQuota = 5 << 30
	DefaultInlineMax    = 256 << 10
	DefaultURLTTL       = 10 * time.Minute

	CodeTooLarge      = "too_large"
	CodeQuotaExceeded = "quota_exceeded"

	maxNameLen = 255
)

const (
	ScopeAgent   = "agent"
	ScopeTask    = "task"
	ScopeProject = "project"
)

var scopeRe = regexp.MustCompile(`^(agent|task|project):([A-Za-z0-9][A-Za-z0-9_-]{0,62})$`)

type Scope struct {
	Kind string
	ID   string
}

func (s Scope) String() string { return s.Kind + ":" + s.ID }

func ParseScope(v string) (Scope, error) {
	m := scopeRe.FindStringSubmatch(v)
	if m == nil {
		return Scope{}, model.BadRequest("scope must be agent:<id>, task:<id> or project:<id>")
	}
	return Scope{Kind: m[1], ID: m[2]}, nil
}

type Directory interface {
	Task(ctx context.Context, id string) (*model.Task, error)
	Brief(ctx context.Context, id string) (*model.Brief, error)
	Agent(ctx context.Context, id string) (*model.Agent, error)
	AgentTasks(ctx context.Context, agentID string) ([]*model.Task, error)
}

type Auditor interface {
	Record(ctx context.Context, e *model.AuditEvent) error
}

type Config struct {
	Root         string
	MaxFileSize  int64
	ProjectQuota int64
	InlineMax    int64
	Store        Store
	Directory    Directory
	Auditor      Auditor
	URLBase      string
	URLTTL       time.Duration
	Secret       []byte
	Now          func() time.Time
	Logger       *slog.Logger
}

type Service struct {
	cfg   Config
	blobs blobs
	mu    sync.Mutex
}

func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Directory == nil || cfg.Auditor == nil {
		return nil, errors.New("artifacts: Store, Directory and Auditor are required")
	}
	if cfg.Root == "" {
		cfg.Root = DefaultRoot
	}
	if cfg.MaxFileSize <= 0 {
		cfg.MaxFileSize = DefaultMaxFileSize
	}
	if cfg.ProjectQuota <= 0 {
		cfg.ProjectQuota = DefaultProjectQuota
	}
	if cfg.InlineMax <= 0 {
		cfg.InlineMax = DefaultInlineMax
	}
	if cfg.URLTTL <= 0 {
		cfg.URLTTL = DefaultURLTTL
	}
	if len(cfg.Secret) == 0 {
		cfg.Secret = make([]byte, 32)
		if _, err := rand.Read(cfg.Secret); err != nil {
			return nil, err
		}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	b, err := newBlobs(cfg.Root)
	if err != nil {
		return nil, err
	}
	return &Service{cfg: cfg, blobs: b}, nil
}

type Upload struct {
	Scope       string
	Name        string
	ContentType string
	Body        io.Reader
}

func (s *Service) Put(ctx context.Context, actor model.Actor, u Upload) (*Artifact, error) {
	sc, err := ParseScope(u.Scope)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, actor, sc); err != nil {
		return nil, err
	}
	project, err := s.project(ctx, sc)
	if err != nil {
		return nil, err
	}
	name, err := cleanName(u.Name)
	if err != nil {
		return nil, err
	}

	used, err := s.cfg.Store.ProjectUsage(ctx, project)
	if err != nil {
		return nil, err
	}
	limit, over := s.cfg.MaxFileSize, tooLarge(s.cfg.MaxFileSize)
	if left := max(s.cfg.ProjectQuota-used, 0); left < limit {
		limit, over = left, quotaExceeded(project, s.cfg.ProjectQuota)
	}

	tmp, err := s.blobs.temp()
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(u.Body, limit+1))
	if err != nil {
		_ = tmp.Close()
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, tooLarge(s.cfg.MaxFileSize)
		}
		return nil, model.BadRequest("reading upload: %v", err)
	}
	if n > limit {
		_ = tmp.Close()
		return nil, over
	}
	ctype := cleanContentType(u.ContentType)
	if ctype == "" {
		ctype = sniff(tmp)
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}

	a := &Artifact{
		Scope:       sc.String(),
		Name:        name,
		Size:        n,
		SHA256:      hex.EncodeToString(h.Sum(nil)),
		ContentType: ctype,
		UploadedBy:  actor.ID,
		Project:     project,
		CreatedAt:   s.cfg.Now().UTC().Truncate(time.Second),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	used, err = s.cfg.Store.ProjectUsage(ctx, project)
	if err != nil {
		return nil, err
	}
	if used+n > s.cfg.ProjectQuota {
		return nil, quotaExceeded(project, s.cfg.ProjectQuota)
	}
	created, err := s.blobs.commit(tmp.Name(), a.SHA256)
	if err != nil {
		return nil, err
	}
	rollback := func() {
		if created {
			_ = s.blobs.remove(a.SHA256)
		}
	}
	if err := s.cfg.Store.Insert(ctx, a); err != nil {
		rollback()
		return nil, err
	}
	if err := s.audit(ctx, actor, "put_artifact", sc, nil, a); err != nil {
		_ = s.cfg.Store.Delete(ctx, a.ID)
		rollback()
		return nil, err
	}
	return a, nil
}

func (s *Service) Get(ctx context.Context, actor model.Actor, id string) (*Artifact, error) {
	a, err := s.meta(ctx, id)
	if err != nil {
		return nil, err
	}
	sc, err := ParseScope(a.Scope)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, actor, sc); err != nil {
		return nil, err
	}
	return a, nil
}

func (s *Service) Open(ctx context.Context, actor model.Actor, id string) (*Artifact, *os.File, error) {
	a, err := s.Get(ctx, actor, id)
	if err != nil {
		return nil, nil, err
	}
	f, err := s.openBlob(a)
	return a, f, err
}

func (s *Service) List(ctx context.Context, actor model.Actor, scope string, limit int) ([]*Artifact, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var scopes []string
	switch {
	case scope != "":
		sc, err := ParseScope(scope)
		if err != nil {
			return nil, err
		}
		if err := s.authorize(ctx, actor, sc); err != nil {
			return nil, err
		}
		scopes = []string{sc.String()}
	case !s.unrestricted(actor):
		var err error
		if scopes, err = s.visibleScopes(ctx, actor); err != nil {
			return nil, err
		}
	}
	out, err := s.cfg.Store.List(ctx, Filter{Scopes: scopes, Limit: limit})
	if out == nil {
		out = []*Artifact{}
	}
	return out, err
}

func (s *Service) Delete(ctx context.Context, actor model.Actor, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.Get(ctx, actor, id)
	if err != nil {
		return err
	}
	sc, err := ParseScope(a.Scope)
	if err != nil {
		return err
	}
	if err := s.audit(ctx, actor, "delete_artifact", sc, a, nil); err != nil {
		return err
	}
	if err := s.cfg.Store.Delete(ctx, id); err != nil {
		return notFound(err, id)
	}
	left, err := s.cfg.Store.CountBySHA(ctx, a.SHA256)
	if err != nil {
		return err
	}
	if left == 0 {
		return s.blobs.remove(a.SHA256)
	}
	return nil
}

func (s *Service) SignedURL(a *Artifact) (string, time.Time, error) {
	if s.cfg.URLBase == "" {
		return "", time.Time{}, model.BadRequest("artifact is larger than %d bytes and URL downloads are not configured", s.cfg.InlineMax)
	}
	exp := s.cfg.Now().Add(s.cfg.URLTTL).UTC().Truncate(time.Second)
	q := url.Values{
		"exp": {strconv.FormatInt(exp.Unix(), 10)},
		"sig": {s.sign(a.ID, exp.Unix())},
	}
	return strings.TrimRight(s.cfg.URLBase, "/") + "/" + a.ID + "?" + q.Encode(), exp, nil
}

func (s *Service) verify(id, exp, sig string) bool {
	ts, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || s.cfg.Now().Unix() > ts {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(s.sign(id, ts)))
}

func (s *Service) sign(id string, exp int64) string {
	m := hmac.New(sha256.New, s.cfg.Secret)
	m.Write([]byte(id + "\n" + strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Service) meta(ctx context.Context, id string) (*Artifact, error) {
	a, err := s.cfg.Store.Get(ctx, id)
	if err != nil {
		return nil, notFound(err, id)
	}
	return a, nil
}

func (s *Service) openBlob(a *Artifact) (*os.File, error) {
	f, err := s.blobs.open(a.SHA256)
	if errors.Is(err, os.ErrNotExist) {
		return nil, model.NotFound("artifact %s content is missing", a.ID)
	}
	return f, err
}

func (s *Service) unrestricted(actor model.Actor) bool {
	return actor.Role == model.RoleOwner || actor.Role == model.RoleLeader
}

func (s *Service) authorize(ctx context.Context, actor model.Actor, sc Scope) error {
	if s.unrestricted(actor) {
		return nil
	}
	denied := model.Forbidden("no access to %s", sc)
	if actor.ID == "" {
		return denied
	}
	switch sc.Kind {
	case ScopeAgent:
		if sc.ID == actor.ID {
			return nil
		}
	case ScopeTask:
		t, err := s.cfg.Directory.Task(ctx, sc.ID)
		if errors.Is(err, store.ErrNotFound) {
			return denied
		}
		if err != nil {
			return err
		}
		if holds(t, actor.ID) {
			return nil
		}
	case ScopeProject:
		tasks, err := s.cfg.Directory.AgentTasks(ctx, actor.ID)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(tasks, func(t *model.Task) bool { return t.BriefID == sc.ID }) {
			return nil
		}
	}
	return denied
}

func (s *Service) visibleScopes(ctx context.Context, actor model.Actor) ([]string, error) {
	tasks, err := s.cfg.Directory.AgentTasks(ctx, actor.ID)
	if err != nil {
		return nil, err
	}
	scopes := []string{Scope{ScopeAgent, actor.ID}.String()}
	for _, t := range tasks {
		scopes = append(scopes, Scope{ScopeTask, t.ID}.String(), Scope{ScopeProject, t.BriefID}.String())
	}
	slices.Sort(scopes)
	return slices.Compact(scopes), nil
}

func (s *Service) project(ctx context.Context, sc Scope) (string, error) {
	var err error
	switch sc.Kind {
	case ScopeTask:
		var t *model.Task
		if t, err = s.cfg.Directory.Task(ctx, sc.ID); err == nil {
			return t.BriefID, nil
		}
	case ScopeProject:
		if _, err = s.cfg.Directory.Brief(ctx, sc.ID); err == nil {
			return sc.ID, nil
		}
	default:
		if _, err = s.cfg.Directory.Agent(ctx, sc.ID); err == nil {
			return sc.String(), nil
		}
	}
	return "", notFound(err, sc.String())
}

func (s *Service) audit(ctx context.Context, actor model.Actor, tool string, sc Scope, before, after *Artifact) error {
	e := &model.AuditEvent{
		TS:      s.cfg.Now().UTC(),
		AgentID: actor.ID,
		Role:    actor.Role,
		Tool:    tool,
		Before:  auditFields(before),
		After:   auditFields(after),
	}
	if sc.Kind == ScopeTask {
		e.TaskID = &sc.ID
	}
	return s.cfg.Auditor.Record(ctx, e)
}

func auditFields(a *Artifact) map[string]any {
	if a == nil {
		return nil
	}
	return map[string]any{"id": a.ID, "scope": a.Scope, "name": a.Name, "size": a.Size, "sha256": a.SHA256}
}

func holds(t *model.Task, id string) bool {
	return (t.Assignee != nil && *t.Assignee == id) || (t.Reviewer != nil && *t.Reviewer == id)
}

func notFound(err error, what string) error {
	if errors.Is(err, store.ErrNotFound) {
		return model.NotFound("%s not found", what)
	}
	return err
}

func tooLarge(limit int64) error {
	return model.Errf(CodeTooLarge, "file exceeds the %d byte limit", limit)
}

func quotaExceeded(project string, quota int64) error {
	return model.Errf(CodeQuotaExceeded, "%s is over its %d byte artifact quota", project, quota)
}

func cleanName(raw string) (string, error) {
	n := strings.ToValidUTF8(raw, "")
	n = path.Base(strings.ReplaceAll(n, `\`, "/"))
	n = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, n)
	n = strings.TrimSpace(n)
	if len(n) > maxNameLen {
		n = strings.ToValidUTF8(n[:maxNameLen], "")
	}
	if n == "" || n == "." || n == ".." || n == "/" {
		return "", model.BadRequest("name is required")
	}
	return n, nil
}

func cleanContentType(v string) string {
	mt, _, err := mime.ParseMediaType(v)
	if err != nil {
		return ""
	}
	return mt
}

func sniff(f *os.File) string {
	buf := make([]byte, 512)
	n, _ := f.ReadAt(buf, 0)
	return http.DetectContentType(buf[:n])
}

func contentDisposition(name string) string {
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": name}); v != "" {
		return v
	}
	return "attachment"
}
