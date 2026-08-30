package journald

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/tui-tools/tui-logs/internal/logs"
)

// The version-gated capabilities of the backend, named the way the manifest
// names them. A tool asks the compat set for these instead of comparing
// version numbers in the code.
const (
	// FeatureListBootsJSON is `journalctl --list-boots -o json`, which
	// arrived in systemd 252. Below it the same command prints a table, and
	// the table is what gets parsed instead — the boot picker works either
	// way, and this is the only thing in the whole tool that a supported
	// systemd can fail to have.
	FeatureListBootsJSON = "list-boots-json"
)

// JournalDir is the directory whose existence decides whether the journal
// survives a reboot.
const JournalDir = "/var/log/journal"

// RuntimeJournalDir is where the journal lives when it does not.
const RuntimeJournalDir = "/run/log/journal"

// ConfName is the configuration file `systemd-analyze cat-config` is asked
// for. It is the relative form, which is the form that resolves against every
// prefix systemd searches.
const ConfName = "systemd/journald.conf"

// vacuumSizes and vacuumTimes are what the housekeeping pickers offer. They
// are a closed set because both arguments go into an argv that deletes
// journal files, and a free-text field there is a typo away from vacuuming a
// machine down to nothing.
var vacuumSizes = []string{"4G", "2G", "1G", "500M", "200M"}

var vacuumTimes = []string{"1year", "6months", "90d", "30d", "7d"}

// capabilities describes what the journald backend supports. It is shared by
// the real and the fake backend, so --demo behaves exactly like a real run.
var capabilities = logs.Capabilities{
	SupportsFollow:      true,
	SupportsVacuum:      true,
	SupportsRotate:      true,
	SupportsVerify:      true,
	SupportsExport:      true,
	SupportsUserJournal: true,
	VacuumSizes:         vacuumSizes,
	VacuumTimes:         vacuumTimes,
}

// Capabilities reports what the journald backend supports on a given systemd.
// Only one thing is version-gated, so only one thing can be missing from it.
func Capabilities(hasBootsJSON bool) logs.Capabilities {
	caps := capabilities
	if !hasBootsJSON {
		caps.Unavailable = map[string]string{
			logs.CapBoots: "this systemd predates `--list-boots -o json` " +
				"(252), so the boot list is read from its table instead",
		}
	}
	return caps
}

// unitRe is the set of characters a systemd unit name may contain. The unit
// comes from the machine, or from what a user typed, and ends up in an argv.
var unitRe = regexp.MustCompile(`^[A-Za-z0-9@:._\\-]{1,255}$`)

// bootRe accepts a -b argument: a relative offset, or a 32-character boot id.
var bootRe = regexp.MustCompile(`^(-?[0-9]{1,4}|[0-9a-f]{32})$`)

// sinceRe accepts a --since or --until value in journalctl's own grammar:
// a relative offset, one of its keywords, or a timestamp. It is deliberately
// narrow — the value reaches an argv, and every window this tool offers is a
// preset, so the pattern only has to admit what the presets produce and what
// a person would reasonably type.
var sinceRe = regexp.MustCompile(
	`^([+-]?[0-9]{1,6}\s?(s|sec|second|seconds|m|min|minute|minutes|h|hr|hour|hours|d|day|days|w|week|weeks|month|months|y|year|years)|` +
		`today|yesterday|tomorrow|now|` +
		`[0-9]{4}-[0-9]{2}-[0-9]{2}( [0-9]{2}:[0-9]{2}(:[0-9]{2})?)?)$`)

// grepMax bounds the --grep pattern. journalctl compiles it as a regular
// expression, so the limit is about a UI that stays readable rather than
// about safety: the value is one argv element and never a shell string.
const grepMax = 200

// vacuumSizeRe and vacuumTimeRe accept the two housekeeping arguments.
var vacuumSizeRe = regexp.MustCompile(`^[0-9]{1,6}(K|M|G|T)?$`)

var vacuumTimeRe = regexp.MustCompile(
	`^[0-9]{1,6}(s|m|h|d|w|month|months|year|years)$`)

// exportPathRe accepts an export destination: an absolute path of ordinary
// characters. That it is also inside the user's home directory is checked
// separately, because it is a different kind of claim.
var exportPathRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,1000}$`)

// BuildRead renders the journalctl invocation a filter stands for.
//
// It is the one place an argv is assembled from a filter, and every screen
// goes through it: the table's read, the follow read, the export, and the
// command line the header shows. That is what makes the header's claim true —
// it is not a description of the read, it is the read.
func BuildRead(filter logs.Filter) (logs.Command, error) {
	argv, err := readArgv(filter)
	if err != nil {
		return logs.Command{}, err
	}
	return logs.Command{
		Argv:        argv,
		Description: "Read the journal: " + filter.Label(),
	}, nil
}

// BuildExportRead renders the read behind an export. It is BuildRead with a
// different `-o`: what goes into a file is the plain text a person reads,
// `short-iso`, with a timestamp that carries its own time zone rather than
// the reader's.
func BuildExportRead(filter logs.Filter) (logs.Command, error) {
	argv, err := readArgvFormat(filter, "short-iso")
	if err != nil {
		return logs.Command{}, err
	}
	return logs.Command{
		Argv:        argv,
		Description: "Read the journal for the export: " + filter.Label(),
	}, nil
}

// readArgv is BuildRead's argument list.
func readArgv(filter logs.Filter) ([]string, error) {
	return readArgvFormat(filter, "json")
}

// readArgvFormat assembles a read, in a fixed argument order so a test can
// assert on it and a reader can recognise it.
func readArgvFormat(filter logs.Filter, format string) ([]string, error) {
	if err := CheckFilter(filter); err != nil {
		return nil, err
	}
	argv := []string{"journalctl", "--no-pager", "-o", format}
	if filter.User {
		argv = append(argv, "--user")
	}
	if filter.Kernel {
		argv = append(argv, "-k")
	}
	if filter.Unit != "" {
		argv = append(argv, "-u", filter.Unit)
	}
	if filter.Priority != logs.PriorityAny {
		argv = append(argv, "-p", strconv.Itoa(int(filter.Priority)))
	}
	if filter.Boot != "" {
		argv = append(argv, "-b", filter.Boot)
	}
	if filter.Since != "" {
		argv = append(argv, "--since", filter.Since)
	}
	if filter.Until != "" {
		argv = append(argv, "--until", filter.Until)
	}
	if filter.Grep != "" {
		argv = append(argv, "--grep", filter.Grep)
	}
	lines := filter.Lines
	if lines <= 0 {
		lines = logs.DefaultLines
	}
	return append(argv, "-n", strconv.Itoa(lines)), nil
}

// BuildFollow renders the read that asks for what arrived after a cursor.
//
// Follow is a re-read rather than `journalctl -f`, and that is deliberate:
// `-f` never exits, and a command that never exits cannot be the same value
// the confirm dialog showed and the runner ran. `--after-cursor` asks for
// exactly the entries the screen has not seen, which is the same information
// at the cost of one short invocation every couple of seconds.
func BuildFollow(filter logs.Filter, cursor string) (logs.Command, error) {
	if !cursorRe.MatchString(cursor) {
		return logs.Command{}, fmt.Errorf(
			"journald: %q is not a journal cursor", cursor)
	}
	argv, err := readArgv(filter)
	if err != nil {
		return logs.Command{}, err
	}
	return logs.Command{
		Argv:        append(argv, "--after-cursor", cursor),
		Description: "Read what the journal recorded after the last entry on screen",
	}, nil
}

// cursorRe accepts a journal cursor, which is the `field=value;…` string
// journalctl prints as __CURSOR.
var cursorRe = regexp.MustCompile(`^[A-Za-z0-9=;_-]{1,512}$`)

// BuildEntry renders the invocation that shows one entry in full.
//
// This is the tool's answer to "let me copy that": rather than a clipboard,
// which is a thing a terminal cannot promise, the detail screen shows the
// journalctl command that prints exactly this record, and it is a command
// that works on any machine holding the same journal.
func BuildEntry(entry logs.Entry) (logs.Command, error) {
	if !cursorRe.MatchString(entry.Cursor) {
		return logs.Command{}, fmt.Errorf(
			"journald: this entry carries no cursor to address it by")
	}
	return logs.Command{
		Argv: []string{"journalctl", "--no-pager", "-o", "verbose",
			"-n", "1", "--cursor", entry.Cursor},
		Description: "Show this entry with every field it carries",
	}, nil
}

// BuildListBoots renders the boot list read, in JSON when this systemd can
// print it and as its table when it cannot.
func BuildListBoots(hasJSON bool) logs.Command {
	argv := []string{"journalctl", "--no-pager", "--list-boots"}
	if hasJSON {
		argv = append(argv, "-o", "json")
	}
	return logs.Command{Argv: argv, Description: "List the boots the journal still holds"}
}

// BuildListUnits renders the unit list the picker is built from.
//
// The list comes from systemd rather than from the entries on screen, because
// the unit a reader is looking for is usually the one that stopped logging.
// `--all` is what includes the inactive ones, and `--plain --no-legend`
// is what makes the output a table rather than a decorated report.
func BuildListUnits() logs.Command {
	return logs.Command{
		Argv: []string{"systemctl", "list-units", "--all", "--plain",
			"--no-legend", "--no-pager"},
		Description: "List the units this machine knows about",
	}
}

// BuildDiskUsage renders the read that asks what the journal costs.
func BuildDiskUsage() logs.Command {
	return logs.Command{
		Argv:        []string{"journalctl", "--disk-usage"},
		Description: "Ask how much disk the journal is using",
	}
}

// BuildCatConfig renders the read of the effective journald configuration.
func BuildCatConfig() logs.Command {
	return logs.Command{
		Argv:        []string{"systemd-analyze", "cat-config", ConfName},
		Description: "Show the journald configuration in force, with its sources",
	}
}

// BuildVacuumSize shrinks the journal to a size.
//
// It is the one action in this tool that destroys data, and it is worth being
// plain about what it destroys: whole archived journal *files*, oldest first,
// until the total is under the limit. It never removes part of a file, so the
// result is usually smaller than what was asked for.
func BuildVacuumSize(size string) (logs.Command, error) {
	size = strings.TrimSpace(size)
	if !vacuumSizeRe.MatchString(size) {
		return logs.Command{}, fmt.Errorf(
			"journald: %q is not a size (100M, 2G)", size)
	}
	return logs.Command{
		Argv:        []string{"journalctl", "--vacuum-size=" + size},
		Description: "Delete the oldest archived journal files until the journal is under " + size,
		Destructive: true,
	}, nil
}

// BuildVacuumTime shrinks the journal to an age.
func BuildVacuumTime(age string) (logs.Command, error) {
	age = strings.TrimSpace(age)
	if !vacuumTimeRe.MatchString(age) {
		return logs.Command{}, fmt.Errorf(
			"journald: %q is not an age (30d, 6months)", age)
	}
	return logs.Command{
		Argv:        []string{"journalctl", "--vacuum-time=" + age},
		Description: "Delete archived journal entries older than " + age,
		Destructive: true,
	}, nil
}

// BuildRotate closes the active journal files and starts new ones.
//
// It loses nothing: what was active becomes archived, under a new name. It is
// what makes an entry that is on disk stop being appended to, which is what a
// vacuum then has something to work with — and it is the safe half of the
// "the journal is enormous" answer.
func BuildRotate() (logs.Command, error) {
	return logs.Command{
		Argv: []string{"journalctl", "--rotate"},
		Description: "Close the active journal files and start new ones; " +
			"nothing is deleted",
		Destructive: false,
	}, nil
}

// BuildVerify checks the journal's internal consistency.
//
// It is a read — it opens every file and validates its hashes — and it is
// still put behind the confirm dialog, because on a journal of several
// gigabytes it takes minutes and there is no way to say so afterwards.
func BuildVerify() (logs.Command, error) {
	return logs.Command{
		Argv:        []string{"journalctl", "--verify"},
		Description: "Check every journal file's internal consistency (a read, and a slow one)",
	}, nil
}

// BuildInstallExport copies a staged export into its destination.
//
// `install` rather than `cp` because it sets the mode in the same call: a
// journal export can carry anything the machine logged, so there must be no
// window in which it is on disk world-readable.
//
// It checks the shape of both paths and not where the destination is. That
// second question — is this inside the user's home directory — belongs to the
// caller, because the answer differs between a real run, which asks the
// operating system, and --demo, whose home directory is a name in a constant.
// Both callers ask it before they get here, and neither builds a command
// without asking.
func BuildInstallExport(tempPath, destination string) (logs.Command, error) {
	if !exportPathRe.MatchString(tempPath) {
		return logs.Command{}, fmt.Errorf("journald: %q is not a staging path", tempPath)
	}
	if !exportPathRe.MatchString(destination) ||
		strings.Contains(destination, "/../") ||
		strings.HasSuffix(destination, "/..") ||
		strings.HasSuffix(destination, "/") {
		return logs.Command{}, fmt.Errorf(
			"journald: %q is not a path this tool will write to", destination)
	}
	return logs.Command{
		Argv:        []string{"install", "-m", ExportMode, tempPath, destination},
		Description: "Write the entries on screen to " + destination,
		Destructive: true,
	}, nil
}

// ExportMode is the mode an exported file gets: readable by its owner and
// nobody else, because the system journal is not public.
const ExportMode = "600"

// CheckExportPath refuses a destination outside the user's home directory.
//
// The rule is not about permissions — the copy runs as the user, so the
// kernel would refuse most of what this refuses anyway. It is about what a
// tool is allowed to offer: a file browser that can write to /etc is a
// different tool, and one keystroke away from a mistake nobody asked for.
func CheckExportPath(path string) error {
	if !exportPathRe.MatchString(path) {
		return fmt.Errorf("journald: %q is not a path this tool will write to", path)
	}
	if strings.Contains(path, "/../") || strings.HasSuffix(path, "/..") {
		return fmt.Errorf(
			"journald: an export path may not contain a parent-directory step")
	}
	if strings.HasSuffix(path, "/") {
		return fmt.Errorf("journald: %q is a directory, not a file", path)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return fmt.Errorf("journald: there is no home directory to export into")
	}
	clean := filepath.Clean(path)
	if clean != home && !strings.HasPrefix(clean, home+"/") {
		return fmt.Errorf("journald: exports go under %s; %q is outside it", home, path)
	}
	return nil
}

// DefaultExportPath is where the export dialog starts: a name that says what
// the file is and when it was taken.
func DefaultExportPath(home, stamp string) string {
	return filepath.Join(home, "journal-"+stamp+".log")
}

// CheckFilter validates every part of a filter that reaches an argv.
//
// Nothing here comes from a shell, so this is not about quoting. It is about
// the tool refusing to build a command it cannot explain: a unit name with a
// space in it, a since value journalctl would reject, a priority that is not
// a level. The refusal reaches the user as a message on the screen that
// offered the value.
func CheckFilter(filter logs.Filter) error {
	if filter.Unit != "" && !unitRe.MatchString(filter.Unit) {
		return fmt.Errorf("journald: %q is not a unit name", filter.Unit)
	}
	if filter.Unit != "" && filter.Kernel {
		return fmt.Errorf(
			"journald: the kernel has no unit, so -k and a unit filter cannot both apply")
	}
	if filter.Kernel && filter.User {
		return fmt.Errorf(
			"journald: the kernel logs to the system journal, not to a user one")
	}
	if filter.Priority != logs.PriorityAny &&
		(filter.Priority < logs.PriEmerg || filter.Priority > logs.PriDebug) {
		return fmt.Errorf("journald: %d is not a syslog priority", filter.Priority)
	}
	if filter.Boot != "" && !bootRe.MatchString(filter.Boot) {
		return fmt.Errorf("journald: %q is not a boot", filter.Boot)
	}
	for _, bound := range []struct{ flag, value string }{
		{"--since", filter.Since}, {"--until", filter.Until},
	} {
		if bound.value != "" && !sinceRe.MatchString(bound.value) {
			return fmt.Errorf("journald: %q is not a %s journalctl reads",
				bound.value, bound.flag)
		}
	}
	if len(filter.Grep) > grepMax {
		return fmt.Errorf("journald: a search pattern is at most %d characters", grepMax)
	}
	if strings.ContainsAny(filter.Grep, "\n\r\x00") {
		return fmt.Errorf("journald: a search pattern cannot contain a newline")
	}
	if filter.Lines < 0 || filter.Lines > MaxLines {
		return fmt.Errorf("journald: a read is between 1 and %d entries", MaxLines)
	}
	return nil
}

// MaxLines bounds one read. It is generous enough for a busy hour and small
// enough that the JSON of it fits in memory without thinking about it.
const MaxLines = 20000
