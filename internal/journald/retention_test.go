package journald

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tui-tools/tui-logs/internal/logs"
)

// What matters here is what may reach the drop-in and what may not. Every
// value the form collects ends up as a line in a file journald parses, and
// journald ignores what it does not understand — so a value that gets through
// wrong is not an error anyone sees, it is a limit that silently never
// applied.

func TestValidateRetentionValue(t *testing.T) {
	size, ok := RetentionSettingFor(KeySystemMaxUse)
	if !ok {
		t.Fatal("SystemMaxUse is not in the settings table")
	}
	for _, value := range []string{"", "500M", "2G", "100K", "4T", "1000000"} {
		if err := ValidateRetentionValue(size, value); err != nil {
			t.Errorf("SystemMaxUse %q = %v, want it accepted", value, err)
		}
	}
	for _, value := range []string{
		"2 G", "2GB", "half", "-1", "10%",
		// The shape that would matter if it got through: a second key, or a
		// comment, smuggled into one answer.
		"2G\nStorage=none", "2G # and", "2G]", "2G=x",
	} {
		if err := ValidateRetentionValue(size, value); err == nil {
			t.Errorf("SystemMaxUse %q was accepted", value)
		}
	}

	age, _ := RetentionSettingFor(KeyMaxRetentionSec)
	for _, value := range []string{"", "30d", "6months", "1year", "0", "12h"} {
		if err := ValidateRetentionValue(age, value); err != nil {
			t.Errorf("MaxRetentionSec %q = %v, want it accepted", value, err)
		}
	}
	for _, value := range []string{"forever", "30 d", "30d 2h", "d30", "30d\nSeal=no"} {
		if err := ValidateRetentionValue(age, value); err == nil {
			t.Errorf("MaxRetentionSec %q was accepted", value)
		}
	}

	storage, _ := RetentionSettingFor(KeyStorage)
	for _, value := range StorageChoices {
		if err := ValidateRetentionValue(storage, value); err != nil {
			t.Errorf("Storage %q = %v, want it accepted", value, err)
		}
	}
	// `none` throws every entry away, and a log reader does not offer it.
	for _, value := range []string{"none", "Persistent", "persistent yes"} {
		if err := ValidateRetentionValue(storage, value); err == nil {
			t.Errorf("Storage %q was accepted", value)
		}
	}
}

func TestValidateRetentionValuesRefusesAKeyThisToolDoesNotWrite(t *testing.T) {
	if err := ValidateRetentionValues(map[string]string{
		KeySystemMaxUse: "2G", "ForwardToSyslog": "yes",
	}); err == nil {
		t.Error("a key outside the form was accepted")
	}
}

func TestRenderDropIn(t *testing.T) {
	content, err := RenderDropIn(map[string]string{
		KeySystemMaxUse:    "2G",
		KeyMaxRetentionSec: "1month",
		KeyStorage:         "persistent",
	})
	if err != nil {
		t.Fatalf("RenderDropIn: %v", err)
	}
	for _, want := range []string{
		JournalSection, "SystemMaxUse=2G", "MaxRetentionSec=1month",
		"Storage=persistent",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("the drop-in does not carry %q:\n%s", want, content)
		}
	}
	// A file with a tool's name on it says how to undo it.
	if !strings.Contains(content, "Delete this file") {
		t.Errorf("the drop-in does not say how to undo it:\n%s", content)
	}
	// The keys come out in the table's order, whatever order the map was in.
	if strings.Index(content, "SystemMaxUse") > strings.Index(content, "Storage=") {
		t.Errorf("the keys are not in the form's order:\n%s", content)
	}

	// An empty answer leaves the key out, which is how a limit is removed.
	content, err = RenderDropIn(map[string]string{
		KeySystemMaxUse: "", KeyMaxRetentionSec: "", KeyStorage: "volatile",
	})
	if err != nil {
		t.Fatalf("RenderDropIn: %v", err)
	}
	if strings.Contains(content, "SystemMaxUse") ||
		strings.Contains(content, "MaxRetentionSec") {
		t.Errorf("an empty answer still wrote its key:\n%s", content)
	}

	// Three empty answers would be a file with nothing in it, which is a
	// refusal rather than a file.
	if _, err := RenderDropIn(map[string]string{}); err == nil {
		t.Error("an entirely empty form rendered a file")
	}
	// And a value the validator refuses never becomes a line.
	if _, err := RenderDropIn(map[string]string{
		KeySystemMaxUse: "2G\nStorage=none",
	}); err == nil {
		t.Error("a smuggled second key was rendered into the file")
	}
}

func TestParseDropInReadsBackWhatRenderWrote(t *testing.T) {
	values := map[string]string{
		KeySystemMaxUse:    "4G",
		KeyMaxRetentionSec: "90d",
		KeyStorage:         "auto",
	}
	content, err := RenderDropIn(values)
	if err != nil {
		t.Fatalf("RenderDropIn: %v", err)
	}
	got := ParseDropIn(content)
	for key, want := range values {
		if got[key] != want {
			t.Errorf("%s read back as %q, want %q", key, got[key], want)
		}
	}

	// Only the managed keys come back: a key someone added to this file by
	// hand is not one this tool will write, so it is not one it reads either.
	got = ParseDropIn("[Journal]\nSystemMaxUse=1G\nForwardToSyslog=yes\n# a comment\nnonsense\n")
	if len(got) != 1 || got[KeySystemMaxUse] != "1G" {
		t.Errorf("ParseDropIn = %v, want only SystemMaxUse", got)
	}
}

func TestReadDropIn(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "50-tui-logs.conf")

	// A file that is not there is the normal state of a machine this has
	// never run on, and not an error.
	retention, err := ReadDropIn(missing)
	if err != nil {
		t.Fatalf("ReadDropIn on a missing file: %v", err)
	}
	if retention.Exists || retention.Content != "" || len(retention.Values) != 0 {
		t.Errorf("a missing file read as %+v", retention)
	}

	content := "[Journal]\nSystemMaxUse=3G\n"
	if err := os.WriteFile(missing, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	retention, err = ReadDropIn(missing)
	if err != nil {
		t.Fatalf("ReadDropIn: %v", err)
	}
	if !retention.Exists || retention.Content != content {
		t.Errorf("read %+v, want the file verbatim", retention)
	}
	if retention.Values[KeySystemMaxUse] != "3G" {
		t.Errorf("SystemMaxUse = %q, want 3G", retention.Values[KeySystemMaxUse])
	}
}

func TestRetentionValueFallsBackToWhatIsInForce(t *testing.T) {
	retention := logs.Retention{
		Values:    map[string]string{KeySystemMaxUse: "2G"},
		Effective: map[string]string{KeySystemMaxUse: "4G", KeyStorage: "persistent"},
	}
	if got := retention.Value(KeySystemMaxUse); got != "2G" {
		t.Errorf("SystemMaxUse = %q, want the drop-in's 2G", got)
	}
	// A key the drop-in does not set opens on the machine's own value, which
	// is what makes a first run start from real numbers.
	if got := retention.Value(KeyStorage); got != "persistent" {
		t.Errorf("Storage = %q, want the effective persistent", got)
	}
	if got := retention.Value(KeyMaxRetentionSec); got != "" {
		t.Errorf("MaxRetentionSec = %q, want empty", got)
	}
}

func TestEffectiveRetentionTakesOnlyTheManagedKeys(t *testing.T) {
	values := EffectiveRetention([]logs.ConfSetting{
		{Key: KeySystemMaxUse, Value: "4G"},
		{Key: KeyStorage, Value: ""},
		{Key: "ForwardToSyslog", Value: "yes"},
		{Key: KeyMaxRetentionSec, Value: "1month"},
	})
	want := map[string]string{KeySystemMaxUse: "4G", KeyMaxRetentionSec: "1month"}
	if len(values) != len(want) {
		t.Fatalf("EffectiveRetention = %v, want %v", values, want)
	}
	for key, value := range want {
		if values[key] != value {
			t.Errorf("%s = %q, want %q", key, values[key], value)
		}
	}
}

func TestRetentionPlanIsInstallThenRestart(t *testing.T) {
	existing := logs.Retention{Path: DropInPath}
	content, err := RenderDropIn(map[string]string{KeySystemMaxUse: "2G"})
	if err != nil {
		t.Fatalf("RenderDropIn: %v", err)
	}
	plan, err := RetentionPlanFor(existing, content, "/tmp/tui-logs-x/50-tui-logs.conf")
	if err != nil {
		t.Fatalf("RetentionPlanFor: %v", err)
	}
	if len(plan.Commands) != 2 {
		t.Fatalf("the plan has %d commands, want install then restart: %v",
			len(plan.Commands), plan.Commands)
	}
	install := strings.Join(plan.Commands[0].Argv, " ")
	want := "install -D -m 644 /tmp/tui-logs-x/50-tui-logs.conf " + DropInPath
	if install != want {
		t.Errorf("install = %q, want %q", install, want)
	}
	restart := strings.Join(plan.Commands[1].Argv, " ")
	if restart != "systemctl restart "+JournaldUnit {
		t.Errorf("restart = %q", restart)
	}
	// The restart is a restart and not a reload, and the plan's own
	// description is where that is written down.
	if !strings.Contains(plan.Commands[1].Description, "limits") {
		t.Errorf("the restart does not say what it is for: %q",
			plan.Commands[1].Description)
	}
	if !strings.Contains(plan.Diff, "+SystemMaxUse=2G") {
		t.Errorf("the diff does not show the new line:\n%s", plan.Diff)
	}

	// Writing the file the machine already has is refused rather than run.
	existing = logs.Retention{Path: DropInPath, Exists: true, Content: content}
	if _, err := RetentionPlanFor(existing, content, "/tmp/x/f"); err == nil {
		t.Error("an unchanged file built a plan")
	}

	// A staging path that is not a path never reaches an argv.
	if _, err := RetentionPlanFor(logs.Retention{}, content, "relative/path"); err == nil {
		t.Error("a relative staging path was accepted")
	}
}

func TestFakeRetentionRoundTrip(t *testing.T) {
	fake := NewFake()

	// The demo starts with no drop-in of its own, so the form opens on the
	// values `systemd-analyze cat-config` reported for the sample machine.
	before, err := fake.ReadRetention(t.Context())
	if err != nil {
		t.Fatalf("ReadRetention: %v", err)
	}
	if before.Exists {
		t.Error("the sample machine starts with a tui-logs drop-in")
	}
	if got := before.Value(KeySystemMaxUse); got != "4G" {
		t.Errorf("SystemMaxUse opens on %q, want the sample machine's 4G", got)
	}

	plan, err := fake.BuildRetention(map[string]string{
		KeySystemMaxUse: "2G", KeyStorage: "persistent",
	})
	if err != nil {
		t.Fatalf("BuildRetention: %v", err)
	}
	// Nothing has happened yet: a plan is a plan.
	if after, _ := fake.ReadRetention(t.Context()); after.Exists {
		t.Fatal("building the plan already wrote the drop-in")
	}

	for _, cmd := range plan.Commands {
		if _, err := fake.Run(t.Context(), cmd); err != nil {
			t.Fatalf("Run %v: %v", cmd.Argv, err)
		}
	}
	after, err := fake.ReadRetention(t.Context())
	if err != nil {
		t.Fatalf("ReadRetention: %v", err)
	}
	if !after.Exists || after.Values[KeySystemMaxUse] != "2G" {
		t.Errorf("after the plan the drop-in is %+v", after)
	}
	// The sample machine's effective configuration moves with it, the way a
	// real one does once journald has restarted.
	if after.Effective[KeyStorage] != "persistent" {
		t.Errorf("the effective Storage is %q, want persistent",
			after.Effective[KeyStorage])
	}
}

func TestDropInInstallIsRecognisedByItsDestination(t *testing.T) {
	// The routing that decides whether an `install` escalates: an export goes
	// into a home directory as the user, the drop-in goes into /etc as root,
	// and the difference is visible in the argv.
	drop, err := BuildInstallDropIn("/tmp/tui-logs-1/50-tui-logs.conf")
	if err != nil {
		t.Fatalf("BuildInstallDropIn: %v", err)
	}
	if !isDropInInstall(drop) {
		t.Error("the drop-in install was not recognised")
	}
	export, err := BuildInstallExport("/tmp/tui-logs-2/x.log", "/home/you/x.log")
	if err != nil {
		t.Fatalf("BuildInstallExport: %v", err)
	}
	if isDropInInstall(export) {
		t.Error("an export was mistaken for the drop-in install")
	}
	if !isUnitRestart(BuildRestartJournald()) {
		t.Error("the journald restart was not recognised as one")
	}
	if isUnitRestart(BuildListUnits()) {
		t.Error("listing units was mistaken for a restart")
	}
}
