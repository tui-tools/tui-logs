package main

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-logs/internal/journald"
	"github.com/tui-tools/tui-logs/internal/logs"
)

// Layout constants: the rows the table cannot use.
const (
	headerLines = 2
	footerLines = 2
	// tabLines is the one row the tab bar takes.
	tabLines = 1
	// minTableHeight keeps at least one visible row on a very short terminal.
	minTableHeight = 1
)

// tableHeight is the number of rows that fit on screen.
func (a *app) tableHeight() int {
	// header + tabs + table header + footer + status line.
	return max(a.height-headerLines-tabLines-footerLines-2, minTableHeight)
}

// detailHeight is the number of detail lines that fit on screen.
func (a *app) detailHeight() int {
	return max(a.height-headerLines-tabLines-footerLines-1, minTableHeight)
}

// View renders the whole screen.
func (a *app) View() string {
	switch a.mode {
	case modeConfirm:
		return a.confirm.View(a.theme, a.width, a.height)
	case modeInput:
		return a.input.View(a.theme, a.width, a.height)
	case modePicker:
		return a.picker.View(a.theme, a.width, a.height)
	case modeHelp:
		return placeCenter(
			ui.HelpScreen(a.theme, "tui-logs — keys", helpKeys(), a.width),
			a.width, a.height)
	case modeDetail:
		return a.detailView()
	}
	return a.browseView()
}

// placeCenter centers a rendered box in the terminal.
func placeCenter(box string, width, height int) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// browseView renders a screen: header, tab bar, table, help bar, status.
func (a *app) browseView() string {
	header := a.headerView()
	tabs := a.tabsView()

	var body string
	switch {
	case a.loading && a.rowCount() == 0:
		body = ui.EmptyState(a.theme, "reading the journal…", a.width, a.tableHeight()+1)
	case a.rowCount() == 0 && a.loadFailed:
		body = ui.EmptyState(a.theme,
			"could not read the journal — see the message below",
			a.width, a.tableHeight()+1)
	case a.rowCount() == 0:
		body = ui.EmptyState(a.theme, a.emptyMessage(), a.width, a.tableHeight()+1)
	default:
		body = a.table()
	}

	help := ui.HelpBar(a.theme, a.shortHelpKeys(), a.width)
	status := ui.StatusLine(a.theme, a.statusKind, a.status, a.defaultStatus(), a.width)
	return strings.Join([]string{header, tabs, body, help, status}, "\n")
}

// emptyMessage is what a screen with no rows says, which is different on each.
func (a *app) emptyMessage() string {
	switch a.screen {
	case screenStats:
		return "nothing in this window to count"
	case screenStorage:
		return "the journal's disk usage could not be read"
	default:
		return a.noEntriesMessage()
	}
}

// noEntriesMessage explains an empty entries screen, which is not the same
// failure everywhere.
//
// A machine with a narrow filter and a machine that refused to open its
// journal both show nothing, and telling a reader which one they are looking
// at is the difference between changing a filter and joining a group.
func (a *app) noEntriesMessage() string {
	if a.model.Access.Denied {
		return a.model.Access.Note
	}
	if a.filter.Label() == "everything" {
		return "the journal is empty"
	}
	return "nothing matches " + a.filter.Label() + " — c clears every filter"
}

// headerView renders the facts at the top of every screen.
func (a *app) headerView() string {
	t := a.theme

	// How the journal was opened. It is the first thing that decides how much
	// the rest of the screen is worth, so it is not buried.
	accessValue, accessStyle := "open", t.OK
	switch {
	case a.model.Access.Denied:
		accessValue, accessStyle = "your entries only", t.Danger
	case a.model.Access.Escalated:
		accessValue, accessStyle = "via sudo -n", t.Warn
	}
	facts := []ui.Fact{{Label: "journal", Value: accessValue, Style: &accessStyle}}

	facts = append(facts, ui.Fact{Label: "entries",
		Value: strconv.Itoa(a.model.Stats.Total)})
	if a.model.Stats.Errors > 0 {
		style := t.Danger
		facts = append(facts, ui.Fact{Label: "errors",
			Value: strconv.Itoa(a.model.Stats.Errors), Style: &style})
	}
	if a.model.Storage.DiskUsage != "" {
		facts = append(facts, ui.Fact{Label: "on disk",
			Value: journald.FormatSize(a.model.Storage.Bytes)})
	}
	if a.following {
		style := t.Info
		facts = append(facts, ui.Fact{Label: "follow",
			Value: "every " + logs.FollowInterval.String(), Style: &style})
	}
	// The backend version, when it was probed: quiet on a tested version,
	// coloured on one nobody has run against.
	if a.backendCompat.Backend != "" {
		facts = append(facts, ui.CompatFact(t, a.backendCompat))
	}

	return ui.Header{Title: "tui-logs", Subtitle: a.filter.Label(), Facts: facts}.
		Render(t, a.width)
}

// tabsView renders the three screens as one row, with the current one
// accented.
func (a *app) tabsView() string {
	var parts []string
	for s := screen(0); s < screenCount; s++ {
		label := strconv.Itoa(int(s)+1) + " " + s.title()
		if s == a.screen {
			parts = append(parts, a.theme.Accent.Render("["+label+"]"))
			continue
		}
		parts = append(parts, a.theme.Muted.Render(" "+label+" "))
	}
	return a.theme.Footer.Width(a.width).Render(
		ui.Truncate(strings.Join(parts, " "), a.width-2))
}

// defaultStatus is the hint shown when there is no message to report. On the
// entries screen it is the journalctl command behind what is on show, because
// the whole point of the filters is that they are one.
func (a *app) defaultStatus() string {
	switch a.screen {
	case screenStats:
		return "the window on screen, counted  ·  tab to move  ·  ? for help"
	case screenStorage:
		return "v vacuum by size  ·  V by age  ·  o rotate  ·  y verify  ·  ? for help"
	default:
		if command := a.model.Command.String(); command != "" {
			return "$ " + command
		}
		return "? for help"
	}
}

// table renders the current screen's rows.
func (a *app) table() string {
	columns, rows, styles := a.tableData()
	return ui.Table{
		Columns:  columns,
		Rows:     rows,
		Styles:   styles,
		Selected: a.cursor[a.screen],
		Offset:   a.offset[a.screen],
		Height:   a.tableHeight(),
	}.Render(a.theme, a.width)
}

// tableData builds the columns, cells and row styles of the current screen.
// Every screen drops its widest columns first on a narrow terminal, which is
// what keeps a 40-column pane readable.
func (a *app) tableData() ([]ui.Column, [][]string, []*lipgloss.Style) {
	switch a.screen {
	case screenStats:
		return a.infoTable(a.statsRows())
	case screenStorage:
		return a.infoTable(a.storageRows())
	default:
		return a.entriesTable()
	}
}

// entriesTable is the journal itself: when, how bad, what wrote it, and what
// it said.
func (a *app) entriesTable() ([]ui.Column, [][]string, []*lipgloss.Style) {
	showTime := a.width >= 56
	showPriority := a.width >= 64
	showSource := a.width >= 48

	var columns []ui.Column
	if showTime {
		columns = append(columns, ui.Column{Title: "TIME", Width: 15})
	}
	if showPriority {
		columns = append(columns, ui.Column{Title: "PRI", Width: 7})
	}
	if showSource {
		columns = append(columns, ui.Column{Title: "SOURCE", Width: 22, Flex: true})
	}
	columns = append(columns, ui.Column{Title: "MESSAGE", Width: 30, Flex: true})

	rows := make([][]string, 0, len(a.model.Entries))
	styles := make([]*lipgloss.Style, 0, len(a.model.Entries))
	for _, entry := range a.model.Entries {
		var row []string
		if showTime {
			row = append(row, entry.Time())
		}
		if showPriority {
			row = append(row, priorityLabel(entry.Priority))
		}
		if showSource {
			row = append(row, entry.Source())
		}
		rows = append(rows, append(row, entry.Message))
		styles = append(styles, a.priorityStyle(entry.Priority))
	}
	return columns, rows, styles
}

// priorityLabel is the priority column. It is the word rather than the number
// because "err" is readable and "3" is a thing to look up.
func priorityLabel(priority logs.Priority) string {
	if priority == logs.PriorityAny {
		return "—"
	}
	return priority.Name()
}

// priorityStyle colours a row by how bad the entry is, so what is wrong
// stands out from what is merely happening.
func (a *app) priorityStyle(priority logs.Priority) *lipgloss.Style {
	var style lipgloss.Style
	switch {
	case priority.Severe():
		style = a.theme.Row.Foreground(a.theme.Danger.GetForeground())
	case priority.Warned():
		style = a.theme.Row.Foreground(a.theme.Warn.GetForeground())
	case priority == logs.PriDebug:
		style = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
	default:
		style = a.theme.Row
	}
	return &style
}

// infoRow is one line of the stats and storage screens: a fact and its value.
type infoRow struct{ label, value string }

// infoTable renders those rows, so both screens scroll like the entries do.
func (a *app) infoTable(entries []infoRow) ([]ui.Column, [][]string,
	[]*lipgloss.Style) {
	columns := []ui.Column{
		{Title: "", Width: 18},
		{Title: "", Width: 40, Flex: true},
	}
	rows := make([][]string, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, []string{entry.label, entry.value})
	}
	return columns, rows, nil
}

// statsRows is what the window on screen is made of.
//
// Everything here is counted from the entries that were actually read, and
// the row that says how many those were is first — a "top unit" over a window
// bounded by a line count means something different from one over an hour,
// and the screen says which it is rather than leaving it to be assumed.
func (a *app) statsRows() []infoRow {
	stats := a.model.Stats
	rows := []infoRow{
		{"window", a.filter.Label()},
		{"entries", strconv.Itoa(stats.Total)},
	}
	if stats.Truncated {
		rows = append(rows, infoRow{"", "this is the read limit (" +
			strconv.Itoa(a.filter.Lines) +
			"), so what follows counts the window, not the whole period"})
	}
	if !stats.From.IsZero() {
		rows = append(rows, infoRow{"covering",
			stats.From.Format("Jan 02 15:04:05") + " → " +
				stats.To.Format("Jan 02 15:04:05")})
	}
	rows = append(rows,
		infoRow{"errors", strconv.Itoa(stats.Errors) + "  (err and worse)"},
		infoRow{"warnings", strconv.Itoa(stats.Warnings)})

	if spark := sparkline(stats.ErrorBuckets); spark != "" {
		rows = append(rows, infoRow{"errors over time", spark +
			"   peak " + strconv.Itoa(maxOf(stats.ErrorBuckets))})
	}

	if len(stats.TopUnits) > 0 {
		rows = append(rows, infoRow{"noisiest", ""})
		for _, unit := range stats.TopUnits {
			rows = append(rows, infoRow{"  " + unit.Name,
				strconv.Itoa(unit.Count) + "  " + bar(unit.Count, stats.Total)})
		}
	}
	if len(stats.PerBoot) > 0 {
		rows = append(rows, infoRow{"by boot", ""})
		for _, boot := range stats.PerBoot {
			rows = append(rows, infoRow{"  " + a.bootName(boot.Name),
				strconv.Itoa(boot.Count)})
		}
	}
	rows = append(rows, infoRow{"read with", a.model.Command.String()})
	return rows
}

// bootName renders a boot id as the picker does, when the boot list knows it.
func (a *app) bootName(id string) string {
	if boot, ok := a.model.BootFor(id); ok {
		if boot.Index == 0 {
			return "0 (this boot)"
		}
		return strconv.Itoa(boot.Index) + "  " + id[:12]
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// storageRows is what the journal costs and how it is configured.
func (a *app) storageRows() []infoRow {
	storage := a.model.Storage
	rows := []infoRow{
		{"disk usage", orNone(storage.DiskUsage)},
		{"persistent", yesNo(storage.Persistent)},
		{"", storage.PersistentNote},
		{"boots held", strconv.Itoa(len(a.model.Boots))},
	}
	for _, boot := range a.model.Boots {
		rows = append(rows, infoRow{"  boot " + strconv.Itoa(boot.Index),
			boot.Label()})
	}

	rows = append(rows, infoRow{"journald.conf", orNone(storage.ConfSource)})
	if storage.ConfUnavailable != "" {
		rows = append(rows, infoRow{"", storage.ConfUnavailable})
	}
	for _, setting := range storage.Conf {
		value := setting.Value
		if setting.Value == "" {
			value = "(unset)"
		}
		suffix := "   " + setting.File + ":" + strconv.Itoa(setting.Line)
		if setting.Default {
			suffix = "   (compiled-in default)"
		}
		rows = append(rows, infoRow{"  " + setting.Key, value + suffix})
	}
	rows = append(rows, infoRow{"housekeeping",
		"v vacuums by size, V by age, o rotates, y verifies — each previewed first"})
	return rows
}

// yesNo renders a boolean the way the storage screen reads best.
func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// sparkRunes are the eight block heights the sparkline is drawn with.
var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// sparkline draws a bucket series as one row of blocks. An all-zero series
// renders as nothing at all, because a flat line of empty blocks reads as
// data and there is none.
func sparkline(buckets []int) string {
	peak := maxOf(buckets)
	if peak == 0 {
		return ""
	}
	var b strings.Builder
	for _, value := range buckets {
		if value == 0 {
			b.WriteRune(' ')
			continue
		}
		index := (value-1)*len(sparkRunes)/peak + 0
		b.WriteRune(sparkRunes[min(max(index, 0), len(sparkRunes)-1)])
	}
	return b.String()
}

// bar draws a proportion as a short row of blocks, for the noisiest units.
func bar(count, total int) string {
	const width = 16
	if total <= 0 || count <= 0 {
		return ""
	}
	filled := count * width / total
	return strings.Repeat("█", min(max(filled, 1), width))
}

// maxOf is the largest value of a series, 0 for an empty one.
func maxOf(values []int) int {
	peak := 0
	for _, value := range values {
		peak = max(peak, value)
	}
	return peak
}

// detailView renders the selected row in full.
func (a *app) detailView() string {
	header := a.headerView()
	tabs := a.tabsView()
	lines := a.detailLines()

	height := a.detailHeight()
	offset := min(a.detailOffset, max(len(lines)-height, 0))
	a.detailOffset = offset
	end := min(offset+height, len(lines))

	body := make([]string, 0, height)
	for _, line := range lines[offset:end] {
		body = append(body, a.theme.Row.Width(a.width).Render(
			ui.Truncate(line, a.width-2)))
	}
	for i := len(body); i < height; i++ {
		body = append(body, a.theme.Row.Width(a.width).Render(""))
	}

	help := ui.HelpBar(a.theme, a.shortHelpKeys(), a.width)
	position := strconv.Itoa(offset+1) + "–" + strconv.Itoa(end) +
		" of " + strconv.Itoa(len(lines)) + " lines  ·  esc to go back"
	status := ui.StatusLine(a.theme, a.statusKind, a.status, position, a.width)
	return strings.Join([]string{header, tabs,
		strings.Join(body, "\n"), help, status}, "\n")
}

// detailLines builds the detail screen's text for whichever row is selected.
func (a *app) detailLines() []string {
	switch a.screen {
	case screenStats:
		return a.rowsDetail("The window, counted", a.statsRows())
	case screenStorage:
		return a.rowsDetail("The journal on disk", a.storageRows())
	default:
		return a.entryDetail()
	}
}

// rowsDetail renders an info screen's rows as a scrollable page, which is
// where a value too wide for its column becomes readable.
func (a *app) rowsDetail(title string, rows []infoRow) []string {
	lines := []string{title, ""}
	for _, row := range rows {
		if row.label == "" {
			lines = append(lines, "  "+ui.Pad("", 18)+" "+row.value)
			continue
		}
		lines = append(lines, "  "+ui.Pad(row.label, 18)+" "+row.value)
	}
	return lines
}

// entryDetail shows one record: the message, the fields the screens are built
// on, everything else the journal carried, and the command that prints it.
//
// The last part is this tool's answer to "let me copy that". There is no
// clipboard here — a terminal cannot promise one — so what the screen offers
// instead is the journalctl invocation that shows exactly this entry, which
// works in any shell on any machine holding the same journal.
func (a *app) entryDetail() []string {
	entry, ok := a.selectedEntry()
	if !ok {
		return []string{"(nothing selected)"}
	}

	lines := []string{
		entry.Time() + "  " + priorityLabel(entry.Priority) + "  " + entry.Source(),
		"",
	}
	lines = append(lines, wrapped(entry.Message, max(a.width-4, 20))...)
	lines = append(lines, "")

	if name, known := journald.MessageName(entry.MessageID); known {
		lines = append(lines, "This is a known event: "+name, "")
	}

	lines = append(lines, "Fields")
	for _, field := range journald.DetailFields {
		value, present := entry.Fields[field.Field]
		if !present || value == "" {
			continue
		}
		lines = append(lines, "  "+ui.Pad(field.Label, 16)+" "+value)
	}

	if extra := journald.ExtraFields(entry.Fields); len(extra) > 0 {
		lines = append(lines, "", "Everything else this entry carried")
		for _, field := range extra {
			lines = append(lines, "  "+ui.Pad(field, 30)+" "+entry.Fields[field])
		}
	}

	lines = append(lines, "", "Show it with journalctl",
		"  $ "+a.backend.Preview(a.backend.CommandForEntry(entry)),
		"", "The window on screen came from",
		"  $ "+a.model.Command.String())
	return lines
}

// wrapped breaks a message onto lines that fit, so a long one is readable
// rather than truncated at the right edge.
func wrapped(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	var lines []string
	current := ""
	for _, word := range strings.Fields(text) {
		switch {
		case current == "":
			current = word
		case len(current)+1+len(word) <= width:
			current += " " + word
		default:
			lines = append(lines, current)
			current = word
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	if len(lines) == 0 {
		return []string{"(no message)"}
	}
	return lines
}

// wrapParagraphs wraps every paragraph of a text to a width, keeping the
// blank lines between them.
//
// The kit's confirm dialog renders its body verbatim and clips what does not
// fit, which is the right behaviour for a command line — a truncated one must
// never look complete — and the wrong one for prose. So the prose is wrapped
// here, before it ever reaches the dialog.
func wrapParagraphs(text string, width int) string {
	paragraphs := strings.Split(text, "\n\n")
	wrappedParagraphs := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		wrappedParagraphs = append(wrappedParagraphs,
			strings.Join(wrapped(paragraph, width), "\n"))
	}
	return strings.Join(wrappedParagraphs, "\n\n")
}

// clipLines truncates each line of a text to a width without reflowing it.
// It is what the export preview needs: a log line that was wrapped would stop
// looking like the line it is.
func clipLines(text string, width int) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = ui.Truncate(line, width)
	}
	return strings.Join(lines, "\n")
}

// orNone renders an empty value as a visible placeholder, so a blank line is
// never mistaken for a missing read.
func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

// humanBytes renders a byte count for the export dialog.
func humanBytes(bytes int) string { return journald.FormatSize(int64(bytes)) }

// shortHelpKeys is the single-line hint bar, which changes with the screen
// because the keys that do anything change with it.
func (a *app) shortHelpKeys() []ui.KeyHint {
	follow := "follow"
	if a.following {
		follow = "stop following"
	}
	hints := []ui.KeyHint{{Key: "tab", Desc: "screen"}}
	switch a.screen {
	case screenStorage:
		hints = append(hints,
			ui.KeyHint{Key: "v/V", Desc: "vacuum"},
			ui.KeyHint{Key: "o", Desc: "rotate"},
			ui.KeyHint{Key: "y", Desc: "verify"})
	case screenStats:
		hints = append(hints,
			ui.KeyHint{Key: "enter", Desc: "detail"},
			ui.KeyHint{Key: "u/p/t", Desc: "filter"})
	default:
		hints = append(hints,
			ui.KeyHint{Key: "enter", Desc: "detail"},
			ui.KeyHint{Key: "f", Desc: follow},
			ui.KeyHint{Key: "u/p/b/t", Desc: "filter"},
			ui.KeyHint{Key: "/", Desc: "search"},
			ui.KeyHint{Key: "x", Desc: "export"})
	}
	return append(hints,
		ui.KeyHint{Key: "?", Desc: "help"},
		ui.KeyHint{Key: "q", Desc: "quit"})
}

// helpKeys is the full key list shown on the help screen.
func helpKeys() []ui.KeyHint {
	return []ui.KeyHint{
		{Key: "tab / 1-3", Desc: "entries, stats, storage"},
		{Key: "↑/k, ↓/j", Desc: "move the selection, or scroll the detail screen"},
		{Key: "g / G", Desc: "first / last row"},
		{Key: "pgup/pgdn", Desc: "scroll a page"},
		{Key: "enter", Desc: "open the selected entry with every field it carries"},
		{Key: "esc", Desc: "leave the detail screen"},
		{Key: "f", Desc: "follow: re-read what is new every two seconds"},
		{Key: "u", Desc: "filter by unit (-u), from the machine's own unit list"},
		{Key: "p", Desc: "filter by priority (-p), this level and worse"},
		{Key: "b", Desc: "filter by boot (-b)"},
		{Key: "t", Desc: "filter by time (--since): 15m, 1h, 6h, today, this boot"},
		{Key: "/", Desc: "search the journal (--grep), not the rows on screen"},
		{Key: "K", Desc: "kernel messages only (-k)"},
		{Key: "U", Desc: "your own user journal (--user) instead of the system one"},
		{Key: "c", Desc: "clear every filter"},
		{Key: "x", Desc: "export this window to a file under your home directory"},
		{Key: "v / V", Desc: "vacuum the journal by size / by age"},
		{Key: "o", Desc: "rotate: close the active files, delete nothing"},
		{Key: "y", Desc: "verify every journal file (a read, and a slow one)"},
		{Key: "R", Desc: "re-read everything"},
		{Key: "?", Desc: "this help"},
		{Key: "q", Desc: "quit"},
		{Key: "", Desc: ""},
		{Key: "note", Desc: "every filter is a journalctl argument, and the " +
			"command is on the status line"},
		{Key: "note", Desc: "every change is previewed and confirmed first"},
	}
}
