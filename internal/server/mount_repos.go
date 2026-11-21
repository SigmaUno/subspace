package server

import "github.com/SigmaUno/subspace/internal/repos"

func init() { registerMount(mountRepos) }

func repoSource(d Deps) repos.Source {
	if !repos.SupportsSQL(d.Ctx, d.Store) {
		return nil
	}
	return &repos.Store{St: d.Store, Now: d.Cfg.Now}
}

func mountRepos(d Deps) {
	if !repos.SupportsSQL(d.Ctx, d.Store) {
		d.Log.Error("repository registry disabled", "err", "store does not support SQL extensions")
		return
	}
	s := &repos.Store{St: d.Store, Now: d.Cfg.Now}
	d.Board.SetRepoCheck(s.Check)
	d.Board.SetRepoCredentials(s.AgentCredentials)
	h := repos.Handler(s, nil, d.Log)
	d.API.Mount(d.Mux, "/api/v1/repos", h)
	d.API.Mount(d.Mux, "/api/v1/repos/", h)
}
