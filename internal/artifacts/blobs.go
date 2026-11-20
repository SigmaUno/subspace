package artifacts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

type blobs struct{ root string }

func newBlobs(root string) (blobs, error) {
	for _, d := range []string{"blobs", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			return blobs{}, err
		}
	}
	stale, err := filepath.Glob(filepath.Join(root, "tmp", "*"))
	if err != nil {
		return blobs{}, err
	}
	for _, p := range stale {
		_ = os.Remove(p)
	}
	return blobs{root: root}, nil
}

func (b blobs) path(sha string) (string, error) {
	if !shaRe.MatchString(sha) {
		return "", fmt.Errorf("artifacts: invalid digest %q", sha)
	}
	return filepath.Join(b.root, "blobs", sha[:2], sha), nil
}

func (b blobs) temp() (*os.File, error) {
	return os.CreateTemp(filepath.Join(b.root, "tmp"), "up-*")
}

func (b blobs) commit(tmp, sha string) (bool, error) {
	dst, err := b.path(sha)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(dst); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return false, err
	}
	return true, nil
}

func (b blobs) open(sha string) (*os.File, error) {
	p, err := b.path(sha)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(b.root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	rel, err := filepath.Rel(b.root, p)
	if err != nil {
		return nil, err
	}
	return root.Open(rel)
}

func (b blobs) remove(sha string) error {
	p, err := b.path(sha)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
