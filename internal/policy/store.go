package policy

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
	"github.com/SigmaUno/subspace/internal/store/postgres"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS policies (
  id TEXT PRIMARY KEY, seq INTEGER NOT NULL, name TEXT NOT NULL, version INTEGER NOT NULL, enabled INTEGER NOT NULL,
  doc TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS policy_versions (
  policy_id TEXT NOT NULL, version INTEGER NOT NULL, doc TEXT NOT NULL, created_at INTEGER NOT NULL,
  PRIMARY KEY (policy_id, version)
);
CREATE TABLE IF NOT EXISTS policy_holds (
  id TEXT PRIMARY KEY, seq INTEGER NOT NULL, hold_key TEXT NOT NULL, status TEXT NOT NULL, action TEXT NOT NULL,
  actor_id TEXT NOT NULL, actor_role TEXT NOT NULL, task_id TEXT NOT NULL, brief_id TEXT NOT NULL, plan_id TEXT NOT NULL,
  target TEXT NOT NULL, args TEXT NOT NULL, message TEXT NOT NULL, matched TEXT NOT NULL, created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL, decided_at INTEGER NOT NULL DEFAULT 0, note TEXT NOT NULL DEFAULT '', result TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS policy_holds_key ON policy_holds(hold_key, status);
CREATE TABLE IF NOT EXISTS policy_decisions (
  id TEXT PRIMARY KEY, seq INTEGER NOT NULL, ts INTEGER NOT NULL, action TEXT NOT NULL, actor_id TEXT NOT NULL,
  actor_role TEXT NOT NULL, task_id TEXT NOT NULL, brief_id TEXT NOT NULL, target TEXT NOT NULL, outcome TEXT NOT NULL,
  message TEXT NOT NULL, hold_id TEXT NOT NULL, matched TEXT NOT NULL, facts TEXT NOT NULL, errors TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS policy_decisions_seq ON policy_decisions(seq);
`

const pgSchema = `
CREATE TABLE IF NOT EXISTS policies (
  id TEXT COLLATE "C" PRIMARY KEY, seq BIGINT NOT NULL, name TEXT NOT NULL, version INTEGER NOT NULL, enabled INTEGER NOT NULL,
  doc TEXT NOT NULL, created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS policy_versions (
  policy_id TEXT COLLATE "C" NOT NULL, version INTEGER NOT NULL, doc TEXT NOT NULL, created_at BIGINT NOT NULL,
  PRIMARY KEY (policy_id, version)
);
CREATE TABLE IF NOT EXISTS policy_holds (
  id TEXT COLLATE "C" PRIMARY KEY, seq BIGINT NOT NULL, hold_key TEXT NOT NULL, status TEXT NOT NULL, action TEXT NOT NULL,
  actor_id TEXT NOT NULL, actor_role TEXT NOT NULL, task_id TEXT NOT NULL, brief_id TEXT NOT NULL, plan_id TEXT NOT NULL,
  target TEXT NOT NULL, args TEXT NOT NULL, message TEXT NOT NULL, matched TEXT NOT NULL, created_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL, decided_at BIGINT NOT NULL DEFAULT 0, note TEXT NOT NULL DEFAULT '', result TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS policy_holds_key ON policy_holds(hold_key, status);
CREATE TABLE IF NOT EXISTS policy_decisions (
  id TEXT COLLATE "C" PRIMARY KEY, seq BIGINT NOT NULL, ts BIGINT NOT NULL, action TEXT NOT NULL, actor_id TEXT NOT NULL,
  actor_role TEXT NOT NULL, task_id TEXT NOT NULL, brief_id TEXT NOT NULL, target TEXT NOT NULL, outcome TEXT NOT NULL,
  message TEXT NOT NULL, hold_id TEXT NOT NULL, matched TEXT NOT NULL, facts TEXT NOT NULL, errors TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS policy_decisions_seq ON policy_decisions(seq);
`

const (
	revKey       = "policy_rev"
	maxDecisions = 20000
	policyCols   = `id,name,version,enabled,doc,created_at,updated_at`
	holdCols     = `id,hold_key,status,action,actor_id,actor_role,task_id,brief_id,plan_id,target,args,message,matched,created_at,expires_at,decided_at,note,result`
	decisionCols = `id,ts,action,actor_id,actor_role,task_id,brief_id,target,outcome,message,hold_id,matched,facts,errors`
)

func init() {
	sqlite.RegisterSchema(schema)
	postgres.RegisterSchema(pgSchema, "policies", "policy_versions", "policy_holds", "policy_decisions")
}

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func optTime(ms int64) *time.Time {
	if ms == 0 {
		return nil
	}
	t := fromMillis(ms)
	return &t
}

func bumpRev(t store.Tx) error {
	v, _, err := t.GetKV(revKey)
	if err != nil {
		return err
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return t.SetKV(revKey, strconv.FormatInt(n+1, 10))
}

func queryPolicies(q store.SQL, where string, args ...any) ([]*Policy, error) {
	return store.QueryAll(q, scanPolicy, `SELECT `+policyCols+` FROM policies `+where, args...)
}

func scanPolicy(rows *sql.Rows) (*Policy, error) {
	var (
		p                Policy
		doc              string
		enabled          int
		created, updated int64
	)
	if err := rows.Scan(&p.ID, &p.Name, &p.Version, &enabled, &doc, &created, &updated); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(doc), &p.Document); err != nil {
		return nil, err
	}
	p.Enabled = enabled != 0
	p.CreatedAt, p.UpdatedAt = fromMillis(created), fromMillis(updated)
	return &p, nil
}

func listPolicies(q store.SQL) ([]*Policy, error) {
	return queryPolicies(q, `ORDER BY seq`)
}

func getPolicy(q store.SQL, id string) (*Policy, error) {
	return store.QueryOne(q, scanPolicy, model.NotFound("policy %s not found", id), `SELECT `+policyCols+` FROM policies WHERE id=?`, id)
}

func countPolicies(q store.SQL) (int, error) {
	return store.Scalar[int](q, `SELECT COUNT(*) FROM policies`)
}

func insertPolicy(t store.Tx, q store.SQL, p *Policy) error {
	id, err := t.NextID("PO")
	if err != nil {
		return err
	}
	p.ID = id
	doc, err := json.Marshal(p.Document)
	if err != nil {
		return err
	}
	if _, err := q.Exec(`INSERT INTO policies(id,seq,name,version,enabled,doc,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`,
		p.ID, store.IDNum(p.ID), p.Name, p.Version, store.Flag(p.Enabled), string(doc), store.Millis(p.CreatedAt), store.Millis(p.UpdatedAt)); err != nil {
		return err
	}
	return insertVersion(q, p, string(doc))
}

func insertVersion(q store.SQL, p *Policy, doc string) error {
	_, err := q.Exec(`INSERT INTO policy_versions(policy_id,version,doc,created_at) VALUES(?,?,?,?)`, p.ID, p.Version, doc, store.Millis(p.UpdatedAt))
	return err
}

func updatePolicy(q store.SQL, p *Policy) error {
	doc, err := json.Marshal(p.Document)
	if err != nil {
		return err
	}
	res, err := q.Exec(`UPDATE policies SET name=?,version=?,enabled=?,doc=?,updated_at=? WHERE id=?`,
		p.Name, p.Version, store.Flag(p.Enabled), string(doc), store.Millis(p.UpdatedAt), p.ID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return errors.Join(err, model.NotFound("policy %s not found", p.ID))
	}
	return insertVersion(q, p, string(doc))
}

func deletePolicy(q store.SQL, id string) error {
	if _, err := q.Exec(`DELETE FROM policy_versions WHERE policy_id=?`, id); err != nil {
		return err
	}
	_, err := q.Exec(`DELETE FROM policies WHERE id=?`, id)
	return err
}

func listVersions(q store.SQL, id string) ([]Version, error) {
	return store.QueryAll(q, scanVersion, `SELECT version,doc,created_at FROM policy_versions WHERE policy_id=? ORDER BY version DESC`, id)
}

func scanVersion(rows *sql.Rows) (Version, error) {
	var (
		v   Version
		doc string
		at  int64
	)
	if err := rows.Scan(&v.Version, &doc, &at); err != nil {
		return Version{}, err
	}
	if err := json.Unmarshal([]byte(doc), &v.Document); err != nil {
		return Version{}, err
	}
	v.CreatedAt = fromMillis(at)
	return v, nil
}

func queryHolds(q store.SQL, where string, args ...any) ([]*Hold, error) {
	return store.QueryAll(q, scanHold, `SELECT `+holdCols+` FROM policy_holds `+where, args...)
}

func scanHold(rows *sql.Rows) (*Hold, error) {
	var (
		h                           Hold
		argsJSON, matched           string
		created, expires, decidedAt int64
	)
	if err := rows.Scan(&h.ID, &h.key, &h.Status, &h.Action, &h.ActorID, &h.ActorRole, &h.TaskID, &h.BriefID, &h.PlanID,
		&h.Target, &argsJSON, &h.Message, &matched, &created, &expires, &decidedAt, &h.Note, &h.Result); err != nil {
		return nil, err
	}
	if err := errors.Join(json.Unmarshal([]byte(argsJSON), &h.Args), json.Unmarshal([]byte(matched), &h.Matched)); err != nil {
		return nil, err
	}
	h.CreatedAt, h.ExpiresAt, h.DecidedAt = fromMillis(created), fromMillis(expires), optTime(decidedAt)
	return &h, nil
}

func getHold(q store.SQL, id string) (*Hold, error) {
	return store.QueryOne(q, scanHold, model.NotFound("hold %s not found", id), `SELECT `+holdCols+` FROM policy_holds WHERE id=?`, id)
}

func openHold(q store.SQL, key string) (*Hold, error) {
	hs, err := queryHolds(q, `WHERE hold_key=? AND status IN (?,?) ORDER BY seq DESC LIMIT 1`, key, HoldPending, HoldApproved)
	if err != nil || len(hs) == 0 {
		return nil, err
	}
	return hs[0], nil
}

func insertHold(t store.Tx, q store.SQL, h *Hold) error {
	id, err := t.NextID("PH")
	if err != nil {
		return err
	}
	h.ID = id
	args, err := json.Marshal(h.Args)
	if err != nil {
		return err
	}
	matched, err := json.Marshal(h.Matched)
	if err != nil {
		return err
	}
	_, err = q.Exec(`INSERT INTO policy_holds(id,seq,hold_key,status,action,actor_id,actor_role,task_id,brief_id,plan_id,target,args,message,matched,created_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, h.ID, store.IDNum(h.ID), h.key, h.Status, h.Action, h.ActorID, h.ActorRole, h.TaskID, h.BriefID,
		h.PlanID, h.Target, string(args), h.Message, string(matched), store.Millis(h.CreatedAt), store.Millis(h.ExpiresAt))
	return err
}

func setHold(q store.SQL, h *Hold) error {
	_, err := q.Exec(`UPDATE policy_holds SET status=?,decided_at=?,note=?,result=? WHERE id=?`,
		h.Status, store.Millis(derefTime(h.DecidedAt)), h.Note, h.Result, h.ID)
	return err
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func insertDecision(t store.Tx, q store.SQL, d *Decision) error {
	id, err := t.NextID("PD")
	if err != nil {
		return err
	}
	d.ID = id
	matched, err := json.Marshal(d.Matched)
	if err != nil {
		return err
	}
	facts, err := json.Marshal(d.Facts)
	if err != nil {
		return err
	}
	errs, err := json.Marshal(d.Errors)
	if err != nil {
		return err
	}
	seq := store.IDNum(id)
	if _, err := q.Exec(`INSERT INTO policy_decisions(id,seq,ts,action,actor_id,actor_role,task_id,brief_id,target,outcome,message,hold_id,matched,facts,errors)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, d.ID, seq, store.Millis(d.TS), d.Action, d.ActorID, d.ActorRole, d.TaskID, d.BriefID, d.Target,
		d.Outcome, d.Message, d.HoldID, string(matched), string(facts), string(errs)); err != nil {
		return err
	}
	if seq%100 == 0 {
		_, err = q.Exec(`DELETE FROM policy_decisions WHERE seq<=?`, seq-maxDecisions)
	}
	return err
}

func listDecisions(q store.SQL, f DecisionFilter) ([]*Decision, error) {
	where, args := []string{}, []any{}
	if f.Outcome != "" {
		where, args = append(where, "outcome=?"), append(args, f.Outcome)
	}
	if f.Action != "" {
		where, args = append(where, "action=?"), append(args, f.Action)
	}
	if f.TaskID != "" {
		where, args = append(where, "task_id=?"), append(args, f.TaskID)
	}
	if f.Before > 0 {
		where, args = append(where, "seq<?"), append(args, f.Before)
	}
	sqlWhere := ""
	if len(where) > 0 {
		sqlWhere = "WHERE " + strings.Join(where, " AND ")
	}
	return store.QueryAll(q, scanDecision, `SELECT `+decisionCols+` FROM policy_decisions `+sqlWhere+` ORDER BY seq DESC LIMIT ?`, append(args, f.Limit)...)
}

func scanDecision(rows *sql.Rows) (*Decision, error) {
	var (
		d                     Decision
		ts                    int64
		matched, facts, errsJ string
	)
	if err := rows.Scan(&d.ID, &ts, &d.Action, &d.ActorID, &d.ActorRole, &d.TaskID, &d.BriefID, &d.Target, &d.Outcome,
		&d.Message, &d.HoldID, &matched, &facts, &errsJ); err != nil {
		return nil, err
	}
	d.TS = fromMillis(ts)
	if err := errors.Join(json.Unmarshal([]byte(matched), &d.Matched), json.Unmarshal([]byte(facts), &d.Facts),
		json.Unmarshal([]byte(errsJ), &d.Errors)); err != nil {
		return nil, err
	}
	return &d, nil
}
