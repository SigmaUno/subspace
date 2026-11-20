package server

import (
	"net/http"
	"net/http/pprof"
)

func init() {
	registerMount(func(d Deps) {
		mux := http.NewServeMux()
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		d.API.Mount(d.Mux, "/api/v1/debug/pprof/", mux)
	})
}
