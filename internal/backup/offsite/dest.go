package offsite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/backup/s3"
)

var ErrNotFound = errors.New("offsite: object not found")

type Object struct {
	Name    string
	Size    int64
	SHA256  string
	ModTime time.Time
}

type Destination interface {
	Put(ctx context.Context, name string, src *os.File, size int64, sha256hex string) error
	Stat(ctx context.Context, name string) (Object, error)
	ReadRange(ctx context.Context, name string, off, n int64) ([]byte, error)
	Open(ctx context.Context, name string) (io.ReadCloser, error)
	List(ctx context.Context) ([]Object, error)
	Delete(ctx context.Context, name string) error
}

type fsDest struct{ root string }

func (d fsDest) path(name string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("offsite: invalid object name %q", name)
	}
	return filepath.Join(d.root, name), nil
}

func (d fsDest) ready() error {
	fi, err := os.Stat(d.root)
	if err != nil {
		return fmt.Errorf("offsite destination %s: %w", d.root, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("offsite destination %s is not a directory", d.root)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // path built from the configured root
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (d fsDest) Put(ctx context.Context, name string, src *os.File, size int64, sum string) (err error) {
	if err := d.ready(); err != nil {
		return err
	}
	dst, err := d.path(name)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(dst); err == nil && fi.Size() == size {
		if have, err := fileSHA256(dst); err == nil && have == sum {
			return nil
		}
	}
	tmp, err := os.CreateTemp(d.root, ".put-*.part")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = io.Copy(tmp, io.NewSectionReader(src, 0, size)); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o600)
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func (d fsDest) Stat(_ context.Context, name string) (Object, error) {
	p, err := d.path(name)
	if err != nil {
		return Object{}, err
	}
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return Object{}, ErrNotFound
	}
	if err != nil {
		return Object{}, err
	}
	sum, err := fileSHA256(p)
	if err != nil {
		return Object{}, err
	}
	return Object{Name: name, Size: fi.Size(), SHA256: sum, ModTime: fi.ModTime()}, nil
}

func (d fsDest) ReadRange(_ context.Context, name string, off, n int64) ([]byte, error) {
	p, err := d.path(name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p) //nolint:gosec // path built from the configured root
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, n)
	m, err := f.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:m], nil
}

func (d fsDest) Open(_ context.Context, name string) (io.ReadCloser, error) {
	p, err := d.path(name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p) //nolint:gosec // path built from the configured root
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

func (d fsDest) List(context.Context) ([]Object, error) {
	if err := d.ready(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(d.root)
	if err != nil {
		return nil, err
	}
	var out []Object
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Object{Name: e.Name(), Size: fi.Size(), ModTime: fi.ModTime()})
	}
	return out, nil
}

func (d fsDest) Delete(_ context.Context, name string) error {
	p, err := d.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

type s3Dest struct {
	c      *s3.Client
	prefix string
}

func (d s3Dest) key(name string) string { return d.prefix + name }

func wrapS3(err error) error {
	if errors.Is(err, s3.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func (d s3Dest) Put(ctx context.Context, name string, src *os.File, size int64, sum string) error {
	return d.c.Put(ctx, d.key(name), src, size, sum)
}

func (d s3Dest) Stat(ctx context.Context, name string) (Object, error) {
	o, err := d.c.Head(ctx, d.key(name))
	if err != nil {
		return Object{}, wrapS3(err)
	}
	return Object{Name: name, Size: o.Size, SHA256: o.SHA256, ModTime: o.LastModified}, nil
}

func (d s3Dest) ReadRange(ctx context.Context, name string, off, n int64) ([]byte, error) {
	b, err := d.c.GetRange(ctx, d.key(name), off, n)
	return b, wrapS3(err)
}

func (d s3Dest) Open(ctx context.Context, name string) (io.ReadCloser, error) {
	rc, err := d.c.Get(ctx, d.key(name))
	return rc, wrapS3(err)
}

func (d s3Dest) List(ctx context.Context) ([]Object, error) {
	objs, err := d.c.List(ctx, d.prefix)
	if err != nil {
		return nil, err
	}
	out := make([]Object, 0, len(objs))
	for _, o := range objs {
		name := strings.TrimPrefix(o.Key, d.prefix)
		if name == "" || strings.Contains(name, "/") {
			continue
		}
		out = append(out, Object{Name: name, Size: o.Size, ModTime: o.LastModified})
	}
	return out, nil
}

func (d s3Dest) Delete(ctx context.Context, name string) error {
	return wrapS3(d.c.Delete(ctx, d.key(name)))
}
