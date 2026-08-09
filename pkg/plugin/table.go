package plugin

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cast"
)

// ansiEscape matches an SGR color/style escape sequence (e.g. from fatih/color), used by
// visibleWidth to measure a cell's on-screen width rather than its byte length.
var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*m")

func visibleWidth(s string) int {
	return len([]rune(ansiEscape.ReplaceAllString(s, "")))
}

// renderGroupedTable renders rows -- each a []interface{} of cell strings that may carry ANSI
// color escapes -- as space-padded columns under a leading column (e.g. "pod") plus one or more
// grouped columns (e.g. three cpu columns: use/req/lim), each group introduced by a wide spanning
// label (e.g. "cpu use/req/lim (%allocatable)") given in groupLabels, with the column count each
// label spans given in groupSpans. Per-column widths are computed from the data cells only, not
// from the (much longer) group labels: letting a label stretch its columns would separate a
// group's own values -- e.g. push "req"/"lim" far away from "use" -- to make room for text that
// only needs to introduce the group, not size it. A label wider than its group's total column
// width simply runs on towards the next group instead, same as a comment overflowing its column
// in any other compact table. Column widths (also computed from ANSI-escape-stripped visible
// width, not byte length, so colored cells still align once printed to a real terminal) apply to
// the data rows only.
func renderGroupedTable(leadingHeader string, groupLabels []interface{}, groupSpans []interface{}, rows []interface{}) string {
	var table [][]string
	for _, row := range rows {
		if cells, ok := row.([]interface{}); ok {
			table = append(table, cellStrings(cells))
		}
	}
	if len(table) == 0 {
		return ""
	}
	numCols := len(table[0])
	widths := make([]int, numCols)
	for _, row := range table {
		for i, cell := range row {
			if i >= numCols {
				continue
			}
			if w := visibleWidth(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}

	var sb strings.Builder
	sb.WriteString(leadingHeader)
	if pad := widths[0] - visibleWidth(leadingHeader); pad > 0 {
		sb.WriteString(strings.Repeat(" ", pad))
	}
	col := 1
	for gi, groupLabel := range groupLabels {
		span := cast.ToInt(groupSpans[gi])
		groupWidth := 0
		for s := 0; s < span && col+s < numCols; s++ {
			if s > 0 {
				groupWidth += 2
			}
			groupWidth += widths[col+s]
		}
		label := fmt.Sprint(groupLabel)
		sb.WriteString("  ")
		sb.WriteString(label)
		if pad := groupWidth - visibleWidth(label); pad > 0 && gi < len(groupLabels)-1 {
			sb.WriteString(strings.Repeat(" ", pad))
		}
		col += span
	}

	for _, row := range table {
		sb.WriteString("\n")
		for i, cell := range row {
			if i > 0 {
				sb.WriteString("  ")
			}
			sb.WriteString(cell)
			if i < numCols-1 {
				sb.WriteString(strings.Repeat(" ", widths[i]-visibleWidth(cell)))
			}
		}
	}
	return sb.String()
}

func cellStrings(cells []interface{}) []string {
	out := make([]string, len(cells))
	for i, cell := range cells {
		out[i] = fmt.Sprint(cell)
	}
	return out
}
