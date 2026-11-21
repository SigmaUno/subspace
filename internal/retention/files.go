package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const archivedFilesDir = "transcript-files"

func (s *Service) liveFile(name string) string {
	return filepath.Join(s.files, filepath.FromSlash(name))
}

func (s *Service) archivedFile(name string) string {
	return filepath.Join(s.dir, archivedFilesDir, filepath.FromSlash(name))
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // manifest path under a configured directory
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

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func relocate(from, to string, r row) error {
	name := r.str("file")
	if !filepath.IsLocal(filepath.FromSlash(name)) {
		return fmt.Errorf("transcript file %q is not a relative path inside the transcript directory", name)
	}
	got, err := fileSum(from)
	if err != nil {
		return err
	}
	if got != r.str("sha256") {
		return fmt.Errorf("transcript file %s: sha256 %s does not match manifest %s", name, got, r.str("sha256"))
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o750); err != nil {
		return err
	}
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	if err := copyFile(from, to); err != nil {
		return err
	}
	return os.Remove(from)
}

func copyFile(from, to string) (err error) {
	in, err := os.Open(from) //nolint:gosec // manifest path under a configured directory
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(to), ".move-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err = io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), to)
}

func (s *Service) archiveFiles(rows []row) error {
	for _, r := range rows {
		name := r.str("file")
		from, to := s.liveFile(name), s.archivedFile(name)
		switch {
		case exists(from):
			if err := relocate(from, to, r); err != nil {
				return err
			}
		case exists(to):
		default:
			return fmt.Errorf("transcript file %s is missing; refusing to archive its manifest row", name)
		}
	}
	return nil
}

func (s *Service) restoreFiles(rows []row) error {
	for _, r := range rows {
		name := r.str("file")
		from, to := s.archivedFile(name), s.liveFile(name)
		switch {
		case exists(from):
			if err := relocate(from, to, r); err != nil {
				return err
			}
		case exists(to):
		default:
			return fmt.Errorf("archived transcript file %s is missing", name)
		}
	}
	return nil
}

func verifyFiles(dir string, rows []row) string {
	for _, r := range rows {
		name := r.str("file")
		if !filepath.IsLocal(filepath.FromSlash(name)) {
			return fmt.Sprintf("transcript file %q is not a relative path", name)
		}
		got, err := fileSum(filepath.Join(dir, archivedFilesDir, filepath.FromSlash(name)))
		switch {
		case errors.Is(err, os.ErrNotExist):
			return fmt.Sprintf("archived transcript file %s is missing", name)
		case err != nil:
			return err.Error()
		case got != r.str("sha256"):
			return fmt.Sprintf("archived transcript file %s does not match its manifest sha256", name)
		}
	}
	return ""
}
