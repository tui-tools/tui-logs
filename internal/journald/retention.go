package journald

// The journal's retention configuration, written rather than only displayed.
//
// The storage screen has always shown journald.conf — every setting, with the
// file and line that set it — and then had nothing to offer. `v` and `V`
// vacuum the journal now; neither changes what it grows back to, which is the
// question a person opening this screen actually has.
//
// What is written is one file, and always the same one:
// /etc/systemd/journald.conf.d/50-tui-logs.conf. journald reads its
// configuration as a vendor file plus a directory of drop-ins, so a tool with
// its own drop-in can set what it sets without touching anything anyone else
// wrote — and a user who wants it all back deletes one file.

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/tui-tools/tui-logs/internal/logs"
)

// DropInDir is the directory journald reads its drop-ins from, and DropInPath
// the one file this tool owns in it. The 50- prefix puts it after a
// distribution's own drop-in and before a 90- one an administrator adds, which
// is where a tool's opinion belongs.
const (
	DropInDir  = "/etc/systemd/journald.conf.d"
	DropInPath = DropInDir + "/50-tui-logs.conf"
)

// DropInMode is the mode the drop-in gets: readable by everyone, writable
// only by root, which is what systemd ships its own configuration with.
const DropInMode = "644"

// JournaldUnit is what has to be restarted for a retention change to take
// effect.
const JournaldUnit = "systemd-journald"

// JournalSection is the section header every journald.conf key lives under.
const JournalSection = "[Journal]"

// The keys this tool writes. Everything else in journald.conf — the rate
// limits, the forwarding, the compression, the seal — is left alone, because
// this form answers one question and that question is how much journal there
// is.
const (
	// KeySystemMaxUse caps the disk the system journal may take.
	KeySystemMaxUse = "SystemMaxUse"
	// KeyMaxRetentionSec caps how old an entry may get before journald drops
	// it, whatever the size says.
	KeyMaxRetentionSec = "MaxRetentionSec"
	// KeyStorage decides where the journal lives, and so whether it survives
	// a reboot at all.
	KeyStorage = "Storage"
)

// StorageChoices are the values this tool offers for Storage. journald also
// accepts `none`, which throws every entry away; a log reader is the wrong
// place to offer that.
var StorageChoices = []string{"auto", "persistent", "volatile"}

// RetentionSetting is one key of the form: the journald key it writes, the
// prompt that collects it, and what a valid answer looks like.
type RetentionSetting struct {
	// Key is the journald.conf key.
	Key string
	// Title is the prompt's heading.
	Title string
	// Help is the line under the prompt.
	Help string
	// Options, when non-empty, makes this a picker and the only valid
	// answers.
	Options []string
}

// RetentionSettings is the closed set of keys the form collects, in the order
// it asks for them.
var RetentionSettings = []RetentionSetting{
	{Key: KeySystemMaxUse, Title: "SystemMaxUse",
		Help: "How much disk the journal may take: 500M, 2G, 4G. " +
			"Empty leaves the key out, and journald falls back to 10% of the filesystem."},
	{Key: KeyMaxRetentionSec, Title: "MaxRetentionSec",
		Help: "How old an entry may get: 30d, 6months, 1year. " +
			"Empty leaves the key out, and only the size limit applies."},
	{Key: KeyStorage, Title: "Storage", Options: StorageChoices,
		Help: "persistent keeps the journal across reboots and creates " +
			JournalDir + "; volatile keeps it in memory only; auto is persistent " +
			"when that directory already exists."},
}

// PersistentNote and NotPersistentNote are what the storage screen says about
// where the journal lives.
//
// The second one used to end in a shell command for the reader to go and run.
// That was the wrong answer twice over: a tool that knows the command should
// preview the command, and `mkdir` is not even the right one — Storage=persistent
// makes journald create the directory itself on its next start, which is a
// configuration change this tool can now make and show.
const (
	PersistentNote = JournalDir + " exists, so the journal survives a reboot"

	NotPersistentNote = "there is no " + JournalDir + ", so the journal lives in " +
		RuntimeJournalDir + " and is lost on every reboot — S sets Storage=persistent, " +
		"which makes journald create that directory on its next start"
)

// RetentionSettingFor returns the setting a key belongs to.
func RetentionSettingFor(key string) (RetentionSetting, bool) {
	for _, setting := range RetentionSettings {
		if setting.Key == key {
			return setting, true
		}
	}
	return RetentionSetting{}, false
}

// sizeRe accepts a SystemMaxUse: a count of bytes with an optional unit, the
// way systemd.conf(5) writes one.
var sizeRe = regexp.MustCompile(`^[0-9]{1,9}(K|M|G|T)?$`)

// durationRe accepts a MaxRetentionSec. systemd's time-span grammar is wider
// than this — it takes a series of terms, and a dozen unit spellings — and
// this admits one term in the units a person writing a retention window
// actually uses. A value it refuses is a value the form asks again for,
// which is better than one journald silently ignores.
var durationRe = regexp.MustCompile(
	`^[0-9]{1,9}(s|sec|second|seconds|m|min|minute|minutes|h|hour|hours|` +
		`d|day|days|w|week|weeks|month|months|y|year|years)?$`)

// ValidateRetentionValue reports whether an answer is one journald accepts
// for a key. An empty answer is valid everywhere and means "leave this key
// out of the file", which is how a limit is removed rather than only changed.
func ValidateRetentionValue(setting RetentionSetting, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if len(setting.Options) > 0 {
		for _, option := range setting.Options {
			if value == option {
				return nil
			}
		}
		return fmt.Errorf("journald: %s is %s, not %q",
			setting.Key, strings.Join(setting.Options, ", "), value)
	}
	switch setting.Key {
	case KeySystemMaxUse:
		if !sizeRe.MatchString(value) {
			return fmt.Errorf(
				"journald: %q is not a size (500M, 2G, 100K)", value)
		}
	case KeyMaxRetentionSec:
		if !durationRe.MatchString(value) {
			return fmt.Errorf(
				"journald: %q is not a retention window (30d, 6months, 1year)", value)
		}
	default:
		return fmt.Errorf("journald: %q is not a key this tool writes", setting.Key)
	}
	return nil
}

// ValidateRetentionValues checks a whole set of answers, in the form's order
// so the first complaint is about the first prompt.
func ValidateRetentionValues(values map[string]string) error {
	for _, setting := range RetentionSettings {
		if err := ValidateRetentionValue(setting, values[setting.Key]); err != nil {
			return err
		}
	}
	for key := range values {
		if _, ok := RetentionSettingFor(key); !ok {
			return fmt.Errorf("journald: %q is not a key this tool writes", key)
		}
	}
	return nil
}

// dropInKeyRe accepts a key name in the drop-in this tool reads back. A line
// whose key is not one is kept verbatim rather than parsed, so a key someone
// added by hand survives being read.
var dropInKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,63}$`)

// ParseDropIn reads the keys out of a journald drop-in.
//
// It is deliberately forgiving about the shape of the file and strict about
// what it takes out of it: a section header is skipped, a comment is skipped,
// and a line that is not KEY=VALUE is skipped. What comes back is only the
// keys this tool manages, because they are the only ones it will write back.
func ParseDropIn(text string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, ";") || strings.HasPrefix(line, "[") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || !dropInKeyRe.MatchString(key) {
			continue
		}
		if _, managed := RetentionSettingFor(key); !managed {
			continue
		}
		// A value carrying a control character is dropped rather than kept.
		// The validator would refuse it before it reached a command line, but
		// it is put on screen first — it seeds the prompt that offers it back
		// — and a NUL drawn into a terminal is not a value, it is a defect.
		// Found by FuzzParseDropIn.
		if strings.ContainsFunc(value, unicode.IsControl) {
			continue
		}
		values[key] = value
	}
	return values
}

// dropInHeader is what the file says about itself. A file with a tool's name
// on it and no way back is a file people delete in anger; this one says how.
const dropInHeader = "# Written by tui-logs. Every line was previewed and confirmed.\n" +
	"# Delete this file and `systemctl restart " + JournaldUnit + "` to undo it.\n\n"

// RenderDropIn returns the text of the drop-in the answers describe.
//
// The file is rewritten rather than patched, because it belongs to this tool
// and holds nothing else: what is not in the answers is not in the file, which
// is what makes clearing a limit possible at all. A key someone added to this
// file by hand does not survive, and the header says whose file it is.
func RenderDropIn(values map[string]string) (string, error) {
	if err := ValidateRetentionValues(values); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(dropInHeader)
	b.WriteString(JournalSection + "\n")
	written := 0
	for _, setting := range RetentionSettings {
		value := strings.TrimSpace(values[setting.Key])
		if value == "" {
			continue
		}
		fmt.Fprintf(&b, "%s=%s\n", setting.Key, value)
		written++
	}
	if written == 0 {
		return "", fmt.Errorf(
			"journald: every answer was empty, so there would be nothing in the file")
	}
	return b.String(), nil
}

// ReadDropIn reads the drop-in this tool owns. A file that is not there is
// not an error: it is the normal state of a machine this has never run on.
func ReadDropIn(path string) (logs.Retention, error) {
	retention := logs.Retention{Path: path, Values: map[string]string{}}
	// The path is DropInPath in production and a temporary file in the tests.
	raw, err := os.ReadFile(path) //#nosec G304 -- the caller passes DropInPath or a test fixture
	if os.IsNotExist(err) {
		return retention, nil
	}
	if err != nil {
		retention.Unavailable = err.Error()
		return retention, nil
	}
	retention.Exists = true
	retention.Content = string(raw)
	retention.Values = ParseDropIn(retention.Content)
	return retention, nil
}

// EffectiveRetention picks the managed keys out of the configuration in
// force, which is what the form falls back to on a machine with no drop-in
// yet: a first run opens on the machine's own numbers rather than on blanks.
func EffectiveRetention(conf []logs.ConfSetting) map[string]string {
	values := map[string]string{}
	for _, setting := range conf {
		if _, managed := RetentionSettingFor(setting.Key); !managed {
			continue
		}
		if setting.Value == "" {
			continue
		}
		values[setting.Key] = setting.Value
	}
	return values
}

// BuildInstallDropIn copies a staged drop-in into place.
//
// `install -D` rather than `cp` for two reasons in one call: it sets the mode,
// so there is no window in which the file is on disk with the wrong one, and
// it creates /etc/systemd/journald.conf.d on a machine that has never had a
// drop-in before.
func BuildInstallDropIn(tempPath string) (logs.Command, error) {
	if !stagedPathRe.MatchString(tempPath) {
		return logs.Command{}, fmt.Errorf(
			"journald: %q is not a staged file path", tempPath)
	}
	return logs.Command{
		Argv: []string{"install", "-D", "-m", DropInMode, tempPath, DropInPath},
		Description: "Install " + tempPath + " as " + DropInPath +
			", creating " + DropInDir + " if it is not there",
		Destructive: true,
	}, nil
}

// stagedPathRe accepts the temporary file the install copies from. It is a
// path this process wrote, so this is a shape check rather than a defence:
// what it stops is an empty or relative path reaching an argv.
var stagedPathRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,1000}$`)

// BuildRestartJournald renders the restart that makes a retention change take
// effect.
//
// It is a restart and not a reload on purpose. systemd-journald re-reads
// journald.conf on SIGHUP, and `systemctl reload` sends one — but the size and
// retention limits are applied when the journal files are opened, so a
// reloaded journald keeps enforcing the limits it started with until
// something else makes it rotate. A restart is the only thing that makes the
// new numbers true now, and it costs a moment during which entries are held
// in the kernel buffer rather than lost.
func BuildRestartJournald() logs.Command {
	return logs.Command{
		Argv: []string{"systemctl", "restart", JournaldUnit},
		Description: "Restart " + JournaldUnit +
			" so the new limits apply to the journal it opens",
		Destructive: true,
	}
}

// RetentionPlanFor assembles the plan around a staged file. It is shared by
// the real backend and the fake, so --demo previews exactly the commands a
// machine would run.
func RetentionPlanFor(existing logs.Retention, content, tempPath string) (logs.RetentionPlan, error) {
	if content == existing.Content {
		return logs.RetentionPlan{}, logs.ErrRetentionUnchanged
	}
	installCmd, err := BuildInstallDropIn(tempPath)
	if err != nil {
		return logs.RetentionPlan{}, err
	}
	plan := logs.RetentionPlan{
		Path:     DropInPath,
		Content:  content,
		TempPath: tempPath,
		Diff: logs.UnifiedDiff(diffName(existing), DropInPath,
			existing.Content, content),
		Commands: []logs.Command{installCmd, BuildRestartJournald()},
	}
	if existing.Exists {
		plan.Warning = DropInPath + " already exists and is replaced in full."
	}
	return plan, nil
}

// diffName is what the left-hand side of the diff is called: the file when
// there is one, and a name saying there is not when there is not.
func diffName(existing logs.Retention) string {
	if existing.Exists {
		return existing.Path
	}
	return existing.Path + " (does not exist yet)"
}
