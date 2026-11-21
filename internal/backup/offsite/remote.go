package offsite

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/backup/s3"
	"github.com/SigmaUno/subspace/internal/backup/seal"
	"github.com/SigmaUno/subspace/internal/netguard"
)

const (
	objectSuffix = ".db.enc"
	timeLayout   = "20060102T150405Z"
)

var (
	objectPattern = regexp.MustCompile(`^subspace-(\d{8}T\d{6}Z)\.db\.enc$`)

	ErrNoBackups = errors.New("offsite: no backups at the destination")
)

type Deps struct {
	Getenv   func(string) string
	HTTP     *http.Client
	Resolver netguard.Resolver
	Sleep    func(ctx context.Context, d time.Duration) error
	Now      func() time.Time
}

type Remote struct {
	dest Destination
	keys []seal.Key
	desc string
}

func Open(cfg Config, d Deps) (*Remote, error) {
	if !cfg.Enabled() {
		return nil, errors.New("offsite: not configured")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	key, err := seal.LoadKey(cfg.KeyFile, cfg.KeyID)
	if err != nil {
		return nil, err
	}
	keys := []seal.Key{key}
	for _, k := range cfg.OldKeys {
		old, err := seal.LoadKey(k.File, k.ID)
		if err != nil {
			return nil, err
		}
		keys = append(keys, old)
	}
	r := &Remote{keys: keys}
	switch cfg.Kind {
	case KindFS:
		r.dest, r.desc = fsDest{root: cfg.Path}, "directory "+cfg.Path
	case KindS3:
		creds, err := cfg.S3.credentials(d.Getenv)
		if err != nil {
			return nil, err
		}
		hc := d.HTTP
		if hc == nil {
			hc = cfg.S3.httpClient(d.Resolver)
		}
		c, err := s3.New(s3.Config{
			Endpoint: cfg.S3.Endpoint, Region: cfg.S3.Region, Bucket: cfg.S3.Bucket, VirtualHost: cfg.S3.VirtualHost,
			Credentials: creds, HTTP: hc, PartSize: cfg.S3.PartSize, Sleep: d.Sleep, Now: d.Now,
		})
		if err != nil {
			return nil, err
		}
		prefix := strings.Trim(cfg.S3.Prefix, "/")
		if prefix != "" {
			prefix += "/"
		}
		r.dest, r.desc = s3Dest{c: c, prefix: prefix}, "s3 bucket "+cfg.S3.Bucket
	default:
		return nil, fmt.Errorf("offsite: unknown kind %q", cfg.Kind)
	}
	return r, nil
}

func (r *Remote) Describe() string { return r.desc }

func (r *Remote) KeyID() string { return r.keys[0].ID }

func ObjectName(created time.Time) string {
	return "subspace-" + created.UTC().Format(timeLayout) + objectSuffix
}

func ParseObjectName(name string) (time.Time, bool) {
	m := objectPattern.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.Parse(timeLayout, m[1])
	return t, err == nil
}

func (r *Remote) List(ctx context.Context) ([]Entry, error) {
	objs, err := r.dest.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, o := range objs {
		if t, ok := ParseObjectName(o.Name); ok {
			out = append(out, Entry{Name: o.Name, Created: t, Size: o.Size})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

func (r *Remote) Latest(ctx context.Context) (Entry, error) {
	list, err := r.List(ctx)
	if err != nil {
		return Entry{}, err
	}
	if len(list) == 0 {
		return Entry{}, ErrNoBackups
	}
	return list[0], nil
}

func (r *Remote) Download(ctx context.Context, name string, w io.Writer) (int64, error) {
	if _, ok := ParseObjectName(name); !ok {
		return 0, fmt.Errorf("offsite: %q is not a backup object name", name)
	}
	rc, err := r.dest.Open(ctx, name)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rc.Close() }()
	sr, err := seal.NewReader(rc, r.keys...)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	n, err := io.Copy(w, sr)
	if err != nil {
		return n, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}

func (r *Remote) Fetch(ctx context.Context, w io.Writer) (string, int64, error) {
	e, err := r.Latest(ctx)
	if err != nil {
		return "", 0, err
	}
	n, err := r.Download(ctx, e.Name, w)
	return e.Name, n, err
}
