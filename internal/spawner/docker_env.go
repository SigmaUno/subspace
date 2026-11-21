package spawner

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	labelPrefixRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	resourceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	imageRe        = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]{0,254}(@sha256:[0-9a-f]{64})?$`)
	egressHostRe   = regexp.MustCompile(`^(\*\.|\.)?[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?)*$`)
)

func DockerConfigFromEnv(env func(string) string) (DockerConfig, error) {
	cfg := DockerConfig{
		Socket:         env("SUBSPACE_DOCKER_SOCKET"),
		User:           env("SUBSPACE_DOCKER_USER"),
		LabelPrefix:    env("SUBSPACE_DOCKER_LABEL_PREFIX"),
		Network:        env("SUBSPACE_DOCKER_NETWORK"),
		EgressImage:    env("SUBSPACE_DOCKER_EGRESS_IMAGE"),
		EgressNetwork:  env("SUBSPACE_DOCKER_EGRESS_NETWORK"),
		EgressAllow:    splitList(env("SUBSPACE_DOCKER_EGRESS_ALLOW"), ","),
		EgressPrivate:  splitList(env("SUBSPACE_DOCKER_EGRESS_PRIVATE"), ","),
		RepoVolume:     env("SUBSPACE_DOCKER_REPO_VOLUME") == "true" || env("SUBSPACE_DOCKER_REPO_VOLUME") == "1",
		SecretsDir:     env("SUBSPACE_DOCKER_SECRETS_DIR"),
		HostSecretsDir: env("SUBSPACE_DOCKER_HOST_SECRETS_DIR"),
	}
	if host := publicHost(env("SUBSPACE_PUBLIC_URL")); host != "" && cfg.EgressImage != "" {
		cfg.EgressAllow = appendUnique(cfg.EgressAllow, host)
		cfg.EgressPrivate = appendUnique(cfg.EgressPrivate, host)
	}
	images, err := parseImages(env("SUBSPACE_DOCKER_IMAGES"))
	if err != nil {
		return cfg, err
	}
	cfg.Images = images
	if cfg.Limits, err = parseLimits(env("SUBSPACE_DOCKER_LIMITS")); err != nil {
		return cfg, err
	}
	if cfg.Profiles, err = parseProfiles(env("SUBSPACE_DOCKER_PROFILES")); err != nil {
		return cfg, err
	}
	if v := env("SUBSPACE_DOCKER_WORKDIR_SIZE"); v != "" {
		if cfg.WorkdirBytes, err = parseBytes(v); err != nil {
			return cfg, err
		}
	}
	if cfg.LogPoll, err = envDuration(env, "SUBSPACE_DOCKER_LOG_POLL"); err != nil {
		return cfg, err
	}
	if v := env("SUBSPACE_DOCKER_LOG_RATE"); v != "" {
		rate, burst, ok := strings.Cut(v, ":")
		r, err1 := strconv.ParseFloat(rate, 64)
		b, err2 := strconv.Atoi(burst)
		if !ok || err1 != nil || err2 != nil || r <= 0 || b <= 0 {
			return cfg, fmt.Errorf("spawner: invalid SUBSPACE_DOCKER_LOG_RATE %q (want lines-per-second:burst)", v)
		}
		cfg.LogRate, cfg.LogBurst = r, b
	}
	return cfg, nil
}

func publicHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func appendUnique(xs []string, x string) []string {
	if slices.Contains(xs, x) {
		return xs
	}
	return append(xs, x)
}

func splitList(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseImages(s string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, entry := range splitList(s, ";") {
		kind, imgs, ok := strings.Cut(entry, "=")
		kind = strings.TrimSpace(kind)
		list := splitList(imgs, ",")
		if !ok || kind == "" || len(list) == 0 {
			return nil, fmt.Errorf("spawner: invalid SUBSPACE_DOCKER_IMAGES entry %q (want kind=image[,image];...)", entry)
		}
		out[kind] = append(out[kind], list...)
	}
	return out, nil
}

func parseProfiles(s string) (map[string]Limits, error) {
	out := map[string]Limits{}
	for _, entry := range splitList(s, ";") {
		kind, spec, ok := strings.Cut(entry, "=")
		kind = strings.TrimSpace(kind)
		if !ok || kind == "" {
			return nil, fmt.Errorf("spawner: invalid SUBSPACE_DOCKER_PROFILES entry %q (want kind=cpus:2,memory:4g,pids:512;...)", entry)
		}
		l, err := parseLimits(spec)
		if err != nil {
			return nil, err
		}
		out[kind] = l
	}
	return out, nil
}

func parseLimits(s string) (Limits, error) {
	var l Limits
	for _, kv := range splitList(s, ",") {
		k, v, ok := strings.Cut(kv, ":")
		if !ok {
			return l, fmt.Errorf("spawner: invalid docker limit %q (want name:value)", kv)
		}
		var err error
		switch strings.TrimSpace(k) {
		case "cpus":
			l.CPUs, err = strconv.ParseFloat(strings.TrimSpace(v), 64)
		case "memory":
			l.MemoryBytes, err = parseBytes(v)
		case "pids":
			l.Pids, err = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		default:
			return l, fmt.Errorf("spawner: unknown docker limit %q (want cpus, memory or pids)", k)
		}
		if err != nil {
			return l, fmt.Errorf("spawner: invalid docker limit %q", kv)
		}
	}
	return l, l.validate()
}

func parseBytes(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "g"):
		mult, s = 1<<30, strings.TrimSuffix(s, "g")
	case strings.HasSuffix(s, "m"):
		mult, s = 1<<20, strings.TrimSuffix(s, "m")
	case strings.HasSuffix(s, "k"):
		mult, s = 1<<10, strings.TrimSuffix(s, "k")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > (1<<62)/mult {
		return 0, fmt.Errorf("spawner: invalid size %q", s)
	}
	return n * mult, nil
}
