package presets

import (
	"fmt"
	"strings"
)

// DiffLine is one line of a line-based diff.
type DiffLine struct {
	Op   string `json:"op"` // " ", "+", "-"
	Text string `json:"text"`
}

// Diff returns a line diff of a → b (LCS based; preset files are small).
func Diff(a, b string) []DiffLine {
	x, y := splitLines(a), splitLines(b)
	n, m := len(x), len(y)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
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
			out = append(out, DiffLine{" ", x[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, DiffLine{"-", x[i]})
			i++
		default:
			out = append(out, DiffLine{"+", y[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, DiffLine{"-", x[i]})
	}
	for ; j < m; j++ {
		out = append(out, DiffLine{"+", y[j]})
	}
	return out
}

// Changed reports whether a diff contains any change.
func Changed(d []DiffLine) bool {
	for _, l := range d {
		if l.Op != " " {
			return true
		}
	}
	return false
}

// Unified renders a diff in unified format with the given context lines (for the CLI).
func Unified(d []DiffLine, context int) string {
	var b strings.Builder
	show := make([]bool, len(d))
	for i, l := range d {
		if l.Op != " " {
			for k := max(0, i-context); k <= min(len(d)-1, i+context); k++ {
				show[k] = true
			}
		}
	}
	gap := false
	for i, l := range d {
		if !show[i] {
			gap = true
			continue
		}
		if gap && b.Len() > 0 {
			b.WriteString("@@\n")
		}
		gap = false
		fmt.Fprintf(&b, "%s%s\n", l.Op, l.Text)
	}
	return b.String()
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
