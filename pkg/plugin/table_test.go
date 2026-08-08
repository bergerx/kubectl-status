package plugin

import (
	"testing"
)

func TestRenderGroupedTableAlignsOnVisibleWidthNotByteLength(t *testing.T) {
	// Data column widths must be computed from each cell's visible (ANSI-escape-stripped) width,
	// not its byte length: fatih/color's escape codes add bytes a real terminal doesn't render,
	// so byte-based padding would misalign colored output while looking fine in escape-free e2e
	// artifacts (color.NoColor is true when stdout isn't a TTY). Here "short" is colored (more
	// bytes, same visible width as "xyz") and sets column 0's width jointly with "xyz".
	short := "\x1b[31mab\x1b[0m" // visible width 2, byte length 11
	rows := []interface{}{
		[]interface{}{short, "1", "22"},
		[]interface{}{"xyz", "5", "6"},
	}
	got := renderGroupedTable("pod", []interface{}{"grp"}, []interface{}{3}, rows)
	want := "pod  grp\n" + short + "   1  22\nxyz  5  6"
	if got != want {
		t.Fatalf("renderGroupedTable() = %q, want %q", got, want)
	}
}

func TestRenderGroupedTableGroupLabelDoesNotStretchDataColumns(t *testing.T) {
	// A group's spanning label (e.g. Node.tmpl's "cpu use/req/lim (%allocatable)") is typically
	// far wider than the data underneath it -- it must not stretch that data's column widths to
	// fit, or a group's own use/req/lim values end up separated by all the extra padding instead
	// of sitting together.
	rows := []interface{}{
		[]interface{}{"row", "1", "2", "3"},
	}
	got := renderGroupedTable("pod", []interface{}{"cpu use/req/lim (%allocatable)"}, []interface{}{3}, rows)
	want := "pod  cpu use/req/lim (%allocatable)\nrow  1  2  3"
	if got != want {
		t.Fatalf("renderGroupedTable() = %q, want %q", got, want)
	}
}
