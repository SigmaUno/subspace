package tokens

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const (
	TouchEvery = time.Minute
	maxName    = 100
)

var ErrRevoked = errors.New("token already revoked")

type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
}

type AuditFunc func(*Token) *model.AuditEvent

type Store interface {
	CreateAPIToken(ctx context.Context, t *Token, hash string, audit AuditFunc) error
	ListAPITokens(ctx context.Context) ([]*Token, error)
	APITokenByHash(ctx context.Context, hash string) (*Token, error)
	RevokeAPIToken(ctx context.Context, id string, at time.Time, audit AuditFunc) (*Token, error)
	TouchAPIToken(ctx context.Context, id string, at time.Time) error
}

type Service struct {
	st  Store
	now func() time.Time
}

func New(st Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, now: func() time.Time { return now().UTC().Truncate(time.Millisecond) }}
}

type CreateInput struct {
	Name      string     `json:"name"`
	Scope     string     `json:"scope"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Token, string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > maxName {
		return nil, "", model.BadRequest("name is required (at most %d bytes)", maxName)
	}
	if in.Scope != auth.ScopeOwner && in.Scope != auth.ScopeViewer {
		return nil, "", model.BadRequest("scope must be %s or %s", auth.ScopeViewer, auth.ScopeOwner)
	}
	now := s.now()
	t := &Token{Name: name, Scope: in.Scope, CreatedAt: now}
	if in.ExpiresAt != nil {
		if !in.ExpiresAt.After(now) {
			return nil, "", model.BadRequest("expires_at must be in the future")
		}
		e := in.ExpiresAt.UTC().Truncate(time.Millisecond)
		t.ExpiresAt = &e
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, "", err
	}
	secret := auth.OwnerTokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	err := s.st.CreateAPIToken(ctx, t, auth.Hash(secret), s.audit(ctx, "create_token", nil))
	if err != nil {
		return nil, "", err
	}
	return t, secret, nil
}

func (s *Service) List(ctx context.Context) ([]*Token, error) {
	return s.st.ListAPITokens(ctx)
}

func (s *Service) Revoke(ctx context.Context, id string) (*Token, error) {
	t, err := s.st.RevokeAPIToken(ctx, id, s.now(), s.audit(ctx, "revoke_token", map[string]any{"revoked": false}))
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, model.NotFound("token %s not found", id)
	case errors.Is(err, ErrRevoked):
		return nil, model.Conflict("token %s is already revoked", id)
	}
	return t, err
}

func (s *Service) LookupOwnerToken(ctx context.Context, secret string) (*auth.Principal, error) {
	if !strings.HasPrefix(secret, auth.OwnerTokenPrefix) {
		return nil, nil
	}
	t, err := s.st.APITokenByHash(ctx, auth.Hash(secret))
	if err != nil || t == nil {
		return nil, err
	}
	now := s.now()
	if t.RevokedAt != nil || (t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)) {
		return nil, nil
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= TouchEvery {
		if err := s.st.TouchAPIToken(ctx, t.ID, now); err != nil {
			return nil, err
		}
	}
	return &auth.Principal{TokenID: t.ID, Scope: t.Scope}, nil
}

func (s *Service) audit(ctx context.Context, tool string, before map[string]any) AuditFunc {
	note := auth.AuditNote(ctx, "")
	return func(t *Token) *model.AuditEvent {
		e := &model.AuditEvent{TS: s.now(), AgentID: model.Owner.ID, Role: model.RoleOwner, Tool: tool, Before: before,
			After: map[string]any{"token_id": t.ID, "name": t.Name, "scope": t.Scope, "revoked": t.RevokedAt != nil}}
		if note != "" {
			e.Note = &note
		}
		return e
	}
}
