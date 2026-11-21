package reload

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/SigmaUno/subspace/internal/logpipe"
)

type fake struct {
	keys    []string
	got     map[string]string
	err     error
	ignored []Ignored
	commits int
}

func (f *fake) PrepareReload(getenv func(string) string) (Plan, error) {
	f.got = map[string]string{}
	for _, k := range f.keys {
		f.got[k] = getenv(k)
	}
	if f.err != nil {
		return Plan{}, f.err
	}
	return Plan{Commit: func() { f.commits++ }, Ignored: f.ignored}, nil
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func manager(file string, env map[string]string) *Manager {
	return New(file, func(k string) string { return env[k] })
}

func TestFileOverlaysEnvironmentWithListsNumbersAndNull(t *testing.T) {
	f := &fake{keys: []string{"A", "B", "C", "D", "E"}}
	m := manager(writeFile(t, `{"A":"file","B":["10.0.0.0/8","192.168.0.0/16"],"C":5,"D":null,"E":true}`),
		map[string]string{"A": "env", "D": "env", "F": "env"})
	m.Register("fake", f.keys, f)
	res, err := m.Reload()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "file", "B": "10.0.0.0/8,192.168.0.0/16", "C": "5", "D": "", "E": "true"}
	for k, v := range want {
		if f.got[k] != v {
			t.Errorf("%s = %q, want %q", k, f.got[k], v)
		}
	}
	if !slices.Equal(res.Applied, []string{"A", "B", "C", "D", "E"}) || len(res.Ignored) != 0 || f.commits != 1 {
		t.Fatalf("result %+v commits %d", res, f.commits)
	}
}

func TestAnyErrorRejectsEveryPart(t *testing.T) {
	good, bad := &fake{keys: []string{"A"}}, &fake{keys: []string{"B"}, err: errors.New("B: nope")}
	m := manager(writeFile(t, `{"A":"1","B":"2"}`), nil)
	m.Register("good", good.keys, good)
	m.Register("bad", bad.keys, bad)
	_, err := m.Reload()
	var rejected *Error
	if !errors.As(err, &rejected) || !slices.Equal(rejected.Errors, []string{"bad: B: nope"}) {
		t.Fatalf("err = %v", err)
	}
	if good.commits != 0 || bad.commits != 0 {
		t.Fatal("a rejected reload committed a part")
	}
}

func TestRestartOnlyKeysAreIgnoredAndUnknownKeysRejected(t *testing.T) {
	f := &fake{keys: []string{"A"}, ignored: []Ignored{{Key: "A", Reason: "overridden"}}}
	p := writeFile(t, `{"A":"1","SUBSPACE_DB":"/elsewhere.db","SUBSPACE_OWNER_TOKEN":"x","SUBSPACE_ADDR":":1"}`)
	m := manager(p, nil)
	m.Register("fake", f.keys, f)
	res, err := m.Reload()
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, ig := range res.Ignored {
		keys = append(keys, ig.Key)
	}
	if !slices.Equal(keys, []string{"SUBSPACE_ADDR", "SUBSPACE_DB", "SUBSPACE_OWNER_TOKEN", "A"}) {
		t.Fatalf("ignored = %+v", res.Ignored)
	}
	if len(res.Applied) != 0 {
		t.Fatalf("a component-ignored key was reported as applied: %v", res.Applied)
	}

	if err := os.WriteFile(p, []byte(`{"A":"1","SUBSPACE_TYPO":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.commits = 0
	if _, err := m.Reload(); err == nil || f.commits != 0 {
		t.Fatalf("unknown key: err=%v commits=%d", err, f.commits)
	}
}

func TestMalformedFilesAreRejected(t *testing.T) {
	for name, content := range map[string]string{
		"syntax": `{"A":`, "array": `["A"]`, "object value": `{"A":{"x":1}}`, "nested list": `{"A":[["x"]]}`, "trailing": `{"A":"1"} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{keys: []string{"A"}}
			m := manager(writeFile(t, content), nil)
			m.Register("fake", f.keys, f)
			if _, err := m.Reload(); err == nil || f.commits != 0 {
				t.Fatalf("err=%v commits=%d", err, f.commits)
			}
		})
	}
	m := manager(filepath.Join(t.TempDir(), "missing.json"), nil)
	if _, err := m.Reload(); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestWithoutFileReloadReadsTheEnvironment(t *testing.T) {
	f := &fake{keys: []string{"A", "B"}}
	m := manager("", map[string]string{"A": "env"})
	m.Register("fake", f.keys, f)
	res, err := m.Reload()
	if err != nil || f.got["A"] != "env" || !slices.Equal(res.Applied, []string{"A"}) {
		t.Fatalf("res=%+v err=%v got=%v", res, err, f.got)
	}
}

func TestLogLevel(t *testing.T) {
	pipe := logpipe.New(logpipe.Options{Out: io.Discard, Level: slog.LevelInfo})
	m := manager(writeFile(t, `{"SUBSPACE_LOG_LEVEL":"Debug"}`), nil)
	m.Register("log", []string{EnvLogLevel}, &LogLevel{Pipe: pipe})
	if _, err := m.Reload(); err != nil || pipe.Level() != slog.LevelDebug {
		t.Fatalf("level %v err %v", pipe.Level(), err)
	}
	if err := os.WriteFile(m.file, []byte(`{"SUBSPACE_LOG_LEVEL":"loud"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err == nil || pipe.Level() != slog.LevelDebug {
		t.Fatalf("invalid level accepted: %v %v", err, pipe.Level())
	}
	pipe.SetLevel(slog.LevelError)
	if err := os.WriteFile(m.file, []byte(`{"SUBSPACE_LOG_LEVEL":"Debug"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload(); err != nil || pipe.Level() != slog.LevelError {
		t.Fatalf("unchanged level overrode runtime level: %v %v", err, pipe.Level())
	}
}
