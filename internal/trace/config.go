package trace

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/netguard"
)

const (
	EnvEndpoint     = "SUBSPACE_OTLP_ENDPOINT"
	EnvHeaders      = "SUBSPACE_OTLP_HEADERS"
	EnvSample       = "SUBSPACE_OTLP_SAMPLE"
	EnvAllowPrivate = "SUBSPACE_OTLP_ALLOW_PRIVATE"
	EnvQueue        = "SUBSPACE_OTLP_QUEUE"

	DefaultSample = 0.01
	tracesPath    = "/v1/traces"
	maxQueue      = 100000
)

type Config struct {
	URL          string
	Headers      map[string]string
	Sample       float64
	AllowPrivate bool
	Queue        int
}

func (c Config) Enabled() bool { return c.URL != "" }

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	c := Config{Sample: DefaultSample, Queue: DefaultQueue}
	raw := strings.TrimSpace(getenv(EnvEndpoint))
	if v := strings.TrimSpace(getenv(EnvSample)); v != "" {
		r, err := strconv.ParseFloat(v, 64)
		if err != nil || r < 0 || r > 1 {
			return c, fmt.Errorf("%s must be a number between 0 and 1", EnvSample)
		}
		c.Sample = r
	}
	if v := strings.TrimSpace(getenv(EnvQueue)); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxQueue {
			return c, fmt.Errorf("%s must be an integer between 1 and %d", EnvQueue, maxQueue)
		}
		c.Queue = n
	}
	switch strings.ToLower(strings.TrimSpace(getenv(EnvAllowPrivate))) {
	case "1", "true", "yes":
		c.AllowPrivate = true
	}
	if raw == "" {
		return c, nil
	}
	u, err := checkEndpoint(raw, c.AllowPrivate)
	if err != nil {
		return c, fmt.Errorf("%s: %w", EnvEndpoint, err)
	}
	c.URL = u
	if c.Headers, err = parseHeaders(getenv(EnvHeaders)); err != nil {
		return c, fmt.Errorf("%s: %w", EnvHeaders, err)
	}
	return c, nil
}

func guard(allowPrivate bool) *netguard.Guard {
	return &netguard.Guard{AllowAll: allowPrivate, DialTimeout: 5 * time.Second}
}

func checkEndpoint(raw string, allowPrivate bool) (string, error) {
	g := guard(allowPrivate)
	u, err := g.CheckURL(raw)
	if err != nil {
		return "", err
	}
	if err := g.CheckHost(u.Hostname()); err != nil {
		return "", err
	}
	if u.RawQuery != "" {
		return "", fmt.Errorf("url must not have a query")
	}
	if !strings.HasSuffix(u.Path, tracesPath) {
		u.Path = strings.TrimSuffix(u.Path, "/") + tracesPath
	}
	return u.String(), nil
}

var reservedHeaders = map[string]bool{
	"host": true, "content-length": true, "content-type": true, "transfer-encoding": true, "connection": true,
}

func parseHeaders(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !headerToken(k) || reservedHeaders[strings.ToLower(k)] {
			return nil, fmt.Errorf("invalid header entry, want name=value")
		}
		if dec, err := url.PathUnescape(v); err == nil {
			v = dec
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return nil, fmt.Errorf("invalid header value for %q", k)
		}
		out[k] = v
	}
	return out, nil
}

func headerToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

type Runtime struct {
	Tracer   *Tracer
	Exporter *OTLP
}

func (c Config) Build(log *slog.Logger, version, instance string) Runtime {
	if !c.Enabled() {
		return Runtime{Tracer: NewTracer(0, nil)}
	}
	t := guard(c.AllowPrivate).Transport()
	t.DisableKeepAlives = false
	t.MaxIdleConns = 2
	t.IdleConnTimeout = 30 * time.Second
	t.ResponseHeaderTimeout = DefaultTimeout
	exp := NewOTLP(OTLPOptions{
		URL: c.URL, Headers: c.Headers, Queue: c.Queue, Log: log,
		Client: &http.Client{Timeout: DefaultTimeout, Transport: t, CheckRedirect: netguard.NoRedirect},
		Resource: []Attr{
			String("service.name", "subspace"),
			String("service.version", version),
			String("service.instance.id", instance),
		},
	})
	return Runtime{Tracer: NewTracer(c.Sample, exp), Exporter: exp}
}

func (r Runtime) Start() {
	if r.Exporter != nil {
		r.Exporter.Start()
	}
}

func (r Runtime) Close(ctx context.Context) error {
	if r.Exporter == nil {
		return nil
	}
	return r.Exporter.Close(ctx)
}
