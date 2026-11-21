package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const msgDefaultLimit = 200

func (t *tx) InsertMessage(m *model.Message) error {
	res, err := t.q.ExecContext(t.ctx,
		`INSERT INTO messages(thread_kind,thread_id,author_id,author_role,kind,body,created_at) VALUES(?,?,?,?,?,?,?)`,
		m.ThreadKind, m.ThreadID, m.AuthorID, m.AuthorRole, m.Kind, m.Body, m.CreatedAt.UnixMilli())
	if err != nil {
		return msgErr(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	m.ID = id
	return nil
}

func (t *tx) ListMessages(kind, id string, since int64, limit int) ([]*model.Message, error) {
	if limit <= 0 {
		limit = msgDefaultLimit
	}
	rows, err := t.q.QueryContext(t.ctx,
		`SELECT id,thread_kind,thread_id,author_id,author_role,kind,body,created_at
		 FROM messages WHERE thread_kind=? AND thread_id=? AND id>? ORDER BY id ASC LIMIT ?`,
		kind, id, since, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*model.Message{}
	for rows.Next() {
		var m model.Message
		var created int64
		if err := rows.Scan(&m.ID, &m.ThreadKind, &m.ThreadID, &m.AuthorID, &m.AuthorRole, &m.Kind, &m.Body, &created); err != nil {
			return nil, err
		}
		m.CreatedAt = time.UnixMilli(created).UTC()
		out = append(out, &m)
	}
	return out, rows.Err()
}

const questionCols = `id,brief_id,task_id,agent_id,question,options,status,answer,answered_at,created_at`

type msgScanner interface{ Scan(dest ...any) error }

func questionScan(r msgScanner) (*model.Question, error) {
	var q model.Question
	var brief, task, answer, opts string
	var answered, created int64
	if err := r.Scan(&q.ID, &brief, &task, &q.AgentID, &q.Question, &opts, &q.Status, &answer, &answered, &created); err != nil {
		return nil, err
	}
	q.BriefID = questionStrPtr(brief)
	q.TaskID = questionStrPtr(task)
	q.Answer = questionStrPtr(answer)
	if answered != 0 {
		at := time.UnixMilli(answered).UTC()
		q.AnsweredAt = &at
	}
	q.CreatedAt = time.UnixMilli(created).UTC()
	q.Options = []string{}
	if opts != "" {
		if err := json.Unmarshal([]byte(opts), &q.Options); err != nil {
			return nil, err
		}
		if q.Options == nil {
			q.Options = []string{}
		}
	}
	return &q, nil
}

func questionStrPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func questionStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func questionMs(p *time.Time) int64 {
	if p == nil || p.IsZero() {
		return 0
	}
	return p.UnixMilli()
}

func questionOpts(o []string) (string, error) {
	if o == nil {
		o = []string{}
	}
	b, err := json.Marshal(o)
	return string(b), err
}

func (t *tx) InsertQuestion(q *model.Question) error {
	opts, err := questionOpts(q.Options)
	if err != nil {
		return err
	}
	_, err = t.q.ExecContext(t.ctx,
		`INSERT INTO questions(`+questionCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		q.ID, questionStr(q.BriefID), questionStr(q.TaskID), q.AgentID, q.Question, opts, q.Status,
		questionStr(q.Answer), questionMs(q.AnsweredAt), q.CreatedAt.UnixMilli())
	return msgErr(err)
}

func (t *tx) GetQuestion(id string) (*model.Question, error) {
	q, err := questionScan(t.q.QueryRowContext(t.ctx, `SELECT `+questionCols+` FROM questions WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return q, err
}

func (t *tx) UpdateQuestion(q *model.Question) error {
	opts, err := questionOpts(q.Options)
	if err != nil {
		return err
	}
	res, err := t.q.ExecContext(t.ctx,
		`UPDATE questions SET brief_id=?,task_id=?,agent_id=?,question=?,options=?,status=?,answer=?,answered_at=?,created_at=? WHERE id=?`,
		questionStr(q.BriefID), questionStr(q.TaskID), q.AgentID, q.Question, opts, q.Status,
		questionStr(q.Answer), questionMs(q.AnsweredAt), q.CreatedAt.UnixMilli(), q.ID)
	if err != nil {
		return msgErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (t *tx) ListQuestions(f store.QuestionFilter) ([]*model.Question, error) {
	query := `SELECT ` + questionCols + ` FROM questions WHERE 1=1`
	var args []any
	if f.Status != "" {
		query += ` AND status=?`
		args = append(args, f.Status)
	}
	if f.AfterID != "" {
		c, a := afterCreatedClause("questions", f.AfterID)
		query += ` AND ` + c
		args = append(args, a...)
	}
	query += ` ORDER BY created_at, id`
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := t.q.QueryContext(t.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*model.Question{}
	for rows.Next() {
		q, err := questionScan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func msgErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return store.ErrDuplicate
	}
	return err
}
