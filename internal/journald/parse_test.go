package journald

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tui-tools/tui-logs/internal/logs"
)

// fixture reads one captured output. Everything under testdata came off a
// real Fedora 42 host except journal-json-shapes.txt, which is written by
// hand: a journal that happens to carry a non-UTF-8 message or a repeated
// field is not something a capture can be relied on to contain, and both
// shapes are ones the parser has to survive.
func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

func TestParseEntriesReadsACapturedJournal(t *testing.T) {
	entries := ParseEntries(fixture(t, "journal-json.txt"))
	if len(entries) != 7 {
		t.Fatalf("parsed %d entries, want 7", len(entries))
	}

	// journalctl prints oldest first and the screen shows newest first, so
	// the order must have been reversed.
	if !entries[0].Realtime.After(entries[len(entries)-1].Realtime) {
		t.Errorf("entries are not newest first: %s then %s",
			entries[0].Time(), entries[len(entries)-1].Time())
	}

	var kernel, logind *logs.Entry
	for i := range entries {
		switch {
		case entries[i].Transport == "kernel" && kernel == nil:
			kernel = &entries[i]
		case entries[i].Unit == "systemd-logind.service" && logind == nil:
			logind = &entries[i]
		}
	}
	if kernel == nil || logind == nil {
		t.Fatalf("the fixture no longer carries both a kernel and a unit entry")
	}

	// A kernel line has no unit at all, which is exactly why the table falls
	// back to the syslog identifier for its source column.
	if kernel.Unit != "" {
		t.Errorf("kernel entry unit = %q, want empty", kernel.Unit)
	}
	if kernel.Source() != "kernel" {
		t.Errorf("kernel entry source = %q", kernel.Source())
	}
	if kernel.Cursor == "" {
		t.Error("a kernel entry with no cursor cannot be addressed again")
	}

	if logind.Priority != logs.PriInfo {
		t.Errorf("logind priority = %v, want info", logind.Priority)
	}
	if logind.PID == 0 || logind.CodeFile == "" || logind.CodeLine == 0 {
		t.Errorf("logind entry lost its process or source fields: %+v", logind)
	}
	if _, known := MessageName(logind.MessageID); !known {
		t.Errorf("MESSAGE_ID %q is not in the catalogue", logind.MessageID)
	}
	// Every field the record carried is kept, because the journal has no
	// schema and the field that explains an entry is often one nobody named.
	if len(logind.Fields) < 15 {
		t.Errorf("only %d fields survived the parse", len(logind.Fields))
	}
}

func TestParseEntriesHandlesEveryFieldShape(t *testing.T) {
	entries := ParseEntries(fixture(t, "journal-json-shapes.txt"))
	if len(entries) != 6 {
		t.Fatalf("parsed %d entries, want 6 (the line that is not JSON is skipped)",
			len(entries))
	}

	by := map[string]logs.Entry{}
	for _, entry := range entries {
		by[entry.Identifier] = entry
	}

	// A message whose bytes are not valid UTF-8 arrives as an array of
	// numbers, and has to come back as the text it stands for — with the
	// control characters replaced, because an escape written straight into a
	// terminal is a way to repaint the screen from a log line.
	binary := by["binary"].Message
	if !strings.HasPrefix(binary, "hi there") {
		t.Errorf("binary message = %q, want the bytes it stands for", binary)
	}
	if strings.ContainsRune(binary, 0x07) {
		t.Errorf("binary message kept a control character: %q", binary)
	}

	// A field that repeated in the record arrives as an array of strings.
	if got := by["repeated"].Fields["ENV"]; got != "one two" {
		t.Errorf("repeated field = %q, want both values", got)
	}

	// An entry with no PRIORITY is not level 0 — emerg — it is unknown, and
	// colouring it red would be a lie about the machine.
	if got := by["nopriority"].Priority; got != logs.PriorityAny {
		t.Errorf("missing PRIORITY parsed as %v, want unknown", got)
	}

	// On the system journal a user service logs under user@UID.service, and
	// the unit worth showing is the one in _SYSTEMD_USER_UNIT.
	if got := by["app"].Unit; got != "app.service" {
		t.Errorf("user unit = %q, want app.service", got)
	}
}

// TestTheBootListShapeIsTheSameOnEveryTestedSystemd pins the two ends of the
// range the lab runs. `--list-boots -o json` was the one read in this tool
// gated on a systemd version, so the shape it answers with on the oldest and
// newest systemd anyone has run this against is worth having on disk rather
// than assumed: both fixtures were captured from a real guest, systemd 255 on
// Ubuntu 24.04 and 261 on Omarchy Server 4.0.1, and they are byte-identical in
// structure — four keys, same names, same types. If a future release adds or
// renames one, this is where it surfaces.
func TestTheBootListShapeIsTheSameOnEveryTestedSystemd(t *testing.T) {
	for _, name := range []string{
		"list-boots-json-systemd255.txt",
		"list-boots-json-systemd261.txt",
	} {
		boots := ParseBootsJSON(fixture(t, name))
		if len(boots) == 0 {
			t.Fatalf("%s parsed to no boots at all", name)
		}
		// The running boot is index 0 and sorts first, which is what the
		// picker opens on.
		if boots[0].Index != 0 {
			t.Errorf("%s: first boot index = %d, want 0", name, boots[0].Index)
		}
		for i, boot := range boots {
			if len(boot.ID) != 32 {
				t.Errorf("%s: boot %d has id %q, want 32 hex characters",
					name, i, boot.ID)
			}
			if boot.First.IsZero() || boot.Last.IsZero() {
				t.Errorf("%s: boot %d lost a timestamp (%v..%v)",
					name, i, boot.First, boot.Last)
			}
			if boot.Last.Before(boot.First) {
				t.Errorf("%s: boot %d ends before it starts", name, i)
			}
		}
	}
}

func TestBootListsAgreeWhicheverFormatWasRead(t *testing.T) {
	fromJSON := ParseBootsJSON(fixture(t, "list-boots-json.txt"))
	fromTable := ParseBootsTable(fixture(t, "list-boots-table.txt"))

	if len(fromJSON) == 0 || len(fromJSON) != len(fromTable) {
		t.Fatalf("json has %d boots, the table %d", len(fromJSON), len(fromTable))
	}
	// The running boot is first, which is what the picker opens on.
	if fromJSON[0].Index != 0 {
		t.Errorf("first boot index = %d, want 0", fromJSON[0].Index)
	}
	for i := range fromJSON {
		if fromJSON[i].ID != fromTable[i].ID ||
			fromJSON[i].Index != fromTable[i].Index {
			t.Errorf("boot %d differs: json %+v, table %+v",
				i, fromJSON[i], fromTable[i])
		}
		// The table's timestamps carry a zone offset and a day name, and are
		// only useful if they parsed.
		if fromTable[i].First.IsZero() {
			t.Errorf("boot %d lost its first-entry timestamp", i)
		}
		if delta := fromJSON[i].First.Sub(fromTable[i].First); delta > 0 &&
			delta.Seconds() > 1 {
			t.Errorf("boot %d starts at %s in json and %s in the table",
				i, fromJSON[i].First, fromTable[i].First)
		}
	}
}

func TestParseBootsTableSkipsTheHeader(t *testing.T) {
	boots := ParseBootsTable("IDX BOOT ID FIRST ENTRY LAST ENTRY\n" +
		" 0 b76f7229a3e74d4d9e1c2453b87131c9 Wed 2026-08-19 09:39:58 -03 " +
		"Sun 2026-08-30 00:22:04 -03\n")
	if len(boots) != 1 || boots[0].Index != 0 {
		t.Fatalf("boots = %+v", boots)
	}
}

func TestParseUnitsTakesOnlyTheName(t *testing.T) {
	units := ParseUnits(fixture(t, "list-units.txt"))
	if len(units) == 0 {
		t.Fatal("no units were parsed")
	}
	seen := map[string]bool{}
	for _, unit := range units {
		if strings.ContainsAny(unit, " \t") {
			t.Errorf("unit %q carries more than the name", unit)
		}
		if seen[unit] {
			t.Errorf("unit %q appears twice", unit)
		}
		seen[unit] = true
	}
	// The list is sorted, so the picker does not shuffle between reads.
	for i := 1; i < len(units); i++ {
		if units[i-1] > units[i] {
			t.Fatalf("units are not sorted: %q before %q", units[i-1], units[i])
		}
	}
	// A unit systemd could not load is still a unit somebody may want to
	// filter on, so it is kept.
	if !seen["boot.automount"] {
		t.Error("a not-found unit was dropped")
	}
}

func TestParseDiskUsage(t *testing.T) {
	text, bytes := ParseDiskUsage(fixture(t, "disk-usage.txt"))
	if !strings.Contains(text, "journals take up") {
		t.Errorf("text = %q", text)
	}
	if bytes <= 0 {
		t.Fatalf("bytes = %d, want the size the sentence named", bytes)
	}
	// systemd's own formatter is binary whatever the letter suggests, so a
	// round trip has to land on the same string.
	if got := FormatSize(bytes); !strings.Contains(text, got) {
		t.Errorf("FormatSize(%d) = %q, which is not in %q", bytes, got, text)
	}
}

func TestParseDiskUsageOnNonsense(t *testing.T) {
	text, bytes := ParseDiskUsage("Failed to determine disk usage: Permission denied")
	if bytes != 0 {
		t.Errorf("bytes = %d, want 0 when nothing was a size", bytes)
	}
	if text == "" {
		t.Error("the sentence is still worth showing, whatever it says")
	}
}

func TestParseCatConfigTellsDefaultsFromSettings(t *testing.T) {
	settings := ParseCatConfig(fixture(t, "cat-config-journald.txt"))
	if len(settings) == 0 {
		t.Fatal("no settings were parsed")
	}
	by := map[string]logs.ConfSetting{}
	for _, setting := range settings {
		by[setting.Key] = setting
	}

	storage, ok := by["Storage"]
	if !ok {
		t.Fatal("Storage is missing from the effective configuration")
	}
	// The vendor file ships every setting commented out, which is how
	// cat-config shows a compiled-in default. Reporting those as somebody's
	// choice would send a reader looking for a file that does not exist.
	if !storage.Default {
		t.Errorf("Storage = %+v, want it marked as the compiled-in default", storage)
	}
	if storage.Value != "auto" {
		t.Errorf("Storage value = %q, want auto", storage.Value)
	}
	if !strings.HasPrefix(storage.File, "/") || storage.Line <= 0 {
		t.Errorf("Storage has no source: %+v", storage)
	}
}

func TestParseCatConfigLetsALaterFileWin(t *testing.T) {
	// journald's rule is the opposite of sshd's: the last value read is the
	// one in force, which is why a drop-in beats the vendor file.
	settings := ParseCatConfig(`# /usr/lib/systemd/journald.conf
[Journal]
#SystemMaxUse=
#Compress=yes

# /etc/systemd/journald.conf.d/50-size.conf
[Journal]
SystemMaxUse=4G
`)
	by := map[string]logs.ConfSetting{}
	for _, setting := range settings {
		by[setting.Key] = setting
	}
	if got := by["SystemMaxUse"]; got.Value != "4G" || got.Default ||
		got.File != "/etc/systemd/journald.conf.d/50-size.conf" {
		t.Errorf("SystemMaxUse = %+v, want the drop-in's value", got)
	}
	// And a commented line in a later file must not overwrite a real one.
	if got := by["Compress"]; !got.Default || got.Value != "yes" {
		t.Errorf("Compress = %+v, want the default", got)
	}
}

func TestMessageNameIsHonestAboutWhatItDoesNotKnow(t *testing.T) {
	if _, ok := MessageName("00000000000000000000000000000000"); ok {
		t.Error("an unknown MESSAGE_ID must not get a name invented for it")
	}
	name, ok := MessageName("BE02CF6855D2428BA40DF7E9D022F03D")
	if !ok || !strings.Contains(name, "failed") {
		t.Errorf("MessageName = %q, %v; the lookup must ignore case", name, ok)
	}
}

func TestExtraFieldsHidesTheJournalsOwnAddresses(t *testing.T) {
	extra := ExtraFields(map[string]string{
		"MESSAGE": "x", "__CURSOR": "y", "__REALTIME_TIMESTAMP": "1",
		"_PID": "3", "SESSION_ID": "25", "LEADER": "900",
	})
	for _, field := range extra {
		if strings.HasPrefix(field, "__") || field == "MESSAGE" || field == "_PID" {
			t.Errorf("%q should not be repeated at the bottom of the detail screen",
				field)
		}
	}
	if len(extra) != 2 {
		t.Errorf("extra = %v, want the two fields nothing else showed", extra)
	}
}

func TestComputeStatsCountsTheWindow(t *testing.T) {
	entries := ParseEntries(fixture(t, "journal-json.txt"))
	stats := logs.ComputeStats(entries, 500)
	if stats.Total != len(entries) {
		t.Errorf("total = %d, want %d", stats.Total, len(entries))
	}
	// The fixture carries three `err` lines and two warnings.
	if stats.Errors != 3 {
		t.Errorf("errors = %d, want 3", stats.Errors)
	}
	if stats.Warnings != 2 {
		t.Errorf("warnings = %d, want 2", stats.Warnings)
	}
	if len(stats.TopUnits) == 0 {
		t.Error("no top units were counted")
	}
	if stats.Truncated {
		t.Error("a seven-entry window is not the read limit")
	}
}
