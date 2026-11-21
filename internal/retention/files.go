package retention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/SigmaUno/subspace/internal/store"
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

func prunedMarker(path string) string { return path + ".pruned" }

func (s *Service) manifestRowExists(ctx context.Context, r row) (bool, error) {
	n, err := store.ViewSQL(ctx, s.st, func(q store.SQL) (int64, error) {
		return scalar(q, `SELECT COUNT(*) FROM task_transcripts WHERE id = ?`, r.int("id"))
	})
	return n > 0, err
}

func (s *Service) archiveFiles(ctx context.Context, rows []row) error {
	for _, r := range rows {
		name := r.str("file")
		if !filepath.IsLocal(filepath.FromSlash(name)) {
			return fmt.Errorf("transcript file %q is not a relative path inside the transcript directory", name)
		}
		from, to := s.liveFile(name), s.archivedFile(name)
		switch {
		case exists(from):
			if err := relocate(from, to, r); err != nil {
				return err
			}
		case exists(to), exists(prunedMarker(to)):
		default:
			live, err := s.manifestRowExists(ctx, r)
			if err != nil {
				return err
			}
			if live {
				return fmt.Errorf("transcript file %s is missing; refusing to archive its manifest row", name)
			}
			if err := os.MkdirAll(filepath.Dir(to), 0o750); err != nil {
				return err
			}
			if err := os.WriteFile(prunedMarker(to), []byte(r.str("sha256")+"\n"), 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) restoreFiles(rows []row) ([]row, error) {
	kept := rows[:0:0]
	for _, r := range rows {
		name := r.str("file")
		if !filepath.IsLocal(filepath.FromSlash(name)) {
			return nil, fmt.Errorf("transcript file %q is not a relative path inside the transcript directory", name)
		}
		from, to := s.archivedFile(name), s.liveFile(name)
		switch {
		case exists(from):
			if err := relocate(from, to, r); err != nil {
				return nil, err
			}
		case exists(to):
		case exists(prunedMarker(from)):
			continue
		default:
			return nil, fmt.Errorf("archived transcript file %s is missing", name)
		}
		kept = append(kept, r)
	}
	return kept, nil
}

func verifyFiles(dir string, rows []row) string {
	for _, r := range rows {
		name := r.str("file")
		if !filepath.IsLocal(filepath.FromSlash(name)) {
			return fmt.Sprintf("transcript file %q is not a relative path", name)
		}
		path := filepath.Join(dir, archivedFilesDir, filepath.FromSlash(name))
		got, err := fileSum(path)
		switch {
		case errors.Is(err, os.ErrNotExist) && exists(prunedMarker(path)):
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
