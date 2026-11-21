package prsync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	KindGitHub  = "github"
	KindForgejo = "forgejo"

	DefaultInterval = 30 * time.Second
)

type Rate struct {
	PerMinute float64 `json:"per_minute"`
	Burst     int     `json:"burst"`
}

type HostConfig struct {
	Kind     string `json:"kind"`
	URL      string `json:"url"`
	APIURL   string `json:"api_url"`
	Token    string `json:"token"`
	TokenEnv string `json:"token_env"`
	Rate     *Rate  `json:"rate"`

	Authors []string `json:"authors"`
}

type Config struct {
	Interval time.Duration
	Rate     Rate
	Hosts    map[string]HostConfig
	Repos    map[string]string
}

var (
	defaultGlobalRate = Rate{PerMinute: 600, Burst: 60}
	defaultHostRate   = Rate{PerMinute: 120, Burst: 20}
)

type fileConfig struct {
	Interval string                `json:"interval"`
	Rate     *Rate                 `json:"rate"`
	Hosts    map[string]HostConfig `json:"hosts"`
	Repos    map[string]string     `json:"repos"`
}

func LoadConfig(path, interval string) (Config, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return Config{}, err
	}
	return ParseConfig(raw, interval)
}

func ParseConfig(raw []byte, interval string) (Config, error) {
	var f fileConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return Config{}, fmt.Errorf("prsync config: %w", err)
	}
	c := Config{Interval: DefaultInterval, Rate: defaultGlobalRate, Hosts: map[string]HostConfig{}, Repos: map[string]string{}}
	if interval == "" {
		interval = f.Interval
	}
	if interval != "" {
		d, err := time.ParseDuration(interval)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("prsync config: invalid interval %q", interval)
		}
		c.Interval = d
	}
	if f.Rate != nil {
		c.Rate = *f.Rate
	}
	if len(f.Hosts) == 0 {
		return Config{}, fmt.Errorf("prsync config: no hosts")
	}
	for name, h := range f.Hosts {
		h, err := normalizeHost(name, h)
		if err != nil {
			return Config{}, err
		}
		c.Hosts[name] = h
	}
	for repo, name := range f.Repos {
		if _, ok := c.Hosts[name]; !ok {
			return Config{}, fmt.Errorf("prsync config: repo %q maps to unknown host %q", repo, name)
		}
		c.Repos[strings.ToLower(repo)] = name
	}
	return c, nil
}

func normalizeHost(name string, h HostConfig) (HostConfig, error) {
	if h.TokenEnv != "" {
		h.Token = os.Getenv(h.TokenEnv)
	}
	if h.Rate == nil {
		r := defaultHostRate
		h.Rate = &r
	}
	switch h.Kind {
	case KindGitHub:
		if h.URL == "" {
			h.URL = "https://github.com"
		}
	case KindForgejo:
		if h.URL == "" {
			return h, fmt.Errorf("prsync config: host %q needs a url", name)
		}
	default:
		return h, fmt.Errorf("prsync config: host %q has unknown kind %q", name, h.Kind)
	}
	u, err := url.Parse(h.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return h, fmt.Errorf("prsync config: host %q has an invalid url", name)
	}
	h.URL = strings.TrimRight(h.URL, "/")
	if h.APIURL == "" {
		switch {
		case h.Kind == KindForgejo:
			h.APIURL = h.URL + "/api/v1"
		case strings.EqualFold(u.Host, "github.com"):
			h.APIURL = "https://api.github.com"
		default:
			h.APIURL = h.URL + "/api/v3"
		}
	}
	h.APIURL = strings.TrimRight(h.APIURL, "/")
	return h, nil
}
