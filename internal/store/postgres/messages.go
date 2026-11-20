package postgres

import (
	"github.com/SigmaUno/subspace/internal/model"
)

const (
	msgDefaultLimit = 200
	questionCols    = `id, brief_id, task_id, agent_id, question, options, status, answer, answered_at, created_at`
)

func (t *tx) InsertMessage(m *model.Message) error {
	err := t.q.QueryRowContext(t.ctx,
		`INSERT INTO messages(thread_kind, thread_id, author_id, author_role, kind, body, created_at)
  VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		m.ThreadKind, m.ThreadID, m.AuthorID, m.AuthorRole, m.Kind, m.Body, ms(m.CreatedAt)).Scan(&m.ID)
	return mapErr(err)
}

func scanMessage(r scanner) (*model.Message, error) {
	var (
		m       model.Message
		created int64
	)
	if err := r.Scan(&m.ID, &m.ThreadKind, &m.ThreadID, &m.AuthorID, &m.AuthorRole, &m.Kind, &m.Body, &created); err != nil {
		return nil, err
	}
	m.CreatedAt = fromMS(created)
	return &m, nil
}

func (t *tx) ListMessages(kind, id string, since int64, limit int) ([]*model.Message, error) {
	if limit <= 0 {
		limit = msgDefaultLimit
	}
	return list(t, scanMessage, `SELECT id, thread_kind, thread_id, author_id, author_role, kind, body, created_at
  FROM messages WHERE thread_kind = $1 AND thread_id = $2 AND id > $3 ORDER BY id LIMIT $4`, kind, id, since, limit)
}

func scanQuestion(r scanner) (*model.Question, error) {
	var (
		q                   model.Question
		brief, task, answer string
		opts                []byte
		answered, created   int64
	)
	if err := r.Scan(&q.ID, &brief, &task, &q.AgentID, &q.Question, &opts, &q.Status, &answer, &answered, &created); err != nil {
		return nil, err
	}
	var err error
	if q.Options, err = fromJSON(opts); err != nil {
		return nil, err
	}
	q.BriefID = strPtr(brief)
	q.TaskID = strPtr(task)
	q.Answer = strPtr(answer)
	q.AnsweredAt = fromMSPtr(answered)
	q.CreatedAt = fromMS(created)
	return &q, nil
}

func questionArgs(q *model.Question) ([]any, error) {
	opts, err := toJSON(q.Options)
	if err != nil {
		return nil, err
	}
	return []any{q.ID, str(q.BriefID), str(q.TaskID), q.AgentID, q.Question, opts, q.Status,
		str(q.Answer), msPtr(q.AnsweredAt), ms(q.CreatedAt)}, nil
}

func (t *tx) InsertQuestion(q *model.Question) error {
	a, err := questionArgs(q)
	if err != nil {
		return err
	}
	return t.exec(`INSERT INTO questions(`+questionCols+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, a...)
}

func (t *tx) GetQuestion(id string) (*model.Question, error) {
	q, err := scanQuestion(t.q.QueryRowContext(t.ctx, `SELECT `+questionCols+` FROM questions WHERE id = $1`, id))
	return q, mapErr(err)
}

func (t *tx) UpdateQuestion(q *model.Question) error {
	a, err := questionArgs(q)
	if err != nil {
		return err
	}
	return t.update(`UPDATE questions SET brief_id=$2, task_id=$3, agent_id=$4, question=$5, options=$6, status=$7,
  answer=$8, answered_at=$9, created_at=$10 WHERE id=$1`, a...)
}

func (t *tx) ListQuestions(status string) ([]*model.Question, error) {
	var a args
	a.eq("status", status)
	return list(t, scanQuestion, `SELECT `+questionCols+` FROM questions`+a.clause()+` ORDER BY created_at, id`, a.vals...)
}
