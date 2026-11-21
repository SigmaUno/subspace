package server

import (
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/playbooks"
	"github.com/SigmaUno/subspace/internal/store"
)

func init() { registerMount(mountPlaybooks) }

func mountPlaybooks(d Deps) {
	if !playbooks.SupportsSQL(d.Ctx, d.Store) {
		d.Log.Error("playbooks disabled", "err", "store does not support SQL extensions")
		return
	}
	s := &playbooks.Store{St: d.Store, Now: d.Cfg.Now}
	if err := s.Seed(d.Ctx); err != nil {
		d.Log.Error("playbooks: seeding built-in playbooks", "err", err)
	}
	d.Board.SetPlaybooks(playbooks.Lookup)
	h := playbooks.Handler(s, playbookInUse, d.Log)
	d.API.Mount(d.Mux, "/api/v1/playbooks", h)
	d.API.Mount(d.Mux, "/api/v1/playbooks/", h)
	mcp.BindPlaybooks(d.Registry, s.Get)
}

func playbookInUse(tx store.Tx, name string) error {
	briefs, err := tx.ListBriefs(store.BriefFilter{})
	if err != nil {
		return err
	}
	for _, b := range briefs {
		if b.Playbook == name {
			return model.Conflict("playbook %s is used by brief %s", name, b.ID)
		}
	}
	return nil
}
