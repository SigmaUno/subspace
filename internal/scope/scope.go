package scope

import (
	"fmt"
	"slices"
	"strings"
)

const (
	SeverityError   = "error"
	SeverityWarning = "warning"

	RuleOutsidePaths     = "outside_paths"
	RuleProtectedPath    = "protected_path"
	RuleDiffTooLarge     = "diff_too_large"
	RuleBranchMismatch   = "branch_mismatch"
	RuleUnexpectedAuthor = "unexpected_author"
	RuleDiffTruncated    = "diff_truncated"

	MaxFiles       = 3000
	LinesPerMinute = 20

	maxListed = 20
)

type Diff struct {
	HeadSHA   string   `json:"head_sha"`
	HeadRef   string   `json:"head_ref"`
	Files     []string `json:"files"`
	Additions int      `json:"additions"`
	Deletions int      `json:"deletions"`
	Truncated bool     `json:"truncated"`
	Authors   []string `json:"authors"`
}

type Input struct {
	Diff           Diff
	Paths          []string
	Branch         string
	MaxMinutes     int
	AllowedAuthors []string
}

type Finding struct {
	Rule     string   `json:"rule"`
	Severity string   `json:"severity"`
	Message  string   `json:"message"`
	Paths    []string `json:"paths,omitempty"`
}

type Report struct {
	HeadSHA   string    `json:"head_sha"`
	Files     int       `json:"files"`
	Additions int       `json:"additions"`
	Deletions int       `json:"deletions"`
	Truncated bool      `json:"truncated"`
	Findings  []Finding `json:"findings"`
}

func (r *Report) Blocking() bool {
	return r != nil && slices.ContainsFunc(r.Findings, func(f Finding) bool { return f.Severity == SeverityError })
}

func Score(in Input) Report {
	d := in.Diff
	r := Report{HeadSHA: d.HeadSHA, Files: len(d.Files), Additions: d.Additions, Deletions: d.Deletions,
		Truncated: d.Truncated, Findings: []Finding{}}

	if len(in.Paths) > 0 {
		var outside []string
		for _, f := range d.Files {
			if !matchesAny(in.Paths, f) {
				outside = append(outside, f)
			}
		}
		if len(outside) > 0 {
			r.add(RuleOutsidePaths, SeverityError, fmt.Sprintf("%d changed files are outside the task paths", len(outside)), outside)
		}
	}

	var protected []string
	for _, f := range d.Files {
		if protectedReason(f) != "" && !allows(in.Paths, f) {
			protected = append(protected, f)
		}
	}
	if len(protected) > 0 {
		r.add(RuleProtectedPath, SeverityError, fmt.Sprintf("%d changed files are in protected locations", len(protected)), protected)
	}

	if limit := in.MaxMinutes * LinesPerMinute; in.MaxMinutes > 0 && d.Additions+d.Deletions > limit {
		r.add(RuleDiffTooLarge, SeverityWarning,
			fmt.Sprintf("%d changed lines exceed %d for a budget of %d minutes", d.Additions+d.Deletions, limit, in.MaxMinutes), nil)
	}

	if d.HeadRef != "" && in.Branch != "" && d.HeadRef != in.Branch {
		r.add(RuleBranchMismatch, SeverityError, fmt.Sprintf("pull request branch %q is not the task branch %q", d.HeadRef, in.Branch), nil)
	}

	if len(in.AllowedAuthors) > 0 {
		allowed := make([]string, len(in.AllowedAuthors))
		for i, a := range in.AllowedAuthors {
			allowed[i] = strings.ToLower(a)
		}
		var unexpected []string
		for _, a := range d.Authors {
			if !slices.Contains(allowed, strings.ToLower(a)) {
				unexpected = append(unexpected, a)
			}
		}
		if len(unexpected) > 0 {
			r.add(RuleUnexpectedAuthor, SeverityError, fmt.Sprintf("commits by unexpected identities: %s", strings.Join(unexpected, ", ")), nil)
		}
	}

	if d.Truncated {
		r.add(RuleDiffTruncated, SeverityWarning, fmt.Sprintf("only the first %d changed files were checked", MaxFiles), nil)
	}
	return r
}

func (r *Report) add(rule, severity, msg string, paths []string) {
	if len(paths) > maxListed {
		paths = paths[:maxListed]
	}
	r.Findings = append(r.Findings, Finding{Rule: rule, Severity: severity, Message: msg, Paths: paths})
}

func matchesAny(patterns []string, file string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return match(p, file) })
}

func allows(patterns []string, file string) bool {
	for _, p := range patterns {
		if !match(p, file) {
			continue
		}
		base := strings.TrimSuffix(p, "/**")
		if protectedReason(base) != "" || protectedReason(base+"/x") != "" {
			return true
		}
	}
	return false
}

func match(pattern, file string) bool {
	p, f := strings.Split(pattern, "/"), strings.Split(file, "/")
	memo := make(map[[2]int]bool)
	var walk func(i, j int) bool
	walk = func(i, j int) bool {
		if i == len(p) {
			return true
		}
		k := [2]int{i, j}
		if v, ok := memo[k]; ok {
			return v
		}
		var ok bool
		switch {
		case p[i] == "**":
			for n := j; n <= len(f) && !ok; n++ {
				ok = walk(i+1, n)
			}
		case j < len(f):
			ok = matchSegment(p[i], f[j]) && walk(i+1, j+1)
		}
		memo[k] = ok
		return ok
	}
	return walk(0, 0)
}

func matchSegment(pattern, s string) bool {
	p, t := []rune(pattern), []rune(s)
	pi, ti, star, mark := 0, 0, -1, 0
	for ti < len(t) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == t[ti]) && p[pi] != '*':
			pi++
			ti++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ti
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			ti = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
