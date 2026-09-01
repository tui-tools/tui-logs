package logs

// A unified diff of two short texts.
//
// It exists because a configuration change is only reviewable if the reader
// can see what moves. Showing the whole new file works for a file of three
// lines and stops working the moment there are twenty, and "SystemMaxUse is
// now 2G" is a claim about the file rather than the file itself. A diff is
// neither: it is the change, in the format every reader already knows.
//
// The implementation is a plain longest-common-subsequence over lines. The
// texts here are drop-ins of a few lines each, so the quadratic table is
// nothing, and DiffMaxLines refuses anything big enough to make that untrue.

import (
	"fmt"
	"strings"
)

// DiffMaxLines bounds each side of a diff. Beyond it the texts are not the
// small configuration files this is for, and the answer is a refusal rather
// than a slow one.
const DiffMaxLines = 2000

// diffContext is how many unchanged lines are shown around a change.
const diffContext = 3

// UnifiedDiff renders the change from one text to another in unified format,
// with the two names in its header. It returns the empty string when the two
// texts are identical, which is the caller's cue that there is nothing to
// confirm.
func UnifiedDiff(oldName, newName, oldText, newText string) string {
	if oldText == newText {
		return ""
	}
	oldLines, newLines := diffLines(oldText), diffLines(newText)
	if len(oldLines) > DiffMaxLines || len(newLines) > DiffMaxLines {
		return fmt.Sprintf("(these files are longer than %d lines, so no diff is shown)",
			DiffMaxLines)
	}

	edits := diffEdits(oldLines, newLines)
	hunks := groupHunks(edits)
	if len(hunks) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", oldName, newName)
	for _, hunk := range hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n",
			hunk.oldStart, hunk.oldCount, hunk.newStart, hunk.newCount)
		for _, edit := range hunk.edits {
			b.WriteString(edit.sign())
			b.WriteString(edit.text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// diffLines splits a text into the lines a diff compares. A trailing newline
// is not a line of its own, which is what keeps a file ending in one from
// showing a phantom empty line at the bottom of every diff.
func diffLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// editKind is what happened to one line.
type editKind int

const (
	editKeep editKind = iota
	editDelete
	editInsert
)

// edit is one line of the diff, with the position it had on each side.
type edit struct {
	kind editKind
	text string
	// oldLine and newLine are 1-based positions, and 0 where the line does
	// not exist on that side.
	oldLine, newLine int
}

// sign is the character unified format puts in front of a line.
func (e edit) sign() string {
	switch e.kind {
	case editDelete:
		return "-"
	case editInsert:
		return "+"
	default:
		return " "
	}
}

// diffEdits walks the LCS table backwards into the edit script, which comes
// out in order.
func diffEdits(oldLines, newLines []string) []edit {
	rows, cols := len(oldLines), len(newLines)
	// table[i][j] is the length of the longest common subsequence of
	// oldLines[i:] and newLines[j:].
	table := make([][]int, rows+1)
	for i := range table {
		table[i] = make([]int, cols+1)
	}
	for i := rows - 1; i >= 0; i-- {
		for j := cols - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				table[i][j] = table[i+1][j+1] + 1
				continue
			}
			table[i][j] = max(table[i+1][j], table[i][j+1])
		}
	}

	edits := make([]edit, 0, rows+cols)
	i, j := 0, 0
	for i < rows && j < cols {
		switch {
		case oldLines[i] == newLines[j]:
			edits = append(edits, edit{editKeep, oldLines[i], i + 1, j + 1})
			i, j = i+1, j+1
		case table[i+1][j] >= table[i][j+1]:
			edits = append(edits, edit{editDelete, oldLines[i], i + 1, 0})
			i++
		default:
			edits = append(edits, edit{editInsert, newLines[j], 0, j + 1})
			j++
		}
	}
	for ; i < rows; i++ {
		edits = append(edits, edit{editDelete, oldLines[i], i + 1, 0})
	}
	for ; j < cols; j++ {
		edits = append(edits, edit{editInsert, newLines[j], 0, j + 1})
	}
	return edits
}

// hunk is one run of changes with its context.
type hunk struct {
	oldStart, oldCount int
	newStart, newCount int
	edits              []edit
}

// groupHunks cuts the edit script into the runs unified format prints,
// keeping diffContext unchanged lines on each side of every change.
func groupHunks(edits []edit) []hunk {
	keep := make([]bool, len(edits))
	for i, e := range edits {
		if e.kind == editKeep {
			continue
		}
		low := max(i-diffContext, 0)
		high := min(i+diffContext, len(edits)-1)
		for k := low; k <= high; k++ {
			keep[k] = true
		}
	}

	var hunks []hunk
	for i := 0; i < len(edits); {
		if !keep[i] {
			i++
			continue
		}
		start := i
		for i < len(edits) && keep[i] {
			i++
		}
		hunks = append(hunks, buildHunk(edits[start:i]))
	}
	return hunks
}

// buildHunk turns one run of edits into a hunk with its line ranges.
func buildHunk(run []edit) hunk {
	h := hunk{edits: run}
	for _, e := range run {
		if e.kind != editInsert {
			h.oldCount++
			if h.oldStart == 0 {
				h.oldStart = e.oldLine
			}
		}
		if e.kind != editDelete {
			h.newCount++
			if h.newStart == 0 {
				h.newStart = e.newLine
			}
		}
	}
	// A hunk that only adds lines has no old line to start on, and unified
	// format writes the position it was added after — 0 for the top of a file
	// that had nothing in it.
	if h.oldStart == 0 && h.oldCount == 0 {
		h.oldStart = 0
	}
	if h.newStart == 0 && h.newCount == 0 {
		h.newStart = 0
	}
	return h
}
