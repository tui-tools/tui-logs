package journald

import (
	"context"
	"strings"
	"testing"

	"github.com/tui-tools/tui-logs/internal/logs"
)

// everything is the filter the tool opens on.
func everything() logs.Filter {
	return logs.Filter{Priority: logs.PriorityAny, Lines: logs.DefaultLines}
}

// TestTheSampleJournalIsWhatTheReadmeSays: --demo is documented in the README
// and in the manifest, and a demo that quietly drifted from its description
// would make both wrong.
func TestTheSampleJournalIsWhatTheReadmeSays(t *testing.T) {
	fake := NewFake()
	model, err := fake.Load(context.Background(), everything())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(model.Entries) != demoEntries {
		t.Errorf("the sample journal has %d entries, want %d",
			len(model.Entries), demoEntries)
	}
	if len(model.Boots) != 2 {
		t.Errorf("boots = %d, want 2", len(model.Boots))
	}
	// Five units and the kernel, which is what the description promises.
	units := map[string]bool{}
	kernel := false
	for _, entry := range model.Entries {
		if entry.Transport == "kernel" {
			kernel = true
			continue
		}
		units[entry.Source()] = true
	}
	if !kernel {
		t.Error("the sample journal has no kernel entries")
	}
	if len(units) < 5 {
		t.Errorf("the sample journal carries %d sources, want at least 5: %v",
			len(units), units)
	}
	if model.Stats.Errors == 0 {
		t.Error("the database that keeps failing to start is not failing")
	}
}

// TestTheDemoFiltersLikeJournalctlDoes: --demo exists so every key can be
// tried, and a filter that behaved differently there would teach the wrong
// thing about the real one.
func TestTheDemoFiltersLikeJournalctlDoes(t *testing.T) {
	fake := NewFake()
	ctx := context.Background()

	// -p is "this level and worse", not "exactly this level".
	filter := everything()
	filter.Priority = logs.PriWarning
	entries, _, err := fake.ReadEntries(ctx, filter)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries at warning and worse")
	}
	for _, entry := range entries {
		if entry.Priority > logs.PriWarning {
			t.Fatalf("%v survived -p warning", entry.Priority)
		}
	}

	// --since bounds the window on the sample machine's own clock.
	filter = everything()
	filter.Since = "-1h"
	entries, _, err = fake.ReadEntries(ctx, filter)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	if len(entries) == 0 || len(entries) >= demoEntries {
		t.Fatalf("the last hour returned %d of %d entries",
			len(entries), demoEntries)
	}

	// --grep is journalctl's smart case: a lower-case pattern matches either.
	filter = everything()
	filter.Grep = "invalid user"
	entries, _, err = fake.ReadEntries(ctx, filter)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the burst should have matched")
	}
	filter.Grep = "INVALID USER"
	entries, _, err = fake.ReadEntries(ctx, filter)
	if err != nil {
		t.Fatalf("ReadEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a capitalised pattern matched %d entries, and journalctl "+
			"would have matched none", len(entries))
	}
}

// TestTheDemoRunsNothing is the promise --demo makes: every command is built
// and previewed for real, and none of them reaches the system.
func TestTheDemoRunsNothing(t *testing.T) {
	fake := NewFake()
	cmd, err := fake.BuildVacuumSize("500M")
	if err != nil {
		t.Fatalf("BuildVacuumSize: %v", err)
	}
	if got := fake.Preview(cmd); got != "sudo -n journalctl --vacuum-size=500M" {
		t.Errorf("Preview = %q", got)
	}
	before, _ := fake.ReadStorage(context.Background())
	if _, err := fake.Run(context.Background(), cmd); err != nil {
		t.Fatalf("Run: %v", err)
	}
	after, _ := fake.ReadStorage(context.Background())
	if after.Bytes >= before.Bytes {
		t.Errorf("the demo's disk usage did not move: %d then %d",
			before.Bytes, after.Bytes)
	}
	if ran := fake.Ran(); len(ran) != 1 || ran[0].String() != cmd.String() {
		t.Errorf("the recorded command is not the previewed one: %v", ran)
	}
}

func TestTheDemoExportRefusesTheSamePathsTheRealOneDoes(t *testing.T) {
	fake := NewFake()
	for _, bad := range []string{"/etc/passwd", "relative.log",
		"/home/you/../etc/passwd"} {
		if _, err := fake.BuildExport(context.Background(), everything(),
			bad); err == nil {
			t.Errorf("the demo accepted %q as an export path", bad)
		}
	}
	plan, err := fake.BuildExport(context.Background(), everything(),
		"/home/you/journal.log")
	if err != nil {
		t.Fatalf("BuildExport: %v", err)
	}
	if plan.Lines != demoEntries || plan.Bytes == 0 {
		t.Errorf("the export staged %d lines and %d bytes", plan.Lines, plan.Bytes)
	}
	if !strings.Contains(plan.Source.String(), "-o short-iso") {
		t.Errorf("an export reads as text, not as JSON: %s", plan.Source)
	}
}
