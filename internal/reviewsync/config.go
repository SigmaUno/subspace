package reviewsync

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/SigmaUno/subspace/internal/githost"
	"github.com/SigmaUno/subspace/internal/prsync"
)

func HostsFromConfig(cfg prsync.Config, hc *http.Client) map[string]Host {
	out := make(map[string]Host, len(cfg.Hosts))
	for name, h := range cfg.Hosts {
		u, err := url.Parse(h.URL)
		if err != nil {
			continue
		}
		style := githost.StyleForgejo
		if h.Kind == prsync.KindGitHub {
			style = githost.StyleGitHub
		}
		out[name] = Host{
			Web:       strings.ToLower(u.Host),
			Commenter: githost.NewCommenter(githost.CommenterConfig{Style: style, APIURL: h.APIURL, Token: h.Token, HTTPClient: hc}),
		}
	}
	return out
}
