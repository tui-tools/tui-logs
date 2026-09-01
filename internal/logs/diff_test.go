package logs

import (
	"strings"
	"testing"
)

func TestUnifiedDiffOfIdenticalTextsIsEmpty(t *testing.T) {
	if got := UnifiedDiff("a", "b", "one\ntwo\n", "one\ntwo\n"); got != "" {
		t.Errorf("UnifiedDiff of identical texts = %q, want empty", got)
	}
	if got := UnifiedDiff("a", "b", "", ""); got != "" {
		t.Errorf("UnifiedDiff of two empty texts = %q, want empty", got)
	}
}

func TestUnifiedDiffShowsTheChange(t *testing.T) {
	before := "[Journal]\nSystemMaxUse=4G\nMaxRetentionSec=1month\n"
	after := "[Journal]\nSystemMaxUse=2G\nMaxRetentionSec=1month\nStorage=persistent\n"
	got := UnifiedDiff("old.conf", "new.conf", before, after)

	for _, want := range []string{
		"--- old.conf", "+++ new.conf", "@@ ",
		"-SystemMaxUse=4G", "+SystemMaxUse=2G", "+Storage=persistent",
		" [Journal]", " MaxRetentionSec=1month",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the diff does not carry %q:\n%s", want, got)
		}
	}
	// A line that did not move must not appear as both a removal and an
	// addition, which is what a diff that is really a rewrite looks like.
	if strings.Contains(got, "-MaxRetentionSec") {
		t.Errorf("an unchanged line was reported as removed:\n%s", got)
	}
}

func TestUnifiedDiffOfANewFile(t *testing.T) {
	got := UnifiedDiff("none", "new.conf", "", "[Journal]\nStorage=persistent\n")
	if !strings.Contains(got, "+[Journal]") ||
		!strings.Contains(got, "+Storage=persistent") {
		t.Errorf("a new file's diff does not add its lines:\n%s", got)
	}
	if strings.Contains(got, "\n-") {
		t.Errorf("a new file's diff removes something:\n%s", got)
	}
}

func TestUnifiedDiffKeepsContextAndSplitsHunks(t *testing.T) {
	before := lines("a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l")
	after := lines("A", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "L")
	got := UnifiedDiff("old", "new", before, after)

	// Two changes twelve lines apart are two hunks, not one that drags every
	// unchanged line between them along.
	if count := strings.Count(got, "@@ "); count != 2 {
		t.Errorf("the diff has %d hunks, want 2:\n%s", count, got)
	}
	for _, unwanted := range []string{" f\n", " g\n"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("the diff carried a line far from any change:\n%s", got)
		}
	}
	// Three lines of context around each change, and no more.
	for _, want := range []string{"-a", "+A", " b", " c", " d", "-l", "+L", " k"} {
		if !strings.Contains(got, want) {
			t.Errorf("the diff does not carry %q:\n%s", want, got)
		}
	}
}

func TestUnifiedDiffRefusesSomethingTooLong(t *testing.T) {
	var b strings.Builder
	for range DiffMaxLines + 1 {
		b.WriteString("x\n")
	}
	got := UnifiedDiff("old", "new", "", b.String())
	if !strings.Contains(got, "no diff is shown") {
		t.Errorf("a huge text produced %q", got)
	}
}

// lines joins its arguments into a text with a trailing newline, the way a
// file on disk has one.
func lines(values ...string) string {
	return strings.Join(values, "\n") + "\n"
}
