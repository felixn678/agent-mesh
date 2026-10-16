package cli

import "strings"

// lineDiff renders a minimal line diff (LCS) of a and b: " " unchanged, "-"
// removed, "+" added. settings.json is small, so O(n·m) is fine.
func lineDiff(a, b string) string {
	x, y := strings.SplitAfter(a, "\n"), strings.SplitAfter(b, "\n")
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
	var out strings.Builder
	line := func(prefix, s string) {
		if s == "" {
			return
		}
		out.WriteString(prefix + s)
		if !strings.HasSuffix(s, "\n") {
			out.WriteString("\n")
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case x[i] == y[j]:
			line(" ", x[i])
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			line("-", x[i])
			i++
		default:
			line("+", y[j])
			j++
		}
	}
	for ; i < n; i++ {
		line("-", x[i])
	}
	for ; j < m; j++ {
		line("+", y[j])
	}
	return out.String()
}
