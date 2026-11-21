package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

type OpenCode struct {
	Path    string
	AgentID string
}

func (o *OpenCode) Name() string { return "opencode:" + o.Path }

func (o *OpenCode) open() (*sql.DB, error) {
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "query_only(1)")
	db, err := sql.Open("sqlite", "file:"+o.Path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

const openCodeSessionTTL = 30 * 24 * time.Hour

type openCodeSession struct {
	Updated    int64   `json:"u"`
	TokensIn   int64   `json:"i,omitempty"`
	TokensOut  int64   `json:"o,omitempty"`
	CacheRead  int64   `json:"r,omitempty"`
	CacheWrite int64   `json:"w,omitempty"`
	CostUSD    float64 `json:"c,omitempty"`
}

type openCodeCursor struct {
	Since    int64                      `json:"since"`
	Sessions map[string]openCodeSession `json:"sessions,omitempty"`
}

func parseOpenCodeCursor(cursor string) (openCodeCursor, error) {
	c := openCodeCursor{Sessions: map[string]openCodeSession{}}
	if v, err := strconv.ParseInt(cursor, 10, 64); err == nil {
		c.Since = v
		return c, nil
	}
	if err := loadCursor(cursor, &c); err != nil {
		return c, err
	}
	if c.Sessions == nil {
		c.Sessions = map[string]openCodeSession{}
	}
	return c, nil
}

func (o *OpenCode) Collect(ctx context.Context, cursor string, prev Totaler) ([]Record, string, error) {
	cur, err := parseOpenCodeCursor(cursor)
	if err != nil {
		return nil, cursor, err
	}
	since := cur.Since
	db, err := o.open()
	if err != nil {
		return nil, cursor, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(model, ''), cost, tokens_input, tokens_output, tokens_reasoning,
		tokens_cache_read, tokens_cache_write, time_updated FROM session_v2 WHERE time_updated >= ? ORDER BY time_updated, id`, since)
	if err != nil {
		return nil, cursor, err
	}
	type row struct {
		id, model string
		cur       Record
		updated   int64
	}
	var rs []row
	for rows.Next() {
		var r row
		var reasoning int64
		if err := rows.Scan(&r.id, &r.model, &r.cur.CostUSD, &r.cur.TokensIn, &r.cur.TokensOut, &reasoning,
			&r.cur.CacheRead, &r.cur.CacheWrite, &r.updated); err != nil {
			_ = rows.Close()
			return nil, cursor, err
		}
		r.cur.TokensOut += reasoning
		rs = append(rs, r)
	}
	if err := rows.Close(); err != nil {
		return nil, cursor, err
	}
	if err := rows.Err(); err != nil {
		return nil, cursor, err
	}
	var out []Record
	next := since
	for _, r := range rs {
		next = max(next, r.updated)
		var p Record
		if s, ok := cur.Sessions[r.id]; ok {
			p = Record{TokensIn: s.TokensIn, TokensOut: s.TokensOut, CacheRead: s.CacheRead, CacheWrite: s.CacheWrite, CostUSD: s.CostUSD}
		} else if p, err = prev.SessionTotal(ctx, SourceOpenCode, r.id); err != nil {
			return nil, cursor, err
		}
		cur.Sessions[r.id] = openCodeSession{
			Updated: r.updated, TokensIn: max(r.cur.TokensIn, p.TokensIn), TokensOut: max(r.cur.TokensOut, p.TokensOut),
			CacheRead: max(r.cur.CacheRead, p.CacheRead), CacheWrite: max(r.cur.CacheWrite, p.CacheWrite), CostUSD: max(r.cur.CostUSD, p.CostUSD),
		}
		d := Record{
			TokensIn: max(r.cur.TokensIn-p.TokensIn, 0), TokensOut: max(r.cur.TokensOut-p.TokensOut, 0),
			CacheRead: max(r.cur.CacheRead-p.CacheRead, 0), CacheWrite: max(r.cur.CacheWrite-p.CacheWrite, 0),
		}
		if c := r.cur.CostUSD - p.CostUSD; c > 1e-9 {
			d.CostUSD = c
		}
		if d.TokensIn == 0 && d.TokensOut == 0 && d.CacheRead == 0 && d.CacheWrite == 0 && d.CostUSD == 0 {
			continue
		}
		var m struct {
			ID         string `json:"id"`
			ProviderID string `json:"providerID"`
		}
		if r.model != "" {
			_ = json.Unmarshal([]byte(r.model), &m)
		}
		d.TS = time.UnixMilli(r.updated).UTC()
		d.AgentID, d.Provider, d.Model = o.AgentID, m.ProviderID, m.ID
		d.Source, d.Session, d.Key = SourceOpenCode, r.id, "opencode:"+r.id+":"+strconv.FormatInt(r.updated, 10)
		out = append(out, d)
	}
	cur.Since = next
	cutoff := next - openCodeSessionTTL.Milliseconds()
	for id, s := range cur.Sessions {
		if s.Updated < cutoff {
			delete(cur.Sessions, id)
		}
	}
	nextCursor, err := saveCursor(cur)
	return out, nextCursor, err
}
