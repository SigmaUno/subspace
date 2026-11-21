package prsync

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/repos"
)

const (
	ConfigEnv   = "SUBSPACE_PRSYNC_CONFIG"
	IntervalEnv = "SUBSPACE_PRSYNC_INTERVAL"
)

func FromRepos(rs []repos.Repo, interval string) (Config, error) {
	c := Config{Interval: DefaultInterval, Rate: defaultGlobalRate, Hosts: map[string]HostConfig{}, Repos: map[string]string{}, Strict: true}
	if interval != "" {
		d, err := time.ParseDuration(interval)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("prsync config: invalid interval %q", interval)
		}
		c.Interval = d
	}
	names := map[string]string{}
	for _, r := range rs {
		u, err := url.Parse(r.BaseURL)
		if err != nil {
			return Config{}, fmt.Errorf("prsync config: repo %q has an invalid base_url", r.Name)
		}
		key := strings.Join([]string{r.Host, strings.ToLower(r.BaseURL), r.TokenEnv}, "\x00")
		name, ok := names[key]
		if !ok {
			name = strings.ToLower(u.Host)
			if _, taken := c.Hosts[name]; taken {
				name = fmt.Sprintf("%s#%d", name, len(c.Hosts)+1)
			}
			h, err := normalizeHost(name, HostConfig{Kind: r.Host, URL: r.BaseURL, APIURL: r.APIURL(), TokenEnv: r.TokenEnv})
			if err != nil {
				return Config{}, err
			}
			c.Hosts[name], names[key] = h, name
		}
		c.Repos[strings.ToLower(r.Name)] = name
	}
	return c, nil
}

func Resolve(ctx context.Context, src repos.Source, log *slog.Logger) (Config, bool, error) {
	interval := os.Getenv(IntervalEnv)
	rs, err := repos.Enabled(ctx, src)
	if err != nil {
		return Config{}, false, err
	}
	if len(rs) > 0 {
		c, err := FromRepos(rs, interval)
		return c, err == nil, err
	}
	path := os.Getenv(ConfigEnv)
	if path == "" {
		return Config{}, false, nil
	}
	repos.Deprecated(log, ConfigEnv)
	c, err := LoadConfig(path, interval)
	return c, err == nil, err
}
