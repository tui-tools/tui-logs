package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-logs/internal/logs"
)

// checkTimeout bounds the read. Loading the model shells out to journalctl
// three times, to systemctl and to systemd-analyze, and a machine whose
// journal is enormous must not hang a non-interactive check forever.
const checkTimeout = 120 * time.Second

// errorWindowLines bounds the "errors in the last hour" read. An hour of a
// machine in trouble can be a lot of lines, and what the field reports is a
// count, so it only has to be larger than any count worth acting on.
const errorWindowLines = 5000

// countReport is one row of a "top" list, flattened for a shell script.
type countReport struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// bootReport is one boot, without the timestamps a script cannot compare.
type bootReport struct {
	Index int    `json:"index"`
	ID    string `json:"id"`
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`
}

// checkReport is what --check prints: what the journal costs, what the window
// holds, how it was opened, and the model the backend parsed in full.
//
// It is a report of the read path only. --check never builds and never runs a
// mutation: the whole point is that it is safe to run anywhere, including in
// CI against a production-shaped machine, and a suite that vacuumed the
// journal of the machine it was testing would be a suite nobody could run
// twice.
type checkReport struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
	Backend string `json:"backend"`
	// Describe is the backend's own one-line summary, which is where the demo
	// backend says it is a demo.
	Describe string `json:"describe"`

	// Escalated reports that the plain read was refused and the journal was
	// opened through the privilege prefix; Denied that neither was permitted,
	// so what was read is this user's own entries. AccessNote is the sentence
	// the header shows about it.
	Escalated  bool   `json:"escalated"`
	Denied     bool   `json:"denied"`
	AccessNote string `json:"accessNote,omitempty"`

	// Command is the journalctl invocation the entries came from, so a reader
	// of the JSON can run it themselves.
	Command string `json:"command"`

	// Entries is how many records the window returned, Errors how many of
	// them were `err` or worse, and Truncated whether the read hit its limit.
	Entries   int  `json:"entries"`
	Errors    int  `json:"errors"`
	Warnings  int  `json:"warnings"`
	Truncated bool `json:"truncated"`

	// ErrorsLastHour is a second, narrower read: `-p err --since -1h`. It is
	// the one number worth alerting on, so it is its own field rather than
	// something to be derived from the window.
	ErrorsLastHour int `json:"errorsLastHour"`

	// TopUnits are the noisiest sources of the window, most first.
	TopUnits []countReport `json:"topUnits"`

	// Boots is how many boots the journal still holds, and BootList what they
	// are.
	Boots    int          `json:"boots"`
	BootList []bootReport `json:"bootList,omitempty"`

	// DiskUsage is `journalctl --disk-usage` verbatim, DiskBytes the size it
	// named, and Persistent whether /var/log/journal exists — which is what
	// decides whether any of this survives a reboot.
	DiskUsage  string `json:"diskUsage,omitempty"`
	DiskBytes  int64  `json:"diskBytes"`
	Persistent bool   `json:"persistent"`

	// Units is how many units the picker was offered, and ConfSettings how
	// many settings the effective journald.conf carried.
	Units        int `json:"units"`
	ConfSettings int `json:"confSettings"`

	// Compat is what the backend version probe found. It is reported rather
	// than asserted: an untested version is a fact about the machine, not a
	// failure of the read path.
	Compat compat.Result `json:"compat"`
	// Storage is the parsed storage state in full.
	Storage logs.Storage `json:"storage"`
}

// runCheck exercises the backend's real read path and prints what it parsed
// as JSON. It returns an error when the backend cannot be read, which main
// turns into a non-zero exit — so a caller can treat the exit code alone as
// the verdict.
//
// A machine where the system journal cannot be opened is not a failure: the
// user's own entries come back, Denied is true and AccessNote says what to do
// about it. That is the read path working, and it is what the smoke test
// asserts on an unprivileged run.
func runCheck(backend logs.Backend, filter logs.Filter,
	backendCompat compat.Result, out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()

	model, err := backend.Load(ctx, filter)
	if err != nil {
		return fmt.Errorf("%s backend read failed: %w", backend.Name(), err)
	}

	report := checkReport{
		Tool:           toolName,
		Version:        version,
		Backend:        backend.Name(),
		Describe:       backend.Describe(),
		Escalated:      model.Access.Escalated,
		Denied:         model.Access.Denied,
		AccessNote:     model.Access.Note,
		Command:        model.Command.String(),
		Entries:        model.Stats.Total,
		Errors:         model.Stats.Errors,
		Warnings:       model.Stats.Warnings,
		Truncated:      model.Stats.Truncated,
		ErrorsLastHour: errorsLastHour(ctx, backend, filter),
		Boots:          len(model.Boots),
		DiskUsage:      model.Storage.DiskUsage,
		DiskBytes:      model.Storage.Bytes,
		Persistent:     model.Storage.Persistent,
		Units:          len(model.Units),
		ConfSettings:   len(model.Storage.Conf),
		Compat:         backendCompat,
		Storage:        model.Storage,
	}
	for _, unit := range model.Stats.TopUnits {
		report.TopUnits = append(report.TopUnits,
			countReport{Name: unit.Name, Count: unit.Count})
	}
	for _, boot := range model.Boots {
		row := bootReport{Index: boot.Index, ID: boot.ID}
		if !boot.First.IsZero() {
			row.First = boot.First.Format(time.RFC3339)
			row.Last = boot.Last.Format(time.RFC3339)
		}
		report.BootList = append(report.BootList, row)
	}

	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

// errorsLastHour counts the `err`-and-worse entries of the last hour.
//
// It is a second read rather than a slice of the first, because the window on
// screen is bounded by a line count and not by a time: on a machine logging
// heavily, five hundred entries can be four minutes, and "errors in the last
// hour" has to mean the hour.
func errorsLastHour(ctx context.Context, backend logs.Backend,
	filter logs.Filter) int {
	narrowed := filter
	narrowed.Priority = logs.PriErr
	narrowed.Since = "-1h"
	narrowed.Until = ""
	narrowed.Grep = ""
	narrowed.Lines = errorWindowLines
	entries, _, err := backend.ReadEntries(ctx, narrowed)
	if err != nil {
		return 0
	}
	return len(entries)
}
