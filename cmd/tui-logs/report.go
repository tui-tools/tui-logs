package main

import (
	"context"
	"fmt"
	"io"

	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/report"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-logs/internal/logs"
)

// bootListFeature is the manifest feature that decides how the boot list is
// read. It is the one thing in this tool the systemd version gates, so it is
// the one capability the report spells out.
const bootListFeature = "list-boots-json"

// runReport prints the block a bug report needs and exits. Everything generic
// — the kit version, the distribution, the kernel, the terminal, where the
// binary came from — is collected by the kit, so the whole family answers
// --report in the same shape. What this function adds is the part only
// tui-logs knows: the systemd the compat probe read, how that version makes
// the boot list be parsed, and how wide a window one read pulls back.
//
// It never reads the journal. --check is the flag that does that, and on a
// machine that refuses the system journal it comes back with this user's own
// entries; a report has to work before any of that, because the refusal may be
// the bug. For the same reason a machine with no journalctl at all still gets
// a report, with the backend error as one of its lines: "there is nothing here
// to drive" is a bug report, not a refusal.
func runReport(cfg config.Config, opts options, out io.Writer) error {
	palette, _ := theme.ResolvePalette()

	// The same probe --check and the header use. There is one version probe in
	// this tool and this is it.
	backendCompat := probeCompat(context.Background(), opts.demo)

	// The backend is built so that a machine without journalctl says so, but
	// its absence must not cost the report: the name is known from the
	// manifest either way.
	name := backendName
	var selectError string
	if backend, err := pickBackend(cfg, opts, backendCompat); err != nil {
		selectError = err.Error()
	} else {
		name = backend.Name()
	}

	info := report.Info{
		Tool:           toolName,
		Version:        version,
		Backend:        name,
		BackendVersion: backendCompat.Version,
		BackendDetail:  backendCompat.Detail,
		Demo:           opts.demo,
		Sudo:           cfg.String(config.KeySudo, ""),
		Theme:          palette.Name,
	}
	if opts.demo {
		// The fake imitates journald down to the argv it builds, so the
		// backend line says demo and the imitated backend is named next to it
		// rather than left to be guessed from the tool's name.
		info.Backend = "demo"
		info.Extra = append(info.Extra, report.Field{
			Key: "demo backend", Value: backendName,
		})
	}
	if !opts.demo {
		// Which boot-list parser ran is a fact about the systemd on this
		// machine, and there is no systemd behind the fake, so the line is
		// live-only rather than an invented "json".
		info.Extra = append(info.Extra, report.Field{
			Key:   "boot list",
			Value: bootListLine(backendCompat.Caps().Has(bootListFeature)),
		})
	}
	info.Extra = append(info.Extra, report.Field{
		Key: "lines", Value: fmt.Sprint(cfg.Int(keyLines, logs.DefaultLines)),
	})
	if selectError != "" {
		info.Extra = append(info.Extra, report.Field{
			Key: "backend error", Value: selectError,
		})
	}

	_, err := io.WriteString(out, report.Render(info))
	return err
}

// bootListLine says which of the two boot-list parsers ran. journalctl learned
// to print `--list-boots` as JSON in systemd 252; below that the same list is
// read out of its table. The picker looks identical either way, so a boot that
// is missing or misdated is a bug in whichever parser this machine used, and
// the report has to name it.
func bootListLine(json bool) string {
	if json {
		return "json"
	}
	return "parsed from the table (systemd before 252)"
}

// reportUsage is the flag's one-line help, kept here next to what it prints.
var reportUsage = fmt.Sprintf(
	"print the versions and machine facts a bug report needs, then exit "+
		"(no UI, no privileges, nothing about you: paste it into a %s issue)",
	toolName)
