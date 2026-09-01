package main

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-logs/internal/journald"
)

// The retention form driven the way a user drives it: one key, three answers,
// a diff, and only then two commands.

// openRetention presses S and plays the read it starts.
func openRetention(t *testing.T, a *app) {
	t.Helper()
	drain(t, a, press(a, "S"))
	if a.mode != modeInput {
		t.Fatalf("S opened mode %v, want the SystemMaxUse prompt", a.mode)
	}
}

func TestRetentionFormPreviewsADiffThenRunsExactlyWhatItShowed(t *testing.T) {
	a, backend := newTestApp(t)
	openRetention(t, a)

	// The form opens on what the sample machine's journald.conf says, not on
	// a blank: the demo's drop-in sets SystemMaxUse=4G.
	if got := a.input.Value(); got != "4G" {
		t.Errorf("SystemMaxUse opened on %q, want the machine's 4G", got)
	}
	typeInto(t, a, "2G")

	if a.mode != modeInput {
		t.Fatalf("after SystemMaxUse the app is in mode %v, want MaxRetentionSec", a.mode)
	}
	if got := a.input.Value(); got != "1month" {
		t.Errorf("MaxRetentionSec opened on %q, want the machine's 1month", got)
	}
	typeInto(t, a, "90d")

	if a.mode != modePicker {
		t.Fatalf("after MaxRetentionSec the app is in mode %v, want the Storage picker", a.mode)
	}
	for i, option := range a.picker.Options {
		if option == "persistent" {
			a.picker.Cursor = i
		}
	}
	drain(t, a, press(a, "enter"))

	if a.mode != modeConfirm {
		t.Fatalf("after the last answer the app is in mode %v, want the dialog", a.mode)
	}
	// Two commands, in the order they run, both in the preview.
	want := "sudo -n install -D -m 644 /tmp/tui-logs/20260830-114500-journald.conf " +
		journald.DropInPath + "\n$ sudo -n systemctl restart " + journald.JournaldUnit
	if a.confirm.Command != want {
		t.Errorf("the dialog shows:\n%q\nwant:\n%q", a.confirm.Command, want)
	}
	// The body is the diff, and it has to show what moves.
	for _, line := range []string{"+SystemMaxUse=2G", "+MaxRetentionSec=90d",
		"+Storage=persistent", "--- " + journald.DropInPath} {
		if !strings.Contains(a.confirm.Body, line) {
			t.Errorf("the dialog body does not carry %q:\n%s", line, a.confirm.Body)
		}
	}
	// And it has to say why journald is restarted rather than reloaded,
	// because that is the part a reader cannot work out from the argv.
	if !strings.Contains(a.confirm.Body, "SIGHUP") {
		t.Errorf("the dialog does not explain the restart:\n%s", a.confirm.Body)
	}
	if ran := backend.Ran(); len(ran) != 0 {
		t.Fatalf("filling in the form ran %v", ran)
	}

	drain(t, a, press(a, "y"))
	ran := backend.Ran()
	if len(ran) != 2 {
		t.Fatalf("ran %d commands, want 2: %v", len(ran), ran)
	}
	got := backend.Preview(ran[0]) + "\n$ " + backend.Preview(ran[1])
	if got != want {
		t.Errorf("ran:\n%q\npreviewed:\n%q", got, want)
	}
	// The machine is re-read after a change, and the sample machine's own
	// configuration has moved.
	after, err := backend.ReadRetention(t.Context())
	if err != nil {
		t.Fatalf("ReadRetention: %v", err)
	}
	if after.Values[journald.KeySystemMaxUse] != "2G" {
		t.Errorf("the drop-in reads %+v after the run", after.Values)
	}
}

func TestRetentionFormReAsksARefusedValue(t *testing.T) {
	a, backend := newTestApp(t)
	openRetention(t, a)

	typeInto(t, a, "lots")
	if a.mode != modeInput {
		t.Fatalf("a refused value left the app in mode %v, want the prompt again", a.mode)
	}
	if a.input.Title != journald.KeySystemMaxUse {
		t.Errorf("the app moved on to %q instead of asking again", a.input.Title)
	}
	if a.status == "" {
		t.Error("a refused value should say why")
	}
	// The prompt comes back holding what was typed, so it is corrected rather
	// than retyped.
	if got := a.input.Value(); got != "lots" {
		t.Errorf("the prompt reopened on %q, want the rejected answer", got)
	}
	if ran := backend.Ran(); len(ran) != 0 {
		t.Fatalf("a refused value ran %v", ran)
	}
}

func TestRetentionFormCanBeAbandoned(t *testing.T) {
	a, backend := newTestApp(t)
	openRetention(t, a)
	drain(t, a, press(a, "esc"))

	if a.mode != modeBrowse {
		t.Errorf("esc left the form in mode %v", a.mode)
	}
	if a.retention != nil {
		t.Error("the abandoned form is still open")
	}
	if ran := backend.Ran(); len(ran) != 0 {
		t.Fatalf("abandoning the form ran %v", ran)
	}

	// And abandoning it at the picker, which is the other way out.
	openRetention(t, a)
	typeInto(t, a, "2G")
	typeInto(t, a, "30d")
	if a.mode != modePicker {
		t.Fatalf("the third step is mode %v, want the picker", a.mode)
	}
	drain(t, a, press(a, "esc"))
	if a.retention != nil {
		t.Error("the form abandoned at the picker is still open")
	}
	if ran := backend.Ran(); len(ran) != 0 {
		t.Fatalf("abandoning at the picker ran %v", ran)
	}
}

func TestRetentionFormWithNoAnswersAtAllWritesNothing(t *testing.T) {
	a, backend := newTestApp(t)
	openRetention(t, a)
	typeInto(t, a, "")
	typeInto(t, a, "")
	// The Storage picker has no empty option, so it is cancelled instead —
	// which abandons the form rather than writing an empty file.
	drain(t, a, press(a, "esc"))

	if a.mode == modeConfirm {
		t.Fatalf("an empty form opened a dialog for %q", a.confirm.Command)
	}
	if ran := backend.Ran(); len(ran) != 0 {
		t.Fatalf("an empty form ran %v", ran)
	}
}

func TestRetentionFormRefusesToRewriteTheSameFile(t *testing.T) {
	a, _ := newTestApp(t)

	// Write the drop-in once…
	openRetention(t, a)
	typeInto(t, a, "2G")
	typeInto(t, a, "90d")
	for i, option := range a.picker.Options {
		if option == "persistent" {
			a.picker.Cursor = i
		}
	}
	drain(t, a, press(a, "enter"))
	drain(t, a, press(a, "y"))

	// …then answer the form with exactly the same three values. There is
	// nothing to install, and the tool says so rather than restarting
	// journald for nothing.
	openRetention(t, a)
	typeInto(t, a, "2G")
	typeInto(t, a, "90d")
	for i, option := range a.picker.Options {
		if option == "persistent" {
			a.picker.Cursor = i
		}
	}
	drain(t, a, press(a, "enter"))

	if a.mode == modeConfirm {
		t.Fatalf("an unchanged form opened a dialog for %q", a.confirm.Command)
	}
	if !strings.Contains(a.status, "nothing to write") {
		t.Errorf("status = %q, want it to say there is nothing to write", a.status)
	}
}

func TestStorageScreenPointsAtTheForm(t *testing.T) {
	a, _ := newTestApp(t)
	a.screen = screenStorage
	a.clampCursor()
	frame := a.View()
	if !strings.Contains(frame, "S ") || !strings.Contains(frame, "retention") {
		t.Errorf("the storage screen does not offer the retention key:\n%s", frame)
	}
}

func TestANonPersistentJournalPointsAtTheFormRatherThanAtAShell(t *testing.T) {
	// The note used to end in a command for the reader to go and run. A tool
	// that knows the command previews the command.
	if strings.Contains(journald.NotPersistentNote, "sudo ") ||
		strings.Contains(journald.NotPersistentNote, "mkdir") {
		t.Errorf("the note still hands the user a shell command: %q",
			journald.NotPersistentNote)
	}
	if !strings.Contains(journald.NotPersistentNote, "S sets Storage=persistent") {
		t.Errorf("the note does not point at the form: %q", journald.NotPersistentNote)
	}
}
