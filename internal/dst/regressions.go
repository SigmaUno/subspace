package dst

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	ModeBoard  = "board"
	ModeServer = "server"
)

type Regression struct {
	Seed  int64
	Steps int
	Mode  string
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
		line, _, _ := strings.Cut(sc.Text(), "#")
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		r, err := parseRegression(f)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: want \"<seed> <steps> [board|server]\": %w", path, n, err)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func parseRegression(f []string) (Regression, error) {
	r := Regression{Mode: ModeBoard}
	if len(f) < 2 || len(f) > 3 {
		return r, errors.New("wrong number of fields")
	}
	if len(f) == 3 {
		r.Mode = f[2]
	}
	if r.Mode != ModeBoard && r.Mode != ModeServer {
		return r, fmt.Errorf("unknown mode %q", r.Mode)
	}
	var err error
	if r.Seed, err = strconv.ParseInt(f[0], 10, 64); err != nil {
		return r, err
	}
	r.Steps, err = strconv.Atoi(f[1])
	return r, err
}

func AppendRegression(path string, r Regression, note string) error {
	have, err := LoadRegressions(path)
	if err != nil {
		return err
	}
	for _, h := range have {
		if h.Seed == r.Seed && h.Mode == r.Mode {
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
	if r.Mode == "" {
		r.Mode = ModeBoard
	}
	if _, err := fmt.Fprintf(f, "%d %d %s # %s\n", r.Seed, r.Steps, r.Mode, note); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
