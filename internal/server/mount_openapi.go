package server

import (
	"net/http"
	"net/url"
	"regexp"
	"sync"

	"github.com/SigmaUno/subspace/internal/board"
	"github.com/SigmaUno/subspace/internal/mcp"
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/openapi"
)

var probePlaceholder = regexp.MustCompile(`\{[^}]*\}`)

func init() {
	registerMount(func(d Deps) {
		var (
			once sync.Once
			doc  []byte
			err  error
		)
		d.API.Mount(d.Mux, "GET /api/v1/openapi.json", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			once.Do(func() {
				doc, err = openapi.Build(openapi.Options{
					Version:    board.Version,
					ServerURL:  d.Cfg.PublicURL,
					Registered: muxRegistered(d.Mux),
					Tools:      mcpTools(d.Registry),
				})
			})
			if err != nil {
				writeAPIError(w, model.Errf("internal", "openapi: %v", err))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(doc)
		}))
	})
}

func muxRegistered(mux *http.ServeMux) func(method, path string) bool {
	return func(method, path string) bool {
		probe := &http.Request{Method: method, URL: &url.URL{Path: probePlaceholder.ReplaceAllString(path, "x")}, Host: "openapi.invalid"}
		_, pattern := mux.Handler(probe)
		return pattern != "" && pattern != "/api/v1/"
	}
}

func mcpTools(reg *mcp.Registry) []openapi.Tool {
	index := map[string]int{}
	var out []openapi.Tool
	for _, role := range mcp.AllRoles {
		for _, t := range reg.List(role) {
			i, ok := index[t.Name]
			if !ok {
				i = len(out)
				index[t.Name] = i
				out = append(out, openapi.Tool{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
			}
			out[i].Roles = append(out[i].Roles, role)
		}
	}
	return out
}
