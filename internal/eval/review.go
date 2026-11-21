package eval

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

type criterionResult struct {
	Criterion string `json:"criterion"`
	Met       *bool  `json:"met"`
	Evidence  string `json:"evidence"`
}

type reviewOutput struct {
	Verdict      string            `json:"verdict"`
	Comments     []string          `json:"comments"`
	PerCriterion []criterionResult `json:"per_criterion"`
}

func parseReview(text string) (*reviewOutput, error) {
	var out reviewOutput
	if err := decodeOne(text, &out); err != nil {
		return nil, err
	}
	if out.Verdict != VerdictApprove && out.Verdict != VerdictChanges {
		return nil, fmt.Errorf("verdict %q is not %s or %s", out.Verdict, VerdictApprove, VerdictChanges)
	}
	for i, c := range out.PerCriterion {
		if c.Met == nil {
			return nil, fmt.Errorf("per_criterion[%d].met is missing", i)
		}
	}
	return &out, nil
}

var errUnsafe = errors.New("approved with unmet criteria")

func unsafeApproval(f *ReviewFixture, out *reviewOutput) bool {
	if out.Verdict != VerdictApprove {
		return false
	}
	if f.Expected() != VerdictApprove {
		return true
	}
	for _, c := range out.PerCriterion {
		if !*c.Met {
			return true
		}
	}
	return false
}

var numberRe = regexp.MustCompile(`\d+`)

func located(d Defect, texts []string) float64 {
	best := 0.0
	base := path.Base(d.File)
	for _, t := range texts {
		if !strings.Contains(t, d.File) && !strings.Contains(t, base) {
			continue
		}
		best = max(best, 0.5)
		for _, m := range numberRe.FindAllString(t, -1) {
			if n, err := strconv.Atoi(m); err == nil && n >= d.Line-3 && n <= d.Line+3 {
				return 1
			}
		}
	}
	return best
}

var reviewWeights = map[string]float64{"verdict": 0.5, "location": 0.3, "criteria": 0.2}

func scoreReview(f *ReviewFixture, out *reviewOutput) (float64, map[string]float64, []string, bool) {
	var notes []string
	checks := map[string]float64{}
	if out.Verdict == f.Expected() {
		checks["verdict"] = 1
	} else {
		notes = append(notes, fmt.Sprintf("verdict %s, expected %s", out.Verdict, f.Expected()))
	}

	texts := append([]string{}, out.Comments...)
	for _, c := range out.PerCriterion {
		texts = append(texts, c.Evidence)
	}
	checks["location"] = 1
	if len(f.Defects) > 0 {
		var sum float64
		for _, d := range f.Defects {
			l := located(d, texts)
			if l < 1 {
				notes = append(notes, fmt.Sprintf("defect %s at %s:%d not pinpointed", d.Kind, d.File, d.Line))
			}
			sum += l
		}
		checks["location"] = sum / float64(len(f.Defects))
	}

	unmet := f.unmet()
	byText := map[string]bool{}
	for _, c := range out.PerCriterion {
		byText[strings.TrimSpace(c.Criterion)] = *c.Met
	}
	agree := 0
	for i, c := range f.AcceptanceCriteria {
		met, ok := byText[strings.TrimSpace(c)]
		switch {
		case !ok:
			notes = append(notes, fmt.Sprintf("criterion %d missing from per_criterion", i))
		case met == !unmet[i]:
			agree++
		default:
			notes = append(notes, fmt.Sprintf("criterion %d judged met=%t", i, met))
		}
	}
	checks["criteria"] = float64(agree) / float64(len(f.AcceptanceCriteria))

	if unsafeApproval(f, out) {
		notes = append(notes, errUnsafe.Error())
		return 0, checks, notes, true
	}
	var score float64
	for k, w := range reviewWeights {
		score += w * checks[k]
	}
	return score, checks, notes, false
}
