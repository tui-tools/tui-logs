package journald

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tui-tools/tui-logs/internal/logs"
)

// TestBuildReadIsExactlyTheseArguments is the tool's central claim as a test:
// what the status line shows is an argv, in a fixed order, with nothing
// implicit in it. A change to the order is a change to what the user is shown,
// so it is asserted element by element rather than by substring.
func TestBuildReadIsExactlyTheseArguments(t *testing.T) {
	tests := []struct {
		name   string
		filter logs.Filter
		want   []string
	}{
		{
			name:   "no filter at all",
			filter: logs.Filter{Priority: logs.PriorityAny},
			want:   []string{"journalctl", "--no-pager", "-o", "json", "-n", "500"},
		},
		{
			name: "a unit and a level",
			filter: logs.Filter{Unit: "sshd.service", Priority: logs.PriErr,
				Lines: 200},
			want: []string{"journalctl", "--no-pager", "-o", "json",
				"-u", "sshd.service", "-p", "3", "-n", "200"},
		},
		{
			name: "a boot and a window",
			filter: logs.Filter{Priority: logs.PriorityAny, Boot: "-1",
				Since: "-1h", Until: "today", Lines: 500},
			want: []string{"journalctl", "--no-pager", "-o", "json",
				"-b", "-1", "--since", "-1h", "--until", "today", "-n", "500"},
		},
		{
			name: "the kernel, searched",
			filter: logs.Filter{Priority: logs.PriorityAny, Kernel: true,
				Grep: "oom", Lines: 500},
			want: []string{"journalctl", "--no-pager", "-o", "json", "-k",
				"--grep", "oom", "-n", "500"},
		},
		{
			name:   "the user journal",
			filter: logs.Filter{Priority: logs.PriorityAny, User: true, Lines: 50},
			want: []string{"journalctl", "--no-pager", "-o", "json", "--user",
				"-n", "50"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd, err := BuildRead(test.filter)
			if err != nil {
				t.Fatalf("BuildRead: %v", err)
			}
			assertArgv(t, cmd.Argv, test.want)
			if cmd.Destructive {
				t.Error("a read is not destructive")
			}
		})
	}
}

// assertArgv compares two argvs element by element.
func assertArgv(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argv = %v\n want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q\n  got  %v\n  want %v",
				i, got[i], want[i], got, want)
		}
	}
}

func TestBuildExportReadDiffersOnlyInTheFormat(t *testing.T) {
	filter := logs.Filter{Unit: "nginx.service", Priority: logs.PriorityAny,
		Lines: 500}
	read, err := BuildRead(filter)
	if err != nil {
		t.Fatalf("BuildRead: %v", err)
	}
	exported, err := BuildExportRead(filter)
	if err != nil {
		t.Fatalf("BuildExportRead: %v", err)
	}
	want := append([]string{}, read.Argv...)
	want[3] = "short-iso"
	assertArgv(t, exported.Argv, want)
}

func TestBuildFollowAsksOnlyForWhatIsNew(t *testing.T) {
	cursor := "s=abc;i=1;b=def;m=2;t=3"
	cmd, err := BuildFollow(logs.Filter{Priority: logs.PriorityAny, Lines: 500},
		cursor)
	if err != nil {
		t.Fatalf("BuildFollow: %v", err)
	}
	assertArgv(t, cmd.Argv, []string{"journalctl", "--no-pager", "-o", "json",
		"-n", "500", "--after-cursor", cursor})
	// -f never exits, and a command that never exits cannot be the value the
	// preview showed and the runner ran.
	for _, arg := range cmd.Argv {
		if arg == "-f" || arg == "--follow" {
			t.Fatalf("follow must not block: %v", cmd.Argv)
		}
	}
}

func TestBuildFollowRefusesSomethingThatIsNotACursor(t *testing.T) {
	if _, err := BuildFollow(logs.Filter{Priority: logs.PriorityAny},
		"; rm -rf /"); err == nil {
		t.Fatal("a value that is not a cursor must be refused")
	}
}

func TestBuildEntryAddressesOneRecord(t *testing.T) {
	entry := logs.Entry{Cursor: "s=abc;i=9;b=def;m=2;t=3"}
	cmd, err := BuildEntry(entry)
	if err != nil {
		t.Fatalf("BuildEntry: %v", err)
	}
	assertArgv(t, cmd.Argv, []string{"journalctl", "--no-pager", "-o", "verbose",
		"-n", "1", "--cursor", entry.Cursor})

	if _, err := BuildEntry(logs.Entry{}); err == nil {
		t.Fatal("an entry with no cursor cannot be addressed, and must say so")
	}
}

func TestBuildListBootsFallsBackToTheTable(t *testing.T) {
	assertArgv(t, BuildListBoots(true).Argv,
		[]string{"journalctl", "--no-pager", "--list-boots", "-o", "json"})
	assertArgv(t, BuildListBoots(false).Argv,
		[]string{"journalctl", "--no-pager", "--list-boots"})
}

func TestBuildListUnitsAsksSystemdNotTheEntries(t *testing.T) {
	assertArgv(t, BuildListUnits().Argv, []string{"systemctl", "list-units",
		"--all", "--plain", "--no-legend", "--no-pager"})
}

func TestBuildHousekeeping(t *testing.T) {
	size, err := BuildVacuumSize("500M")
	if err != nil {
		t.Fatalf("BuildVacuumSize: %v", err)
	}
	assertArgv(t, size.Argv, []string{"journalctl", "--vacuum-size=500M"})
	if !size.Destructive {
		t.Error("vacuuming deletes journal files and must be marked destructive")
	}

	age, err := BuildVacuumTime("30d")
	if err != nil {
		t.Fatalf("BuildVacuumTime: %v", err)
	}
	assertArgv(t, age.Argv, []string{"journalctl", "--vacuum-time=30d"})

	rotate, err := BuildRotate()
	if err != nil {
		t.Fatalf("BuildRotate: %v", err)
	}
	assertArgv(t, rotate.Argv, []string{"journalctl", "--rotate"})
	// Rotating deletes nothing: it renames the active files. Painting it in
	// the danger colour would teach a reader to ignore that colour.
	if rotate.Destructive {
		t.Error("rotate is not destructive")
	}

	verify, err := BuildVerify()
	if err != nil {
		t.Fatalf("BuildVerify: %v", err)
	}
	assertArgv(t, verify.Argv, []string{"journalctl", "--verify"})
	if verify.Destructive {
		t.Error("verify is a read")
	}
}

func TestVacuumArgumentsAreValidated(t *testing.T) {
	for _, bad := range []string{"", "lots", "500 M", "-1G", "500M; rm -rf /"} {
		if _, err := BuildVacuumSize(bad); err == nil {
			t.Errorf("BuildVacuumSize(%q) was accepted", bad)
		}
	}
	for _, bad := range []string{"", "soon", "30 days", "30x"} {
		if _, err := BuildVacuumTime(bad); err == nil {
			t.Errorf("BuildVacuumTime(%q) was accepted", bad)
		}
	}
}

func TestCheckFilterRefusesWhatJournalctlWould(t *testing.T) {
	tests := []struct {
		name   string
		filter logs.Filter
	}{
		{"a unit with a space", logs.Filter{Unit: "not a unit",
			Priority: logs.PriorityAny}},
		{"a priority that is not a level", logs.Filter{Priority: logs.Priority(9)}},
		{"a boot that is neither an offset nor an id",
			logs.Filter{Priority: logs.PriorityAny, Boot: "last-tuesday"}},
		{"a since journalctl would reject",
			logs.Filter{Priority: logs.PriorityAny, Since: "a while ago"}},
		{"a pattern with a newline in it",
			logs.Filter{Priority: logs.PriorityAny, Grep: "one\ntwo"}},
		{"a read larger than the tool will make",
			logs.Filter{Priority: logs.PriorityAny, Lines: MaxLines + 1}},
		{"the kernel and a unit at once",
			logs.Filter{Priority: logs.PriorityAny, Kernel: true, Unit: "sshd.service"}},
		{"the kernel in a user journal",
			logs.Filter{Priority: logs.PriorityAny, Kernel: true, User: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := CheckFilter(test.filter); err == nil {
				t.Fatalf("%+v was accepted", test.filter)
			}
			if _, err := BuildRead(test.filter); err == nil {
				t.Fatal("and BuildRead built a command out of it anyway")
			}
		})
	}
}

func TestCheckFilterAcceptsWhatThePresetsProduce(t *testing.T) {
	for _, preset := range logs.Presets() {
		filter := logs.Filter{Priority: logs.PriorityAny, Since: preset.Since,
			Boot: preset.Boot}
		if err := CheckFilter(filter); err != nil {
			t.Errorf("the %q preset produces a filter the checker refuses: %v",
				preset.Label, err)
		}
	}
	// The unit names systemd itself prints include escaped device paths, and
	// a picker that offered one the checker then refused would be a trap.
	for _, unit := range []string{
		"sshd.service", "user@1000.service", "dev-disk-by\\x2ddiskseq-1.device",
		"system-getty.slice", "run-user-1000.mount",
	} {
		if err := CheckFilter(logs.Filter{Unit: unit,
			Priority: logs.PriorityAny}); err != nil {
			t.Errorf("CheckFilter(%q): %v", unit, err)
		}
	}
}

func TestExportStaysUnderTheHomeDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("this account has no home directory")
	}
	good := filepath.Join(home, "journal.log")
	if err := CheckExportPath(good); err != nil {
		t.Fatalf("CheckExportPath(%q): %v", good, err)
	}
	for _, bad := range []string{
		"/etc/passwd",
		"/etc/systemd/journald.conf",
		filepath.Join(home, "..", "someone-else", "notes.log"),
		home + "/",
		"relative.log",
		filepath.Join(home, "with a space.log"),
	} {
		if err := CheckExportPath(bad); err == nil {
			t.Errorf("CheckExportPath(%q) was accepted", bad)
		}
	}
}

func TestBuildInstallExportSetsTheMode(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("this account has no home directory")
	}
	destination := filepath.Join(home, "journal.log")
	cmd, err := BuildInstallExport("/tmp/tui-logs-1/journal.log", destination)
	if err != nil {
		t.Fatalf("BuildInstallExport: %v", err)
	}
	assertArgv(t, cmd.Argv, []string{"install", "-m", "600",
		"/tmp/tui-logs-1/journal.log", destination})
	// A journal export can carry anything the machine logged, so there must
	// be no window in which it is on disk world-readable.
	if !strings.Contains(cmd.String(), "-m 600") {
		t.Errorf("the export is not written 0600: %s", cmd)
	}
}

func TestCapabilitiesExplainAnOlderSystemd(t *testing.T) {
	current := Capabilities(true)
	if _, ok := current.Reason(logs.CapBoots); ok {
		t.Error("a current systemd has nothing to explain about the boot list")
	}
	older := Capabilities(false)
	reason, ok := older.Reason(logs.CapBoots)
	if !ok || !strings.Contains(reason, "252") {
		t.Errorf("reason = %q, want it to name the version the JSON arrived in",
			reason)
	}
	// The version gate is about how the boots are read, not about whether the
	// tool works: everything else must still be offered.
	if !older.SupportsFollow || !older.SupportsVacuum || !older.SupportsExport {
		t.Errorf("an older systemd lost capabilities it has: %+v", older)
	}
}
