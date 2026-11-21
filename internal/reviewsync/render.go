package reviewsync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	VerdictApprove          = "approve"
	VerdictChangesRequested = "changes_requested"

	MaxBodyBytes     = 8000
	maxCommentRunes  = 3000
	maxCriterionRune = 200
	maxCriteria      = 20
	maxTitleRunes    = 100
)

var (
	taskIDRe    = regexp.MustCompile(`^T-[0-9]+$`)
	criterionRe = regexp.MustCompile(`(?im)^\s*(?:[-*]\s*)?(?:criterion|ac)\s*#?(\d+)\s*[:=-]\s*(pass(?:ed)?|met|ok|fail(?:ed)?|unmet)\b`)
)

type Review struct {
	TaskID     string
	Title      string
	Criteria   []string
	Round      int
	NeedsOwner bool
	Verdict    string
	Reviewer   string
	Comments   string
	TaskURL    string

	extraCriteria int
}

func Marker(taskID string) string { return "<!-- subspace:review task=" + taskID + " -->" }

func Digest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func Render(r Review) string {
	r.Title, r.Reviewer, r.Comments = prepare(r.Title, 8*maxTitleRunes), prepare(r.Reviewer, 256), prepare(r.Comments, 6*maxCommentRunes)
	total := len(r.Criteria)
	r.Criteria = slices.Clone(r.Criteria[:min(total, maxCriteria)])
	for i := range r.Criteria {
		r.Criteria[i] = prepare(r.Criteria[i], 10*maxCriterionRune)
	}
	r.extraCriteria = total - len(r.Criteria)
	for c, k := maxCommentRunes, maxCriterionRune; ; c, k = c/2, k/2 {
		body := build(r, c, k)
		if len(body) <= MaxBodyBytes || (c == 0 && k == 0) {
			return body
		}
	}
}

func prepare(s string, bound int) string {
	s, _ = truncate(s, bound)
	return clean(s)
}

func results(comments string) map[int]bool {
	out := map[int]bool{}
	for _, m := range criterionRe.FindAllStringSubmatch(comments, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		v := strings.ToLower(m[2])
		out[n] = strings.HasPrefix(v, "pass") || v == "met" || v == "ok"
	}
	return out
}

func build(r Review, commentRunes, criterionRunes int) string {
	var b strings.Builder
	b.WriteString(Marker(r.TaskID))
	b.WriteString("\n### Subspace review: ")
	approved := r.Verdict == VerdictApprove
	if approved {
		b.WriteString("approved")
	} else {
		b.WriteString("changes requested")
	}
	b.WriteString("\n\n")
	if r.TaskURL != "" {
		fmt.Fprintf(&b, "Task [%s](%s)", r.TaskID, r.TaskURL)
	} else {
		b.WriteString("Task " + r.TaskID)
	}
	if t := inline(r.Title, maxTitleRunes); t != "" {
		b.WriteString(": " + t)
	}
	fmt.Fprintf(&b, "\n\nRound %d", max(r.Round, 1))
	if rev := inline(r.Reviewer, 64); rev != "" {
		b.WriteString(", reviewer " + rev)
	}
	b.WriteString("\n")
	if r.NeedsOwner {
		b.WriteString("\nThe round limit is reached; the owner decides how to continue.\n")
	}

	if len(r.Criteria) > 0 {
		got := results(r.Comments)
		b.WriteString("\n**Acceptance criteria**\n\n")
		for i, c := range r.Criteria {
			state := "not reported"
			if met, ok := got[i+1]; ok {
				state = map[bool]string{true: "met", false: "not met"}[met]
			} else if approved {
				state = "met"
			}
			fmt.Fprintf(&b, "%d\\. **%s**: %s\n", i+1, state, inline(c, criterionRunes))
		}
		if r.extraCriteria > 0 {
			fmt.Fprintf(&b, "\nand %d more\n", r.extraCriteria)
		}
	}

	if q, cut := quote(r.Comments, commentRunes); q != "" {
		b.WriteString("\n**Review comments**\n\n" + q + "\n")
		if cut {
			b.WriteString("\nComments were shortened; the full text is in the task thread.\n")
		}
	}
	b.WriteString("\nThis comment is updated in place on every review round.\n")
	return b.String()
}
