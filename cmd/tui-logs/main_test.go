package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/config"
	"github.com/tui-tools/tui-logs/internal/journald"
	"github.com/tui-tools/tui-logs/internal/logs"
)

// baseConfig is the configuration as it stands before the flags are folded in.
func baseConfig() config.Config {
	return config.Config{Tool: toolName, Values: defaults()}
}

// demoFilter is the window the tool opens on.
func demoFilter() logs.Filter {
	return logs.Filter{Priority: logs.PriorityAny, Lines: logs.DefaultLines}
}

func TestParseFlags(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	opts, err := parseFlags([]string{"--demo", "--theme", "/t/colors.toml",
		"--lines", "50"}, devNull)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !opts.demo || opts.themePath != "/t/colors.toml" || opts.lines != 50 {
		t.Errorf("opts = %+v", opts)
	}
	if opts.sudoSet {
		t.Error("sudoSet should be false when -sudo is absent")
	}
	if !opts.linesSet {
		t.Error("linesSet should be true when -lines was passed")
	}
}

func TestApplyOverrides(t *testing.T) {
	cfg := baseConfig()
	applyOverrides(&cfg, options{themePath: "/t/colors.toml"})
	if got := cfg.Theme(); got != "/t/colors.toml" {
		t.Errorf("Theme() = %q", got)
	}
	// An untouched -sudo must not clear the configured prefix.
	if got := cfg.String(config.KeySudo, ""); got != "sudo -n" {
		t.Errorf("sudo = %q, want the config value", got)
	}
	// And an untouched -lines must not overwrite the configured backlog with
	// the flag's own default.
	if got := cfg.Int(keyLines, 0); got != logs.DefaultLines {
		t.Errorf("lines = %d, want the config value", got)
	}

	// An explicit empty -sudo disables escalation.
	cfg = baseConfig()
	applyOverrides(&cfg, options{sudoSet: true, sudo: ""})
	if got := cfg.String(config.KeySudo, "unset"); got != "" {
		t.Errorf("sudo = %q, want empty", got)
	}
	if got := cfg.SudoPrefix(); got != nil {
		t.Errorf("SudoPrefix = %q, want nil", got)
	}
}

func TestDefaultsCoverEveryFlag(t *testing.T) {
	// Every key a flag can override must be declared, otherwise the
	// environment layer silently skips it.
	for _, key := range []string{config.KeySudo, config.KeyTheme, keyLines} {
		if _, ok := defaults()[key]; !ok {
			t.Errorf("defaults() is missing %q", key)
		}
	}
}

func TestPickBackendDemo(t *testing.T) {
	backend, err := pickBackend(baseConfig(), options{demo: true}, compat.Result{})
	if err != nil {
		t.Fatalf("pickBackend: %v", err)
	}
	if !strings.Contains(backend.Describe(), "demo") {
		t.Errorf("Describe = %q, want it to say it is a demo", backend.Describe())
	}
}

// TestCheckReportsTheState covers the contract the smoke test depends on: the
// counts, the boots, the disk usage and how the journal was opened, all under
// names a shell script can grep for.
func TestCheckReportsTheState(t *testing.T) {
	backend := journald.NewFake()
	var out bytes.Buffer
	if err := runCheck(backend, demoFilter(), compat.Result{}, &out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	for _, want := range []string{
		`"tool": "tui-logs"`,
		`"backend": "journald"`,
		// The sample journal is the one the README describes: three hundred
		// entries, two boots, and postgresql failing over and over.
		`"entries": 300`,
		`"boots": 2`,
		`"denied": false`,
		`"persistent": true`,
		`"command": "journalctl --no-pager -o json -n 500"`,
		`"name": "sshd.service"`,
		`"diskUsage": "Archived and active journals take up 3.9G`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("--check output is missing %s", want)
		}
	}
}

// TestCheckRunsNothing: --check exists to be safe to run anywhere, including
// in CI against a production-shaped machine, so it must not run a single
// command through the backend — and least of all one that vacuums a journal.
func TestCheckRunsNothing(t *testing.T) {
	backend := journald.NewFake()
	var out bytes.Buffer
	if err := runCheck(backend, demoFilter(), compat.Result{}, &out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if ran := backend.Ran(); len(ran) != 0 {
		t.Errorf("--check ran %d commands: %v", len(ran), ran)
	}
	for _, forbidden := range []string{"--vacuum", "--rotate", "install -m"} {
		if strings.Contains(out.String(), forbidden) {
			t.Errorf("--check printed %q, which means it built one", forbidden)
		}
	}
}

// TestCheckCountsTheHourSeparately: the window on screen is bounded by a line
// count, so "errors in the last hour" has to be its own read or it means
// nothing on a machine logging heavily.
func TestCheckCountsTheHourSeparately(t *testing.T) {
	backend := journald.NewFake()
	var out bytes.Buffer
	if err := runCheck(backend, demoFilter(), compat.Result{}, &out); err != nil {
		t.Fatalf("runCheck: %v", err)
	}
	if !strings.Contains(out.String(), `"errorsLastHour": 6`) {
		t.Errorf("--check did not count the sample machine's recent errors:\n%s",
			out.String())
	}
}
