package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/SigmaUno/subspace/internal/auth"
	"github.com/SigmaUno/subspace/internal/autopilot"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/profiles"
	"github.com/SigmaUno/subspace/internal/store"
)

func init() {
	registerMount(mountProfiles)
	wraps = append(wraps, func(next http.Handler, d Deps) http.Handler {
		if !profiles.SupportsSQL(d.Ctx, d.Store) {
			return next
		}
		s := &profiles.Store{St: d.Store, Now: d.Cfg.Now}
		return profiles.Expand(next, s, func(r *http.Request) bool { return isOwnerRequest(d, r) }, d.Log)
	})
}

func mountProfiles(d Deps) {
	if !profiles.SupportsSQL(d.Ctx, d.Store) {
		d.Log.Error("agent profiles disabled", "err", "store does not support SQL extensions")
		return
	}
	s := &profiles.Store{St: d.Store, Now: d.Cfg.Now}
	if err := s.Seed(d.Ctx); err != nil {
		d.Log.Error("agent profiles: seeding built-in profiles", "err", err)
	}
	h := profiles.Handler(s, profileInAutopilot, d.Log)
	d.API.Mount(d.Mux, "/api/v1/agent-profiles", h)
	d.API.Mount(d.Mux, "/api/v1/agent-profiles/", h)
	mcp.BindSpawnProfiles(d.Registry, func(ctx context.Context, name string) (profiles.Profile, error) { return s.Get(ctx, name) })
}

func profileInAutopilot(tx store.Tx, name string) error {
	briefs, err := tx.ListBriefs(store.BriefFilter{})
	if err != nil {
		return err
	}
	for _, b := range briefs {
		raw, ok, err := tx.GetKV(autopilotSpawnKVPrefix + b.ID)
		if err != nil {
			return err
		}
		var c autopilot.SpawnConfig
		if ok && json.Unmarshal([]byte(raw), &c) == nil && c.Profile == name {
			return model.Conflict("profile %s is used by the autopilot options of brief %s", name, b.ID)
		}
	}
	return nil
}

func isOwnerRequest(d Deps, r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	if auth.OwnerMatch(tok, d.Cfg.OwnerToken) {
		return true
	}
	if d.API.Tokens == nil {
		return false
	}
	p, err := d.API.Tokens.LookupOwnerToken(r.Context(), tok)
	return err == nil && p != nil && p.Scope == auth.ScopeOwner
}
