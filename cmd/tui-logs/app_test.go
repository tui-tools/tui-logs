package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-logs/internal/journald"
	"github.com/tui-tools/tui-logs/internal/logs"
)

// newTestApp builds an app on the sample journal, sized like a normal
// terminal and already loaded.
func newTestApp(t *testing.T) (*app, *journald.Fake) {
	t.Helper()
	backend := journald.NewFake()
	a := newApp(backend, theme.New(), demoFilter(), compat.Result{})
	a.width, a.height = 110, 30
	drain(t, a, a.Init())
	return a, backend
}

// drain runs a tea.Cmd and feeds its message back into the model, which is
// what the Bubble Tea runtime does. It is how a test exercises a read.
func drain(t *testing.T, a *app, cmd tea.Cmd) {
	t.Helper()
	for range 4 {
		if cmd == nil {
			return
		}
		msg := cmd()
		if msg == nil {
			return
		}
		_, cmd = a.Update(msg)
	}
}

// press sends one key and returns the command it produced.
func press(a *app, key string) tea.Cmd {
	var msg tea.KeyMsg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	_, cmd := a.Update(msg)
	return cmd
}

// pick opens a picker with one key, moves to an option by name and accepts it.
func pick(t *testing.T, a *app, key, option string) {
	t.Helper()
	drain(t, a, press(a, key))
	if a.mode != modePicker {
		t.Fatalf("%q did not open a picker (mode %v)", key, a.mode)
	}
	found := false
	for i, candidate := range a.picker.Options {
		if strings.Contains(candidate, option) {
			a.picker.Cursor, found = i, true
			break
		}
	}
	if !found {
		t.Fatalf("the %q picker offers no %q: %v", key, option, a.picker.Options)
	}
	drain(t, a, press(a, "enter"))
}

// typeInto fills an open prompt and submits it.
func typeInto(t *testing.T, a *app, text string) {
	t.Helper()
	if a.mode != modeInput {
		t.Fatalf("no prompt is open (mode %v)", a.mode)
	}
	a.input.Model.SetValue(text)
	drain(t, a, press(a, "enter"))
}

func TestLoadsTheSampleJournal(t *testing.T) {
	a, _ := newTestApp(t)
	if len(a.model.Entries) != 300 {
		t.Fatalf("loaded %d entries, want 300", len(a.model.Entries))
	}
	if len(a.model.Boots) != 2 {
		t.Errorf("boots = %d, want 2", len(a.model.Boots))
	}
	if len(a.model.Units) == 0 {
		t.Error("the unit picker has nothing to offer")
	}
	if a.model.Stats.Errors == 0 {
		t.Error("the sample journal has failures in it and none were counted")
	}
	frame := a.View()
	if !strings.Contains(frame, "sshd") {
		t.Error("the first frame does not show the entries")
	}
	// The command behind what is on screen is the whole point of the filters,
	// so it is on the status line from the first frame.
	if !strings.Contains(frame, "journalctl") {
		t.Error("the first frame does not show the command it read with")
	}
}

// TestActionsPreviewExactlyWhatTheyRun is the family's central promise, as a
// test: for every action key, the command line in the confirm dialog is the
// command line the backend is then asked to run.
func TestActionsPreviewExactlyWhatTheyRun(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *app)
		want  string
	}{
		{
			name:  "vacuum by size",
			setup: func(t *testing.T, a *app) { pick(t, a, "v", "500M") },
			want:  "sudo -n journalctl --vacuum-size=500M",
		},
		{
			name:  "vacuum by age",
			setup: func(t *testing.T, a *app) { pick(t, a, "V", "30d") },
			want:  "sudo -n journalctl --vacuum-time=30d",
		},
		{
			name:  "rotate",
			setup: func(t *testing.T, a *app) { drain(t, a, press(a, "o")) },
			want:  "sudo -n journalctl --rotate",
		},
		{
			name:  "verify",
			setup: func(t *testing.T, a *app) { drain(t, a, press(a, "y")) },
			want:  "sudo -n journalctl --verify",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a, backend := newTestApp(t)
			test.setup(t, a)
			if a.mode != modeConfirm {
				t.Fatalf("no confirm dialog opened (mode %v)", a.mode)
			}
			if a.confirm.Command != test.want {
				t.Fatalf("the dialog shows %q, want %q",
					a.confirm.Command, test.want)
			}
			// The dialog must also say what the command does, not only what
			// it is: a preview nobody can interpret is not a preview.
			if len(a.confirm.Body) < 20 {
				t.Errorf("the dialog explains nothing: %q", a.confirm.Body)
			}

			drain(t, a, press(a, "y"))
			ran := backend.Ran()
			if len(ran) != 1 {
				t.Fatalf("ran %d commands, want 1: %v", len(ran), ran)
			}
			if got := backend.Preview(ran[0]); got != test.want {
				t.Errorf("ran %q, previewed %q", got, test.want)
			}
		})
	}
}

func TestCancellingRunsNothing(t *testing.T) {
	a, backend := newTestApp(t)
	drain(t, a, press(a, "o"))
	if a.mode != modeConfirm {
		t.Fatal("rotate did not ask")
	}
	drain(t, a, press(a, "n"))
	if ran := backend.Ran(); len(ran) != 0 {
		t.Fatalf("cancelling ran %v", ran)
	}
	if a.mode != modeBrowse {
		t.Errorf("mode = %v after cancelling", a.mode)
	}
}

// TestEveryFilterIsAJournalctlArgument: the filters are not a view over the
// rows already read, they are arguments, and the command on the status line
// has to prove it.
func TestEveryFilterIsAJournalctlArgument(t *testing.T) {
	a, _ := newTestApp(t)

	pick(t, a, "u", "postgresql.service")
	if !strings.Contains(a.model.Command.String(), "-u postgresql.service") {
		t.Fatalf("command = %q, want the unit in it", a.model.Command)
	}

	pick(t, a, "p", "err")
	command := a.model.Command.String()
	if !strings.Contains(command, "-p 3") {
		t.Fatalf("command = %q, want the priority in it", command)
	}
	for _, entry := range a.model.Entries {
		if !entry.Priority.Severe() {
			t.Fatalf("an entry at %v survived a -p err filter", entry.Priority)
		}
	}

	pick(t, a, "t", "last hour")
	if !strings.Contains(a.model.Command.String(), "--since -1h") {
		t.Fatalf("command = %q, want the window in it", a.model.Command)
	}

	// And clearing puts it all back.
	drain(t, a, press(a, "c"))
	if got := a.model.Command.String(); got !=
		"journalctl --no-pager -o json -n 500" {
		t.Fatalf("after clearing, command = %q", got)
	}
	if len(a.model.Entries) != 300 {
		t.Errorf("after clearing, %d entries", len(a.model.Entries))
	}
}

// TestSearchGoesToJournalctlNotToTheRowsOnScreen is the reason `/` is --grep
// here rather than a client-side filter: the line somebody is looking for is
// almost never in the last five hundred entries.
func TestSearchGoesToJournalctlNotToTheRowsOnScreen(t *testing.T) {
	a, _ := newTestApp(t)
	drain(t, a, press(a, "/"))
	typeInto(t, a, "invalid user")
	if !strings.Contains(a.model.Command.String(), "--grep invalid user") {
		t.Fatalf("command = %q, want --grep in it", a.model.Command)
	}
	if len(a.model.Entries) == 0 {
		t.Fatal("the burst in the sample journal should have matched")
	}
	for _, entry := range a.model.Entries {
		if !strings.Contains(strings.ToLower(entry.Message), "invalid user") {
			t.Fatalf("%q does not match the pattern", entry.Message)
		}
	}
}

func TestBootFilterNarrowsToOneBoot(t *testing.T) {
	a, _ := newTestApp(t)
	pick(t, a, "b", "-1")
	if !strings.Contains(a.model.Command.String(), "-b -1") {
		t.Fatalf("command = %q, want the boot in it", a.model.Command)
	}
	if len(a.model.Entries) == 0 {
		t.Fatal("the previous boot has entries and none came back")
	}
	first := a.model.Entries[0].BootID
	for _, entry := range a.model.Entries {
		if entry.BootID != first {
			t.Fatalf("two boots in one boot's window: %s and %s",
				first, entry.BootID)
		}
	}
}

func TestKernelAndUnitCannotBothApply(t *testing.T) {
	a, _ := newTestApp(t)
	pick(t, a, "u", "sshd.service")
	drain(t, a, press(a, "K"))
	// Turning the kernel filter on drops the unit rather than building a
	// command journalctl would refuse.
	if a.filter.Unit != "" {
		t.Errorf("unit = %q, want it cleared by -k", a.filter.Unit)
	}
	if !strings.Contains(a.model.Command.String(), "-k") {
		t.Errorf("command = %q, want -k in it", a.model.Command)
	}
}

func TestFollowStopsWhenItIsSwitchedOff(t *testing.T) {
	a, _ := newTestApp(t)
	if cmd := press(a, "f"); cmd == nil {
		t.Fatal("f should start the follow loop")
	}
	if !a.following {
		t.Fatal("f did not turn following on")
	}
	seq := a.followSeq

	press(a, "f")
	if a.following {
		t.Fatal("f did not turn following off")
	}
	// A tick left over from the follow that was switched off must not restart
	// it, which is what the sequence number is for.
	if _, cmd := a.Update(tickMsg{seq: seq}); cmd != nil {
		t.Error("a stale tick started another read")
	}
}

func TestEntryDetailShowsTheRecordAndHowToPrintItAgain(t *testing.T) {
	a, _ := newTestApp(t)
	// Move to a postgres failure, which is the entry with source fields on it.
	for i, entry := range a.model.Entries {
		if strings.Contains(entry.Message, "could not open directory") {
			a.cursor[screenEntries] = i
			break
		}
	}
	drain(t, a, press(a, "enter"))
	if a.mode != modeDetail {
		t.Fatal("enter did not open the detail screen")
	}
	frame := strings.Join(a.detailLines(), "\n")
	for _, want := range []string{
		"pid", "executable", "source file", "postgresql.service",
		"journalctl --no-pager -o verbose -n 1 --cursor",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("the detail screen is missing %q", want)
		}
	}
}

func TestDetailNamesAKnownMessageID(t *testing.T) {
	a, _ := newTestApp(t)
	for i, entry := range a.model.Entries {
		if entry.MessageID == "" {
			continue
		}
		a.cursor[screenEntries] = i
		break
	}
	frame := strings.Join(a.detailLines(), "\n")
	if !strings.Contains(frame, "This is a known event") {
		t.Errorf("a catalogued MESSAGE_ID was shown as a hex string only:\n%s",
			frame)
	}
}

func TestExportStagesTheWindowAndPreviewsTheCopy(t *testing.T) {
	a, backend := newTestApp(t)
	drain(t, a, press(a, "x"))
	if a.mode != modeInput {
		t.Fatal("x did not ask where to write")
	}
	if !strings.HasPrefix(a.input.Value(), "/home/") {
		t.Errorf("the suggested path is %q", a.input.Value())
	}
	typeInto(t, a, "/home/you/journal.log")
	drain(t, a, nil)
	if a.mode != modeConfirm {
		t.Fatalf("the export did not reach a confirm dialog (mode %v): %s",
			a.mode, a.status)
	}
	if !strings.Contains(a.confirm.Command,
		"install -m 600 /tmp/tui-logs/") {
		t.Errorf("the dialog shows %q", a.confirm.Command)
	}
	if !strings.Contains(a.confirm.Body, "lines") {
		t.Errorf("the dialog does not say how much is about to be written: %q",
			a.confirm.Body)
	}
	drain(t, a, press(a, "y"))
	if ran := backend.Ran(); len(ran) != 1 || ran[0].Argv[0] != "install" {
		t.Fatalf("the export ran %v", ran)
	}
}

func TestExportRefusesAPathOutsideTheHomeDirectory(t *testing.T) {
	a, backend := newTestApp(t)
	drain(t, a, press(a, "x"))
	typeInto(t, a, "/etc/systemd/journald.conf")
	drain(t, a, nil)
	if a.mode == modeConfirm {
		t.Fatal("writing outside the home directory reached a dialog")
	}
	if len(backend.Ran()) != 0 {
		t.Fatal("and something ran")
	}
	if !strings.Contains(a.status, "outside") {
		t.Errorf("status = %q, want it to say why", a.status)
	}
}

func TestVacuumingMovesTheNumberInTheHeader(t *testing.T) {
	a, _ := newTestApp(t)
	before := a.model.Storage.Bytes
	pick(t, a, "v", "500M")
	drain(t, a, press(a, "y"))
	if a.model.Storage.Bytes >= before {
		t.Errorf("disk usage went from %d to %d", before, a.model.Storage.Bytes)
	}
}

// TestTheHeaderNamesTheJournalOnScreen: the subtitle carries the backend's own
// description, so a demo says it is one. The lab found this: --demo rendered a
// frame indistinguishable from a real machine's, because the subtitle held the
// filter alone and nothing on screen said the entries were invented.
func TestTheHeaderNamesTheJournalOnScreen(t *testing.T) {
	a, _ := newTestApp(t)
	a.width, a.height = 120, 40
	header := a.headerView()
	if !strings.Contains(header, "demo") {
		t.Errorf("the header of a demo run does not say so: %q", header)
	}
	// And the filter it replaced is still there.
	if !strings.Contains(header, a.filter.Label()) {
		t.Errorf("the header dropped the filter %q: %q", a.filter.Label(), header)
	}
}

func TestStatsCountTheWindowAndSayWhichWindow(t *testing.T) {
	a, _ := newTestApp(t)
	drain(t, a, press(a, "2"))
	if a.screen != screenStats {
		t.Fatal("2 is the stats screen")
	}
	frame := a.View()
	for _, want := range []string{"window", "errors", "noisiest", "by boot"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the stats screen is missing %q", want)
		}
	}
}

func TestStorageScreenSaysWhetherTheJournalSurvivesAReboot(t *testing.T) {
	a, _ := newTestApp(t)
	drain(t, a, press(a, "3"))
	frame := strings.Join(a.rowsDetail("", a.storageRows()), "\n")
	for _, want := range []string{"disk usage", "persistent", "journald.conf",
		"SystemMaxUse", "compiled-in default"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the storage screen is missing %q", want)
		}
	}
}

// TestRendersAtEveryWidth is the responsive contract: from a narrow pane to a
// wide screen, no frame may wrap, because a wrapped row desynchronises Bubble
// Tea's line accounting and every frame after it lands in the wrong place.
func TestRendersAtEveryWidth(t *testing.T) {
	for width := 40; width <= 200; width += 4 {
		a, _ := newTestApp(t)
		a.width, a.height = width, 24
		a.clampCursor()

		for s := screen(0); s < screenCount; s++ {
			a.screen = s
			for _, m := range []mode{modeBrowse, modeDetail} {
				a.mode = m
				checkWidth(t, a, s.title(), width)
			}
		}

		a.mode = modeHelp
		checkWidth(t, a, "help", width)

		a.screen, a.mode = screenEntries, modeBrowse
		drain(t, a, press(a, "/"))
		checkWidth(t, a, "search", width)

		a.mode = modeBrowse
		drain(t, a, press(a, "u"))
		checkWidth(t, a, "unit picker", width)

		a.mode = modeBrowse
		drain(t, a, press(a, "o"))
		checkWidth(t, a, "confirm", width)
	}
}

// checkWidth renders the current frame and fails when a line overflows.
func checkWidth(t *testing.T, a *app, name string, width int) {
	t.Helper()
	for i, line := range strings.Split(a.View(), "\n") {
		if got := lineWidth(line); got > width {
			t.Fatalf("%s at %d cols: line %d is %d cells wide",
				name, width, i, got)
		}
	}
}

// lineWidth measures a rendered line, ignoring the ANSI escapes the theme
// adds.
func lineWidth(line string) int {
	width, inEscape := 0, false
	for _, r := range line {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape && (r == 'm' || r == 'K' || r == 'H'):
			inEscape = false
		case inEscape:
		default:
			width++
		}
	}
	return width
}

func TestBusyStateSwallowsInput(t *testing.T) {
	a, backend := newTestApp(t)
	a.busy = true
	press(a, "o")
	if a.mode == modeConfirm {
		t.Error("a key pressed while a command runs opened a dialog")
	}
	if len(backend.Ran()) != 0 {
		t.Error("and it ran something")
	}
}

// TestHelpCoversEveryActionKey: the help screen is generated from one table,
// and a key that does something without appearing in it is a key nobody will
// find.
func TestHelpCoversEveryActionKey(t *testing.T) {
	listed := ""
	for _, hint := range helpKeys() {
		listed += hint.Key + " "
	}
	for _, key := range []string{"f", "u", "p", "b", "t", "/", "K", "U", "c",
		"x", "v", "o", "y", "R", "q"} {
		if !strings.Contains(listed, key) {
			t.Errorf("the help screen does not mention %q", key)
		}
	}
}

// TestUnknownPriorityIsNotAnEmergency: an entry with no PRIORITY must not be
// coloured as level 0.
func TestUnknownPriorityIsNotAnEmergency(t *testing.T) {
	if logs.PriorityAny.Severe() {
		t.Fatal("an unknown priority reads as an emergency")
	}
	if got := priorityLabel(logs.PriorityAny); got != "—" {
		t.Errorf("priorityLabel(any) = %q", got)
	}
}

// TestTheUnitPickerIsShortAndReachesAnyUnit is the fix for a picker that was
// unusable on a real machine: it offered every unit systemd knows — 327 of
// them on the machine this was found on — with no filter and no type-ahead,
// so finding sshd.service meant holding an arrow key down.
//
// It now offers the units that have written to the journal, which is the only
// set filtering by unit can return anything for, and its first entry types a
// name for everything else.
func TestTheUnitPickerIsShortAndReachesAnyUnit(t *testing.T) {
	a, _ := newTestApp(t)
	drain(t, a, press(a, "u"))
	if a.mode != modePicker {
		t.Fatalf("u did not open a picker (mode %v)", a.mode)
	}
	if len(a.picker.Options) == 0 {
		t.Fatal("the picker offers nothing")
	}
	// The way out of the list comes first, so it is reachable without moving.
	if a.picker.Options[0] != typeUnitChoice {
		t.Errorf("the first entry is %q, not the one that types a name",
			a.picker.Options[0])
	}
	// The title says how long the list is and which question it answers, so a
	// short list does not read as a list with something missing from it.
	if !strings.Contains(a.picker.Title, "with journal entries") {
		t.Errorf("the picker title does not say what the list is: %q",
			a.picker.Title)
	}
	if !strings.Contains(a.picker.Title, fmt.Sprint(len(a.model.Units))) {
		t.Errorf("the picker title does not carry the count: %q", a.picker.Title)
	}

	// Typing a name filters on a unit that is not on the list at all.
	drain(t, a, press(a, "esc"))
	pick(t, a, "u", typeUnitChoice)
	if a.mode != modeInput {
		t.Fatalf("the first entry did not open a prompt (mode %v)", a.mode)
	}
	typeInto(t, a, "chronyd.service")
	if a.filter.Unit != "chronyd.service" {
		t.Errorf("the typed unit did not reach the filter: %q", a.filter.Unit)
	}
	if !strings.Contains(a.model.Command.String(), "-u chronyd.service") {
		t.Errorf("the read did not ask for the unit: %s", a.model.Command.String())
	}

	// And an empty answer takes the filter off again.
	pick(t, a, "u", typeUnitChoice)
	typeInto(t, a, "  ")
	if a.filter.Unit != "" {
		t.Errorf("an empty answer left the filter on %q", a.filter.Unit)
	}
}

// TestUnitPickerTitleSaysWhichListItIs covers the fallback: a journal that
// could not be asked leaves the long systemctl list, and the title has to say
// so rather than claiming these units have entries.
func TestUnitPickerTitleSaysWhichListItIs(t *testing.T) {
	cases := []struct {
		count  int
		source string
		want   string
	}{
		{count: 6, source: logs.UnitsFromJournal, want: "Unit (6 with journal entries)"},
		{count: 327, source: logs.UnitsFromSystemd, want: "Unit (327 known to systemd)"},
		{count: 0, source: "", want: "Unit"},
	}
	for _, tc := range cases {
		if got := unitPickerTitle(tc.count, tc.source); got != tc.want {
			t.Errorf("unitPickerTitle(%d, %q) = %q, want %q",
				tc.count, tc.source, got, tc.want)
		}
	}
}
