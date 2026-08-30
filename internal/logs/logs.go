// Package logs defines the backend-agnostic model tui-logs renders and the
// interface every log backend satisfies. The UI knows only these types: it
// never builds a journalctl, systemctl or systemd-analyze argv itself.
// Mutations are Command values produced by the backend, shown in a preview
// dialog and only then executed.
package logs

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tui-tools/tui-kit/runner"
)

// Command is a single invocation the user is about to run. Argv excludes any
// privilege wrapper: the backend adds it when previewing and when executing.
//
// It is an alias rather than a type of its own, so a backend hands the very
// value the confirm dialog displayed straight to the kit runner, with no
// conversion in between. That identity is what makes the preview a promise.
type Command = runner.Command

// Priority is a syslog level, 0 (emerg) to 7 (debug), as the journal records
// it in PRIORITY.
type Priority int

// PriorityAny is the sentinel that means "do not filter on a level". It is
// -1 rather than a separate type, so a filter carries one field.
const PriorityAny Priority = -1

// The eight syslog priorities, written out rather than counted with iota, so
// the number next to each name is the number journalctl prints.
const (
	PriEmerg   Priority = 0
	PriAlert   Priority = 1
	PriCrit    Priority = 2
	PriErr     Priority = 3
	PriWarning Priority = 4
	PriNotice  Priority = 5
	PriInfo    Priority = 6
	PriDebug   Priority = 7
)

// priorityNames are the words journalctl accepts for -p, indexed by level.
var priorityNames = []string{
	"emerg", "alert", "crit", "err", "warning", "notice", "info", "debug",
}

// Name is the word journalctl uses for a priority, and what the table shows.
// An out-of-range level renders as its number, because a journal that carries
// one is a journal worth seeing rather than hiding.
func (p Priority) Name() string {
	if p < 0 || int(p) >= len(priorityNames) {
		return strconv.Itoa(int(p))
	}
	return priorityNames[p]
}

// Severe reports a priority at or above `err`, which is what the counts, the
// sparkline and the row colours are about.
func (p Priority) Severe() bool { return p >= 0 && p <= PriErr }

// Warned reports a priority worth colouring but not counting as an error.
func (p Priority) Warned() bool { return p == PriWarning || p == PriNotice }

// ParsePriority reads a level written as a number or as one of journalctl's
// own words.
func ParsePriority(text string) (Priority, bool) {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" || text == "any" {
		return PriorityAny, true
	}
	for level, name := range priorityNames {
		if text == name {
			return Priority(level), true
		}
	}
	if level, err := strconv.Atoi(text); err == nil && level >= 0 && level <= 7 {
		return Priority(level), true
	}
	return PriorityAny, false
}

// Entry is one journal record.
//
// The named fields are the ones the table and the detail screen are built on;
// Fields carries the record in full, because the journal is a schema-less
// store and the field that explains an entry is often one no tool knows about
// in advance.
type Entry struct {
	// Cursor is the journal's own opaque position for this entry. It is what
	// makes a follow read incremental, and what lets the detail screen offer a
	// journalctl command that reproduces exactly this line.
	Cursor string
	// Realtime is when the entry was written, from __REALTIME_TIMESTAMP.
	Realtime time.Time
	// Priority is the syslog level, PriorityAny when the record carried none.
	Priority Priority
	// Unit is _SYSTEMD_UNIT (or _SYSTEMD_USER_UNIT on the user journal), and
	// Identifier is SYSLOG_IDENTIFIER. A kernel line has the second and not
	// the first, which is why both are kept.
	Unit       string
	Identifier string
	// Message is MESSAGE, which is the line a reader came for.
	Message string
	// PID and UID are the process that logged it, 0 and -1 when unknown.
	PID int
	UID int
	// Comm, Exe and Cmdline are what that process was.
	Comm    string
	Exe     string
	Cmdline string
	// CodeFile, CodeLine and CodeFunc are set by programs that log through
	// sd-journal, and name the source line that produced the entry.
	CodeFile string
	CodeLine int
	CodeFunc string
	// MessageID is the catalogue id of a well-known event, empty for most
	// entries.
	MessageID string
	// BootID, Hostname and Transport are the record's provenance.
	BootID    string
	Hostname  string
	Transport string
	// Fields is the whole record, field name to value, including the ones
	// above and the journal's own double-underscore addresses.
	Fields map[string]string
}

// Source names the entry the way the table's third column does: the unit when
// there is one, the syslog identifier otherwise.
func (e Entry) Source() string {
	switch {
	case e.Unit != "":
		return e.Unit
	case e.Identifier != "":
		return e.Identifier
	case e.Comm != "":
		return e.Comm
	default:
		return "—"
	}
}

// Time renders the entry's timestamp the way the table shows it.
func (e Entry) Time() string {
	if e.Realtime.IsZero() {
		return "—"
	}
	return e.Realtime.Format("Jan 02 15:04:05")
}

// Filter is what the user has narrowed the journal down to. Every field maps
// onto one journalctl argument, and the backend renders the whole set into a
// single argv the header and the detail screen can both show.
type Filter struct {
	// Unit is -u, empty for every unit.
	Unit string
	// Priority is -p, PriorityAny for every level.
	Priority Priority
	// Boot is -b: a boot id, a relative offset ("0", "-1"), or empty for
	// every boot the journal still holds.
	Boot string
	// Since and Until are --since and --until, in journalctl's own grammar
	// ("-1h", "today", "2026-08-30 09:00").
	Since string
	Until string
	// Grep is --grep, a case-insensitive pattern journalctl matches against
	// MESSAGE. It is the server-side filter: a client-side one would only
	// search the window that was already read.
	Grep string
	// Kernel restricts the read to the kernel ring buffer (-k).
	Kernel bool
	// User reads the calling user's own journal (--user) instead of the
	// system one.
	User bool
	// Lines bounds one read (-n).
	Lines int
}

// DefaultLines is how much backlog one read pulls back. The journal on a
// busy machine is millions of entries, and a screen shows tens.
const DefaultLines = 500

// WithLines returns the filter with a line budget, defaulted when unset.
func (f Filter) WithLines(lines int) Filter {
	if lines <= 0 {
		lines = DefaultLines
	}
	f.Lines = lines
	return f
}

// Label renders the filter as the one line the header shows.
func (f Filter) Label() string {
	var parts []string
	if f.User {
		parts = append(parts, "user journal")
	}
	if f.Kernel {
		parts = append(parts, "kernel")
	}
	if f.Unit != "" {
		parts = append(parts, f.Unit)
	}
	if f.Priority != PriorityAny {
		parts = append(parts, f.Priority.Name()+" and worse")
	}
	if f.Boot != "" {
		parts = append(parts, "boot "+f.Boot)
	}
	if f.Since != "" {
		parts = append(parts, "since "+f.Since)
	}
	if f.Until != "" {
		parts = append(parts, "until "+f.Until)
	}
	if f.Grep != "" {
		parts = append(parts, "matching "+strconv.Quote(f.Grep))
	}
	if len(parts) == 0 {
		return "everything"
	}
	return strings.Join(parts, "  ·  ")
}

// Preset is one entry of the time picker: a label and the window it sets.
type Preset struct {
	// Label is what the picker shows.
	Label string
	// Since is the --since value, empty for no lower bound.
	Since string
	// Boot is the -b value the preset implies, empty for every boot.
	Boot string
}

// Presets are the windows the picker offers. They are journalctl's own
// grammar rather than timestamps computed here, so the command the header
// shows is one that can be pasted and re-run tomorrow with the same meaning.
func Presets() []Preset {
	return []Preset{
		{Label: "last 15 minutes", Since: "-15m"},
		{Label: "last hour", Since: "-1h"},
		{Label: "last 6 hours", Since: "-6h"},
		{Label: "today", Since: "today"},
		{Label: "this boot", Boot: "0"},
		{Label: "everything", Since: ""},
	}
}

// Boot is one entry of `journalctl --list-boots`.
type Boot struct {
	// Index is the offset journalctl uses: 0 is the running boot, -1 the one
	// before it.
	Index int
	// ID is the boot's 32-character id.
	ID string
	// First and Last are the timestamps of its first and last entry.
	First time.Time
	Last  time.Time
}

// Label renders a boot for the picker: the offset, the dates, and how long it
// lasted.
func (b Boot) Label() string {
	label := strconv.Itoa(b.Index)
	if b.Index == 0 {
		label = "0 (this boot)"
	}
	if b.First.IsZero() {
		return label + "  " + b.ID
	}
	return label + "  " + b.First.Format("2006-01-02 15:04") + " → " +
		b.Last.Format("2006-01-02 15:04") + "  " + b.ID
}

// Count is one row of a "top" list.
type Count struct {
	Name  string
	Count int
}

// ConfSetting is one setting of the effective journald configuration.
//
// It carries JSON tags because `--check` prints it, and a report a shell
// script greps has to have field names somebody can predict.
type ConfSetting struct {
	// Key and Value are the setting as it is in force.
	Key   string `json:"key"`
	Value string `json:"value"`
	// File is the file the winning line came from, and Line its number.
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	// Default reports that no file set it and this is the compiled-in value,
	// which `systemd-analyze cat-config` prints commented out.
	Default bool `json:"default"`
}

// Storage is what the journal costs and where it lives.
type Storage struct {
	// DiskUsage is `journalctl --disk-usage` verbatim, and Bytes the size it
	// named, 0 when it could not be parsed.
	DiskUsage string `json:"diskUsage,omitempty"`
	Bytes     int64  `json:"bytes"`
	// Persistent reports that /var/log/journal exists, which is what decides
	// whether the journal survives a reboot.
	Persistent bool `json:"persistent"`
	// PersistentNote explains what that means and how to change it. tui-logs
	// does not change it: making a journal persistent is a decision with a
	// disk budget behind it.
	PersistentNote string `json:"persistentNote,omitempty"`
	// Conf is the effective journald.conf, and ConfSource the command that
	// produced it.
	Conf       []ConfSetting `json:"conf,omitempty"`
	ConfSource string        `json:"confSource,omitempty"`
	// ConfUnavailable explains why Conf is empty, when it is.
	ConfUnavailable string `json:"confUnavailable,omitempty"`
}

// Stats is what the window the user is looking at is made of.
type Stats struct {
	// Total is how many entries the read returned, and Errors how many of
	// them were `err` or worse.
	Total  int
	Errors int
	// Warnings counts `warning` and `notice`.
	Warnings int
	// TopUnits are the noisiest sources in the window, most first.
	TopUnits []Count
	// PerBoot is how many of the entries fell in each boot, newest boot
	// first, and is what makes a boot with a burst in it visible.
	PerBoot []Count
	// ErrorBuckets counts the `err`-and-worse entries of the window into
	// SparkBuckets equal slices of it, oldest first. It is the sparkline.
	//
	// The buckets divide the window rather than being an hour each, because a
	// window here is whatever the filters made it: on a busy machine five
	// hundred entries is four minutes, and a row of empty hours would say
	// nothing about it.
	ErrorBuckets []int
	// From and To are the span the buckets cover.
	From time.Time
	To   time.Time
	// Truncated reports that the read hit its line budget, so the counts
	// describe the window that was read rather than everything in it.
	Truncated bool
}

// Access records how the journal was actually opened, which is the first
// thing a reader has to know: reading the system journal needs membership of
// systemd-journal, adm or wheel, or root.
type Access struct {
	// Escalated reports that the read went through the privilege prefix
	// because the plain one was refused.
	Escalated bool
	// Note is the sentence the header shows about it.
	Note string
	// Denied reports that neither read was permitted, so what is on screen is
	// this user's own entries rather than the machine's.
	Denied bool
}

// Model is the whole picture tui-logs renders.
type Model struct {
	// Backend names the implementation that produced this model.
	Backend string
	// Filter is what was asked for, and Command the argv that asked.
	Filter  Filter
	Command Command
	// Entries are the records that came back, newest first.
	Entries []Entry
	// Units are the unit names the picker offers, from the machine's own
	// unit list rather than from the entries on screen.
	Units []string
	// Boots are what `journalctl --list-boots` reported.
	Boots []Boot
	// Storage is what the journal costs, and Stats what the window holds.
	Storage Storage
	Stats   Stats
	// Access records how the journal was opened.
	Access Access
	// Unavailable explains an empty screen when the read itself failed.
	Unavailable string
}

// BootFor returns the boot with an id or offset, and whether there is one.
func (m Model) BootFor(ref string) (Boot, bool) {
	for _, boot := range m.Boots {
		if boot.ID == ref || strconv.Itoa(boot.Index) == ref {
			return boot, true
		}
	}
	return Boot{}, false
}

// Newest is the most recent entry's cursor, which is where a follow read
// picks up from. It is the first entry because the list is newest first.
func (m Model) Newest() string {
	if len(m.Entries) == 0 {
		return ""
	}
	return m.Entries[0].Cursor
}

// ComputeStats counts a window of entries. It lives here rather than in the
// backend because it is arithmetic over the model, and a second backend
// should not have to reimplement it.
func ComputeStats(entries []Entry, limit int) Stats {
	stats := Stats{Total: len(entries), Truncated: limit > 0 && len(entries) >= limit}
	byUnit := map[string]int{}
	byBoot := map[string]int{}
	var bootOrder []string

	for _, entry := range entries {
		switch {
		case entry.Priority.Severe():
			stats.Errors++
		case entry.Priority.Warned():
			stats.Warnings++
		}
		byUnit[entry.Source()]++
		if entry.BootID != "" {
			if _, seen := byBoot[entry.BootID]; !seen {
				bootOrder = append(bootOrder, entry.BootID)
			}
			byBoot[entry.BootID]++
		}
		if entry.Realtime.IsZero() {
			continue
		}
		if stats.From.IsZero() || entry.Realtime.Before(stats.From) {
			stats.From = entry.Realtime
		}
		if entry.Realtime.After(stats.To) {
			stats.To = entry.Realtime
		}
	}

	stats.TopUnits = topCounts(byUnit, TopCount)
	// The boots keep the order the entries arrived in, which is newest first,
	// rather than being sorted by volume: a boot list that reordered itself
	// by noise would be unreadable as a timeline.
	for _, id := range bootOrder {
		stats.PerBoot = append(stats.PerBoot, Count{Name: id, Count: byBoot[id]})
	}
	stats.ErrorBuckets = errorBuckets(entries, stats.From, stats.To)
	return stats
}

// TopCount bounds every "top" list in the tool.
const TopCount = 5

// SparkBuckets is how many buckets the error sparkline carries. It is 24
// because a day divided into hours is the shape most people already read a
// sparkline as, and because that many blocks still fit next to a label on a
// narrow terminal.
const SparkBuckets = 24

// errorBuckets buckets the severe entries of a window into SparkBuckets
// slots between its first and last entry.
func errorBuckets(entries []Entry, from, to time.Time) []int {
	if from.IsZero() || !to.After(from) {
		return nil
	}
	buckets := make([]int, SparkBuckets)
	span := to.Sub(from)
	for _, entry := range entries {
		if !entry.Priority.Severe() || entry.Realtime.IsZero() {
			continue
		}
		offset := entry.Realtime.Sub(from)
		slot := int(float64(offset) / float64(span) * float64(SparkBuckets))
		buckets[min(max(slot, 0), SparkBuckets-1)]++
	}
	return buckets
}

// topCounts turns a tally into the busiest few, most first and ties broken by
// name so the list does not shuffle between reads.
func topCounts(counts map[string]int, limit int) []Count {
	out := make([]Count, 0, len(counts))
	for name, count := range counts {
		out = append(out, Count{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// CapBoots names the boot list in Capabilities.Unavailable. It is here rather
// than in a backend because the UI is what asks the question, and the UI
// knows only this package.
const CapBoots = "boots"

// Capabilities tells the UI what a backend supports, so the key map is built
// from the backend rather than hardcoded.
type Capabilities struct {
	// SupportsFollow reports that a follow read is possible.
	SupportsFollow bool
	// SupportsVacuum, SupportsRotate and SupportsVerify report the
	// housekeeping actions.
	SupportsVacuum bool
	SupportsRotate bool
	SupportsVerify bool
	// SupportsExport reports that a window can be written to a file.
	SupportsExport bool
	// SupportsUserJournal reports that --user is a journal this backend can
	// read.
	SupportsUserJournal bool
	// VacuumSizes and VacuumTimes are the values the housekeeping pickers
	// offer, in the order they offer them.
	VacuumSizes []string
	VacuumTimes []string
	// Unavailable explains a capability the running backend version does not
	// have, keyed by a name the UI can show. It is what a version gate turns
	// into on screen.
	Unavailable map[string]string
}

// Reason returns the sentence explaining why something is unavailable, and
// whether there is one.
func (c Capabilities) Reason(name string) (string, bool) {
	reason, ok := c.Unavailable[name]
	return reason, ok
}

// FollowInterval is how often a follow read asks for what is new. It is a
// re-read rather than a `journalctl -f` process, because the family's promise
// is that every invocation is one previewable argv that starts and ends.
const FollowInterval = 2 * time.Second

// Backend is the boundary between the UI and the machine. Load and the Read*
// methods read; the Build* methods turn user intent into previewable
// Commands; Run executes a Command the user confirmed. Nothing else may
// mutate the system.
type Backend interface {
	// Name is the backend identifier ("journald").
	Name() string
	// Describe is the one-line summary shown in the header.
	Describe() string
	// Capabilities reports what this backend supports.
	Capabilities() Capabilities

	// Preview renders the exact command line Run will execute, privilege
	// wrapper included. This is the text shown in the confirm dialog.
	Preview(cmd Command) string

	// Load reads a window of the journal plus everything around it: the unit
	// list, the boots and what the journal costs on disk.
	Load(ctx context.Context, filter Filter) (Model, error)
	// ReadEntries re-reads only the entries, which is what changing a filter
	// does.
	ReadEntries(ctx context.Context, filter Filter) ([]Entry, Command, error)
	// ReadSince reads only what arrived after a cursor, which is what follow
	// mode does.
	ReadSince(ctx context.Context, filter Filter, cursor string) ([]Entry, error)
	// ReadStorage re-reads the disk usage and the effective configuration.
	ReadStorage(ctx context.Context) (Storage, error)
	// Run executes a previously previewed command.
	Run(ctx context.Context, cmd Command) (string, error)

	// CommandFor renders the journalctl invocation a filter stands for, so
	// the screen can always show the command behind what it is displaying.
	CommandFor(filter Filter) Command
	// CommandForEntry renders the invocation that shows one entry in full,
	// which is how a reader reproduces a line outside the tool.
	CommandForEntry(entry Entry) Command

	// BuildVacuumSize and BuildVacuumTime shrink the journal.
	BuildVacuumSize(size string) (Command, error)
	BuildVacuumTime(age string) (Command, error)
	// BuildRotate closes the active journal files and starts new ones.
	BuildRotate() (Command, error)
	// BuildVerify checks the journal's own consistency. It is a read, and is
	// still confirmed because it walks every file and can take minutes.
	BuildVerify() (Command, error)
	// BuildExport writes the current window to a file. It returns the plan
	// rather than a bare command because the content is staged and checked
	// first, the same way a configuration change is.
	BuildExport(ctx context.Context, filter Filter, path string) (ExportPlan, error)
	// SuggestExportPath is the path the export prompt starts on, which is a
	// backend question because the backend is what knows where it may write.
	SuggestExportPath() string
}

// ExportPlan is a window of the journal about to be written to a file: what
// was read, how big it is, and the exact command that installs it.
type ExportPlan struct {
	// Path is the destination, always under the user's home directory.
	Path string
	// Source is the journalctl invocation that produced the content.
	Source Command
	// TempPath is the staging file the install command copies from.
	TempPath string
	// Lines and Bytes describe what was staged, so the dialog can say how
	// much is about to be written.
	Lines int
	Bytes int
	// Preview is the first few lines of the file, for the dialog.
	Preview string
	// Warning is a caveat the dialog must show, empty when there is none.
	Warning string
	// Commands are run in order, and are what the confirm dialog shows.
	Commands []Command
}

// ErrEmptyExport reports that the filter matched nothing, so there is no file
// worth writing.
var ErrEmptyExport = fmt.Errorf("this filter matched no entries, so there is nothing to export")
