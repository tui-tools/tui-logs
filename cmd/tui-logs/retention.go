// The retention form: the storage screen's one writing key.
//
// Everything else on that screen reads. `v` and `V` vacuum the journal down
// now; neither changes what it grows back to, and the configuration that
// decides that has been on the screen since the first version with nothing
// to do about it but read it. `S` collects three answers, renders the drop-in
// this tool owns, and shows the unified diff of it against what is on disk
// before either command runs.
package main

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-logs/internal/journald"
	"github.com/tui-tools/tui-logs/internal/logs"
)

// retentionPrefix marks the input and picker fields the form collects, so one
// dispatch handles all three and adding a key to the table adds a step.
const retentionPrefix = "retention:"

// retentionField is the field id for one journald key.
func retentionField(key string) string { return retentionPrefix + key }

// retentionKeyOf returns the journald key a field id carries.
func retentionKeyOf(field string) (string, bool) {
	return strings.CutPrefix(field, retentionPrefix)
}

// retentionForm is the state of a form in progress: what the machine said
// when it opened, what has been answered, and which prompt is next.
type retentionForm struct {
	seed   logs.Retention
	values map[string]string
	step   int
}

// retentionMsg carries the drop-in read the form opens on.
type retentionMsg struct {
	retention logs.Retention
	err       error
}

// openRetentionForm reads the drop-in this tool owns, then asks the first
// question. The read is in the background because it asks systemd-analyze
// what the configuration in force is, which is a process like any other.
func (a *app) openRetentionForm() tea.Cmd {
	if !a.caps.SupportsRetention {
		if reason, ok := a.caps.Reason(logs.CapRetention); ok {
			a.setStatus(ui.StatusWarn, "the retention settings cannot be written — "+reason)
			return nil
		}
		a.setStatus(ui.StatusWarn, "this backend cannot write the retention settings")
		return nil
	}
	a.screen = screenStorage
	a.busy = true
	a.setStatus(ui.StatusInfo, "reading "+journald.DropInPath+"…")
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		retention, err := backend.ReadRetention(ctx)
		return retentionMsg{retention: retention, err: err}
	}
}

// onRetention starts the form on what the machine said.
func (a *app) onRetention(msg retentionMsg) tea.Cmd {
	a.busy = false
	if msg.err != nil {
		a.setStatus(ui.StatusError, msg.err.Error())
		return nil
	}
	if msg.retention.Unavailable != "" {
		a.setStatusf(ui.StatusWarn, "%s could not be read: %s",
			msg.retention.Path, msg.retention.Unavailable)
		return nil
	}
	a.retention = &retentionForm{
		seed:   msg.retention,
		values: map[string]string{},
	}
	a.setStatus(ui.StatusInfo, "")
	return a.askRetention()
}

// askRetention opens the prompt for the current step. Each one starts on what
// the machine says today: the drop-in's value when it has one, and the value
// in force otherwise — so a first run opens on the machine's real numbers
// rather than on three blanks.
func (a *app) askRetention() tea.Cmd {
	form := a.retention
	if form == nil {
		return nil
	}
	if form.step >= len(journald.RetentionSettings) {
		return a.stageRetention()
	}
	setting := journald.RetentionSettings[form.step]
	current, answered := form.values[setting.Key]
	if !answered {
		current = form.seed.Value(setting.Key)
	}

	if len(setting.Options) > 0 {
		a.openPicker(retentionField(setting.Key), setting.Title,
			setting.Options, current)
		return nil
	}
	a.input = ui.NewInput(setting.Title, "empty leaves this key out", current)
	a.input.Help = setting.Help
	a.inputFor = retentionField(setting.Key)
	a.mode = modeInput
	return nil
}

// answerRetention records one answer and moves on. A value journald would not
// take sends the same prompt back with the reason, rather than failing at the
// end of a form the user has finished filling in.
func (a *app) answerRetention(key, value string) tea.Cmd {
	form := a.retention
	if form == nil {
		return nil
	}
	setting, ok := journald.RetentionSettingFor(key)
	if !ok {
		a.cancelRetention()
		return nil
	}
	value = strings.TrimSpace(value)
	if err := journald.ValidateRetentionValue(setting, value); err != nil {
		a.setStatus(ui.StatusWarn, err.Error())
		form.values[key] = value
		return a.askRetention()
	}
	form.values[key] = value
	form.step++
	return a.askRetention()
}

// cancelRetention abandons a half-filled form.
func (a *app) cancelRetention() { a.retention = nil }

// stageRetention renders the drop-in and opens the confirm dialog.
//
// The rendering and the staging are local file work — a few lines written to
// a private temporary directory — so they happen here rather than in the
// background, unlike the export, whose staging is a read of the journal.
func (a *app) stageRetention() tea.Cmd {
	form := a.retention
	a.retention = nil
	if form == nil {
		return nil
	}
	plan, err := a.backend.BuildRetention(form.values)
	if err != nil {
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	a.openRetentionConfirm(plan)
	return nil
}

// openRetentionConfirm shows what the file will say, as a diff, and the two
// commands that make it true.
func (a *app) openRetentionConfirm(retention logs.RetentionPlan) {
	title := "Write " + retention.Path
	body := "The journal's retention is set by this file, which belongs to " +
		"tui-logs and holds nothing else. Restarting " + journald.JournaldUnit +
		" is what applies it: journald re-reads its configuration on SIGHUP, " +
		"but the size and age limits are enforced from the moment the journal " +
		"files are opened, so a reload alone would leave the old limits in " +
		"force until something else rotated them. The restart loses no entries " +
		"— what arrives during it waits in the kernel's buffer."
	if retention.Warning != "" {
		body += "\n\n" + retention.Warning
	}
	// The prose is wrapped and the diff is only clipped: a diff that was
	// reflowed would stop being a diff.
	body = wrapParagraphs(body, a.dialogWidth()) + "\n\n" +
		clipLines(strings.TrimRight(retention.Diff, "\n"), a.dialogWidth())

	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   title,
		Body:    body,
		Command: a.previewAll(retention.Commands),
		Danger:  true,
		Payload: plan{title: title, commands: retention.Commands},
	}
}

// retentionHint is the storage screen's line about the form, and says what
// the machine is set to now so the key is worth pressing for a reason.
func (a *app) retentionHint() string {
	if !a.caps.SupportsRetention {
		if reason, ok := a.caps.Reason(logs.CapRetention); ok {
			return "cannot be changed from here — " + reason
		}
		return "cannot be changed from here"
	}
	return fmt.Sprintf("S edits %s, %s and %s in %s — previewed as a diff first",
		journald.KeySystemMaxUse, journald.KeyMaxRetentionSec,
		journald.KeyStorage, journald.DropInPath)
}
