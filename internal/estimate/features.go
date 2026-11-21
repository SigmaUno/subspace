package estimate

import (
	"strings"
	"unicode/utf8"

	"github.com/SigmaUno/subspace/internal/model"
)

const (
	SizeSmall  = "small"
	SizeMedium = "medium"
	SizeLarge  = "large"
)

var (
	lengthEdges   = []int{200, 600, 1500, 4000}
	criteriaEdges = []int{1, 3, 6}
	pathEdges     = []int{0, 2, 9}
)

type Features struct {
	SpecLength int `json:"spec_length"`
	Criteria   int `json:"criteria"`
	Paths      int `json:"paths"`
}

func FeaturesOf(t *model.Task) Features {
	n := utf8.RuneCountInString(strings.TrimSpace(t.Spec))
	criteria := 0
	for _, c := range t.AcceptanceCriteria {
		if c = strings.TrimSpace(c); c != "" {
			n += utf8.RuneCountInString(c)
			criteria++
		}
	}
	return Features{SpecLength: n, Criteria: criteria, Paths: len(t.Paths)}
}

func (f Features) Size() string {
	score := bucket(f.SpecLength, lengthEdges) + bucket(f.Criteria, criteriaEdges) + bucket(f.Paths, pathEdges)
	switch {
	case score <= 2:
		return SizeSmall
	case score <= 5:
		return SizeMedium
	}
	return SizeLarge
}

func bucket(v int, edges []int) int {
	for i, e := range edges {
		if v <= e {
			return i
		}
	}
	return len(edges)
}
