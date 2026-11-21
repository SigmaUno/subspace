package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

const (
	SessionTTL          = sessionTTL
	MaxSessionsPerAgent = maxSessionsPer
)

type SessionStore interface {
	CreateSession(ctx context.Context, id, agent string, now time.Time) error
	SessionValid(ctx context.Context, id, agent string, now time.Time) (bool, error)
	DropSession(ctx context.Context, id, agent string) (bool, error)
}

func (s *Server) openSession(ctx context.Context, agent string) (string, error) {
	if s.Sessions == nil {
		return s.newSession(agent), nil
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	return id, s.Sessions.CreateSession(ctx, id, agent, s.Now())
}

func (s *Server) checkSession(ctx context.Context, id, agent string) (bool, error) {
	if s.Sessions == nil {
		return s.sessionOK(id, agent), nil
	}
	return s.Sessions.SessionValid(ctx, id, agent, s.Now())
}

func (s *Server) closeSession(ctx context.Context, id, agent string) (bool, error) {
	if s.Sessions == nil {
		return s.dropSession(id, agent), nil
	}
	return s.Sessions.DropSession(ctx, id, agent)
}
