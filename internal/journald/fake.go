package journald

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tui-tools/tui-kit/runner"
	"github.com/tui-tools/tui-logs/internal/logs"
)

// The sample machine's two boots. The current one is where everything
// interesting happens; the one before it is there so the boot picker has
// something to pick and the per-boot counts have two rows.
const (
	demoBootCurrent  = "b76f7229a3e74d4d9e1c2453b87131c9"
	demoBootPrevious = "08bf341424424155acdce8f52a67fcc8"
)

// demoNow is the sample machine's idea of the present. It is fixed so the
// screenshots in the README are the frames the tool produces, and so a test
// asserting on a timestamp is not a test that fails at midnight.
var demoNow = time.Date(2026, 8, 30, 11, 45, 0, 0, time.UTC)

// demoHost is the sample machine's hostname.
const demoHost = "lab"

// demoStamp names the export file in --demo.
const demoStamp = "20260830-114500"

// demoHome is the home directory the demo's export path is built on. --demo
// writes nothing at all, so it is a name rather than a directory.
const demoHome = "/home/you"

// demoDiskUsage is what `journalctl --disk-usage` says on the sample machine
// before anything is vacuumed.
const demoDiskUsage = "Archived and active journals take up 3.9G in the file system."

// demoEntries is how many records the sample journal carries. It is asserted
// on, so a change to the generator that quietly drops half of it fails a test
// rather than a screenshot.
const demoEntries = 300

// demoConf is what `systemd-analyze cat-config systemd/journald.conf` prints
// on the sample machine: the vendor file with its defaults commented out, and
// a drop-in that actually sets two of them. It goes through the real parser,
// so the demo exercises the same code a machine does.
const demoConf = `# /usr/lib/systemd/journald.conf
#  This file is part of systemd.
#
# Entries in this file show the compile time defaults.
#
# See journald.conf(5) for details.

[Journal]
#Storage=auto
#Compress=yes
#Seal=yes
#SplitMode=uid
#SyncIntervalSec=5m
#RateLimitIntervalSec=30s
#RateLimitBurst=10000
#SystemMaxUse=
#SystemKeepFree=
#SystemMaxFileSize=
#MaxRetentionSec=
#MaxFileSec=1month
#ForwardToSyslog=no
#ForwardToKMsg=no
#ForwardToConsole=no
#ForwardToWall=yes
#Audit=no

# /etc/systemd/journald.conf.d/50-size.conf
[Journal]
SystemMaxUse=4G
MaxRetentionSec=1month
`

// Fake is an in-memory journal. It backs --demo and the tests: every key
// works, every command is built and previewed exactly as the real backend
// builds it, and nothing reaches the system.
//
// The commands are recorded rather than run, and a hook applies to the
// in-memory machine the change the real command would have made — so
// vacuuming in --demo really does shrink the number in the header, and the
// argv the confirm dialog displayed is the argv a test can assert on.
type Fake struct {
	entries []logs.Entry
	boots   []logs.Boot
	units   []string
	storage logs.Storage
	run     *runner.Fake
	// staged is the pending export content, keyed by destination path.
	// --demo writes no file at all, so the "staging directory" is this map.
	staged map[string]string
	// dropIn is the sample machine's copy of the retention drop-in this tool
	// owns, empty when it has never been written.
	dropIn string
}

// NewFake builds the sample machine: two boots, five units and the kernel,
// three hundred entries, and one address working through a user list against
// sshd for four minutes.
func NewFake() *Fake {
	f := &Fake{staged: map[string]string{}}
	f.run = &runner.Fake{Prefix: "sudo -n", Hook: f.apply}
	f.reset()
	return f
}

// reset builds the sample state. It is a function rather than a literal so
// --demo starts from the same machine every time, however it was left.
func (f *Fake) reset() {
	// The entries go through the real JSON parser rather than being built as
	// structs, so --demo exercises the code a machine's journal goes through.
	f.entries = ParseEntries(demoJournal())
	f.boots = ParseBootsJSON(demoBoots())
	f.units = ParseJournalUnits(demoJournalUnits())
	f.storage = logs.Storage{
		Persistent:     true,
		PersistentNote: PersistentNote,
		ConfSource:     "systemd-analyze cat-config " + ConfName,
		Conf:           ParseCatConfig(demoConf),
	}
	f.storage.DiskUsage, f.storage.Bytes = ParseDiskUsage(demoDiskUsage)
}

// demoJournalUnits is what `journalctl --field _SYSTEMD_UNIT` prints on the
// sample machine: the units that have written to its journal, in the order the
// field index happens to hold them, which is the order the parser sorts away.
func demoJournalUnits() string {
	return strings.Join([]string{
		"nginx.service",
		"systemd-journald.service",
		"sshd.service",
		"backup.service",
		"postgresql.service",
		"NetworkManager.service",
	}, "\n")
}

// demoBoots is what `journalctl --list-boots -o json` prints on the sample
// machine: the running boot, and the one before it.
func demoBoots() string {
	previousStart := demoNow.Add(-72 * time.Hour)
	previousEnd := demoNow.Add(-50 * time.Hour)
	currentStart := demoNow.Add(-26 * time.Hour)
	return fmt.Sprintf(
		`[{"index":-1,"boot_id":"%s","first_entry":%d,"last_entry":%d},`+
			`{"index":0,"boot_id":"%s","first_entry":%d,"last_entry":%d}]`,
		demoBootPrevious, previousStart.UnixMicro(), previousEnd.UnixMicro(),
		demoBootCurrent, currentStart.UnixMicro(), demoNow.UnixMicro())
}

// demoEntry renders one record the way journalctl prints it.
func demoEntry(when time.Time, boot string, priority logs.Priority,
	unit, identifier string, pid int, message string, extra map[string]string) string {
	fields := map[string]string{
		"__CURSOR": fmt.Sprintf("s=demo;i=%x;b=%s;m=%x;t=%x",
			when.UnixMicro(), boot, when.UnixNano(), when.UnixMicro()),
		"__REALTIME_TIMESTAMP": fmt.Sprint(when.UnixMicro()),
		"_BOOT_ID":             boot,
		"_HOSTNAME":            demoHost,
		"_MACHINE_ID":          "6339e64ae08b437082fafc14a79ff2f7",
		"PRIORITY":             fmt.Sprint(int(priority)),
		"SYSLOG_IDENTIFIER":    identifier,
		"MESSAGE":              message,
		"_PID":                 fmt.Sprint(pid),
		"_TRANSPORT":           "journal",
	}
	if unit != "" {
		fields["_SYSTEMD_UNIT"] = unit
		fields["_UID"] = "0"
		fields["_GID"] = "0"
		fields["_COMM"] = identifier
		fields["_EXE"] = "/usr/sbin/" + identifier
		fields["_CMDLINE"] = "/usr/sbin/" + identifier
		fields["_SYSTEMD_SLICE"] = "system.slice"
		fields["_SYSTEMD_CGROUP"] = "/system.slice/" + unit
	}
	for key, value := range extra {
		fields[key] = value
	}

	var b strings.Builder
	b.WriteByte('{')
	first := true
	// The order is fixed rather than a map walk, so the sample journal is
	// byte-identical between runs and a screenshot does not shuffle.
	for _, key := range demoFieldOrder(fields) {
		if !first {
			b.WriteByte(',')
		}
		first = false
		fmt.Fprintf(&b, "%q:%q", key, fields[key])
	}
	b.WriteByte('}')
	return b.String()
}

// demoFieldOrder is the field order the sample records are written in.
func demoFieldOrder(fields map[string]string) []string {
	preferred := []string{
		"__CURSOR", "__REALTIME_TIMESTAMP", "_BOOT_ID", "_HOSTNAME",
		"_MACHINE_ID", "PRIORITY", "SYSLOG_IDENTIFIER", "_SYSTEMD_UNIT",
		"MESSAGE", "MESSAGE_ID", "_PID", "_UID", "_GID", "_COMM", "_EXE",
		"_CMDLINE", "_TRANSPORT", "_SYSTEMD_SLICE", "_SYSTEMD_CGROUP",
		"CODE_FILE", "CODE_LINE", "CODE_FUNC", "_KERNEL_SUBSYSTEM",
	}
	var order []string
	seen := map[string]bool{}
	for _, key := range preferred {
		if _, ok := fields[key]; ok {
			order = append(order, key)
			seen[key] = true
		}
	}
	for _, key := range sortedKeys(fields) {
		if !seen[key] {
			order = append(order, key)
		}
	}
	return order
}

// demoJournal builds the sample machine's journal: three hundred entries
// across five units, the kernel, and two boots — with one address working
// through a user list against sshd, which is the burst the stats screen and
// the priority filter are there to find.
//
// journalctl prints oldest first, and so does this.
func demoJournal() string {
	var lines []string
	write := func(entry string) { lines = append(lines, entry) }

	// The previous boot: a quiet day, so the boot picker has a boot whose
	// counts are visibly different from the current one's.
	start := demoNow.Add(-72 * time.Hour)
	for i := range 40 {
		when := start.Add(time.Duration(i) * 30 * time.Minute)
		write(demoEntry(when, demoBootPrevious, logs.PriInfo,
			"NetworkManager.service", "NetworkManager", 812,
			fmt.Sprintf("<info> [%d] device (eth0): carrier is up", i), nil))
	}

	// The current boot opens the way every boot does.
	boot := demoNow.Add(-26 * time.Hour)
	write(demoEntry(boot, demoBootCurrent, logs.PriInfo, "", "kernel", 0,
		"Linux version 6.16.4-200.fc42.x86_64", map[string]string{
			"_TRANSPORT": "kernel", "SYSLOG_FACILITY": "0",
		}))
	write(demoEntry(boot.Add(2*time.Second), demoBootCurrent, logs.PriWarning,
		"", "kernel", 0,
		"pcieport 0000:00:1c.0: AER: Correctable error message received",
		map[string]string{"_TRANSPORT": "kernel", "_KERNEL_SUBSYSTEM": "pci"}))
	write(demoEntry(boot.Add(9*time.Second), demoBootCurrent, logs.PriInfo,
		"systemd-journald.service", "systemd-journald", 411,
		"Journal started", map[string]string{
			"MESSAGE_ID": "f77379a8490b408bbe5f6940505a777b",
		}))
	write(demoEntry(boot.Add(31*time.Second), demoBootCurrent, logs.PriInfo,
		"", "systemd", 1, "Startup finished in 4.812s (kernel) + 9.104s (userspace).",
		map[string]string{"MESSAGE_ID": "b07a249cd024414a82dd00cd181378ff"}))

	// A day of ordinary traffic from the units that are simply running.
	chatter := []struct {
		unit, identifier string
		pid              int
		priority         logs.Priority
		message          string
	}{
		{"nginx.service", "nginx", 1203, logs.PriInfo,
			`10.0.0.14 - - "GET /health HTTP/1.1" 200 2`},
		{"NetworkManager.service", "NetworkManager", 812, logs.PriInfo,
			"<info> dhcp4 (eth0): state changed new lease, address=10.0.0.21"},
		{"sshd.service", "sshd", 2044, logs.PriInfo,
			"Accepted publickey for deploy from 10.0.0.5 port 51422 ssh2"},
		{"systemd-journald.service", "systemd-journald", 411, logs.PriInfo,
			"Time spent on flushing to /var/log/journal was 34.112ms"},
	}
	// 194 here, which with everything around it makes the sample journal
	// exactly demoEntries lines long.
	for i := range 194 {
		item := chatter[i%len(chatter)]
		when := boot.Add(time.Duration(i)*7*time.Minute + time.Minute)
		write(demoEntry(when, demoBootCurrent, item.priority, item.unit,
			item.identifier, item.pid, item.message, nil))
	}

	// postgresql keeps failing to start, which is what an error filter finds
	// on a machine nobody has looked at yet.
	for i := range 6 {
		when := demoNow.Add(-time.Duration(6-i) * 20 * time.Minute)
		write(demoEntry(when, demoBootCurrent, logs.PriErr,
			"postgresql.service", "postgres", 3300+i,
			"FATAL: could not open directory \"/var/lib/pgsql/data\": Permission denied",
			map[string]string{
				"CODE_FILE": "src/backend/storage/file/fd.c",
				"CODE_LINE": "3411", "CODE_FUNC": "AllocateDir",
			}))
		write(demoEntry(when.Add(time.Second), demoBootCurrent, logs.PriErr,
			"", "systemd", 1,
			"postgresql.service: Failed with result 'exit-code'.",
			map[string]string{
				"MESSAGE_ID": "d9b373ed55a64feb8242e02dbe79a49c",
			}))
	}

	// And the burst: one address working through a user list against sshd,
	// forty-eight failures in four minutes.
	burst := demoNow.Add(-38 * time.Minute)
	names := []string{"root", "admin", "oracle", "postgres", "test", "ubuntu"}
	for i := range 48 {
		when := burst.Add(time.Duration(i) * 5 * time.Second)
		user := names[i%len(names)]
		port := 41000 + i
		if user == "root" {
			write(demoEntry(when, demoBootCurrent, logs.PriInfo, "sshd.service",
				"sshd", 4400+i, fmt.Sprintf(
					"Failed password for root from 203.0.113.7 port %d ssh2", port), nil))
			continue
		}
		write(demoEntry(when, demoBootCurrent, logs.PriInfo, "sshd.service",
			"sshd", 4400+i, fmt.Sprintf(
				"Invalid user %s from 203.0.113.7 port %d", user, port), nil))
	}
	write(demoEntry(burst.Add(5*time.Minute), demoBootCurrent, logs.PriWarning,
		"sshd.service", "sshd", 4500,
		"error: maximum authentication attempts exceeded for root from "+
			"203.0.113.7 port 41240 ssh2 [preauth]", nil))
	write(demoEntry(demoNow.Add(-2*time.Minute), demoBootCurrent, logs.PriNotice,
		"systemd-journald.service", "systemd-journald", 411,
		"Suppressed 1183 messages from sshd.service", map[string]string{
			"MESSAGE_ID": "a596d6fe7bfa4994828e72309e95d61e",
		}))

	return strings.Join(lines, "\n") + "\n"
}

// Name identifies the backend. It is the real backend's name, because --demo
// shows what the real one would show.
func (f *Fake) Name() string { return "journald" }

// Describe says plainly that nothing here is real.
func (f *Fake) Describe() string { return "demo (in-memory sample journal)" }

// Capabilities reports the same capabilities as a current systemd.
func (f *Fake) Capabilities() logs.Capabilities { return Capabilities(true) }

// Preview renders the command line the real backend would run.
func (f *Fake) Preview(cmd logs.Command) string { return f.run.Preview(cmd) }

// Load returns the sample machine, filtered.
func (f *Fake) Load(_ context.Context, filter logs.Filter) (logs.Model, error) {
	entries, cmd, err := f.ReadEntries(context.Background(), filter)
	if err != nil {
		return logs.Model{}, err
	}
	return logs.Model{
		Backend:     f.Name(),
		Filter:      filter,
		Command:     cmd,
		Entries:     entries,
		Units:       f.units,
		UnitsSource: logs.UnitsFromJournal,
		Boots:       f.boots,
		Storage:     f.storage,
		Stats:       logs.ComputeStats(entries, filter.WithLines(filter.Lines).Lines),
		Access: logs.Access{
			Note: "the sample journal opens without any privileges at all",
		},
	}, nil
}

// ReadEntries applies a filter to the sample journal, the way journalctl
// would apply it to a real one.
func (f *Fake) ReadEntries(_ context.Context, filter logs.Filter) ([]logs.Entry,
	logs.Command, error) {
	cmd, err := BuildRead(filter.WithLines(filter.Lines))
	if err != nil {
		return nil, logs.Command{}, err
	}
	return f.match(filter), cmd, nil
}

// ReadSince returns the entries after a cursor, which in a demo whose journal
// does not grow is none of them.
func (f *Fake) ReadSince(_ context.Context, filter logs.Filter,
	cursor string) ([]logs.Entry, error) {
	if cursor == "" {
		return f.match(filter), nil
	}
	var fresh []logs.Entry
	for _, entry := range f.match(filter) {
		if entry.Cursor == cursor {
			break
		}
		fresh = append(fresh, entry)
	}
	return fresh, nil
}

// ReadStorage returns the sample machine's disk usage and configuration.
func (f *Fake) ReadStorage(_ context.Context) (logs.Storage, error) {
	return f.storage, nil
}

// match filters the sample journal the way journalctl filters a real one:
// -p is "this level and worse", -u matches the unit, -b the boot, -k the
// kernel transport, --since and --until the window, and --grep the message.
func (f *Fake) match(filter logs.Filter) []logs.Entry {
	lines := filter.WithLines(filter.Lines).Lines
	since := demoBound(filter.Since)
	until := demoBound(filter.Until)
	var out []logs.Entry
	for _, entry := range f.entries {
		if !since.IsZero() && entry.Realtime.Before(since) {
			continue
		}
		if !until.IsZero() && entry.Realtime.After(until) {
			continue
		}
		if filter.User {
			// The sample machine's user journal is empty, which is the honest
			// answer for a demo with no user services in it.
			return nil
		}
		if filter.Kernel && entry.Transport != "kernel" {
			continue
		}
		if filter.Unit != "" && entry.Unit != filter.Unit {
			continue
		}
		if filter.Priority != logs.PriorityAny &&
			(entry.Priority == logs.PriorityAny || entry.Priority > filter.Priority) {
			continue
		}
		if filter.Boot != "" && !f.inBoot(entry, filter.Boot) {
			continue
		}
		if filter.Grep != "" && !grepMatches(entry.Message, filter.Grep) {
			continue
		}
		out = append(out, entry)
		if len(out) >= lines {
			break
		}
	}
	return out
}

// demoBound turns a --since or --until value into an instant on the sample
// machine's clock, whose present is demoNow.
//
// Only the forms the tool itself produces are understood — a relative offset
// and `today` — because those are what the presets and `--check` pass. An
// absolute timestamp is read as itself, and anything else leaves the bound
// open rather than filtering everything away.
func demoBound(value string) time.Time {
	value = strings.TrimSpace(value)
	switch value {
	case "":
		return time.Time{}
	case "now":
		return demoNow
	case "today":
		return time.Date(demoNow.Year(), demoNow.Month(), demoNow.Day(), 0, 0, 0, 0,
			demoNow.Location())
	case "yesterday":
		return time.Date(demoNow.Year(), demoNow.Month(), demoNow.Day()-1, 0, 0, 0, 0,
			demoNow.Location())
	}
	if offset, ok := demoOffset(value); ok {
		return demoNow.Add(offset)
	}
	if when, err := time.ParseInLocation("2006-01-02 15:04:05", value,
		demoNow.Location()); err == nil {
		return when
	}
	if when, err := time.ParseInLocation("2006-01-02", value,
		demoNow.Location()); err == nil {
		return when
	}
	return time.Time{}
}

// demoOffsetUnits maps the suffixes journalctl accepts on a relative offset onto
// durations.
var demoOffsetUnits = map[string]time.Duration{
	"s": time.Second, "sec": time.Second, "second": time.Second,
	"seconds": time.Second,
	"m":       time.Minute, "min": time.Minute, "minute": time.Minute,
	"minutes": time.Minute,
	"h":       time.Hour, "hr": time.Hour, "hour": time.Hour, "hours": time.Hour,
	"d": 24 * time.Hour, "day": 24 * time.Hour, "days": 24 * time.Hour,
	"w": 7 * 24 * time.Hour, "week": 7 * 24 * time.Hour,
	"weeks": 7 * 24 * time.Hour,
}

// demoOffset reads "-1h", "-15m", "+30s" into a duration.
func demoOffset(value string) (time.Duration, bool) {
	sign := time.Duration(1)
	switch {
	case strings.HasPrefix(value, "-"):
		sign, value = -1, value[1:]
	case strings.HasPrefix(value, "+"):
		value = value[1:]
	}
	value = strings.TrimSpace(value)
	digits := 0
	for digits < len(value) && value[digits] >= '0' && value[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return 0, false
	}
	count, err := strconv.Atoi(value[:digits])
	if err != nil {
		return 0, false
	}
	unit, ok := demoOffsetUnits[strings.TrimSpace(value[digits:])]
	if !ok {
		return 0, false
	}
	return sign * time.Duration(count) * unit, true
}

// inBoot reports whether an entry belongs to a boot named by id or by offset.
func (f *Fake) inBoot(entry logs.Entry, ref string) bool {
	for _, boot := range f.boots {
		if boot.ID != ref && fmt.Sprint(boot.Index) != ref {
			continue
		}
		return entry.BootID == boot.ID
	}
	return false
}

// grepMatches is journalctl's own matching rule for --grep: case-insensitive
// while the pattern is all lower case, case-sensitive as soon as it is not.
// The demo obeys it so a search that finds something here finds it on a real
// machine too.
func grepMatches(message, pattern string) bool {
	if pattern == strings.ToLower(pattern) {
		return strings.Contains(strings.ToLower(message), pattern)
	}
	return strings.Contains(message, pattern)
}

// Run records the command and applies its effect to the sample machine.
func (f *Fake) Run(ctx context.Context, cmd logs.Command) (string, error) {
	return f.run.Run(ctx, cmd)
}

// Ran exposes the recorded commands, which is what a test asserts on.
func (f *Fake) Ran() []logs.Command { return f.run.Ran }

// apply is the hook the fake runner calls: it makes to the in-memory machine
// the change the real command would have made, so the demo stays coherent as
// keys are pressed.
func (f *Fake) apply(cmd logs.Command) (string, error) {
	if len(cmd.Argv) < 2 {
		return "", nil
	}
	argv := cmd.Argv
	switch {
	case argv[0] == "install" && isDropInInstall(cmd):
		return f.installDropIn()
	case argv[0] == "install" && argv[1] == "-m":
		return f.installExport(argv)
	case argv[0] == "systemctl" && argv[1] == "restart":
		return "", nil
	case argv[0] == "journalctl" && argv[1] == "--rotate":
		return "", nil
	case argv[0] == "journalctl" && argv[1] == "--verify":
		return "PASS: /var/log/journal/" + demoBootCurrent + "/system.journal", nil
	case argv[0] == "journalctl" && strings.HasPrefix(argv[1], "--vacuum-"):
		return f.vacuum(argv[1])
	}
	return "", nil
}

// vacuum shrinks the sample machine, so the number in the header moves the
// way it would on a real one.
func (f *Fake) vacuum(argument string) (string, error) {
	_, value, _ := strings.Cut(argument, "=")
	if strings.HasPrefix(argument, "--vacuum-size=") {
		if bytes, ok := parseSize(value); ok && bytes < f.storage.Bytes {
			// A vacuum removes whole files, so the result lands under the
			// limit rather than on it. Nine tenths is a plausible landing.
			f.storage.Bytes = bytes * 9 / 10
		}
	} else {
		// Vacuuming by age on the sample machine takes it down to the current
		// boot, which is most of what it holds.
		f.storage.Bytes = f.storage.Bytes / 3
	}
	f.storage.DiskUsage = "Archived and active journals take up " +
		FormatSize(f.storage.Bytes) + " in the file system."
	return "Vacuuming done, freed some archived journal files", nil
}

// installExport records the export the demo would have written.
func (f *Fake) installExport(argv []string) (string, error) {
	destination := argv[len(argv)-1]
	if _, ok := f.staged[destination]; !ok {
		return "", fmt.Errorf("install: nothing staged for %s", destination)
	}
	return "", nil
}

// CommandFor renders the journalctl invocation a filter stands for.
func (f *Fake) CommandFor(filter logs.Filter) logs.Command {
	cmd, err := BuildRead(filter.WithLines(filter.Lines))
	if err != nil {
		return logs.Command{Argv: []string{"journalctl"}, Description: err.Error()}
	}
	return cmd
}

// CommandForEntry renders the invocation that shows one entry in full.
func (f *Fake) CommandForEntry(entry logs.Entry) logs.Command {
	cmd, err := BuildEntry(entry)
	if err != nil {
		return logs.Command{Argv: []string{"journalctl"}, Description: err.Error()}
	}
	return cmd
}

// BuildVacuumSize shrinks the sample journal to a size.
func (f *Fake) BuildVacuumSize(size string) (logs.Command, error) {
	return BuildVacuumSize(size)
}

// BuildVacuumTime shrinks the sample journal to an age.
func (f *Fake) BuildVacuumTime(age string) (logs.Command, error) {
	return BuildVacuumTime(age)
}

// BuildRotate closes the sample machine's active journal files.
func (f *Fake) BuildRotate() (logs.Command, error) { return BuildRotate() }

// BuildVerify checks the sample journal.
func (f *Fake) BuildVerify() (logs.Command, error) { return BuildVerify() }

// BuildExport stages the window in memory and returns the same plan the real
// backend returns — the same source command, and the same install. --demo
// writes nothing at all, so the staging path is a name rather than a file.
func (f *Fake) BuildExport(_ context.Context, filter logs.Filter,
	path string) (logs.ExportPlan, error) {
	if err := checkDemoExportPath(path); err != nil {
		return logs.ExportPlan{}, err
	}
	source, err := BuildExportRead(filter.WithLines(filter.Lines))
	if err != nil {
		return logs.ExportPlan{}, err
	}
	entries := f.match(filter)
	if len(entries) == 0 {
		return logs.ExportPlan{}, logs.ErrEmptyExport
	}

	var b strings.Builder
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		fmt.Fprintf(&b, "%s %s %s[%d]: %s\n",
			entry.Realtime.Format("2006-01-02T15:04:05-0700"), demoHost,
			entry.Identifier, entry.PID, entry.Message)
	}
	content := b.String()

	temp := "/tmp/tui-logs/" + demoStamp + ".log"
	f.staged[path] = content
	installCmd, err := BuildInstallExport(temp, path)
	if err != nil {
		return logs.ExportPlan{}, err
	}
	return logs.ExportPlan{
		Path:     path,
		Source:   source,
		TempPath: temp,
		Lines:    len(entries),
		Bytes:    len(content),
		Preview:  previewLines(content, exportPreviewLines),
		Commands: []logs.Command{installCmd},
	}, nil
}

// checkDemoExportPath applies the real path rule against the sample machine's
// home directory, so --demo refuses exactly what a real run refuses without
// depending on the account the demo happens to run as.
func checkDemoExportPath(path string) error {
	if !exportPathRe.MatchString(path) {
		return fmt.Errorf("journald: %q is not a path this tool will write to", path)
	}
	if strings.Contains(path, "/../") || strings.HasSuffix(path, "/..") {
		return fmt.Errorf(
			"journald: an export path may not contain a parent-directory step")
	}
	if !strings.HasPrefix(path, demoHome+"/") {
		return fmt.Errorf("journald: exports go under %s; %q is outside it",
			demoHome, path)
	}
	return nil
}

// ReadRetention reports the sample machine's drop-in, and the effective
// values behind it. The demo starts with no drop-in of its own, which is the
// state a machine this has never run on is in — so the form opens on the
// numbers `systemd-analyze cat-config` reported, exactly as a first real run
// would.
func (f *Fake) ReadRetention(_ context.Context) (logs.Retention, error) {
	retention := logs.Retention{
		Path:      DropInPath,
		Exists:    f.dropIn != "",
		Content:   f.dropIn,
		Values:    ParseDropIn(f.dropIn),
		Effective: EffectiveRetention(f.storage.Conf),
	}
	return retention, nil
}

// BuildRetention renders the same drop-in the real backend would and returns
// the same two commands. --demo writes no file at all, so the staging path is
// a name rather than a file.
func (f *Fake) BuildRetention(values map[string]string) (logs.RetentionPlan, error) {
	content, err := RenderDropIn(values)
	if err != nil {
		return logs.RetentionPlan{}, err
	}
	existing, err := f.ReadRetention(context.Background())
	if err != nil {
		return logs.RetentionPlan{}, err
	}
	temp := "/tmp/tui-logs/" + demoStamp + "-journald.conf"
	f.staged[DropInPath] = content
	return RetentionPlanFor(existing, content, temp)
}

// installDropIn records the drop-in the demo would have written, so pressing
// S twice shows a diff against what the first one wrote.
func (f *Fake) installDropIn() (string, error) {
	content, ok := f.staged[DropInPath]
	if !ok {
		return "", fmt.Errorf("install: nothing staged for " + DropInPath)
	}
	f.dropIn = content
	// The sample machine's effective configuration moves with it, the way a
	// real one does once journald has been restarted.
	f.storage.Conf = applyDropIn(f.storage.Conf, ParseDropIn(content))
	return "", nil
}

// applyDropIn folds the drop-in's values into the effective configuration, so
// the storage screen shows the change the way a real cat-config would after
// the restart.
func applyDropIn(conf []logs.ConfSetting, values map[string]string) []logs.ConfSetting {
	out := make([]logs.ConfSetting, 0, len(conf)+len(values))
	seen := map[string]bool{}
	for _, setting := range conf {
		if value, ok := values[setting.Key]; ok {
			setting.Value, setting.Default = value, false
			setting.File, setting.Line = DropInPath, 0
			seen[setting.Key] = true
		}
		out = append(out, setting)
	}
	for _, setting := range RetentionSettings {
		value, ok := values[setting.Key]
		if !ok || seen[setting.Key] {
			continue
		}
		out = append(out, logs.ConfSetting{
			Key: setting.Key, Value: value, File: DropInPath})
	}
	return out
}

// SuggestExportPath is where the export prompt starts on the sample machine.
func (f *Fake) SuggestExportPath() string {
	return DefaultExportPath(demoHome, demoStamp)
}

// sortedKeys returns a map's keys in order, so the sample records are written
// the same way every time.
func sortedKeys(fields map[string]string) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
