// Package journald is the systemd-journal backend of tui-logs, and the only
// place in the repository that starts a process.
//
// Everything about reaching the machine — resolving the binaries, applying the
// privilege prefix, bounding each call, turning a failure into one readable
// line — belongs to the kit runner. What is left here is the translation
// between journalctl's output and the backend-neutral model in internal/logs,
// and the assembly of the argv that a confirm dialog will show before it runs.
//
// The programs driven, each through its own runner:
//
//	journalctl        the entries, the boots, the disk usage, and the three
//	                  housekeeping verbs
//	systemctl         the unit list the picker is built from
//	systemd-analyze   the journald configuration in force
//	install           the one command that writes an export, and the only
//	                  command here that never escalates
//
// journalctl appears twice: once unprivileged, which is how the tool opens
// instantly for a user in systemd-journal, adm or wheel, and once through the
// privilege prefix, which is the fallback for a machine that refuses the first
// and the runner every action goes through.
package journald

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/runner"
	"github.com/tui-tools/tui-logs/internal/logs"
)

// ErrNotAvailable reports that the journald backend cannot be used on this
// machine (journalctl missing, or no non-interactive privilege escalation).
var ErrNotAvailable = runner.ErrNotAvailable

// searchPaths are the locations a non-root PATH commonly omits.
var searchPaths = map[string][]string{
	"journalctl":      {"/usr/bin/journalctl", "/bin/journalctl"},
	"systemctl":       {"/usr/bin/systemctl", "/bin/systemctl"},
	"systemd-analyze": {"/usr/bin/systemd-analyze", "/bin/systemd-analyze"},
	"install":         {"/usr/bin/install", "/bin/install"},
}

// installHint is appended to the "not found" error.
const installHint = "this tool needs systemd's journal; " +
	"or use --demo to explore the UI"

// readTimeout bounds a read. It is generous because a journal of several
// gigabytes takes its time answering a query that has to walk it.
const readTimeout = 45 * time.Second

// actionTimeout bounds an action. `--verify` walks every file on disk, and on
// a large journal that is minutes rather than seconds.
const actionTimeout = 15 * time.Minute

// unprivileged is the address-of-false the runner options need.
var unprivileged = false

// privileged is its counterpart, for the runner whose reads escalate.
var privileged = true

// permissionMarkers are what journalctl says when it was not allowed to open
// the system journal. It says it on standard error and still exits zero,
// having shown the caller's own entries — so the marker is looked for in the
// output rather than in the exit code, and finding it is what triggers the
// escalated retry.
var permissionMarkers = []string{
	"No journal files were opened due to insufficient permissions",
	"Insufficient permissions",
	"Permission denied",
	"Operation not permitted",
}

// Real drives the journal on the host. It satisfies logs.Backend.
type Real struct {
	journal *runner.Runner
	// journalRoot is the same binary through the privilege prefix. It is what
	// the escalated retry uses, and what every action runs through.
	journalRoot *runner.Runner
	systemctl   *runner.Runner
	analyze     *runner.Runner
	// install writes an export. It is built without the privilege prefix on
	// purpose: an export goes into the user's own home directory, as the
	// user, and a copy that escalated would leave a root-owned file behind.
	install *runner.Runner
	// installRoot and systemctlRoot are the escalated twins the retention
	// form needs: its drop-in goes into /etc, and journald has to be
	// restarted for it to take effect. They are nil on a machine with no
	// usable privilege prefix, which is what turns the form off.
	installRoot   *runner.Runner
	systemctlRoot *runner.Runner

	// caps gates what only exists on a new enough systemd. It comes from the
	// manifest, so no version number is written into this file.
	caps compat.Caps
	// access records how the last journal read was actually permitted, which
	// is the first thing the header says.
	access logs.Access
	// now names the export file. It is a field so a test and a screenshot get
	// the same name every time.
	now func() time.Time
}

// Available reports whether journalctl is installed on this host.
func Available() bool {
	return runner.Available("journalctl", searchPaths["journalctl"]...)
}

// NewReal locates the binaries and, when not running as root, validates the
// configured privilege prefix. sudoPrefix comes from the configuration
// ("sudo -n"); pass nil to run the commands directly.
//
// Every read here is unprivileged first. Reading the system journal needs
// membership of systemd-journal, adm or wheel — which is the normal case on
// Fedora, Arch and Debian for anyone in wheel or sudo — and only a machine
// that refuses gets the escalated retry.
func NewReal(sudoPrefix []string, caps compat.Caps) (*Real, error) {
	real := &Real{caps: caps, now: time.Now}

	var err error
	real.journal, err = runner.New(runner.Options{
		Bin:             "journalctl",
		SearchPaths:     searchPaths["journalctl"],
		Timeout:         readTimeout,
		PrivilegedReads: &unprivileged,
		InstallHint:     installHint,
	})
	if err != nil {
		return nil, err
	}

	// The escalated twin. A machine with no usable `sudo -n` simply has no
	// fallback and no actions, which the screens say where the answer would
	// have been — it is not a reason to refuse to start.
	real.journalRoot, _ = runner.New(runner.Options{
		Bin:             "journalctl",
		SearchPaths:     searchPaths["journalctl"],
		SudoPrefix:      sudoPrefix,
		Timeout:         actionTimeout,
		PrivilegedReads: &privileged,
	})
	real.systemctl, _ = runner.New(runner.Options{
		Bin:             "systemctl",
		SearchPaths:     searchPaths["systemctl"],
		Timeout:         readTimeout,
		PrivilegedReads: &unprivileged,
	})
	real.analyze, _ = runner.New(runner.Options{
		Bin:             "systemd-analyze",
		SearchPaths:     searchPaths["systemd-analyze"],
		Timeout:         readTimeout,
		PrivilegedReads: &unprivileged,
	})
	real.install, _ = runner.New(runner.Options{
		Bin:         "install",
		SearchPaths: searchPaths["install"],
		Timeout:     readTimeout,
	})

	// The two escalated twins the retention form needs. Its drop-in goes into
	// /etc and journald has to be restarted for it to mean anything, and
	// neither is something the export's unprivileged `install` may do — a
	// machine with no usable `sudo -n` simply has no retention form, which
	// the storage screen says where the key would have been.
	real.installRoot, _ = runner.New(runner.Options{
		Bin:         "install",
		SearchPaths: searchPaths["install"],
		SudoPrefix:  sudoPrefix,
		Timeout:     readTimeout,
	})
	real.systemctlRoot, _ = runner.New(runner.Options{
		Bin:         "systemctl",
		SearchPaths: searchPaths["systemctl"],
		SudoPrefix:  sudoPrefix,
		Timeout:     actionTimeout,
	})
	return real, nil
}

// Name identifies the backend. It is the manifest's backend name, which is
// what the version probe is keyed on.
func (r *Real) Name() string { return "journald" }

// Describe names the backend for the header, and says how the journal was
// reached — which is the difference between the machine's log and this
// user's own.
//
// The kit runner's own Describe cannot answer this one. It says "(root)" for
// any runner with no escalation prefix, which is true of the plain journalctl
// runner by design — reading the journal is a group membership, not a root
// question — and printing "root" for a user who is nothing of the sort would
// be the most misleading word on the screen.
func (r *Real) Describe() string {
	switch {
	case r.access.Denied:
		return "journalctl — your own entries only"
	case r.access.Escalated && r.journalRoot != nil:
		return "journalctl via " + strings.Join(r.journalRoot.Privilege, " ")
	case os.Geteuid() == 0:
		return "journalctl (root)"
	default:
		return "journalctl"
	}
}

// Capabilities reports what this backend supports on this systemd.
func (r *Real) Capabilities() logs.Capabilities {
	caps := Capabilities(r.caps.Has(FeatureListBootsJSON))
	if r.installRoot == nil || r.systemctlRoot == nil {
		caps.SupportsRetention = false
		if caps.Unavailable == nil {
			caps.Unavailable = map[string]string{}
		}
		caps.Unavailable[logs.CapRetention] = "the retention drop-in goes into " +
			DropInDir + " and journald has to be restarted, and this machine " +
			"has no privilege escalation this tool can use"
	}
	return caps
}

// Preview renders the exact command line Run will execute. Every command goes
// through the runner of its own binary, so the preview carries the privilege
// prefix that binary will really be called with.
func (r *Real) Preview(cmd logs.Command) string {
	if run := r.runnerFor(cmd); run != nil {
		return run.Preview(cmd)
	}
	return cmd.String()
}

// runnerFor picks the runner that owns a command.
//
// journalctl is the one binary with two runners, and which of them a command
// belongs to is a property of the command: the three housekeeping verbs write
// to /var/log/journal and always escalate, and a read escalates only on a
// machine that already refused it unprivileged. Deciding it here, from the
// argv, is what keeps Preview and Run in agreement — the preview is built
// from the same answer the execution uses.
func (r *Real) runnerFor(cmd logs.Command) *runner.Runner {
	if len(cmd.Argv) == 0 {
		return nil
	}
	switch cmd.Argv[0] {
	case "journalctl":
		if needsRoot(cmd) || r.access.Escalated {
			if r.journalRoot != nil {
				return r.journalRoot
			}
		}
		return r.journal
	case "systemctl":
		// Listing units is a read anyone may do; restarting journald is not.
		if isUnitRestart(cmd) {
			return r.systemctlRoot
		}
		return r.systemctl
	case "install":
		// An export goes into the user's own home directory as the user; the
		// retention drop-in goes into /etc and cannot. Which of the two a
		// command is, is visible in its destination.
		if isDropInInstall(cmd) {
			return r.installRoot
		}
		return r.install
	case "systemd-analyze":
		return r.analyze
	default:
		return nil
	}
}

// isUnitRestart reports that a systemctl invocation restarts a unit rather
// than listing something.
func isUnitRestart(cmd logs.Command) bool {
	return len(cmd.Argv) > 1 && cmd.Argv[1] == "restart"
}

// isDropInInstall reports that an install invocation writes this tool's
// journald drop-in, which is the one file it puts outside a home directory.
func isDropInInstall(cmd logs.Command) bool {
	return len(cmd.Argv) > 0 && cmd.Argv[len(cmd.Argv)-1] == DropInPath
}

// needsRoot reports that a journalctl invocation changes something, or reads
// something a plain user cannot: the three housekeeping verbs.
func needsRoot(cmd logs.Command) bool {
	for _, arg := range cmd.Argv[1:] {
		switch {
		case strings.HasPrefix(arg, "--vacuum-"),
			arg == "--rotate",
			arg == "--verify":
			return true
		}
	}
	return false
}

// Run executes a previewed command.
func (r *Real) Run(ctx context.Context, cmd logs.Command) (string, error) {
	run := r.runnerFor(cmd)
	if run == nil {
		return "", fmt.Errorf("journald: %q is not available on this machine",
			firstArg(cmd))
	}
	return run.Run(ctx, cmd)
}

// firstArg names the binary a command wanted, for an error message.
func firstArg(cmd logs.Command) string {
	if len(cmd.Argv) == 0 {
		return "(empty command)"
	}
	return cmd.Argv[0]
}

// read runs one journalctl read, escalating only when the plain one was
// refused, and records which of the two answered.
//
// The escalation is the whole access story of this tool in one function.
// Reading the system journal is a group membership question, not a root
// question: a user in systemd-journal, adm or wheel gets everything and never
// sees a prompt. A user in none of them gets their own entries and a message
// on standard error, and it is that message — not an exit code, because
// journalctl still exits zero — that triggers the retry through `sudo -n`.
// If that is refused too, what is on screen is this user's own entries and
// the header says so rather than pretending the machine is quiet.
func (r *Real) read(ctx context.Context, cmd logs.Command) (string, error) {
	// A machine that refused once refuses every time, so once the escalated
	// read has answered the plain one is not tried again: it would double the
	// number of processes every screen costs to learn the same thing.
	if r.access.Escalated && r.journalRoot != nil {
		out, err := r.journalRoot.Read(ctx, cmd.Argv...)
		if err == nil && !refused(out) {
			return out, nil
		}
		return out, err
	}

	out, err := r.journal.Read(ctx, cmd.Argv...)
	if err == nil && !refused(out) {
		// A read that worked plainly is the normal case, and it also clears a
		// denial recorded earlier — a group membership can be granted while
		// the tool is open, and R is how a user finds that out.
		r.access = logs.Access{}
		return out, nil
	}
	if r.journalRoot == nil {
		r.access = deniedAccess(out, err)
		return out, err
	}

	escalated, escalatedErr := r.journalRoot.Read(ctx, cmd.Argv...)
	if escalatedErr == nil && !refused(escalated) {
		r.access = logs.Access{
			Escalated: true,
			Note: "the plain read was refused, so the journal was opened " +
				"through " + strings.Join(r.journalRoot.Privilege, " "),
		}
		return escalated, nil
	}
	// Both refused. The unprivileged answer is still the more useful one: it
	// is this user's own entries rather than nothing at all.
	r.access = deniedAccess(out, err)
	if err != nil {
		return out, err
	}
	return out, nil
}

// deniedAccess describes a read neither attempt could make.
func deniedAccess(out string, err error) logs.Access {
	note := "the system journal could not be opened: add your account to the " +
		"systemd-journal group (or adm, or wheel), or run this as root"
	if err != nil && strings.TrimSpace(out) == "" {
		note = runner.FirstLine(err.Error())
	}
	return logs.Access{Denied: true, Note: note}
}

// refused reports that journalctl printed the "not allowed to open the
// journal" message, which it does while still exiting zero.
func refused(out string) bool {
	for _, marker := range permissionMarkers {
		if strings.Contains(out, marker) {
			return true
		}
	}
	return false
}

// Load reads a window of the journal plus everything around it.
//
// Every layer is allowed to fail on its own: a machine whose systemd-analyze
// is missing still shows its entries, and one whose journal cannot be opened
// at all still shows the boots it can see and says why the rest is empty.
// Only a failure to run journalctl at all is an error.
func (r *Real) Load(ctx context.Context, filter logs.Filter) (logs.Model, error) {
	model := logs.Model{Backend: r.Name(), Filter: filter}

	entries, cmd, err := r.ReadEntries(ctx, filter)
	if err != nil {
		return logs.Model{}, err
	}
	model.Entries, model.Command = entries, cmd
	model.Access = r.access
	model.Stats = logs.ComputeStats(entries, filter.WithLines(filter.Lines).Lines)

	model.Boots = r.readBoots(ctx)
	model.Units, model.UnitsSource = r.readUnits(ctx)
	storage, storageErr := r.ReadStorage(ctx)
	if storageErr != nil && storage.ConfUnavailable == "" {
		storage.ConfUnavailable = runner.FirstLine(storageErr.Error())
	}
	model.Storage = storage
	return model, nil
}

// ReadEntries reads one window of the journal.
func (r *Real) ReadEntries(ctx context.Context, filter logs.Filter) ([]logs.Entry,
	logs.Command, error) {
	cmd, err := BuildRead(filter.WithLines(filter.Lines))
	if err != nil {
		return nil, logs.Command{}, err
	}
	out, err := r.read(ctx, cmd)
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, cmd, err
	}
	return ParseEntries(out), cmd, nil
}

// ReadSince reads only what arrived after a cursor, which is what follow mode
// does every couple of seconds.
func (r *Real) ReadSince(ctx context.Context, filter logs.Filter,
	cursor string) ([]logs.Entry, error) {
	if cursor == "" {
		entries, _, err := r.ReadEntries(ctx, filter)
		return entries, err
	}
	cmd, err := BuildFollow(filter.WithLines(filter.Lines), cursor)
	if err != nil {
		return nil, err
	}
	out, err := r.read(ctx, cmd)
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, err
	}
	return ParseEntries(out), nil
}

// readBoots lists the boots the journal still holds, in JSON where this
// systemd can print it and from its table where it cannot.
func (r *Real) readBoots(ctx context.Context) []logs.Boot {
	hasJSON := r.caps.Has(FeatureListBootsJSON)
	out, err := r.read(ctx, BuildListBoots(hasJSON))
	if err != nil && strings.TrimSpace(out) == "" {
		return nil
	}
	if hasJSON {
		if boots := ParseBootsJSON(out); len(boots) > 0 {
			return boots
		}
		// A version that reported itself as new enough and then printed a
		// table is not worth failing over: the table parses too.
	}
	return ParseBootsTable(out)
}

// readUnits lists the units the picker offers, and says where they came from.
//
// The journal is asked first: the units that have written to it are the only
// ones filtering by unit can return anything for, and there are one or two
// hundred fewer of them than the machine has units. `systemctl list-units` is
// the fallback, for a journal too small or too restricted to answer — the
// picker is then long, and the picker's own "type a unit name" entry is what
// makes either list reachable anyway.
func (r *Real) readUnits(ctx context.Context) ([]string, string) {
	if out, err := r.read(ctx, BuildListJournalUnits()); err == nil {
		if units := ParseJournalUnits(out); len(units) > 0 {
			return units, logs.UnitsFromJournal
		}
	}
	if r.systemctl == nil {
		return nil, ""
	}
	out, err := r.systemctl.Read(ctx, BuildListUnits().Argv...)
	if err != nil {
		return nil, ""
	}
	units := ParseUnits(out)
	if len(units) == 0 {
		return nil, ""
	}
	return units, logs.UnitsFromSystemd
}

// ReadStorage reads what the journal costs and the configuration in force.
func (r *Real) ReadStorage(ctx context.Context) (logs.Storage, error) {
	storage := logs.Storage{}

	// Whether the journal survives a reboot is a directory, not a setting:
	// journald writes to /var/log/journal when it exists and to /run when it
	// does not, whatever Storage= says, because `auto` is the default and
	// `auto` means exactly that.
	if info, err := os.Stat(JournalDir); err == nil && info.IsDir() {
		storage.Persistent = true
		storage.PersistentNote = PersistentNote
	} else {
		storage.PersistentNote = NotPersistentNote
	}

	out, err := r.read(ctx, BuildDiskUsage())
	if err == nil {
		storage.DiskUsage, storage.Bytes = ParseDiskUsage(out)
	}

	if r.analyze == nil {
		storage.ConfUnavailable = "systemd-analyze is not installed, so the " +
			"configuration in force cannot be shown"
		return storage, nil
	}
	cmd := BuildCatConfig()
	storage.ConfSource = r.analyze.Preview(cmd)
	conf, confErr := r.analyze.Read(ctx, cmd.Argv...)
	if confErr != nil {
		storage.ConfUnavailable = runner.FirstLine(confErr.Error())
		return storage, nil
	}
	storage.Conf = ParseCatConfig(conf)
	if len(storage.Conf) == 0 {
		storage.ConfUnavailable = "`" + storage.ConfSource +
			"` printed nothing that parsed as a setting"
	}
	return storage, nil
}

// CommandFor renders the journalctl invocation a filter stands for.
func (r *Real) CommandFor(filter logs.Filter) logs.Command {
	cmd, err := BuildRead(filter.WithLines(filter.Lines))
	if err != nil {
		return logs.Command{Argv: []string{"journalctl"}, Description: err.Error()}
	}
	return cmd
}

// CommandForEntry renders the invocation that shows one entry in full.
func (r *Real) CommandForEntry(entry logs.Entry) logs.Command {
	cmd, err := BuildEntry(entry)
	if err != nil {
		return logs.Command{Argv: []string{"journalctl"}, Description: err.Error()}
	}
	return cmd
}

// BuildVacuumSize shrinks the journal to a size.
func (r *Real) BuildVacuumSize(size string) (logs.Command, error) {
	return BuildVacuumSize(size)
}

// BuildVacuumTime shrinks the journal to an age.
func (r *Real) BuildVacuumTime(age string) (logs.Command, error) {
	return BuildVacuumTime(age)
}

// BuildRotate closes the active journal files and starts new ones.
func (r *Real) BuildRotate() (logs.Command, error) { return BuildRotate() }

// BuildVerify checks the journal's own consistency.
func (r *Real) BuildVerify() (logs.Command, error) { return BuildVerify() }

// BuildExport reads the current window as text, stages it in a private
// temporary file and returns the plan that installs it.
//
// The read happens here, before the user is asked anything, and that is the
// point: what the confirm dialog offers to copy is a file that already exists
// and whose size it can state, and `install` copies exactly that one. There
// is no shell and no redirection anywhere in the path — `journalctl > file`
// would need one, and a tool whose promise is "the command you saw is the
// command that ran" cannot hand a string to a shell to find out.
func (r *Real) BuildExport(ctx context.Context, filter logs.Filter,
	path string) (logs.ExportPlan, error) {
	if err := CheckExportPath(path); err != nil {
		return logs.ExportPlan{}, err
	}
	source, err := BuildExportRead(filter.WithLines(filter.Lines))
	if err != nil {
		return logs.ExportPlan{}, err
	}
	out, err := r.read(ctx, source)
	if err != nil && strings.TrimSpace(out) == "" {
		return logs.ExportPlan{}, err
	}
	if strings.TrimSpace(out) == "" {
		return logs.ExportPlan{}, logs.ErrEmptyExport
	}

	content := strings.TrimRight(out, "\n") + "\n"
	temp, err := stageFile(path, content)
	if err != nil {
		return logs.ExportPlan{}, err
	}
	installCmd, err := BuildInstallExport(temp, path)
	if err != nil {
		return logs.ExportPlan{}, err
	}

	plan := logs.ExportPlan{
		Path:     path,
		Source:   source,
		TempPath: temp,
		Lines:    strings.Count(content, "\n"),
		Bytes:    len(content),
		Preview:  previewLines(content, exportPreviewLines),
		Commands: []logs.Command{installCmd},
	}
	if _, err := os.Stat(path); err == nil {
		plan.Warning = path + " already exists and will be overwritten."
	}
	return plan, nil
}

// exportPreviewLines is how much of the file the confirm dialog shows. It is
// enough to recognise the window and short enough to leave the command line
// on screen, which is the thing that must never be missed.
const exportPreviewLines = 6

// previewLines takes the first few lines of a text.
func previewLines(text string, count int) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) <= count {
		return text
	}
	return strings.Join(lines[:count], "\n") + "\n… " +
		fmt.Sprint(len(lines)-count) + " more lines"
}

// stageFile writes the pending export to a private temporary directory and
// returns its path.
func stageFile(destination, content string) (string, error) {
	dir, err := os.MkdirTemp("", "tui-logs-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, filepath.Base(destination))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// ReadRetention reads the drop-in this tool owns, and the configuration in
// force behind it, so the form opens on what the machine really says.
func (r *Real) ReadRetention(ctx context.Context) (logs.Retention, error) {
	retention, err := ReadDropIn(DropInPath)
	if err != nil {
		return logs.Retention{}, err
	}
	// The effective values are what the form falls back to for a key the
	// drop-in does not set, which on a first run is all of them.
	storage, storageErr := r.ReadStorage(ctx)
	if storageErr == nil {
		retention.Effective = EffectiveRetention(storage.Conf)
	}
	return retention, nil
}

// BuildRetention renders the drop-in the answers describe, stages it in a
// private temporary file, and returns the plan that installs it.
//
// The staging is what makes the diff honest: the file the dialog shows a diff
// of already exists, and `install` copies exactly that one. Nothing is written
// under /etc until the confirmed commands run.
func (r *Real) BuildRetention(values map[string]string) (logs.RetentionPlan, error) {
	content, err := RenderDropIn(values)
	if err != nil {
		return logs.RetentionPlan{}, err
	}
	existing, err := ReadDropIn(DropInPath)
	if err != nil {
		return logs.RetentionPlan{}, err
	}
	temp, err := stageFile(DropInPath, content)
	if err != nil {
		return logs.RetentionPlan{}, err
	}
	return RetentionPlanFor(existing, content, temp)
}

// SuggestExportPath is where the export dialog starts.
func (r *Real) SuggestExportPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return DefaultExportPath(home, r.now().Format("20060102-150405"))
}
