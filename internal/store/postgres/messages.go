package postgres

import (
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const msgDefaultLimit = 200

var (
	questionSelect = `SELECT ` + store.Questions.Select("") + ` FROM questions`
	questionInsert = store.Questions.Insert(ph)
	questionUpdate = store.Questions.Update(ph)
)

func (t *tx) InsertMessage(m *model.Message) error {
	err := t.q.QueryRowContext(t.ctx,
		`INSERT INTO messages(thread_kind, thread_id, author_id, author_role, kind, body, created_at)
  VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		m.ThreadKind, m.ThreadID, m.AuthorID, m.AuthorRole, m.Kind, m.Body, store.Millis(m.CreatedAt)).Scan(&m.ID)
	return mapErr(err)
}

func (t *tx) ListMessages(kind, id string, since int64, limit int) ([]*model.Message, error) {
	if limit <= 0 {
		limit = msgDefaultLimit
	}
	return list(t, store.ScanMessage, `SELECT `+store.MessageCols+`
  FROM messages WHERE thread_kind = $1 AND thread_id = $2 AND id > $3 ORDER BY id LIMIT $4`, kind, id, since, limit)
}

func (t *tx) InsertQuestion(q *model.Question) error {
	a, err := store.Questions.InsertArgs(q)
	if err != nil {
		return err
	}
	return t.exec(questionInsert, a...)
}

func (t *tx) GetQuestion(id string) (*model.Question, error) {
	return get(t, store.Questions.Scan, questionSelect+` WHERE id = $1`, id)
}

func (t *tx) UpdateQuestion(q *model.Question) error {
	a, err := store.Questions.UpdateArgs(q)
	if err != nil {
		return err
	}
	return t.update(questionUpdate, a...)
}

func (t *tx) ListQuestions(f store.QuestionFilter) ([]*model.Question, error) {
	var a args
	a.eq("status", f.Status)
	a.afterCreated("questions", f.AfterID)
	return list(t, store.Questions.Scan, questionSelect+a.clause()+` ORDER BY created_at, id`+a.limit(f.Limit), a.vals...)
}
