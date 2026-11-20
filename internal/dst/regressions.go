package dst

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type Regression struct {
	Seed  int64
	Steps int
}

func LoadRegressions(path string) ([]Regression, error) {
	f, err := os.Open(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Regression
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var r Regression
		if _, err := fmt.Sscanf(line, "%d %d", &r.Seed, &r.Steps); err != nil {
			return nil, fmt.Errorf("%s:%d: want \"<seed> <steps>\": %w", path, n, err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func AppendRegression(path string, r Regression, note string) error {
	have, err := LoadRegressions(path)
	if err != nil {
		return err
	}
	for _, h := range have {
		if h.Seed == r.Seed {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Clean(path), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "%d %d # %s\n", r.Seed, r.Steps, note); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
