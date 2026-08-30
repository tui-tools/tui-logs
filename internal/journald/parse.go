package journald

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tui-tools/tui-logs/internal/logs"
)

// ParseEntries reads `journalctl -o json` into entries, newest first.
//
// The output is one JSON object per line, and the objects are not a schema:
// the journal stores whatever a program chose to attach, so every field is
// read into a map and only the handful the screens are built on are lifted
// out. A line that does not parse is skipped rather than failing the read —
// one malformed record must not hide the thousand around it.
func ParseEntries(out string) []logs.Entry {
	var entries []logs.Entry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		entries = append(entries, entryFrom(raw))
	}
	// journalctl prints oldest first; what a reader wants on opening the
	// screen is what just happened.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries
}

// entryFrom lifts one decoded record into an Entry.
func entryFrom(raw map[string]json.RawMessage) logs.Entry {
	fields := make(map[string]string, len(raw))
	for key, value := range raw {
		fields[key] = fieldValue(value)
	}

	entry := logs.Entry{
		Cursor:     fields["__CURSOR"],
		Priority:   logs.PriorityAny,
		Unit:       fields["_SYSTEMD_UNIT"],
		Identifier: fields["SYSLOG_IDENTIFIER"],
		Message:    fields["MESSAGE"],
		UID:        -1,
		Comm:       fields["_COMM"],
		Exe:        fields["_EXE"],
		Cmdline:    fields["_CMDLINE"],
		CodeFile:   fields["CODE_FILE"],
		CodeFunc:   fields["CODE_FUNC"],
		MessageID:  strings.ToLower(fields["MESSAGE_ID"]),
		BootID:     fields["_BOOT_ID"],
		Hostname:   fields["_HOSTNAME"],
		Transport:  fields["_TRANSPORT"],
		Fields:     fields,
	}
	// A user-journal entry carries its unit under a different name, and an
	// entry from a user service on the system journal carries both.
	if entry.Unit == "" || entry.Unit == "user@1000.service" {
		if userUnit := fields["_SYSTEMD_USER_UNIT"]; userUnit != "" {
			entry.Unit = userUnit
		}
	}
	if level, err := strconv.Atoi(fields["PRIORITY"]); err == nil &&
		level >= 0 && level <= 7 {
		entry.Priority = logs.Priority(level)
	}
	if pid, err := strconv.Atoi(fields["_PID"]); err == nil {
		entry.PID = pid
	}
	if uid, err := strconv.Atoi(fields["_UID"]); err == nil {
		entry.UID = uid
	}
	if line, err := strconv.Atoi(fields["CODE_LINE"]); err == nil {
		entry.CodeLine = line
	}
	entry.Realtime = timestampFrom(fields["__REALTIME_TIMESTAMP"])
	if entry.Realtime.IsZero() {
		entry.Realtime = timestampFrom(fields["_SOURCE_REALTIME_TIMESTAMP"])
	}
	return entry
}

// fieldValue renders one journal field as text.
//
// Three shapes arrive. A plain string is the common one. A field that
// repeated in the record arrives as an array of strings, and is joined. A
// field whose bytes are not valid UTF-8 — a message with a control character
// in it, most often — arrives as an array of numbers, which is rendered back
// into the bytes it stands for so the entry is readable rather than a row of
// integers.
func fieldValue(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		// Sanitized like the binary form below: JSON carries an escape as
		// \u001b, so a plain string field can hold the same byte a terminal
		// would obey.
		return sanitize(text)
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return strings.Trim(string(raw), `"`)
	}
	var numbers []byte
	var parts []string
	for _, item := range list {
		var element string
		if err := json.Unmarshal(item, &element); err == nil {
			parts = append(parts, element)
			continue
		}
		var value int
		if err := json.Unmarshal(item, &value); err == nil && value >= 0 && value <= 255 {
			numbers = append(numbers, byte(value))
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	return sanitize(string(numbers))
}

// sanitize replaces the control characters a binary field can carry, because
// a raw escape written into a terminal is a way to move the cursor, repaint
// the screen or set the title from a log line nobody wrote on purpose.
func sanitize(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return '·'
		}
		return r
	}, text)
}

// timestampFrom reads one of the journal's microsecond timestamps.
func timestampFrom(text string) time.Time {
	micros, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil || micros <= 0 {
		return time.Time{}
	}
	return time.UnixMicro(micros)
}

// bootJSON is one entry of `journalctl --list-boots -o json`.
type bootJSON struct {
	Index      int    `json:"index"`
	BootID     string `json:"boot_id"`
	FirstEntry int64  `json:"first_entry"`
	LastEntry  int64  `json:"last_entry"`
}

// ParseBootsJSON reads `journalctl --list-boots -o json`, which systemd 252
// and newer print.
func ParseBootsJSON(out string) []logs.Boot {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil
	}
	var raw []bootJSON
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil
	}
	boots := make([]logs.Boot, 0, len(raw))
	for _, entry := range raw {
		// Same rule as the table parser: the id is what `--boot=<id>` is
		// built with, so a record without one is not a boot this picker can
		// offer.
		if !bootIDRe.MatchString(entry.BootID) {
			continue
		}
		boots = append(boots, logs.Boot{
			Index: entry.Index,
			ID:    entry.BootID,
			First: time.UnixMicro(entry.FirstEntry),
			Last:  time.UnixMicro(entry.LastEntry),
		})
	}
	return newestFirst(boots)
}

// ParseBootsTable reads the table `journalctl --list-boots` prints, which is
// what a systemd older than 252 answers with.
//
// The columns are the offset, the boot id and two timestamps, and the
// timestamps carry a day name, a zone offset and spaces of their own — so
// only the first two fields are read positionally and the rest is taken as
// the two dates, split down the middle. A date that will not parse leaves the
// boot with its id and offset, which is all the picker needs.
func ParseBootsTable(out string) []logs.Boot {
	var boots []logs.Boot
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		index, err := strconv.Atoi(fields[0])
		if err != nil {
			// The header row, or the "-- Boot …" banner of an older release.
			continue
		}
		// The id is what `journalctl --boot=<id>` is built with, so only the
		// 32 hex digits journald prints are accepted as one.
		if !bootIDRe.MatchString(fields[1]) {
			continue
		}
		boot := logs.Boot{Index: index, ID: fields[1]}
		rest := fields[2:]
		if len(rest) >= 8 && len(rest)%2 == 0 {
			half := len(rest) / 2
			boot.First = parseListBootsTime(strings.Join(rest[:half], " "))
			boot.Last = parseListBootsTime(strings.Join(rest[half:], " "))
		}
		boots = append(boots, boot)
	}
	return newestFirst(boots)
}

// bootIDRe is a journal boot id: 32 hex digits, nothing else.
var bootIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// listBootsLayouts are the timestamp forms `--list-boots` prints. The zone is
// an offset on a modern systemd and a name on an older one, and a machine set
// to UTC prints neither the same way, so all three are tried.
var listBootsLayouts = []string{
	"Mon 2006-01-02 15:04:05 -07",
	"Mon 2006-01-02 15:04:05 -0700",
	"Mon 2006-01-02 15:04:05 MST",
}

// parseListBootsTime reads one of those timestamps, returning the zero time
// when none of the layouts fit.
func parseListBootsTime(text string) time.Time {
	text = strings.TrimSpace(text)
	for _, layout := range listBootsLayouts {
		if when, err := time.Parse(layout, text); err == nil {
			return when
		}
	}
	return time.Time{}
}

// newestFirst orders the boots the way the picker shows them: the running
// boot at the top, then backwards through the history.
func newestFirst(boots []logs.Boot) []logs.Boot {
	sort.SliceStable(boots, func(i, j int) bool {
		return boots[i].Index > boots[j].Index
	})
	return boots
}

// ParseUnits reads the unit names out of
// `systemctl list-units --all --plain --no-legend`.
//
// Only the first column is taken. The columns after it have changed between
// systemd releases, and what the picker needs is the name — the state of a
// unit is tui-systemd's screen, and duplicating it here would be a second
// place for it to be wrong.
func ParseUnits(out string) []string {
	seen := map[string]bool{}
	var units []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		// A unit systemd could not load is printed with a bullet in front of
		// it, which is a decoration rather than part of the name.
		name = strings.TrimPrefix(name, "●")
		name = strings.TrimSpace(name)
		if name == "" || !unitRe.MatchString(name) || seen[name] {
			continue
		}
		seen[name] = true
		units = append(units, name)
	}
	sort.Strings(units)
	return units
}

// ParseDiskUsage reads `journalctl --disk-usage`, which prints one sentence:
// "Archived and active journals take up 3.9G in the file system."
func ParseDiskUsage(out string) (string, int64) {
	text := strings.TrimSpace(out)
	if text == "" {
		return "", 0
	}
	text = sanitize(strings.TrimSpace(firstLine(text)))
	for _, field := range strings.Fields(text) {
		if bytes, ok := parseSize(field); ok {
			return text, bytes
		}
	}
	return text, 0
}

// sizeUnits are the suffixes systemd prints a size with, and what each is
// worth in bytes. They are powers of 1024: systemd's own formatter is binary,
// whatever the letter suggests.
var sizeUnits = []struct {
	suffix string
	scale  int64
}{
	{"T", 1 << 40}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1},
}

// parseSize reads a size like "3.9G" or "512.0M".
func parseSize(field string) (int64, bool) {
	for _, unit := range sizeUnits {
		number, found := strings.CutSuffix(field, unit.suffix)
		if !found {
			continue
		}
		value, err := strconv.ParseFloat(number, 64)
		// A size is never negative, and one that does not fit in an int64 is
		// not a journal — the byte count is what the housekeeping screen does
		// its arithmetic with, and an overflowed one would be arithmetic on
		// nonsense.
		if err != nil || !(value >= 0) {
			return 0, false
		}
		scaled := value * float64(unit.scale)
		if scaled >= float64(math.MaxInt64) {
			return 0, false
		}
		return int64(scaled), true
	}
	return 0, false
}

// FormatSize renders a byte count the way systemd would, so the tool's own
// arithmetic and journalctl's sentence agree on screen.
func FormatSize(bytes int64) string {
	if bytes <= 0 {
		return "0B"
	}
	for _, unit := range sizeUnits {
		if bytes >= unit.scale {
			return strconv.FormatFloat(float64(bytes)/float64(unit.scale), 'f', 1,
				64) + unit.suffix
		}
	}
	return strconv.FormatInt(bytes, 10) + "B"
}

// ParseCatConfig reads `systemd-analyze cat-config systemd/journald.conf`.
//
// The output is every file that contributes, each preceded by a `# /path`
// header, with the settings a distribution ships commented out to show the
// compiled-in default. Both are kept and told apart: what the journal
// actually does is the uncommented line, and knowing that the rest are
// defaults is what stops a reader chasing a setting nobody set.
//
// A later file's value wins, which is the opposite of sshd's rule and is why
// the walk records the last one it sees rather than the first.
func ParseCatConfig(out string) []logs.ConfSetting {
	order := []string{}
	settings := map[string]logs.ConfSetting{}

	file := ""
	number := 0
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		number++
		if header, ok := configHeader(trimmed); ok {
			file, number = header, 0
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "[") {
			continue
		}

		commented := strings.HasPrefix(trimmed, "#")
		text := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		key, value, found := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		// A key is drawn on screen and matched against by name, so only the
		// shape a unit-file setting has is read as one.
		if !found || !confKeyRe.MatchString(key) {
			continue
		}

		setting := logs.ConfSetting{
			Key:     key,
			Value:   sanitize(strings.TrimSpace(value)),
			File:    file,
			Line:    number,
			Default: commented,
		}
		existing, seen := settings[key]
		if !seen {
			order = append(order, key)
		}
		// A commented line never overrides a value somebody actually set: it
		// is documentation of the default, and it appears in every file.
		if seen && setting.Default && !existing.Default {
			continue
		}
		settings[key] = setting
	}

	out2 := make([]logs.ConfSetting, 0, len(order))
	for _, key := range order {
		out2 = append(out2, settings[key])
	}
	return out2
}

// confKeyRe is the shape of a setting name in a systemd unit-style
// configuration file.
var confKeyRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// configHeader reads the `# /path/to/file` line cat-config puts in front of
// each file it concatenated, and reports whether the line was one.
func configHeader(line string) (string, bool) {
	if !strings.HasPrefix(line, "# /") {
		return "", false
	}
	path := strings.TrimSpace(strings.TrimPrefix(line, "#"))
	if strings.ContainsAny(path, " \t") {
		return "", false
	}
	return path, true
}

// firstLine keeps a message to a single line. A carriage return ends one as
// surely as a newline does, and it is the break a terminal would obey without
// showing anything.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}
