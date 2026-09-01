package journald

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tui-tools/tui-logs/internal/logs"
)

// The journal is a schema-less store: a record holds whatever the program
// that wrote it chose to attach, and `journalctl -o json` hands it over
// verbatim. What comes out of these parsers is drawn straight into a terminal
// and, for a unit name, put back on a command line — so a field that keeps a
// control character, or a name that keeps a space, is a defect the fixtures
// would never show.
//
// `go test` replays every seed below on each commit; `go test -fuzz` explores
// past them locally — see tui-kit/templates/FUZZING.md for the family rule.

// seed starts the corpus from the captured fixtures the table tests use, plus
// the shapes a real capture never has: nothing, a lone brace, a truncated
// record, a header with no body.
func seed(f *testing.F, names ...string) {
	f.Helper()
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // the name is a literal in the tests, and testdata is in the repository
		if err != nil {
			f.Fatalf("read fixture %s: %v", name, err)
		}
		f.Add(string(raw))
	}
	f.Add("")
	f.Add("\n\n\n")
	f.Add("{")
	f.Add("[]")
	f.Add("#")
}

// printable asserts what every screen of this tool assumes about a value it
// draws: it is one line of text with nothing in it the terminal would read as
// an instruction.
func printable(t *testing.T, what, text string) {
	t.Helper()
	for _, r := range text {
		if r == 0x7f || (r < 0x20 && r != '\t') {
			t.Fatalf("%s carries the control character %#U: %q", what, r, text)
		}
	}
}

func FuzzParseEntries(f *testing.F) {
	seed(f, "journal-json.txt", "journal-json-shapes.txt")
	f.Fuzz(func(t *testing.T, out string) {
		for _, e := range ParseEntries(out) {
			if e.Priority != logs.PriorityAny && (e.Priority < 0 || e.Priority > 7) {
				t.Fatalf("priority = %d, not a syslog level", e.Priority)
			}
			if e.Fields == nil {
				t.Fatal("entry kept no fields")
			}
			// Everything the table and the detail view print.
			printable(t, "message", e.Message)
			printable(t, "unit", e.Unit)
			printable(t, "identifier", e.Identifier)
			printable(t, "comm", e.Comm)
			printable(t, "exe", e.Exe)
			printable(t, "cmdline", e.Cmdline)
			printable(t, "hostname", e.Hostname)
			printable(t, "transport", e.Transport)
			for key, value := range e.Fields {
				printable(t, "field "+key, value)
			}
			if e.MessageID != strings.ToLower(e.MessageID) {
				t.Fatalf("message id is not lowercased: %q", e.MessageID)
			}
		}
	})
}

func FuzzParseBootsJSON(f *testing.F) {
	seed(f, "list-boots-json.txt", "list-boots-json-systemd255.txt",
		"list-boots-json-systemd261.txt")
	f.Fuzz(func(t *testing.T, out string) {
		checkBoots(t, ParseBootsJSON(out))
	})
}

func FuzzParseBootsTable(f *testing.F) {
	seed(f, "list-boots-table.txt")
	f.Fuzz(func(t *testing.T, out string) {
		boots := ParseBootsTable(out)
		checkBoots(t, boots)
		// The table parser only accepts a 32-character id, because that id is
		// what `journalctl --boot=<id>` is built with.
		for _, b := range boots {
			if len(b.ID) != 32 {
				t.Fatalf("boot id is %d characters: %q", len(b.ID), b.ID)
			}
		}
	})
}

// checkBoots asserts what the boot picker assumes: an id it can hand back to
// journalctl, a label it can draw, and the running boot first.
func checkBoots(t *testing.T, boots []logs.Boot) {
	t.Helper()
	for i, b := range boots {
		if strings.ContainsAny(b.ID, " \t\n\r") {
			t.Fatalf("boot id is not a bare token: %q", b.ID)
		}
		printable(t, "boot label", b.Label())
		if i > 0 && boots[i-1].Index < b.Index {
			t.Fatalf("boot %d (index %d) sorts after index %d",
				i, b.Index, boots[i-1].Index)
		}
	}
}

func FuzzParseUnits(f *testing.F) {
	seed(f, "list-units.txt")
	f.Fuzz(func(t *testing.T, out string) {
		units := ParseUnits(out)
		seen := map[string]bool{}
		for i, name := range units {
			// A unit name goes back onto the command line as `-u <name>`, so
			// the parser accepts only what a unit name may contain.
			if !unitRe.MatchString(name) {
				t.Fatalf("unit %q is not a name journalctl would take", name)
			}
			if seen[name] {
				t.Fatalf("unit %q listed twice", name)
			}
			seen[name] = true
			if i > 0 && units[i-1] > name {
				t.Fatalf("units are not sorted: %q before %q", units[i-1], name)
			}
		}
	})
}

func FuzzParseDiskUsage(f *testing.F) {
	seed(f, "disk-usage.txt")
	f.Fuzz(func(t *testing.T, out string) {
		text, bytes := ParseDiskUsage(out)
		// The sentence is printed as it came, on one line.
		printable(t, "the sentence", text)
		if text != strings.TrimSpace(text) {
			t.Fatalf("the sentence is not trimmed: %q", text)
		}
		if bytes < 0 {
			t.Fatalf("size = %d bytes, want non-negative", bytes)
		}
		if text == "" && bytes != 0 {
			t.Fatalf("no sentence but %d bytes", bytes)
		}
		// FormatSize is what the housekeeping screen prints its own
		// arithmetic with, so it has to render what this read back.
		printable(t, "formatted size", FormatSize(bytes))
	})
}

func FuzzParseCatConfig(f *testing.F) {
	seed(f, "cat-config-journald.txt")
	f.Fuzz(func(t *testing.T, out string) {
		settings := ParseCatConfig(out)
		seen := map[string]bool{}
		for _, s := range settings {
			// One row per key: the last file to set it wins, and a screen
			// showing the same key twice would be showing a value that is
			// not in force.
			if s.Key == "" || strings.ContainsAny(s.Key, " \t") {
				t.Fatalf("key is not a bare word: %q", s.Key)
			}
			if seen[s.Key] {
				t.Fatalf("key %q reported twice", s.Key)
			}
			seen[s.Key] = true
			printable(t, "key", s.Key)
			printable(t, "value", s.Value)
			if s.File != "" && !strings.HasPrefix(s.File, "/") {
				t.Fatalf("file %q is not an absolute path", s.File)
			}
			if s.Line < 0 {
				t.Fatalf("line = %d, want non-negative", s.Line)
			}
		}
	})
}

func FuzzParsePriority(f *testing.F) {
	for _, s := range []string{"", "any", "err", "3", "8", "-1", "WARNING", " notice "} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		p, ok := logs.ParsePriority(text)
		if !ok && p != logs.PriorityAny {
			t.Fatalf("rejected %q but returned the level %d", text, p)
		}
		if ok && p != logs.PriorityAny && (p < 0 || p > 7) {
			t.Fatalf("accepted %q as the level %d", text, p)
		}
	})
}

// FuzzParseDropIn guards the read the retention form seeds itself from.
//
// A value it returns is offered back as the current setting and, if the user
// leaves it alone, is compared against what the form would write — and a
// value that survives the round trip has to be one RenderDropIn will accept.
// A drop-in someone edited by hand is exactly the input this sees.
func FuzzParseDropIn(f *testing.F) {
	f.Add("[Journal]\nSystemMaxUse=2G\nMaxRetentionSec=1month\nStorage=persistent\n")
	f.Add("SystemMaxUse = 4G")
	f.Add("[Journal]\n#SystemMaxUse=2G\n")
	f.Add("Storage=none\n")
	// The regression FuzzParseDropIn found: a value carrying a NUL used to be
	// kept, and it seeds a prompt the terminal then draws.
	f.Add("MaxRetentionSec=\x00")
	f.Add("=\n[\n]\n")
	f.Add("")
	f.Fuzz(func(t *testing.T, text string) {
		values := ParseDropIn(text)
		for key, value := range values {
			setting, ok := RetentionSettingFor(key)
			if !ok {
				t.Fatalf("a key outside the form came back: %q", key)
			}
			if strings.TrimSpace(key) != key || strings.TrimSpace(value) != value {
				t.Fatalf("%q=%q kept its padding", key, value)
			}
			printable(t, "setting value", value)
			if !strings.Contains(text, value) {
				t.Fatalf("value %q is not in the input", value)
			}
			// The round trip is the real assertion: whatever was read, only a
			// value the validator accepts may be written back out. An empty
			// one is accepted and means "leave this key out", so it has no
			// round trip to make.
			if value == "" || ValidateRetentionValue(setting, value) != nil {
				continue
			}
			out, err := RenderDropIn(map[string]string{key: value})
			if err != nil {
				t.Fatalf("a validated %s=%q would not render: %v", key, value, err)
			}
			if got := ParseDropIn(out)[key]; got != value {
				t.Fatalf("%s round-tripped as %q, want %q", key, got, value)
			}
		}
	})
}
