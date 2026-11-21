package retention

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/fsx"
)

type Kind string

const (
	Audit       Kind = "audit"
	Messages    Kind = "messages"
	Usage       Kind = "usage"
	Idempotency Kind = "idempotency"
	Transcripts Kind = "transcripts"
)

var Kinds = []Kind{Audit, Messages, Usage, Idempotency, Transcripts}

type spec struct {
	table string
	ts    string
	keys  []string
}

var specs = map[Kind]spec{
	Audit:       {"audit", "ts", []string{"id"}},
	Messages:    {"messages", "created_at", []string{"id"}},
	Usage:       {"usage_records", "ts", []string{"key"}},
	Idempotency: {"idempotency_keys", "created_at", []string{"agent_id", "key"}},
	Transcripts: {"task_transcripts", "created_at", []string{"id"}},
}

const (
	StatusPending  = "pending"
	StatusArchived = "archived"
	StatusRestored = "restored"
)

type Segment struct {
	ID        int64     `json:"id"`
	Kind      Kind      `json:"kind"`
	Seq       int64     `json:"seq"`
	File      string    `json:"file"`
	Rows      int64     `json:"rows"`
	Bytes     int64     `json:"bytes"`
	SHA256    string    `json:"sha256"`
	Cutoff    time.Time `json:"cutoff"`
	FirstID   int64     `json:"first_id"`
	LastID    int64     `json:"last_id"`
	PrevHash  string    `json:"prev_hash"`
	LastHash  string    `json:"last_hash"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type row map[string]any

var ident = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func segmentName(k Kind, seq int64) string {
	return filepath.ToSlash(filepath.Join(string(k), fmt.Sprintf("%s-%08d.ndjson.gz", k, seq)))
}

func writeSegment(dir, name string, rows []row) (int64, string, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	enc := json.NewEncoder(zw)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return 0, "", err
		}
	}
	if err := zw.Close(); err != nil {
		return 0, "", err
	}
	if err := fsx.WriteAtomic(filepath.Join(dir, filepath.FromSlash(name)), buf.Bytes()); err != nil {
		return 0, "", err
	}
	return int64(buf.Len()), fsx.SHA256Hex(buf.Bytes()), nil
}

func readSegment(dir string, seg Segment) ([]row, error) {
	raw, err := fsx.ReadVerified(filepath.Join(dir, filepath.FromSlash(seg.File)), seg.SHA256, seg.Bytes)
	if err != nil {
		return nil, fmt.Errorf("segment %s: %w", seg.File, err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("segment %s: %w", seg.File, err)
	}
	var out []row
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for sc.Scan() {
		d := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		d.UseNumber()
		var r row
		if err := d.Decode(&r); err != nil {
			return nil, fmt.Errorf("segment %s line %d: %w", seg.File, len(out)+1, err)
		}
		for k, v := range r {
			if !ident.MatchString(k) {
				return nil, fmt.Errorf("segment %s: invalid column %q", seg.File, k)
			}
			r[k] = fromJSON(v)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("segment %s: %w", seg.File, err)
	}
	if int64(len(out)) != seg.Rows {
		return nil, fmt.Errorf("segment %s: %d rows, manifest says %d", seg.File, len(out), seg.Rows)
	}
	return out, nil
}

func fromJSON(v any) any {
	n, ok := v.(json.Number)
	if !ok {
		return v
	}
	if i, err := n.Int64(); err == nil && !strings.ContainsAny(n.String(), ".eE") {
		return i
	}
	f, _ := n.Float64()
	return f
}

func toJSON(v any) any {
	switch x := v.(type) {
	case nil, int64, float64, string, bool:
		return x
	case []byte:
		return string(x)
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case time.Time:
		return x.UnixMilli()
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func (r row) int(col string) int64 {
	switch x := r[col].(type) {
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}

func (r row) str(col string) string {
	switch x := r[col].(type) {
	case string:
		return x
	case nil:
		return ""
	}
	return fmt.Sprint(r[col])
}
