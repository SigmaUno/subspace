package retention_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/retention"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestPolicyFromEnvOverridesFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "retain.json")
	if err := os.WriteFile(file, []byte(`{"audit_days":30,"messages_days":7,"interval":"30m","batch":50}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := retention.PolicyFromEnv(envOf(map[string]string{
		"SUBSPACE_RETAIN_FILE":          file,
		"SUBSPACE_RETAIN_MESSAGES_DAYS": "14",
		"SUBSPACE_RETAIN_USAGE_DAYS":    "90",
		"SUBSPACE_RETAIN_INTERVAL":      "2h",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if p.AuditDays != 30 || p.MessagesDays != 14 || p.UsageDays != 90 || p.IdempotencyDays != 0 {
		t.Fatalf("days %d/%d/%d/%d", p.AuditDays, p.MessagesDays, p.UsageDays, p.IdempotencyDays)
	}
	if time.Duration(p.Interval) != 2*time.Hour || p.Batch != 50 {
		t.Fatalf("interval %v batch %d", time.Duration(p.Interval), p.Batch)
	}
	if p.SegmentRows != retention.DefaultPolicy().SegmentRows {
		t.Fatalf("unset field lost its default: %d", p.SegmentRows)
	}
}

func TestPolicyFromEnvRejectsInvalidSettings(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"interval":"soon"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(dir, "truncated.json")
	if err := os.WriteFile(truncated, []byte(`{"audit_days":`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"missing file", map[string]string{"SUBSPACE_RETAIN_FILE": filepath.Join(dir, "absent.json")}, "no such file"},
		{"truncated file", map[string]string{"SUBSPACE_RETAIN_FILE": truncated}, "SUBSPACE_RETAIN_FILE"},
		{"bad duration in file", map[string]string{"SUBSPACE_RETAIN_FILE": bad}, "SUBSPACE_RETAIN_FILE"},
		{"non-integer days", map[string]string{"SUBSPACE_RETAIN_AUDIT_DAYS": "a week"}, "SUBSPACE_RETAIN_AUDIT_DAYS must be an integer"},
		{"bad interval", map[string]string{"SUBSPACE_RETAIN_INTERVAL": "10"}, "SUBSPACE_RETAIN_INTERVAL"},
		{"negative days", map[string]string{"SUBSPACE_RETAIN_USAGE_DAYS": "-1"}, "usage must be 0"},
		{"negative interval", map[string]string{"SUBSPACE_RETAIN_INTERVAL": "-1m"}, "interval must not be negative"},
		{"zero batch", map[string]string{"SUBSPACE_RETAIN_BATCH": "0"}, "batch must be positive"},
		{"zero segment rows", map[string]string{"SUBSPACE_RETAIN_SEGMENT_ROWS": "0"}, "segment_rows must be positive"},
		{"negative checkpoint", map[string]string{"SUBSPACE_RETAIN_WAL_AUTOCHECKPOINT": "-1"}, "must not be negative"},
		{"negative vacuum", map[string]string{"SUBSPACE_RETAIN_VACUUM_PAGES": "-5"}, "must not be negative"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := retention.PolicyFromEnv(envOf(tc.env))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestPolicyFromEnvAcceptsBoundaryValues(t *testing.T) {
	p, err := retention.PolicyFromEnv(envOf(map[string]string{
		"SUBSPACE_RETAIN_INTERVAL":           "0s",
		"SUBSPACE_RETAIN_AUDIT_DAYS":         "0",
		"SUBSPACE_RETAIN_BATCH":              "1",
		"SUBSPACE_RETAIN_SEGMENT_ROWS":       "1",
		"SUBSPACE_RETAIN_WAL_AUTOCHECKPOINT": "0",
		"SUBSPACE_RETAIN_VACUUM_PAGES":       "0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if p.Interval != 0 || p.Batch != 1 || p.SegmentRows != 1 {
		t.Fatalf("%+v", p)
	}
}
