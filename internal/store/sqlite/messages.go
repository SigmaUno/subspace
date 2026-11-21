package sqlite

import (
	"github.com/SigmaUno/subspace/internal/model"
	"github.com/SigmaUno/subspace/internal/store"
)

const msgDefaultLimit = 200

func (t *tx) InsertMessage(m *model.Message) error {
	res, err := t.q.ExecContext(t.ctx,
		`INSERT INTO messages(thread_kind,thread_id,author_id,author_role,kind,body,created_at) VALUES(?,?,?,?,?,?,?)`,
		m.ThreadKind, m.ThreadID, m.AuthorID, m.AuthorRole, m.Kind, m.Body, store.Millis(m.CreatedAt))
	if err != nil {
		return mapErr(err)
	}
	m.ID, err = res.LastInsertId()
	return err
}

func (t *tx) ListMessages(kind, id string, since int64, limit int) ([]*model.Message, error) {
	if limit <= 0 {
		limit = msgDefaultLimit
	}
	return list(t, store.ScanMessage, `SELECT `+store.MessageCols+`
		 FROM messages WHERE thread_kind=? AND thread_id=? AND id>? ORDER BY id ASC LIMIT ?`, kind, id, since, limit)
}

var (
	questionSelect = `SELECT ` + store.Questions.Select("") + ` FROM questions`
	questionInsert = store.Questions.Insert(ph)
	questionUpdate = store.Questions.Update(ph)
)

func (t *tx) InsertQuestion(q *model.Question) error {
	args, err := store.Questions.InsertArgs(q)
	if err != nil {
		return err
	}
	return t.exec(questionInsert, args...)
}

func (t *tx) GetQuestion(id string) (*model.Question, error) {
	return get(t, store.Questions.Scan, questionSelect+` WHERE id=?`, id)
}

func (t *tx) UpdateQuestion(q *model.Question) error {
	args, err := store.Questions.UpdateArgs(q)
	if err != nil {
		return err
	}
	return t.update(questionUpdate, args...)
}

func (t *tx) ListQuestions(f store.QuestionFilter) ([]*model.Question, error) {
	var w where
	w.eq("status", f.Status)
	w.afterCreated("questions", f.AfterID)
	q := questionSelect + w.clause() + ` ORDER BY created_at, id` + w.limit(f.Limit)
	return list(t, store.Questions.Scan, q, w.args...)
}
