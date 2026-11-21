package prsync

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func FuzzParseConfig(f *testing.F) {
	for _, s := range []string{
		`{"hosts":{"gh":{"kind":"github"}}}`,
		`{"interval":"30s","rate":{"per_minute":600,"burst":60},"hosts":{"gh":{"kind":"github","token_env":"GITHUB_TOKEN","rate":{"per_minute":120,"burst":20}},"forge":{"kind":"forgejo","url":"https://forge.example.com","token":"x"}},"repos":{"SigmaUno/subspace":"gh"}}`,
		`{"hosts":{"f":{"kind":"forgejo"}}}`,
		`{"hosts":{"f":{"kind":"forgejo","url":"ftp://x"}}}`,
		`{"hosts":{"f":{"kind":"forgejo","url":"https://x/","api_url":"https://x/api/"}}}`,
		`{"hosts":{"g":{"kind":"github","url":"https://GitHub.com"}}}`,
		`{"hosts":{"g":{"kind":"github","url":"https://ghe.example.com/"}}}`,
		`{"hosts":{"g":{"kind":"nope"}}}`,
		`{"hosts":{"g":{"kind":"github"}},"repos":{"A/B":"missing"}}`,
		`{"hosts":{"g":{"kind":"github"}},"repos":{"A/B":"g","a/b":"g"}}`,
		`{"hosts":{},"interval":"-1s"}`,
		`{"interval":"1h","hosts":{"g":{"kind":"github"}}}`,
		`{"unknown":1}`, `{}`, `null`, `[]`, ``, `{"hosts":`,
	} {
		f.Add([]byte(s), "")
		f.Add([]byte(s), "5s")
	}
	f.Add([]byte(`{"hosts":{"g":{"kind":"github"}}}`), "0s")
	f.Add([]byte(`{"hosts":{"g":{"kind":"github"}}}`), "bogus")
	f.Fuzz(func(t *testing.T, raw []byte, interval string) {
		start := time.Now()
		c, err := ParseConfig(raw, interval)
		if el := time.Since(start); el > 5*time.Second {
			t.Fatalf("ParseConfig took %s on %d bytes", el, len(raw))
		}
		if err != nil {
			return
		}
		if c.Interval <= 0 || len(c.Hosts) == 0 {
			t.Fatalf("bad config %+v", c)
		}
		for name, h := range c.Hosts {
			if h.Kind != KindGitHub && h.Kind != KindForgejo || h.Rate == nil || h.APIURL == "" {
				t.Fatalf("host %q: %+v", name, h)
			}
			u, err := url.Parse(h.URL)
			if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" || strings.HasSuffix(h.URL, "/") || strings.HasSuffix(h.APIURL, "/") {
				t.Fatalf("host %q has malformed urls: %+v", name, h)
			}
		}
		for repo, host := range c.Repos {
			if _, ok := c.Hosts[host]; !ok || repo != strings.ToLower(repo) {
				t.Fatalf("repo %q maps to %q", repo, host)
			}
		}
		again, err := ParseConfig(raw, interval)
		if err != nil || !reflect.DeepEqual(c, again) {
			t.Fatalf("ParseConfig is not deterministic: %v", err)
		}
	})
}
