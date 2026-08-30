package journald

import (
	"sort"
	"strings"
)

// messageNames turns a MESSAGE_ID into a human name.
//
// A MESSAGE_ID is systemd's stable identifier for a well-known event, and it
// is the one field of an entry that says what happened independently of the
// wording. The full catalogue lives on the machine, in
// /usr/lib/systemd/catalog, and journalctl renders it with `-x`; what is here
// is the short name for the ones a reader meets — enough for the detail
// screen to say "a unit failed to start" next to a 32-character hex string.
//
// The names are taken from that catalogue's own Subject lines, shortened to
// fit a label. An id that is not here is shown as itself, which is the honest
// answer: nothing is invented for an id this table does not know.
var messageNames = map[string]string{
	"f77379a8490b408bbe5f6940505a777b": "the journal was started",
	"d93fb3c9c24d451a97cea615ce59c00b": "the journal was stopped",
	"ec387f577b844b8fa948f33cad9a75e6": "journal disk usage report",
	"a596d6fe7bfa4994828e72309e95d61e": "messages from a service were suppressed (rate limit)",
	"e9bf28e6e834481bb6f48f548ad13606": "journal messages were missed",
	"fc2e22bc6ee647b6b90729ab34a250b1": "a process dumped core",
	"5aadd8e954dc4b1a8c954d63fd9e1137": "a core file was truncated",
	"8d45620c1a4348dbb17410da57c60c66": "a login session was created",
	"3354939424b4456d9802ca8333ed424a": "a login session ended",
	"fcbefc5da23d428093f97c82a9290f7b": "a seat became available",
	"e7852bfe46784ed0accde04bc864c2d5": "a seat was removed",
	"c7a787079b354eaaa9e77b371893cd27": "the clock changed",
	"45f82f4aef7a4bbf942ce861d1f20990": "the time zone changed",
	"7c8a41f37b764941a0e1780b1be2f037": "the clock was first synchronised",
	"b07a249cd024414a82dd00cd181378ff": "start-up finished",
	"eed00a68ffd84e31882105fd973abdd1": "user manager start-up finished",
	"6bbd95ee977941e497c48be27c254128": "the system went to sleep",
	"8811e6df2a8e40f58a94cea26f8ebf14": "the system woke up",
	"98268866d1d54a499c4e98921d93bc40": "shutdown was initiated",
	"7d4958e842da4a758f6c1cdc7b36dcc5": "a unit started starting",
	"39f53479d3a045ac8e11786248231fbf": "a unit finished starting",
	"be02cf6855d2428ba40df7e9d022f03d": "a unit failed to start",
	"de5b426a63be47a7b6ac3eaac82e2f6f": "a unit started stopping",
	"9d1aaa27d60140bd96365438aad20286": "a unit finished stopping",
	"d34d037fff1847e6ae669a370e694725": "a unit started reloading",
	"7b05ebc668384222baa8881179cfda54": "a unit finished reloading",
	"7ad2d189f7e94e70a38c781354912448": "a unit succeeded",
	"d9b373ed55a64feb8242e02dbe79a49c": "a unit failed",
	"98e322203f7a4ed290d09fe03c09fe15": "a unit's process exited",
	"0e4284a0caca4bfc81c0bb6786972673": "a unit was skipped",
	"5eb03494b6584870a536b337290809b3": "a unit was scheduled to restart",
	"ae8f7b866b0347b9af31fe1c80b127c0": "resources consumed by a unit",
	"fe6faa94e7774663a0da52717891d8ef": "a process was killed by the OOM killer",
	"641257651c1b4ec9a8624d7a40a9e1e7": "a process could not be executed",
	"0027229ca0644181a76c4e92458afa2e": "messages could not be forwarded to syslog",
	"1dee0369c7fc4736b7099b38ecb46ee7": "a mount point was not empty",
	"24d8d4452573402496068381a6312df2": "a machine or container started",
	"58432bd3bace477cb514b56381b8a758": "a machine or container stopped",
	"267437d33fdd41099ad76221cc24a335": "the battery was critically low, powering off",
	"e6f456bd92004d9580160b2207555186": "the battery was critically low, waiting for a charger",
	"1675d7f172174098b1108bf8c7dc8f5d": "DNSSEC validation failed",
	"36db2dfa5a9045e1bd4af5f93e1cf057": "DNSSEC was turned off; the server does not support it",
	"50876a9db00f4c40bde1a2ad381c3a1b": "the system is configured in a way that may cause problems",
}

// MessageName returns the human name of a MESSAGE_ID, and whether the
// catalogue this tool carries knows it.
func MessageName(id string) (string, bool) {
	name, ok := messageNames[strings.ToLower(strings.TrimSpace(id))]
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

// DetailFields are the record fields the detail screen shows first, with the
// label it shows them under, in the order it shows them.
//
// It is a fixed list because the journal has no schema and a screen that
// dumped every field alphabetically would bury _PID under
// _AUDIT_LOGINUID. Everything not named here still appears, below, under the
// name the journal gave it.
var DetailFields = []struct{ Field, Label string }{
	{"_SYSTEMD_UNIT", "unit"},
	{"_SYSTEMD_USER_UNIT", "user unit"},
	{"SYSLOG_IDENTIFIER", "identifier"},
	{"PRIORITY", "priority"},
	{"_PID", "pid"},
	{"_UID", "uid"},
	{"_GID", "gid"},
	{"_COMM", "command"},
	{"_EXE", "executable"},
	{"_CMDLINE", "command line"},
	{"_TRANSPORT", "transport"},
	{"_HOSTNAME", "host"},
	{"_BOOT_ID", "boot"},
	{"_MACHINE_ID", "machine"},
	{"CODE_FILE", "source file"},
	{"CODE_LINE", "source line"},
	{"CODE_FUNC", "source function"},
	{"MESSAGE_ID", "message id"},
	{"ERRNO", "errno"},
	{"SYSLOG_FACILITY", "syslog facility"},
	{"_SELINUX_CONTEXT", "selinux context"},
	{"_AUDIT_SESSION", "audit session"},
	{"_SYSTEMD_CGROUP", "cgroup"},
	{"_SYSTEMD_SLICE", "slice"},
	{"_SYSTEMD_INVOCATION_ID", "invocation"},
}

// hiddenFields are the ones the detail screen does not repeat in its "every
// other field" section: the addresses the journal uses to find a record,
// which are noise next to the record itself, and MESSAGE, which is already
// the first thing on the screen.
var hiddenFields = map[string]bool{
	"MESSAGE":                     true,
	"__CURSOR":                    true,
	"__REALTIME_TIMESTAMP":        true,
	"__MONOTONIC_TIMESTAMP":       true,
	"__SEQNUM":                    true,
	"__SEQNUM_ID":                 true,
	"_SOURCE_REALTIME_TIMESTAMP":  true,
	"_SOURCE_MONOTONIC_TIMESTAMP": true,
	"_SOURCE_BOOTTIME_TIMESTAMP":  true,
}

// ExtraFields returns the record's remaining fields, sorted, so the detail
// screen can show everything it did not lift out by name.
func ExtraFields(fields map[string]string) []string {
	named := map[string]bool{}
	for _, known := range DetailFields {
		named[known.Field] = true
	}
	var extra []string
	for key := range fields {
		if named[key] || hiddenFields[key] {
			continue
		}
		extra = append(extra, key)
	}
	sort.Strings(extra)
	return extra
}
