package offsite

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/backup/s3"
	"github.com/SigmaUno/subspace/internal/backup/seal"
	"github.com/SigmaUno/subspace/internal/netguard"
)

const (
	EnvKind               = "SUBSPACE_OFFSITE_KIND"
	EnvPath               = "SUBSPACE_OFFSITE_PATH"
	EnvKeyFile            = "SUBSPACE_OFFSITE_KEY_FILE"
	EnvKeyID              = "SUBSPACE_OFFSITE_KEY_ID"
	EnvOldKeys            = "SUBSPACE_OFFSITE_OLD_KEYS"
	EnvKeepLast           = "SUBSPACE_OFFSITE_KEEP_LAST"
	EnvKeepDaily          = "SUBSPACE_OFFSITE_KEEP_DAILY"
	EnvKeepWeekly         = "SUBSPACE_OFFSITE_KEEP_WEEKLY" //nolint:gosec // setting names, not credentials
	EnvInterval           = "SUBSPACE_OFFSITE_INTERVAL"
	EnvS3Endpoint         = "SUBSPACE_OFFSITE_S3_ENDPOINT"
	EnvS3Region           = "SUBSPACE_OFFSITE_S3_REGION"
	EnvS3Bucket           = "SUBSPACE_OFFSITE_S3_BUCKET"
	EnvS3Prefix           = "SUBSPACE_OFFSITE_S3_PREFIX"
	EnvS3VirtualHost      = "SUBSPACE_OFFSITE_S3_VIRTUAL_HOST"
	EnvS3AllowPrivate     = "SUBSPACE_OFFSITE_S3_ALLOW_PRIVATE"
	EnvS3PartSize         = "SUBSPACE_OFFSITE_S3_PART_SIZE"
	EnvS3AccessKeyEnv     = "SUBSPACE_OFFSITE_S3_ACCESS_KEY_ENV"
	EnvS3AccessKeyFile    = "SUBSPACE_OFFSITE_S3_ACCESS_KEY_FILE"
	EnvS3SecretKeyEnv     = "SUBSPACE_OFFSITE_S3_SECRET_KEY_ENV"     //nolint:gosec // setting names, not credentials
	EnvS3SecretKeyFile    = "SUBSPACE_OFFSITE_S3_SECRET_KEY_FILE"    //nolint:gosec // setting names, not credentials
	EnvS3SessionTokenEnv  = "SUBSPACE_OFFSITE_S3_SESSION_TOKEN_ENV"  //nolint:gosec // setting names, not credentials
	EnvS3SessionTokenFile = "SUBSPACE_OFFSITE_S3_SESSION_TOKEN_FILE" //nolint:gosec // setting names, not credentials

	KindFS = "fs"
	KindS3 = "s3"

	DefaultInterval = 10 * time.Minute
	s3Timeout       = 2 * time.Minute
)

var (
	bucketPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionPattern  = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

var privateRanges = func() []netip.Prefix {
	p, err := netguard.ParsePrefixes("127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7")
	if err != nil {
		panic(err)
	}
	return p
}()

type KeyRef struct {
	ID   string
	File string
}

type Source struct {
	Env  string
	File string
}

type S3 struct {
	Endpoint     string
	Region       string
	Bucket       string
	Prefix       string
	VirtualHost  bool
	AllowPrivate bool
	PartSize     int64
	AccessKey    Source
	SecretKey    Source
	SessionToken Source
}

type Config struct {
	Kind      string
	Path      string
	KeyFile   string
	KeyID     string
	OldKeys   []KeyRef
	Retention Policy
	Interval  time.Duration
	S3        S3
}

func (c Config) Enabled() bool { return c.Kind != "" }

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	get := func(k string) string { return strings.TrimSpace(getenv(k)) }
	c := Config{Kind: strings.ToLower(get(EnvKind))}
	if c.Kind == "" {
		return Config{}, nil
	}
	var errs []error
	number := func(key string, def int) int {
		v := get(key)
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			errs = append(errs, fmt.Errorf("%s: want a non-negative integer, got %q", key, v))
			return def
		}
		return n
	}
	boolean := func(key string) bool {
		v := get(key)
		if v == "" {
			return false
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: want true or false, got %q", key, v))
		}
		return b
	}
	c.Path, c.KeyFile, c.KeyID = get(EnvPath), get(EnvKeyFile), get(EnvKeyID)
	for _, e := range strings.Split(get(EnvOldKeys), ",") {
		if e = strings.TrimSpace(e); e == "" {
			continue
		}
		id, file, _ := strings.Cut(e, "=")
		c.OldKeys = append(c.OldKeys, KeyRef{ID: id, File: file})
	}
	c.Retention = Policy{Last: number(EnvKeepLast, DefaultPolicy.Last), Daily: number(EnvKeepDaily, DefaultPolicy.Daily), Weekly: number(EnvKeepWeekly, DefaultPolicy.Weekly)}
	c.Interval = DefaultInterval
	if v := get(EnvInterval); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s: want a positive duration such as 10m, got %q", EnvInterval, v))
		} else {
			c.Interval = d
		}
	}
	c.S3 = S3{
		Endpoint: get(EnvS3Endpoint), Region: get(EnvS3Region), Bucket: get(EnvS3Bucket), Prefix: get(EnvS3Prefix),
		VirtualHost: boolean(EnvS3VirtualHost), AllowPrivate: boolean(EnvS3AllowPrivate),
		AccessKey:    Source{get(EnvS3AccessKeyEnv), get(EnvS3AccessKeyFile)},
		SecretKey:    Source{get(EnvS3SecretKeyEnv), get(EnvS3SecretKeyFile)},
		SessionToken: Source{get(EnvS3SessionTokenEnv), get(EnvS3SessionTokenFile)},
	}
	if v := get(EnvS3PartSize); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			errs = append(errs, fmt.Errorf("%s: want a size in bytes, got %q", EnvS3PartSize, v))
		}
		c.S3.PartSize = n
	}
	if err := errors.Join(errs...); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	switch c.Kind {
	case KindFS:
		if c.Path == "" || !filepath.IsAbs(c.Path) {
			bad("%s: want an absolute path to the mounted destination", EnvPath)
		}
	case KindS3:
		errs = append(errs, c.S3.validate())
	default:
		bad("%s: want %q or %q, got %q", EnvKind, KindFS, KindS3, c.Kind)
	}
	if c.KeyFile == "" {
		bad("%s: required, off-site backups are always encrypted", EnvKeyFile)
	}
	for _, k := range c.OldKeys {
		if !seal.ValidKeyID(k.ID) || !filepath.IsAbs(k.File) {
			bad("%s: want a comma-separated list of <key id>=<absolute path>", EnvOldKeys)
			break
		}
	}
	if c.KeyID != "" && !seal.ValidKeyID(c.KeyID) {
		bad("%s: want 1-64 characters of letters, digits, '.', '_' or '-'", EnvKeyID)
	}
	p := c.Retention
	if p.Last < 0 || p.Daily < 0 || p.Weekly < 0 || p.Last+p.Daily+p.Weekly == 0 {
		bad("retention: %s, %s and %s must not all be zero", EnvKeepLast, EnvKeepDaily, EnvKeepWeekly)
	}
	if c.Interval <= 0 {
		bad("%s: must be positive", EnvInterval)
	}
	return errors.Join(errs...)
}

func (s S3) validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if u, err := url.Parse(s.Endpoint); err != nil || s.Endpoint == "" {
		bad("%s: want an https URL", EnvS3Endpoint)
	} else {
		if u.Scheme != "https" && (u.Scheme != "http" || !s.AllowPrivate) {
			bad("%s: scheme must be https (http needs %s)", EnvS3Endpoint, EnvS3AllowPrivate)
		}
		if u.Path != "" && u.Path != "/" || u.RawQuery != "" {
			bad("%s: must not contain a path or query", EnvS3Endpoint)
		}
		g := s.guard(nil)
		var err error
		if !s.AllowPrivate {
			err = g.CheckHost(u.Hostname())
		}
		if err == nil {
			_, err = g.CheckURL(s.Endpoint)
		}
		if err != nil {
			bad("%s: %v (set %s to use a private endpoint)", EnvS3Endpoint, err, EnvS3AllowPrivate)
		}
	}
	if !regionPattern.MatchString(s.Region) {
		bad("%s: required", EnvS3Region)
	}
	if !bucketPattern.MatchString(s.Bucket) {
		bad("%s: not a valid bucket name", EnvS3Bucket)
	}
	if s.Prefix != "" && (strings.HasPrefix(s.Prefix, "/") || strings.Contains(s.Prefix, "..") || strings.ContainsFunc(s.Prefix, func(r rune) bool { return r < 0x20 || r == 0x7f })) {
		bad("%s: must be relative and without '..'", EnvS3Prefix)
	}
	if s.PartSize != 0 && s.PartSize < s3.MinPartSize {
		bad("%s: at least %d bytes", EnvS3PartSize, s3.MinPartSize)
	}
	for _, c := range []struct {
		name     string
		src      Source
		envKey   string
		fileKey  string
		optional bool
	}{
		{"access key", s.AccessKey, EnvS3AccessKeyEnv, EnvS3AccessKeyFile, false},
		{"secret key", s.SecretKey, EnvS3SecretKeyEnv, EnvS3SecretKeyFile, false},
		{"session token", s.SessionToken, EnvS3SessionTokenEnv, EnvS3SessionTokenFile, true},
	} {
		switch {
		case c.src.Env != "" && c.src.File != "":
			bad("%s: set only one of %s and %s", c.name, c.envKey, c.fileKey)
		case c.src.Env == "" && c.src.File == "" && !c.optional:
			bad("%s: set %s (name of an environment variable) or %s", c.name, c.envKey, c.fileKey)
		case c.src.Env != "" && !envNamePattern.MatchString(c.src.Env):
			bad("%s: %s must name an environment variable, not hold the secret", c.name, c.envKey)
		case c.src.File != "" && !filepath.IsAbs(c.src.File):
			bad("%s: %s must be an absolute path", c.name, c.fileKey)
		}
	}
	return errors.Join(errs...)
}

func (s S3) guard(r netguard.Resolver) *netguard.Guard {
	g := &netguard.Guard{Resolver: r, DialTimeout: 10 * time.Second, LookupTimeout: 10 * time.Second}
	if s.AllowPrivate {
		g.Allow = privateRanges
	}
	return g
}

func (s S3) secret(src Source, getenv func(string) string) (string, error) {
	switch {
	case src.Env != "":
		return strings.TrimSpace(getenv(src.Env)), nil
	case src.File != "":
		raw, err := os.ReadFile(src.File)
		if err != nil {
			return "", fmt.Errorf("read credentials file: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	return "", nil
}

func (s S3) httpClient(r netguard.Resolver) *http.Client {
	t := s.guard(r).Transport()
	t.ResponseHeaderTimeout = s3Timeout
	return &http.Client{Transport: t, CheckRedirect: netguard.NoRedirect}
}

func (s S3) credentials(getenv func(string) string) (s3.Credentials, error) {
	var c s3.Credentials
	var err error
	if c.AccessKey, err = s.secret(s.AccessKey, getenv); err != nil {
		return c, err
	}
	if c.SecretKey, err = s.secret(s.SecretKey, getenv); err != nil {
		return c, err
	}
	if c.SessionToken, err = s.secret(s.SessionToken, getenv); err != nil {
		return c, err
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return c, errors.New("s3 credentials are empty: check the variables or files named in the configuration")
	}
	return c, nil
}
