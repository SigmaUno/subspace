package retention

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

type Policy struct {
	AuditDays         int      `json:"audit_days"`
	MessagesDays      int      `json:"messages_days"`
	UsageDays         int      `json:"usage_days"`
	IdempotencyDays   int      `json:"idempotency_days"`
	Interval          Duration `json:"interval"`
	Batch             int      `json:"batch"`
	SegmentRows       int      `json:"segment_rows"`
	WALAutocheckpoint int      `json:"wal_autocheckpoint"`
	VacuumPages       int      `json:"vacuum_pages"`
	Dir               string   `json:"dir"`
}

func DefaultPolicy() Policy {
	return Policy{Interval: Duration(time.Hour), Batch: 500, SegmentRows: 10000, WALAutocheckpoint: 1000, VacuumPages: 2000}
}

func (p Policy) Days(k Kind) int {
	switch k {
	case Audit:
		return p.AuditDays
	case Messages:
		return p.MessagesDays
	case Usage:
		return p.UsageDays
	case Idempotency:
		return p.IdempotencyDays
	}
	return 0
}

func PolicyFromEnv(getenv func(string) string) (Policy, error) {
	p := DefaultPolicy()
	if f := getenv("SUBSPACE_RETAIN_FILE"); f != "" {
		b, err := os.ReadFile(f) //nolint:gosec // operator-supplied path
		if err != nil {
			return p, err
		}
		if err := json.Unmarshal(b, &p); err != nil {
			return p, fmt.Errorf("SUBSPACE_RETAIN_FILE: %w", err)
		}
	}
	for name, dst := range map[string]*int{
		"SUBSPACE_RETAIN_AUDIT_DAYS":         &p.AuditDays,
		"SUBSPACE_RETAIN_MESSAGES_DAYS":      &p.MessagesDays,
		"SUBSPACE_RETAIN_USAGE_DAYS":         &p.UsageDays,
		"SUBSPACE_RETAIN_IDEMPOTENCY_DAYS":   &p.IdempotencyDays,
		"SUBSPACE_RETAIN_BATCH":              &p.Batch,
		"SUBSPACE_RETAIN_SEGMENT_ROWS":       &p.SegmentRows,
		"SUBSPACE_RETAIN_WAL_AUTOCHECKPOINT": &p.WALAutocheckpoint,
		"SUBSPACE_RETAIN_VACUUM_PAGES":       &p.VacuumPages,
	} {
		v := getenv(name)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return p, fmt.Errorf("%s must be an integer", name)
		}
		*dst = n
	}
	if v := getenv("SUBSPACE_RETAIN_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return p, fmt.Errorf("SUBSPACE_RETAIN_INTERVAL: %w", err)
		}
		p.Interval = Duration(d)
	}
	if v := getenv("SUBSPACE_RETAIN_DIR"); v != "" {
		p.Dir = v
	}
	return p, p.validate()
}

func (p Policy) validate() error {
	for _, k := range Kinds {
		if p.Days(k) < 0 {
			return fmt.Errorf("retention days for %s must be 0 (keep forever) or positive", k)
		}
	}
	switch {
	case p.Interval < 0:
		return fmt.Errorf("retention interval must not be negative")
	case p.Batch <= 0:
		return fmt.Errorf("retention batch must be positive")
	case p.SegmentRows <= 0:
		return fmt.Errorf("retention segment_rows must be positive")
	case p.WALAutocheckpoint < 0 || p.VacuumPages < 0:
		return fmt.Errorf("wal_autocheckpoint and vacuum_pages must not be negative")
	}
	return nil
}
