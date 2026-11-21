package sqlite

import (
	"encoding/json"

	"github.com/SigmaUno/subspace/internal/model"
)

func init() {
	RegisterColumn("briefs", "quality_gates", "TEXT NOT NULL DEFAULT '{}'")
}

func gatesJSON(q model.QualityGates) (string, error) {
	if !q.Enabled() {
		return "{}", nil
	}
	b, err := json.Marshal(q)
	return string(b), err
}

func gatesUnJSON(s string) (model.QualityGates, error) {
	var q model.QualityGates
	if s == "" {
		return q, nil
	}
	err := json.Unmarshal([]byte(s), &q)
	return q, err
}
