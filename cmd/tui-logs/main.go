// Command tui-logs is a terminal UI for the systemd journal: the entries with
// the filters that narrow them, what one entry really carries, what the window
// is made of, and what the journal costs on disk. It previews the exact
// command line of every change before running it. systemd's journal is the
// backend implemented today; the code is written against a generic interface
// so another log store could follow.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-logs/internal/journald"
	"github.com/tui-tools/tui-logs/internal/logs"
)

// toolName is the binary name, which is also the configuration directory:
// /etc/tui-logs/config.toml and ~/.config/tui-logs/config.toml.
const toolName = "tui-logs"

// backendName is the manifest's name for the backend this tool drives. It is
// what the version probe and the compatibility block are keyed on.
const backendName = "journald"

// keyLines is the configuration key for how much backlog one read pulls.
const keyLines = "lines"

// version is stamped by the release build (-ldflags "-X main.version=…").
var version = "dev"

// defaults declares the configuration keys tui-logs understands. Only these
// are read from the environment (TUI_LOGS_SUDO, …).
func defaults() map[string]string {
	return map[string]string{
		config.KeySudo:  "sudo -n",
		config.KeyTheme: "",
		keyLines:        fmt.Sprint(logs.DefaultLines),
	}
}

// options holds the parsed command line.
type options struct {
	demo        bool
	check       bool
	report      bool
	themePath   string
	sudo        string
	lines       int
	showVersion bool
	// sudoSet records whether -sudo was passed, so `--sudo ""` can disable
	// escalation instead of reading as "not given".
	sudoSet bool
	// linesSet records whether -lines was passed.
	linesSet bool
}

// parseFlags defines and reads the command line.
func parseFlags(args []string, out *os.File) (options, error) {
	var opts options
	fs := flag.NewFlagSet(toolName, flag.ContinueOnError)
	fs.SetOutput(out)
	fs.BoolVar(&opts.demo, "demo", false,
		"run against a sample journal, without touching the real one")
	fs.BoolVar(&opts.check, "check", false,
		"read the journal and print what it found as JSON, then exit "+
			"(no UI, no changes); exit 1 if the backend cannot be read")
	fs.BoolVar(&opts.report, "report", false, reportUsage)
	fs.StringVar(&opts.themePath, "theme", "",
		"path to an Omarchy-style colors.toml (overrides the config file)")
	fs.StringVar(&opts.sudo, "sudo", "",
		"privilege escalation prefix, e.g. \"sudo -n\" or \"\" to disable")
	fs.IntVar(&opts.lines, "lines", logs.DefaultLines,
		"how many entries one read pulls back")
	fs.BoolVar(&opts.showVersion, "version", false, "print the version and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(out, "tui-logs — a terminal UI for the systemd journal\n\n"+
			"Usage:\n  tui-logs [flags]\n\nFlags:\n")
		fs.PrintDefaults()
		_, _ = fmt.Fprintf(out, "\nConfiguration is read from %s, then %s, "+
			"then TUI_LOGS_* in the environment.\n",
			config.SystemPathFor(toolName), config.UserPathFor(toolName))
	}
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "sudo":
			opts.sudoSet = true
		case "lines":
			opts.linesSet = true
		}
	})
	return opts, nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, toolName+":", err)
		os.Exit(1)
	}
}

// run wires the configuration, the backend and the Bubble Tea program.
func run(args []string) error {
	opts, err := parseFlags(args, os.Stdout)
	if err != nil {
		// flag already printed the reason and the usage.
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if opts.showVersion {
		fmt.Println(toolName, version)
		return nil
	}

	cfg, err := config.Load(config.Options{Tool: toolName, Defaults: defaults()})
	if err != nil {
		return err
	}
	applyOverrides(&cfg, opts)

	// The backend version is probed once, before the backend is built,
	// because the backend needs the capability set: whether the boot list can
	// be asked for as JSON is a version question, and the answer comes from
	// the manifest.
	// The configured theme is handed to the kit through the same variable the
	// user could set by hand, so precedence stays in one place. It is set
	// before the backend is built so --report can name the theme the UI would
	// have used even on a machine where no backend can be.
	if path := cfg.Theme(); path != "" {
		if err := os.Setenv("TUI_THEME", path); err != nil {
			return err
		}
	}

	// --report is the non-interactive path that must work everywhere. It
	// reads nothing privileged and it survives a machine with no journalctl at
	// all, because "there is no backend here" is one of the things a bug
	// report has to be able to say. So it comes before the backend is
	// required.
	if opts.report {
		return runReport(cfg, opts, os.Stdout)
	}

	backendCompat := probeCompat(context.Background(), opts.demo)

	backend, err := pickBackend(cfg, opts, backendCompat)
	if err != nil {
		return err
	}
	filter := logs.Filter{
		Priority: logs.PriorityAny,
		Lines:    cfg.Int(keyLines, logs.DefaultLines),
	}

	// --check is the non-interactive path: it reads the journal and prints,
	// and never starts a terminal program.
	if opts.check {
		return runCheck(backend, filter, backendCompat, os.Stdout)
	}

	program := tea.NewProgram(newApp(backend, theme.New(), filter, backendCompat),
		tea.WithAltScreen())
	_, err = program.Run()
	return err
}

// applyOverrides folds the command line into the configuration, which is the
// last and highest-precedence layer.
func applyOverrides(cfg *config.Config, opts options) {
	if opts.themePath != "" {
		cfg.Set(config.KeyTheme, opts.themePath)
	}
	// An explicitly empty -sudo disables escalation, so the flag is applied
	// whenever it was passed, empty value included.
	if opts.sudoSet {
		cfg.Set(config.KeySudo, opts.sudo)
	}
	if opts.linesSet {
		cfg.Set(keyLines, fmt.Sprint(opts.lines))
	}
}

// pickBackend returns the demo backend or the real one.
func pickBackend(cfg config.Config, opts options,
	backendCompat compat.Result) (logs.Backend, error) {
	if opts.demo {
		return journald.NewFake(), nil
	}
	return journald.NewReal(cfg.SudoPrefix(), backendCompat.Caps())
}
