package gateway

import (
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/netguard"
	"github.com/SigmaUno/subspace/internal/redact"
)

const (
	TransportHTTP  = "streamable-http"
	TransportStdio = "stdio"

	Prefix    = "gw_"
	Separator = "__"

	DefaultTimeout     = 30 * time.Second
	DefaultResultBytes = 256 << 10

	minTimeoutMS   = 1000
	maxTimeoutMS   = 120000
	minResultBytes = 1 << 10
	maxResultBytes = 1 << 20
	maxGlobs       = 50
	maxRateLimits  = 50
	maxPerMinute   = 10000
	maxArgs        = 32
	maxArg         = 500
)

var (
	nameRe     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)
	globRe     = regexp.MustCompile(`^[A-Za-z0-9_.*?-]{1,64}$`)
	credEnvRe  = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	agentRoles = []string{model.RoleLeader, model.RoleWorker, model.RoleTester, model.RoleReviewer}
)

type RateLimit struct {
	Tool      string `json:"tool"`
	PerMinute int    `json:"per_minute"`
}

type Budget struct {
	MaxCallsPerDay       int   `json:"max_calls_per_day"`
	MaxResultBytesPerDay int64 `json:"max_result_bytes_per_day"`
}

type Gateway struct {
	Name              string      `json:"name"`
	Transport         string      `json:"transport"`
	URL               string      `json:"url"`
	Command           string      `json:"command"`
	Args              []string    `json:"args"`
	CredentialEnv     string      `json:"credential_env"`
	AllowedRoles      []string    `json:"allowed_roles"`
	AllowTools        []string    `json:"allow_tools"`
	DenyTools         []string    `json:"deny_tools"`
	RateLimits        []RateLimit `json:"rate_limits"`
	Budget            Budget      `json:"budget"`
	TimeoutMS         int         `json:"timeout_ms"`
	MaxResultBytes    int         `json:"max_result_bytes"`
	AllowPrivateHosts bool        `json:"allow_private_hosts"`
	AuditArguments    bool        `json:"audit_arguments"`
	Enabled           bool        `json:"enabled"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
}

type Input struct {
	Name              string      `json:"name"`
	Transport         string      `json:"transport"`
	URL               string      `json:"url"`
	Command           string      `json:"command"`
	Args              []string    `json:"args"`
	CredentialEnv     string      `json:"credential_env"`
	AllowedRoles      []string    `json:"allowed_roles"`
	AllowTools        []string    `json:"allow_tools"`
	DenyTools         []string    `json:"deny_tools"`
	RateLimits        []RateLimit `json:"rate_limits"`
	Budget            Budget      `json:"budget"`
	TimeoutMS         int         `json:"timeout_ms"`
	MaxResultBytes    int         `json:"max_result_bytes"`
	AllowPrivateHosts bool        `json:"allow_private_hosts"`
	AuditArguments    bool        `json:"audit_arguments"`
	Enabled           *bool       `json:"enabled"`
}

func (in Input) Gateway() Gateway {
	return Gateway{Name: in.Name, Transport: in.Transport, URL: in.URL, Command: in.Command, Args: in.Args,
		CredentialEnv: in.CredentialEnv, AllowedRoles: in.AllowedRoles, AllowTools: in.AllowTools, DenyTools: in.DenyTools,
		RateLimits: in.RateLimits, Budget: in.Budget, TimeoutMS: in.TimeoutMS, MaxResultBytes: in.MaxResultBytes,
		AllowPrivateHosts: in.AllowPrivateHosts, AuditArguments: in.AuditArguments, Enabled: in.Enabled == nil || *in.Enabled}
}

func (g *Gateway) Validate(commands []string) error {
	g.Name = strings.TrimSpace(g.Name)
	if !nameRe.MatchString(g.Name) {
		return model.BadRequest("name must start with a lowercase letter and use at most 16 lowercase letters, digits and '-'")
	}
	switch g.Transport {
	case TransportHTTP:
		if err := g.validateHTTP(); err != nil {
			return err
		}
	case TransportStdio:
		if err := g.validateStdio(commands); err != nil {
			return err
		}
	default:
		return model.BadRequest("transport must be %s or %s", TransportHTTP, TransportStdio)
	}
	if g.CredentialEnv != "" && (!credEnvRe.MatchString(g.CredentialEnv) || redact.Contains(g.CredentialEnv)) {
		return model.BadRequest("credential_env must be the name of an environment variable (A-Z, 0-9 and _), never the credential itself")
	}
	if strings.HasPrefix(g.CredentialEnv, "SUBSPACE_") && !strings.HasPrefix(g.CredentialEnv, "SUBSPACE_GATEWAY_") {
		return model.BadRequest("credential_env must not name a Subspace server setting; only SUBSPACE_GATEWAY_* variables are allowed")
	}
	if err := g.validateRoles(); err != nil {
		return err
	}
	var err error
	if g.AllowTools, err = cleanGlobs("allow_tools", g.AllowTools); err != nil {
		return err
	}
	if len(g.AllowTools) == 0 {
		g.AllowTools = []string{"*"}
	}
	if g.DenyTools, err = cleanGlobs("deny_tools", g.DenyTools); err != nil {
		return err
	}
	if err := g.validateLimits(); err != nil {
		return err
	}
	return nil
}

func (g *Gateway) validateHTTP() error {
	if g.Command != "" || len(g.Args) > 0 {
		return model.BadRequest("command and args are only allowed for stdio gateways")
	}
	guard := &netguard.Guard{AllowAll: g.AllowPrivateHosts}
	u, err := guard.CheckURL(g.URL)
	if err != nil {
		return model.BadRequest("url: %s; private and loopback hosts need allow_private_hosts", err.Error())
	}
	if !g.AllowPrivateHosts && netguard.LocalName(u.Hostname()) {
		return model.BadRequest("url: %s; private and loopback hosts need allow_private_hosts", netguard.ErrBlocked.Error())
	}
	g.Args = []string{}
	return nil
}

func (g *Gateway) validateStdio(commands []string) error {
	if g.URL != "" {
		return model.BadRequest("url is only allowed for streamable-http gateways")
	}
	if g.AllowPrivateHosts {
		return model.BadRequest("allow_private_hosts is only allowed for streamable-http gateways")
	}
	if !filepath.IsAbs(g.Command) || filepath.Clean(g.Command) != g.Command {
		return model.BadRequest("command must be a clean absolute path")
	}
	if !slices.Contains(commands, g.Command) {
		if len(commands) == 0 {
			return model.BadRequest("no stdio commands are allowed; set SUBSPACE_GATEWAY_COMMANDS on the server")
		}
		return model.BadRequest("command %s is not in SUBSPACE_GATEWAY_COMMANDS", g.Command)
	}
	if len(g.Args) > maxArgs {
		return model.BadRequest("args has at most %d entries", maxArgs)
	}
	for _, a := range g.Args {
		if len(a) > maxArg || strings.ContainsAny(a, "\x00\r\n") {
			return model.BadRequest("args must be single lines of at most %d bytes", maxArg)
		}
		if redact.Contains(a) {
			return model.BadRequest("args must not contain credentials; use credential_env")
		}
	}
	if g.Args == nil {
		g.Args = []string{}
	}
	return nil
}

func (g *Gateway) validateRoles() error {
	var roles []string
	for _, r := range g.AllowedRoles {
		if !slices.Contains(agentRoles, r) {
			return model.BadRequest("allowed_roles may only contain %s", strings.Join(agentRoles, ", "))
		}
		if !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	if len(roles) == 0 {
		return model.BadRequest("allowed_roles needs at least one role")
	}
	slices.Sort(roles)
	g.AllowedRoles = roles
	return nil
}

func (g *Gateway) validateLimits() error {
	if len(g.RateLimits) > maxRateLimits {
		return model.BadRequest("rate_limits has at most %d entries", maxRateLimits)
	}
	for _, l := range g.RateLimits {
		if !globRe.MatchString(l.Tool) {
			return model.BadRequest("rate_limits tool must be a tool name or glob of letters, digits, '_', '.', '-', '*' and '?'")
		}
		if l.PerMinute < 1 || l.PerMinute > maxPerMinute {
			return model.BadRequest("rate_limits per_minute must be between 1 and %d", maxPerMinute)
		}
	}
	if g.RateLimits == nil {
		g.RateLimits = []RateLimit{}
	}
	if g.Budget.MaxCallsPerDay < 0 || g.Budget.MaxResultBytesPerDay < 0 {
		return model.BadRequest("budget limits must not be negative")
	}
	if g.TimeoutMS == 0 {
		g.TimeoutMS = int(DefaultTimeout / time.Millisecond)
	}
	if g.TimeoutMS < minTimeoutMS || g.TimeoutMS > maxTimeoutMS {
		return model.BadRequest("timeout_ms must be between %d and %d", minTimeoutMS, maxTimeoutMS)
	}
	if g.MaxResultBytes == 0 {
		g.MaxResultBytes = DefaultResultBytes
	}
	if g.MaxResultBytes < minResultBytes || g.MaxResultBytes > maxResultBytes {
		return model.BadRequest("max_result_bytes must be between %d and %d", minResultBytes, maxResultBytes)
	}
	return nil
}

func cleanGlobs(field string, in []string) ([]string, error) {
	if len(in) > maxGlobs {
		return nil, model.BadRequest("%s has at most %d patterns", field, maxGlobs)
	}
	out := []string{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if !globRe.MatchString(p) {
			return nil, model.BadRequest("%s patterns use letters, digits, '_', '.', '-', '*' and '?' (at most 64 characters)", field)
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out, nil
}

func match(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

func (g *Gateway) Allows(tool string) bool {
	return match(g.AllowTools, tool) && !match(g.DenyTools, tool)
}

func (g *Gateway) AllowsRole(role string) bool { return slices.Contains(g.AllowedRoles, role) }

func (g *Gateway) Timeout() time.Duration { return time.Duration(g.TimeoutMS) * time.Millisecond }

func (g *Gateway) rateFor(tool string) int {
	for _, l := range g.RateLimits {
		if match([]string{l.Tool}, tool) {
			return l.PerMinute
		}
	}
	return 0
}

func ToolName(gateway, tool string) string { return Prefix + gateway + Separator + tool }

func SplitName(name string) (gateway, tool string, ok bool) {
	rest, ok := strings.CutPrefix(name, Prefix)
	if !ok {
		return "", "", false
	}
	gateway, tool, ok = strings.Cut(rest, Separator)
	return gateway, tool, ok && gateway != "" && tool != ""
}
