package sqlite

import "github.com/SigmaUno/subspace/internal/model"

// TODO(split A5): implement messages + questions.
func (t *tx) InsertMessage(m *model.Message) error { return errNotImplemented }
func (t *tx) ListMessages(kind, id string, since int64, limit int) ([]*model.Message, error) {
	return nil, errNotImplemented
}
func (t *tx) InsertQuestion(q *model.Question) error         { return errNotImplemented }
func (t *tx) GetQuestion(id string) (*model.Question, error) { return nil, errNotImplemented }
func (t *tx) UpdateQuestion(q *model.Question) error         { return errNotImplemented }
func (t *tx) ListQuestions(status string) ([]*model.Question, error) {
	return nil, errNotImplemented
}
