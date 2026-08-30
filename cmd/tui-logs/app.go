package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-logs/internal/logs"
)

// screen is one of the three views the tool is made of. They are tabs rather
// than nested screens because they answer three separate questions about the
// same journal — what does it say, what is it made of, what does it cost —
// and a reader arrives with one of them already in mind.
type screen int

const (
	screenEntries screen = iota
	screenStats
	screenStorage
	screenCount
)

// title names a screen for the tab bar.
func (s screen) title() string {
	switch s {
	case screenStats:
		return "stats"
	case screenStorage:
		return "storage"
	default:
		return "entries"
	}
}

// mode is the dialog the app currently has open. Only one is open at a time,
// which keeps the update loop flat.
type mode int

const (
	modeBrowse mode = iota
	modeDetail
	modeConfirm
	modeInput
	modePicker
	modeHelp
)

// The fields a picker or a prompt can be filling. They are named rather than
// numbered so the handler that receives the answer knows what it answered.
const (
	pickUnit        = "unit"
	pickPriority    = "priority"
	pickBoot        = "boot"
	pickWindow      = "window"
	pickVacuumSize  = "vacuum-size"
	pickVacuumTime  = "vacuum-time"
	promptGrep      = "grep"
	promptExport    = "export"
	anyChoice       = "(any)"
	everyUnitChoice = "(every unit)"
	everyBootChoice = "(every boot)"
)

// readTimeout bounds one background read from the UI.
const readTimeout = 60 * time.Second

// actionTimeout bounds a housekeeping command. `--verify` walks the whole
// journal, and on a large one that is minutes.
const actionTimeout = 15 * time.Minute

// app is the tui-logs Bubble Tea model.
type app struct {
	backend logs.Backend
	theme   theme.Theme
	caps    logs.Capabilities
	// backendCompat is what the version probe found, rendered in the header.
	backendCompat compat.Result

	model  logs.Model
	filter logs.Filter

	width, height int
	screen        screen
	// cursor and offset are per screen, so moving between tabs does not lose
	// the row the reader was on.
	cursor [screenCount]int
	offset [screenCount]int

	// detailOffset scrolls the detail screen.
	detailOffset int

	// following reports that the tool is asking for new entries every couple
	// of seconds, and followSeq bounds the ticks to the current follow: a
	// tick from a follow that was switched off must not restart it.
	following bool
	followSeq int

	mode      mode
	confirm   ui.Confirm
	input     ui.Input
	picker    ui.Picker
	pickerFor string
	inputFor  string

	status     string
	statusKind ui.StatusKind
	loading    bool
	// loadFailed reports that the last read returned an error, so the empty
	// state does not claim the machine simply has nothing to say.
	loadFailed bool
	// busy blocks input while a command runs.
	busy bool
}

// loadedMsg carries the result of a full read.
type loadedMsg struct {
	model logs.Model
	err   error
}

// entriesMsg carries the result of re-reading only the entries, which is what
// changing a filter does.
type entriesMsg struct {
	entries []logs.Entry
	command logs.Command
	err     error
}

// freshMsg carries what a follow read found, and the follow it belongs to.
type freshMsg struct {
	seq     int
	entries []logs.Entry
	err     error
}

// tickMsg wakes the follow loop.
type tickMsg struct{ seq int }

// exportMsg carries a staged export, which is a read and so happens in the
// background like every other read.
type exportMsg struct {
	plan logs.ExportPlan
	err  error
}

// ranMsg carries the result of running a plan.
type ranMsg struct {
	// title is the plan's title, echoed in the status line.
	title  string
	output string
	err    error
}

// plan is what a confirm dialog is holding: one or more commands, run in
// order. Every action in this tool is a single command; the plan is a slice
// anyway, so a second one could be added without changing the dialog.
type plan struct {
	title    string
	commands []logs.Command
}

// newApp builds the model around a backend.
func newApp(backend logs.Backend, th theme.Theme, filter logs.Filter,
	backendCompat compat.Result) *app {
	a := &app{
		backend:       backend,
		theme:         th,
		caps:          backend.Capabilities(),
		backendCompat: backendCompat,
		filter:        filter.WithLines(filter.Lines),
		width:         80,
		height:        24,
		loading:       true,
	}
	if th.Warning != "" {
		a.setStatus(ui.StatusWarn, th.Warning)
	}
	return a
}

// Init starts the first read.
func (a *app) Init() tea.Cmd { return a.load() }

// load reads the journal and everything around it in the background.
func (a *app) load() tea.Cmd {
	backend, filter := a.backend, a.filter
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		model, err := backend.Load(ctx, filter)
		return loadedMsg{model: model, err: err}
	}
}

// reread reads only the entries, which is what changing a filter does. It is
// its own command because the boots, the unit list and the disk usage do not
// change when a filter does, and re-reading them would make every keystroke
// cost five processes instead of one.
func (a *app) reread() tea.Cmd {
	backend, filter := a.backend, a.filter
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		entries, cmd, err := backend.ReadEntries(ctx, filter)
		return entriesMsg{entries: entries, command: cmd, err: err}
	}
}

// follow asks for what arrived after the newest entry on screen.
func (a *app) follow() tea.Cmd {
	backend, filter, cursor, seq := a.backend, a.filter, a.model.Newest(), a.followSeq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		entries, err := backend.ReadSince(ctx, filter, cursor)
		return freshMsg{seq: seq, entries: entries, err: err}
	}
}

// tick schedules the next follow read.
func (a *app) tick() tea.Cmd {
	seq := a.followSeq
	return tea.Tick(logs.FollowInterval, func(time.Time) tea.Msg {
		return tickMsg{seq: seq}
	})
}

// run executes a confirmed plan in the background, one command at a time,
// stopping at the first failure.
func (a *app) run(p plan) tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		var outputs []string
		for _, cmd := range p.commands {
			out, err := backend.Run(ctx, cmd)
			if err != nil {
				return ranMsg{title: p.title, output: out, err: err}
			}
			if trimmed := strings.TrimSpace(out); trimmed != "" {
				outputs = append(outputs, trimmed)
			}
		}
		return ranMsg{title: p.title, output: strings.Join(outputs, "; ")}
	}
}

// setStatus records a plain message for the status line.
func (a *app) setStatus(kind ui.StatusKind, message string) {
	a.status = message
	a.statusKind = kind
}

// setStatusf records a formatted message for the status line.
func (a *app) setStatusf(kind ui.StatusKind, format string, args ...any) {
	a.setStatus(kind, fmt.Sprintf(format, args...))
}

// Update is the main event loop.
func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.clampCursor()
		return a, nil

	case loadedMsg:
		a.loading = false
		if msg.err != nil {
			a.loadFailed = true
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, nil
		}
		a.loadFailed = false
		a.model = msg.model
		a.clampCursor()
		return a, nil

	case entriesMsg:
		a.loading = false
		if msg.err != nil {
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, nil
		}
		a.model.Entries = msg.entries
		a.model.Command = msg.command
		a.model.Filter = a.filter
		a.model.Stats = logs.ComputeStats(msg.entries, a.filter.Lines)
		a.cursor[screenEntries], a.offset[screenEntries] = 0, 0
		a.clampCursor()
		return a, nil

	case freshMsg:
		if msg.seq != a.followSeq || !a.following {
			// A late answer from a follow that has been switched off, or from
			// a filter that has since changed. Dropping it is what stops the
			// screen filling with entries nobody asked for.
			return a, nil
		}
		if msg.err != nil {
			a.following = false
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, nil
		}
		a.prepend(msg.entries)
		return a, a.tick()

	case tickMsg:
		if msg.seq != a.followSeq || !a.following {
			return a, nil
		}
		return a, a.follow()

	case exportMsg:
		a.busy = false
		if msg.err != nil {
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, nil
		}
		a.openExportConfirm(msg.plan)
		return a, nil

	case ranMsg:
		a.busy = false
		if msg.err != nil {
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, a.load()
		}
		summary := strings.TrimSpace(msg.output)
		if summary == "" {
			summary = "done"
		}
		a.setStatusf(ui.StatusOK, "%s: %s", msg.title, firstLine(summary))
		a.loading = true
		return a, a.load()

	case tea.KeyMsg:
		return a.handleKey(msg)
	}

	// Anything else (cursor blink, …) only concerns an open text input.
	if a.mode == modeInput {
		cmd, _ := a.input.Update(msg)
		return a, cmd
	}
	return a, nil
}

// prepend puts the entries a follow read found at the top of the list, and
// keeps the window bounded so a machine logging hard cannot grow the model
// without limit.
func (a *app) prepend(entries []logs.Entry) {
	if len(entries) == 0 {
		return
	}
	a.model.Entries = append(entries, a.model.Entries...)
	if limit := a.filter.WithLines(a.filter.Lines).Lines; len(a.model.Entries) > limit {
		a.model.Entries = a.model.Entries[:limit]
	}
	a.model.Stats = logs.ComputeStats(a.model.Entries, a.filter.Lines)
	// Following means watching the newest line, so the cursor stays at the
	// top rather than drifting down with the entry it was on.
	a.cursor[screenEntries], a.offset[screenEntries] = 0, 0
	a.setStatusf(ui.StatusInfo, "following  ·  %d new", len(entries))
	a.clampCursor()
}

// handleKey routes a key press to the open dialog, or to the current screen.
func (a *app) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c always quits, even mid-dialog.
	if msg.Type == tea.KeyCtrlC {
		return a, tea.Quit
	}
	if a.busy {
		// A command is running: swallow input rather than queueing surprises.
		return a, nil
	}

	switch a.mode {
	case modeConfirm:
		return a.handleConfirm(msg)
	case modeInput:
		return a.handleInput(msg)
	case modePicker:
		return a.handlePicker(msg)
	case modeHelp:
		a.mode = modeBrowse
		return a, nil
	case modeDetail:
		return a.handleDetailKey(msg)
	default:
		return a.handleBrowseKey(msg)
	}
}

// handleConfirm resolves the confirm dialog.
func (a *app) handleConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a.confirm.Update(msg)
	if !a.confirm.Done {
		return a, nil
	}
	a.mode = modeBrowse
	confirmed := a.confirm.Confirmed
	pending, ok := a.confirm.Payload.(plan)
	a.confirm = ui.Confirm{}
	if !confirmed || !ok {
		a.setStatus(ui.StatusInfo, "cancelled")
		return a, nil
	}
	a.busy = true
	a.setStatusf(ui.StatusInfo, "running %s…", a.backend.Preview(pending.commands[0]))
	return a, a.run(pending)
}

// handleInput resolves an open text prompt: the search pattern, or the export
// path.
func (a *app) handleInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd, _ := a.input.Update(msg)
	if !a.input.Done {
		return a, cmd
	}
	value, accepted := a.input.Value(), a.input.Accepted
	field := a.inputFor
	a.input, a.inputFor = ui.Input{}, ""
	a.mode = modeBrowse
	if !accepted {
		a.setStatus(ui.StatusInfo, "cancelled")
		return a, nil
	}
	switch field {
	case promptGrep:
		return a, a.applyFilter(func(f *logs.Filter) { f.Grep = value })
	case promptExport:
		return a, a.stageExport(value)
	}
	return a, nil
}

// handlePicker resolves the open picker, which serves every filter with a
// closed set of values and both housekeeping arguments.
func (a *app) handlePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a.picker.Update(msg)
	if !a.picker.Done {
		return a, nil
	}
	choice, accepted := a.picker.Selected(), a.picker.Accepted
	field := a.pickerFor
	a.picker, a.pickerFor = ui.Picker{}, ""
	a.mode = modeBrowse
	if !accepted {
		a.setStatus(ui.StatusInfo, "cancelled")
		return a, nil
	}
	return a, a.applyChoice(field, choice)
}

// applyChoice folds a picked value into the filter, or opens the confirm
// dialog for a housekeeping command.
func (a *app) applyChoice(field, choice string) tea.Cmd {
	switch field {
	case pickUnit:
		return a.applyFilter(func(f *logs.Filter) {
			f.Unit = ""
			if choice != everyUnitChoice {
				f.Unit, f.Kernel = choice, false
			}
		})
	case pickPriority:
		return a.applyFilter(func(f *logs.Filter) {
			f.Priority = logs.PriorityAny
			if parsed, ok := logs.ParsePriority(choice); ok {
				f.Priority = parsed
			}
		})
	case pickBoot:
		return a.applyFilter(func(f *logs.Filter) {
			f.Boot = ""
			if choice != everyBootChoice {
				f.Boot = firstField(choice)
			}
		})
	case pickWindow:
		for _, preset := range logs.Presets() {
			if preset.Label != choice {
				continue
			}
			return a.applyFilter(func(f *logs.Filter) {
				f.Since, f.Until, f.Boot = preset.Since, "", preset.Boot
			})
		}
	case pickVacuumSize:
		return a.buildAndConfirm("Vacuum the journal to "+choice,
			func() (logs.Command, error) { return a.backend.BuildVacuumSize(choice) })
	case pickVacuumTime:
		return a.buildAndConfirm("Vacuum entries older than "+choice,
			func() (logs.Command, error) { return a.backend.BuildVacuumTime(choice) })
	}
	return nil
}

// applyFilter changes the filter and re-reads with it. A filter that
// journalctl would refuse never reaches the read: the backend validates it,
// and the message lands in the status line next to the key that caused it.
func (a *app) applyFilter(change func(*logs.Filter)) tea.Cmd {
	next := a.filter
	change(&next)
	next = next.WithLines(a.filter.Lines)
	a.filter = next
	a.loading = true
	a.setStatusf(ui.StatusInfo, "reading %s…", next.Label())
	if a.following {
		// A new filter is a new window, so the follow starts again from
		// whatever that window's newest entry turns out to be.
		a.followSeq++
	}
	return a.reread()
}

// handleBrowseKey handles a screen's own keys.
func (a *app) handleBrowseKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return a, tea.Quit
	case "?":
		a.mode = modeHelp
	case "j", "down":
		a.moveCursor(1)
	case "k", "up":
		a.moveCursor(-1)
	case "g", "home":
		a.cursor[a.screen], a.offset[a.screen] = 0, 0
	case "G", "end":
		a.cursor[a.screen] = max(a.rowCount()-1, 0)
		a.clampCursor()
	case "pgdown", "ctrl+f":
		a.moveCursor(a.tableHeight())
	case "pgup", "ctrl+b":
		a.moveCursor(-a.tableHeight())
	case "tab", "l", "right":
		a.gotoScreen((a.screen + 1) % screenCount)
	case "shift+tab", "h", "left":
		a.gotoScreen((a.screen + screenCount - 1) % screenCount)
	case "1", "2", "3":
		a.gotoScreen(screen(msg.String()[0] - '1'))
	case "enter":
		if a.rowCount() == 0 {
			a.setStatus(ui.StatusWarn, "nothing selected")
			return a, nil
		}
		a.mode, a.detailOffset = modeDetail, 0
	case "R", "ctrl+r":
		a.loading = true
		return a, a.load()
	default:
		return a, a.handleActionKey(msg)
	}
	return a, nil
}

// handleDetailKey handles the per-row screen. The action keys are the same
// ones the table offers, applied to the row on screen.
func (a *app) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "backspace", "left":
		a.mode, a.detailOffset = modeBrowse, 0
		return a, nil
	case "?":
		a.mode = modeHelp
		return a, nil
	case "j", "down":
		a.detailOffset++
		return a, nil
	case "k", "up":
		a.detailOffset = max(a.detailOffset-1, 0)
		return a, nil
	case "g", "home":
		a.detailOffset = 0
		return a, nil
	case "pgdown", "ctrl+f":
		a.detailOffset += a.detailHeight()
		return a, nil
	case "pgup", "ctrl+b":
		a.detailOffset = max(a.detailOffset-a.detailHeight(), 0)
		return a, nil
	case "R", "ctrl+r":
		a.loading = true
		return a, a.load()
	default:
		return a, a.handleActionKey(msg)
	}
}

// handleActionKey handles the keys that mean the same thing on every screen:
// the six that narrow the journal, and the five that do something to it.
func (a *app) handleActionKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "f":
		return a.toggleFollow()
	case "u":
		return a.openUnitPicker()
	case "p":
		return a.openPriorityPicker()
	case "b":
		return a.openBootPicker()
	case "t":
		return a.openWindowPicker()
	case "/":
		return a.openGrepPrompt()
	case "c":
		return a.clearFilters()
	case "K":
		return a.toggleKernel()
	case "U":
		return a.toggleUserJournal()
	case "x":
		return a.openExportPrompt()
	case "v":
		return a.openVacuumPicker(pickVacuumSize, "Vacuum by size",
			a.caps.VacuumSizes)
	case "V":
		return a.openVacuumPicker(pickVacuumTime, "Vacuum by age",
			a.caps.VacuumTimes)
	case "o":
		return a.confirmRotate()
	case "y":
		return a.confirmVerify()
	}
	return nil
}

// toggleFollow switches the re-read loop on and off.
func (a *app) toggleFollow() tea.Cmd {
	if !a.caps.SupportsFollow {
		a.setStatus(ui.StatusWarn, "this backend cannot follow the journal")
		return nil
	}
	a.followSeq++
	a.following = !a.following
	if !a.following {
		a.setStatus(ui.StatusInfo, "stopped following")
		return nil
	}
	a.screen = screenEntries
	a.setStatusf(ui.StatusInfo, "following, re-reading every %s",
		logs.FollowInterval)
	return a.tick()
}

// openUnitPicker offers the machine's units, not the ones on screen: the unit
// a reader is looking for is usually the one that stopped logging.
func (a *app) openUnitPicker() tea.Cmd {
	if len(a.model.Units) == 0 {
		a.setStatus(ui.StatusWarn,
			"the unit list could not be read; / filters by message instead")
		return nil
	}
	current := a.filter.Unit
	if current == "" {
		current = everyUnitChoice
	}
	a.openPicker(pickUnit, "Unit",
		append([]string{everyUnitChoice}, a.model.Units...), current)
	return nil
}

// openPriorityPicker offers the eight syslog levels. journalctl's -p is "this
// level and worse", which is why the labels say so.
func (a *app) openPriorityPicker() tea.Cmd {
	options := []string{anyChoice}
	for level := logs.PriEmerg; level <= logs.PriDebug; level++ {
		options = append(options, level.Name())
	}
	current := anyChoice
	if a.filter.Priority != logs.PriorityAny {
		current = a.filter.Priority.Name()
	}
	a.openPicker(pickPriority, "Priority (this level and worse)", options, current)
	return nil
}

// openBootPicker offers the boots the journal still holds.
func (a *app) openBootPicker() tea.Cmd {
	if len(a.model.Boots) == 0 {
		if reason, ok := a.caps.Reason(logs.CapBoots); ok {
			a.setStatus(ui.StatusWarn, "no boots were listed — "+reason)
			return nil
		}
		a.setStatus(ui.StatusWarn, "no boots were listed")
		return nil
	}
	options := []string{everyBootChoice}
	current := everyBootChoice
	for _, boot := range a.model.Boots {
		label := boot.Label()
		options = append(options, label)
		if a.filter.Boot != "" && firstField(label) == a.filter.Boot {
			current = label
		}
	}
	a.openPicker(pickBoot, "Boot", options, current)
	return nil
}

// openWindowPicker offers the time presets.
func (a *app) openWindowPicker() tea.Cmd {
	var options []string
	for _, preset := range logs.Presets() {
		options = append(options, preset.Label)
	}
	a.openPicker(pickWindow, "Window", options, "")
	return nil
}

// openVacuumPicker offers one of the two housekeeping arguments.
func (a *app) openVacuumPicker(field, title string, options []string) tea.Cmd {
	if !a.caps.SupportsVacuum || len(options) == 0 {
		a.setStatus(ui.StatusWarn, "this backend cannot vacuum the journal")
		return nil
	}
	a.screen = screenStorage
	a.openPicker(field, title, options, "")
	return nil
}

// openPicker opens a single-choice list.
func (a *app) openPicker(field, title string, options []string, current string) {
	a.pickerFor = field
	a.picker = ui.NewPicker(title, options, current)
	a.mode = modePicker
}

// openGrepPrompt asks for the search pattern.
//
// It is journalctl's own `--grep`, not a filter over the rows already read.
// A client-side filter would search the window on screen, which is the last
// five hundred entries — and the line somebody is looking for is almost never
// in the last five hundred.
func (a *app) openGrepPrompt() tea.Cmd {
	a.input = ui.NewInput("Search the journal", "a pattern, or empty to clear",
		a.filter.Grep)
	a.input.Help = "Passed to journalctl as --grep, so it searches the whole " +
		"window on the machine, not the rows on screen. Lower case matches " +
		"either case; a capital makes it exact."
	a.inputFor = promptGrep
	a.mode = modeInput
	return nil
}

// clearFilters puts the journal back to everything.
func (a *app) clearFilters() tea.Cmd {
	return a.applyFilter(func(f *logs.Filter) {
		lines := f.Lines
		*f = logs.Filter{Priority: logs.PriorityAny, Lines: lines}
	})
}

// toggleKernel switches the kernel-only filter.
func (a *app) toggleKernel() tea.Cmd {
	return a.applyFilter(func(f *logs.Filter) {
		f.Kernel = !f.Kernel
		if f.Kernel {
			// The kernel has no unit and does not log to a user journal, so
			// both are cleared rather than being refused a moment later.
			f.Unit, f.User = "", false
		}
	})
}

// toggleUserJournal switches between the system journal and this user's own.
func (a *app) toggleUserJournal() tea.Cmd {
	if !a.caps.SupportsUserJournal {
		a.setStatus(ui.StatusWarn, "this backend has no user journal")
		return nil
	}
	return a.applyFilter(func(f *logs.Filter) {
		f.User = !f.User
		if f.User {
			f.Kernel = false
		}
	})
}

// openExportPrompt asks where the window should be written.
func (a *app) openExportPrompt() tea.Cmd {
	if !a.caps.SupportsExport {
		a.setStatus(ui.StatusWarn, "this backend cannot export")
		return nil
	}
	a.input = ui.NewInput("Export this window", "a path under your home directory",
		a.backend.SuggestExportPath())
	a.input.Help = "The entries this filter selects, as `journalctl -o short-iso` " +
		"writes them. The file is read and staged first, and the command that " +
		"copies it is shown before it runs."
	a.inputFor = promptExport
	a.mode = modeInput
	return nil
}

// stageExport reads the window and stages it, in the background: on a wide
// filter it is the slowest read the tool makes.
func (a *app) stageExport(path string) tea.Cmd {
	backend, filter := a.backend, a.filter
	a.busy = true
	a.setStatusf(ui.StatusInfo, "reading the window for %s…", path)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		exportPlan, err := backend.BuildExport(ctx, filter, path)
		return exportMsg{plan: exportPlan, err: err}
	}
}

// openExportConfirm shows what was staged and the command that copies it.
func (a *app) openExportConfirm(exported logs.ExportPlan) {
	title := "Write " + exported.Path
	body := fmt.Sprintf("%d lines, %s, from `%s`.",
		exported.Lines, humanBytes(exported.Bytes), exported.Source.String())
	if exported.Warning != "" {
		body += "\n\n" + exported.Warning
	}
	// The prose is wrapped and the preview is only clipped: a log line that
	// was reflowed would stop looking like the line it is.
	body = wrapParagraphs(body, a.dialogWidth()) + "\n\n" +
		clipLines(exported.Preview, a.dialogWidth())

	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   title,
		Body:    body,
		Command: a.previewAll(exported.Commands),
		Danger:  true,
		Payload: plan{title: title, commands: exported.Commands},
	}
}

// confirmRotate asks before rotating the journal files.
func (a *app) confirmRotate() tea.Cmd {
	if !a.caps.SupportsRotate {
		a.setStatus(ui.StatusWarn, "this backend cannot rotate the journal")
		return nil
	}
	a.screen = screenStorage
	return a.buildAndConfirm("Rotate the journal", a.backend.BuildRotate)
}

// confirmVerify asks before verifying, because it is a read that can take
// minutes and there is no way to say so once it has started.
func (a *app) confirmVerify() tea.Cmd {
	if !a.caps.SupportsVerify {
		a.setStatus(ui.StatusWarn, "this backend cannot verify the journal")
		return nil
	}
	a.screen = screenStorage
	return a.buildAndConfirm("Verify the journal", a.backend.BuildVerify)
}

// buildAndConfirm runs a command builder and opens the confirm dialog, or
// reports the builder's error in the status line.
func (a *app) buildAndConfirm(title string,
	build func() (logs.Command, error)) tea.Cmd {
	cmd, err := build()
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	a.openConfirm(title, actionBody(cmd), cmd)
	return nil
}

// actionBody is what the confirm dialog says above the command: what it does,
// and the one consequence worth naming for each.
func actionBody(cmd logs.Command) string {
	body := cmd.Description + "."
	switch {
	case hasArgPrefix(cmd, "--vacuum-"):
		return body + "\n\nWhole journal files are removed, oldest first, so " +
			"the result usually lands under the limit rather than on it. What " +
			"goes is gone: there is no undo, and no other copy unless " +
			"something forwards the journal elsewhere."
	case hasArg(cmd, "--rotate"):
		return body + "\n\nNothing is deleted. The active files become " +
			"archived ones under a new name, which is what a vacuum then has " +
			"something to work with."
	case hasArg(cmd, "--verify"):
		return body + "\n\nThis reads every journal file on disk and checks " +
			"its hashes. On a large journal it takes minutes, and the screen " +
			"waits for it."
	}
	return body
}

// hasArg reports whether a command carries an argument.
func hasArg(cmd logs.Command, want string) bool {
	for _, arg := range cmd.Argv {
		if arg == want {
			return true
		}
	}
	return false
}

// hasArgPrefix reports whether a command carries an argument starting with a
// prefix, which is how the two vacuum forms are recognised together.
func hasArgPrefix(cmd logs.Command, prefix string) bool {
	for _, arg := range cmd.Argv {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}

// dialogWidth is how wide the prose in a confirm dialog may be. The kit's
// dialog clips rather than wraps, so the text is wrapped to this before it
// gets there.
func (a *app) dialogWidth() int { return min(max(a.width-12, 24), 72) }

// openConfirm shows one command and what it does.
func (a *app) openConfirm(title, body string, cmd logs.Command) {
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   title,
		Body:    wrapParagraphs(body, a.dialogWidth()),
		Command: a.backend.Preview(cmd),
		Danger:  cmd.Destructive,
		Payload: plan{title: title, commands: []logs.Command{cmd}},
	}
}

// previewAll renders every command of a plan, one per line, each with the
// prompt the dialog puts in front of the first one.
func (a *app) previewAll(commands []logs.Command) string {
	previews := make([]string, 0, len(commands))
	for _, cmd := range commands {
		previews = append(previews, a.backend.Preview(cmd))
	}
	return strings.Join(previews, "\n$ ")
}

// gotoScreen switches tabs.
func (a *app) gotoScreen(next screen) {
	if next < 0 || next >= screenCount {
		return
	}
	a.screen = next
	a.clampCursor()
}

// rowCount is how many rows the current screen has.
func (a *app) rowCount() int {
	switch a.screen {
	case screenStats:
		return len(a.statsRows())
	case screenStorage:
		return len(a.storageRows())
	default:
		return len(a.model.Entries)
	}
}

// selectedEntry is the highlighted row of the entries screen.
func (a *app) selectedEntry() (logs.Entry, bool) {
	if a.screen != screenEntries {
		return logs.Entry{}, false
	}
	index := a.cursor[screenEntries]
	if index < 0 || index >= len(a.model.Entries) {
		return logs.Entry{}, false
	}
	return a.model.Entries[index], true
}

// moveCursor moves the selection and keeps the viewport in sync.
func (a *app) moveCursor(delta int) {
	a.cursor[a.screen] += delta
	a.clampCursor()
}

// clampCursor keeps the cursor and the scroll offset of every screen in range.
func (a *app) clampCursor() {
	for s := screen(0); s < screenCount; s++ {
		count := a.countFor(s)
		if count == 0 {
			a.cursor[s], a.offset[s] = 0, 0
			continue
		}
		a.cursor[s] = min(max(a.cursor[s], 0), count-1)

		height := a.tableHeight()
		if a.cursor[s] < a.offset[s] {
			a.offset[s] = a.cursor[s]
		}
		if a.cursor[s] >= a.offset[s]+height {
			a.offset[s] = a.cursor[s] - height + 1
		}
		a.offset[s] = max(min(a.offset[s], max(count-height, 0)), 0)
	}
}

// countFor is rowCount for a screen that is not the current one.
func (a *app) countFor(s screen) int {
	current := a.screen
	a.screen = s
	count := a.rowCount()
	a.screen = current
	return count
}

// firstLine keeps status messages to one line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// firstField is the first whitespace-separated token of a string, which is
// how a picker label gives back the value it stands for.
func firstField(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
