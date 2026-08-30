package main

import (
	"context"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/manifest"
	tuilogs "github.com/tui-tools/tui-logs"
)

// probeCompat reads the version of the systemd this tool is about to drive.
//
// The facts it is judged against — the minimum version, the versions the lab
// has actually run against, the caveats that apply to a range, and which
// journalctl output formats exist on which release — come from the
// repository's own tool.json, embedded in the binary, so there is no second
// copy of them in the code.
//
// The version comes from `journalctl --version`, whose first line is
// "systemd 257 (257.13-1.fc42)". It is journalctl rather than systemctl
// because journalctl is the binary this tool cannot work without, and a
// machine can have one without the other in a container image.
//
// It never fails: a manifest that cannot be parsed and a missing binary both
// produce the zero Result, whose capability set answers yes to everything —
// which is the right default, because a backend that cannot do what was asked
// refuses in its own words, and that is a better message than a view hidden
// over an unreadable version string.
func probeCompat(ctx context.Context, demo bool) compat.Result {
	// --demo drives an in-memory journal; probing the real systemd on the
	// host would report a version that has nothing to do with what is on
	// screen.
	if demo {
		return compat.Result{}
	}
	m, err := manifest.Load(tuilogs.ManifestJSON)
	if err != nil {
		return compat.Result{}
	}
	backend, ok := m.Backend(backendName)
	if !ok {
		return compat.Result{}
	}
	return compat.Probe(ctx, backend)
}
