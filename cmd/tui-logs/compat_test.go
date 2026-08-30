package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/manifest"
	"github.com/tui-tools/tui-kit/runner"
	tuilogs "github.com/tui-tools/tui-logs"
	"github.com/tui-tools/tui-logs/internal/journald"
)

// backend loads the manifest block the binary really reads.
func backend(t *testing.T) compat.Backend {
	t.Helper()
	m, err := manifest.Load(tuilogs.ManifestJSON)
	if err != nil {
		t.Fatalf("the embedded manifest does not parse: %v", err)
	}
	if m.Name != toolName {
		t.Fatalf("manifest name = %q, want %q", m.Name, toolName)
	}
	b, ok := m.Backend(backendName)
	if !ok {
		t.Fatalf("the manifest declares no %q backend", backendName)
	}
	return b
}

func TestManifestDeclaresTheBackend(t *testing.T) {
	b := backend(t)
	if b.Binary != "journalctl" {
		t.Errorf("binary = %q, want journalctl", b.Binary)
	}
	// 239 is where `systemd-analyze cat-config` arrived, which is the oldest
	// thing this tool reads that it cannot do without.
	if b.Minimum != "239" {
		t.Errorf("minimum = %q, want 239", b.Minimum)
	}
	if len(b.VersionCommand) == 0 {
		t.Errorf("a backend with no version command cannot be probed")
	}
}

// TestVersionRegexReadsRealOutput uses the `journalctl --version` banner as it
// really prints. The package version in the parentheses is full of digits that
// must not be mistaken for systemd's own release, and the feature line after
// it is full of numbers too.
func TestVersionRegexReadsRealOutput(t *testing.T) {
	b := backend(t)
	tests := map[string]string{
		// Captured from the Fedora 42 host this tool was written on.
		"systemd 257 (257.13-1.fc42)\n+PAM +AUDIT +SELINUX -APPARMOR": "257",
		// Arch, Debian 12 and Ubuntu 24.04.
		"systemd 257 (257.2-1-arch)":              "257",
		"systemd 252 (252.30-1~deb12u2)":          "252",
		"systemd 255 (255.4-1ubuntu8.4)":          "255",
		"systemd 239 (239-82.el8_10.4)":           "239",
		"systemd 249 (249.11-0ubuntu3.12)":        "249",
		"systemd 254 (254.16-1-lp155.2.1.x86_64)": "254",
	}
	for output, want := range tests {
		if got := compat.ParseVersion(output, b.VersionRegex); got != want {
			t.Errorf("ParseVersion(%q) = %q, want %q",
				strings.SplitN(output, "\n", 2)[0], got, want)
		}
	}
}

// TestVersionProbeReadsThisMachine runs the real probe when systemd is here.
// It asserts the shape, not the number: the point is that the command in the
// manifest answers on a real machine.
func TestVersionProbeReadsThisMachine(t *testing.T) {
	if !journald.Available() {
		t.Skip("no journalctl on this machine")
	}
	result := compat.Probe(context.Background(), backend(t))
	if result.Version == "" {
		t.Fatalf("the probe read no version: %s", result.Detail)
	}
	if !regexp.MustCompile(`^\d+$`).MatchString(result.Version) {
		t.Errorf("version = %q, want systemd's plain release number",
			result.Version)
	}
}

// TestFeaturesAreOnesThatCanActuallyBeFalse: a feature declared at or below
// the minimum is always true, so declaring it would be noise in the README's
// compatibility table and a gate in the code that never closes.
func TestFeaturesAreOnesThatCanActuallyBeFalse(t *testing.T) {
	b := backend(t)
	if len(b.Features) == 0 {
		t.Fatal("the boot list JSON gate is the one real feature and it is gone")
	}
	for _, feature := range b.Features {
		if feature.Since == "" {
			t.Errorf("feature %q declares no version", feature.Name)
			continue
		}
		if compat.Compare(feature.Since, b.Minimum) <= 0 {
			t.Errorf("feature %q arrived in %s, at or below the minimum %s, "+
				"so it can never be false and should not be declared",
				feature.Name, feature.Since, b.Minimum)
		}
	}
}

// TestTheCodeAsksForTheFeatureTheManifestDeclares keeps the two halves of the
// gate together: a name that drifts silently turns into a capability that is
// always present, because Caps.Has answers true for anything undeclared.
func TestTheCodeAsksForTheFeatureTheManifestDeclares(t *testing.T) {
	b := backend(t)
	declared := map[string]bool{}
	for _, feature := range b.Features {
		declared[feature.Name] = true
	}
	if !declared[journald.FeatureListBootsJSON] {
		t.Fatalf("the code asks for %q, which the manifest does not declare",
			journald.FeatureListBootsJSON)
	}

	// And the gate does close: below 252 the capability is absent.
	older := compat.NewCaps("251", b.Features)
	if older.Has(journald.FeatureListBootsJSON) {
		t.Error("systemd 251 cannot print the boot list as JSON")
	}
	newer := compat.NewCaps("252", b.Features)
	if !newer.Has(journald.FeatureListBootsJSON) {
		t.Error("systemd 252 can")
	}
}

func TestNotesApplyToTheRangesTheyClaim(t *testing.T) {
	b := backend(t)
	if len(b.Notes) == 0 {
		t.Fatal("a backend with a version gate has something to say about it")
	}
	for _, note := range b.Notes {
		if note.Range == "" || note.Impact == "" {
			t.Errorf("note %+v is incomplete", note)
		}
		// An unparsable range matches nothing, so a typo would hide the note
		// on every machine rather than showing it on the wrong ones.
		if !compat.Match("257", note.Range) && !compat.Match("240", note.Range) {
			t.Errorf("note range %q matches neither 240 nor 257, which is "+
				"either a typo or a note nobody will ever see", note.Range)
		}
	}
}

// TestTestedVersionsAreBackedByEvidence: the manifest's tested list is
// generated from compat/results.jsonl and must never be edited by hand.
//
// The evidence is decoded rather than substring-matched. The first version of
// this test looked for `"version":"255"` in the raw file, which the generator
// never writes — it separates key from value with a space — so the assertion
// could only ever pass while the tested list was empty, and it was: this tool
// had never been near a real machine. The first lab run filled the list in and
// the test failed on three versions that are in the file, spelled differently.
func TestTestedVersionsAreBackedByEvidence(t *testing.T) {
	b := backend(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "compat", "results.jsonl"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read compat/results.jsonl: %v", err)
	}

	recorded := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var result struct {
			Version string `json:"version"`
			Backend string `json:"backend"`
		}
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			t.Errorf("compat/results.jsonl has a line that is not JSON: %v", err)
			continue
		}
		if result.Backend == b.Name {
			recorded[result.Version] = true
		}
	}

	for _, version := range b.Tested {
		if !recorded[version] {
			t.Errorf("the manifest claims %s was tested, and nothing in "+
				"compat/results.jsonl says so", version)
		}
	}
}

// TestTheBackendRefusesGracefullyWithoutJournalctl: a machine with no journal
// gets a message naming what to install, not a panic.
func TestTheBackendRefusesGracefullyWithoutJournalctl(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if journald.Available() {
		t.Skip("journalctl is still resolvable on this machine")
	}
	_, err := journald.NewReal(nil, compat.Caps{})
	if !errors.Is(err, runner.ErrNotAvailable) {
		t.Fatalf("err = %v, want the kit's not-available error", err)
	}
	if !strings.Contains(err.Error(), "--demo") {
		t.Errorf("err = %q, want it to point at --demo", err)
	}
}
