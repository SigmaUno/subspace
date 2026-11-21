package sqlite

import "database/sql"

func OpenReadOnly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+uriPathEscaper.Replace(path)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
