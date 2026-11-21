package store

import "time"

type SearchQuery struct {
	Text  string
	Kind  string
	Limit int
}

type SearchHit struct {
	Kind   string
	ID     string
	Field  string
	Text   string
	Weight int
	At     time.Time
}

type SearchTx interface {
	Search(q SearchQuery) ([]SearchHit, error)
}
