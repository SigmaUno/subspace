package capacity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func FuzzLoad(f *testing.F) {
	for _, s := range []string{
		`{}`,
		`{"global_max_agents":300,"limits":[{"provider":"anthropic","model":"*","max_concurrency":20,"requests_per_min":600}]}`,
		`{"global_max_agents":-1}`,
		`{"limits":[{"provider":"","model":"*"}]}`,
		`{"limits":[{"provider":"a","model":"b","max_concurrency":-1}]}`,
		`{"limits":[{"provider":"a","model":"b","max_concurrency":1.5}]}`,
		`{"limits":[{"provider":"a","model":"b","max_concurrency":9223372036854775807}]}`,
		`{"limits":null,"unknown":true}`,
		`{"limits":[null]}`,
		`[]`, `null`, ``, `{"global_max_agents":`,
	} {
		f.Add([]byte(s), "anthropic", "claude", 1)
	}
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, raw []byte, provider, mdl string, n int) {
		path := filepath.Join(dir, "capacity.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		c, err := Load(path)
		if err != nil {
			return
		}
		if el := time.Since(start); el > 5*time.Second {
			t.Fatalf("Load took %s", el)
		}
		if err := c.Validate(); err != nil {
			t.Fatalf("Load accepted an invalid config: %v", err)
		}
		out, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, out, 0o600); err != nil {
			t.Fatal(err)
		}
		back, err := Load(path)
		if err != nil || !reflect.DeepEqual(back, c) {
			t.Fatalf("round trip changed the config: %v\n%+v\n%+v", err, c, back)
		}
		if n < 0 || n > 1<<20 {
			return
		}
		if err := c.Check(provider, mdl, 0, Usage{}); err != nil && c.GlobalMaxAgents == 0 {
			t.Fatalf("an empty fleet is never over a limit: %v", err)
		}
		reports := c.Report(Usage{{provider, mdl}: n})
		if len(reports) != len(c.Limits) {
			t.Fatalf("%d reports for %d limits", len(reports), len(c.Limits))
		}
		for _, r := range reports {
			if r.InUse < 0 || r.Available < -1 || r.MaxConcurrency > 0 && r.Available > r.MaxConcurrency {
				t.Fatalf("bad report %+v", r)
			}
		}
	})
}
