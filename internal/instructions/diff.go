package instructions

import "strings"

const (
	OpEqual  = "equal"
	OpAdd    = "add"
	OpDelete = "delete"
)

const maxDiffCells = 16 << 20

type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func LineDiff(a, b string) []DiffLine {
	x, y := splitLines(a), splitLines(b)
	prefix := 0
	for prefix < len(x) && prefix < len(y) && x[prefix] == y[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(x)-prefix && suffix < len(y)-prefix && x[len(x)-1-suffix] == y[len(y)-1-suffix] {
		suffix++
	}
	out := make([]DiffLine, 0, len(x)+len(y))
	for _, l := range x[:prefix] {
		out = append(out, DiffLine{OpEqual, l})
	}
	out = append(out, diffMiddle(x[prefix:len(x)-suffix], y[prefix:len(y)-suffix])...)
	for _, l := range x[len(x)-suffix:] {
		out = append(out, DiffLine{OpEqual, l})
	}
	return out
}

func diffMiddle(x, y []string) []DiffLine {
	n, m := len(x), len(y)
	if n*m > maxDiffCells {
		out := make([]DiffLine, 0, n+m)
		for _, l := range x {
			out = append(out, DiffLine{OpDelete, l})
		}
		for _, l := range y {
			out = append(out, DiffLine{OpAdd, l})
		}
		return out
	}
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []DiffLine
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case x[i] == y[j]:
			out = append(out, DiffLine{OpEqual, x[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, DiffLine{OpDelete, x[i]})
			i++
		default:
			out = append(out, DiffLine{OpAdd, y[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, DiffLine{OpDelete, x[i]})
	}
	for ; j < m; j++ {
		out = append(out, DiffLine{OpAdd, y[j]})
	}
	return out
}
