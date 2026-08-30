package main

import (
	"os"
	"os/user"
	"strings"
	"testing"
)

// TestRunReportDemo checks the half of the block this tool owns. The kit's own
// tests cover the machine facts and the scrubbing; what has to be right here is
// that --demo says demo, that the backend the fake imitates is named rather
// than left to be guessed, that the configured window is reported, and that no
// journal was read to produce any of it.
func TestRunReportDemo(t *testing.T) {
	var out strings.Builder
	opts := options{demo: true, report: true}
	if err := runReport(baseConfig(), opts, &out); err != nil {
		t.Fatalf("runReport: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"backend: demo\n",
		"mode: demo (sample data, the system was not read)\n",
		"demo backend: journald\n",
		"lines: 500\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	// There is no systemd behind the fake, so the version-gated parser must
	// not be claimed either way.
	if strings.Contains(got, "boot list:") {
		t.Errorf("demo report should not claim a boot-list parser:\n%s", got)
	}
	if !strings.HasPrefix(got, toolName+" ") {
		t.Errorf("report should start with the tool name:\n%s", got)
	}
}

// TestRunReportIsPublishable is the privacy promise as a test. The block is
// written to be pasted into a public issue, so anything that names this
// machine or the person on it appearing in it is a bug rather than a cosmetic
// detail.
func TestRunReportIsPublishable(t *testing.T) {
	var out strings.Builder
	if err := runReport(baseConfig(), options{report: true}, &out); err != nil {
		t.Fatalf("runReport: %v", err)
	}
	got := out.String()

	if strings.Contains(got, "/home/") || strings.Contains(got, "/root/") {
		t.Errorf("report carries a home path:\n%s", got)
	}
	// The host name is checked against the block as a whole. A machine whose
	// host name is also its distribution or in its kernel release — "fedora"
	// on a stock Fedora — would fail on a line that is not a leak, so that
	// case is skipped rather than asserted wrongly: the report is generated
	// with no way to reach the host name at all, and this test is the guard
	// against that changing.
	if host, err := os.Hostname(); err == nil && host != "" {
		switch {
		case strings.Contains(lineValue(got, "distro"), host),
			strings.Contains(lineValue(got, "kernel"), host):
			t.Logf("host name %q is not distinctive here, skipping", host)
		case strings.Contains(got, host):
			t.Errorf("report carries the host name:\n%s", got)
		}
	}
	if u, err := user.Current(); err == nil && u.Username != "" &&
		len(u.Username) > 2 {
		if strings.Contains(got, u.Username) {
			t.Errorf("report carries the user name:\n%s", got)
		}
	}
}

// TestBootListLine covers the one line the systemd version decides. The two
// parsers must be told apart in a report, because a boot that is missing or
// misdated is a bug in whichever of them this machine ran.
func TestBootListLine(t *testing.T) {
	if got := bootListLine(true); got != "json" {
		t.Errorf("bootListLine(true) = %q", got)
	}
	if got := bootListLine(false); !strings.Contains(got, "252") {
		t.Errorf("bootListLine(false) should name the version gate, got %q", got)
	}
}

// lineValue returns the value of one `key: value` line of a report block, or
// the empty string when the key is not there.
func lineValue(block, key string) string {
	for _, line := range strings.Split(block, "\n") {
		if value, ok := strings.CutPrefix(line, key+": "); ok {
			return value
		}
	}
	return ""
}
