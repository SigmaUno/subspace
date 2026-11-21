package sqlite

import "github.com/SigmaUno/subspace/internal/model"

func init() {
	RegisterColumn("tasks", "paths", "TEXT NOT NULL DEFAULT '[]'")
}

func pathsJSON(p model.Paths) (string, error) {
	return taskJSON(p)
}

func pathsUnJSON(s string) (model.Paths, error) {
	out, err := taskUnJSON(s)
	if err != nil || len(out) == 0 {
		return nil, err
	}
	return out, nil
}
