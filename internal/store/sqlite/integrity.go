package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
)

func IntegrityCheck(ctx context.Context, path string) error {
	if fi, err := os.Stat(path); err != nil {
		return err
	} else if fi.Size() == 0 {
		return errors.New("empty file")
	}
	db, err := sql.Open("sqlite", "file:"+uriPathEscaper.Replace(path)+"?mode=ro&immutable=1")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return err
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("integrity_check: %s", strings.Join(problems[:min(len(problems), 3)], "; "))
	}
	return nil
}
