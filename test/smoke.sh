#!/bin/bash
# Backend smoke test for tui-logs, run inside a lab guest.
#
# The contract (see tui-tools/tui-lab): this script runs on the guest as the
# unprivileged lab user, escalates with `sudo -n` only, prints a short PASS/FAIL
# table and exits non-zero if anything failed. The binary under test is at
# $TUI_LAB_BIN (default: tui-logs on PATH).
#
# What it proves is that the tool reads the machine's *real* journal and agrees
# with journalctl asked directly — not that a fake renders. The lab already
# covers --version and a --demo frame; this covers the backend.
#
# Everything here is read-only. The tool is never asked to vacuum, rotate,
# verify or export: a suite that shrank the journal of the machine it runs on
# would destroy the evidence of its own run, and on a guest whose journal is
# the only record of the test that is the worst thing it could do.
#
# Two shapes of machine are asserted, because both are normal:
#
#   permitted    the account can open the system journal — it is root, or in
#                systemd-journal, adm or wheel — so the entries are the
#                machine's and the counts mean something.
#   denied       it cannot, and neither can `sudo -n`, so what comes back is
#                this user's own entries and the tool must say so rather than
#                report a quiet machine.
set -uo pipefail

bin="${TUI_LAB_BIN:-tui-logs}"
# TOOL is the manifest name, which is what a compatibility result is keyed on.
TOOL=tui-logs
pass=0
fail=0

# check runs one assertion. It takes a label, a command and a grep pattern the
# command's output must match. Output is captured so a failure can show it.
check() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

# check_absent is the inverse of a grep assertion: the command must succeed and
# its output must NOT contain the pattern. It is what proves a read stayed a
# read, which is a claim about something that did not happen.
check_absent() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && ! grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

# --- compatibility evidence -------------------------------------------------
#
# The manifest's `tested` list is generated, not claimed: it is rebuilt from
# compat/results.jsonl by tui-kit/tools/compat-sync.py, and this is where a
# line of that file comes from. The version recorded is the one the tool itself
# probed, read back out of --check, so it describes the machine that really ran
# the suite rather than what the tester assumed was installed.
#
# The line is printed behind a `compat-result:` prefix so it survives the trip
# out of the guest through the lab's per-VM log, and appended to
# $TUI_COMPAT_RESULTS as well for a run outside the lab.
record_compat() {
  local report="$1" outcome="$2" backend version distro today block
  block=$(sed -n '/"compat": {/,/^  }/p' <<<"$report")
  backend=$(sed -n 's/.*"backend": "\([^"]*\)".*/\1/p' <<<"$block" | head -1)
  version=$(sed -n 's/.*"version": "\([^"]*\)".*/\1/p' <<<"$block" | head -1)
  if [[ -z $backend || -z $version ]]; then
    echo "      no version was probed, so no compatibility result is recorded"
    return
  fi

  distro=$(. /etc/os-release && echo "${ID}-${VERSION_ID:-rolling}")
  today=$(date -u +%Y-%m-%d)
  local line
  line=$(printf '{"backend":"%s","date":"%s","distro":"%s","result":"%s","suite":"smoke","tool":"%s","version":"%s"}' \
    "$backend" "$today" "$distro" "$outcome" "$TOOL" "$version")

  printf 'compat-result: %s\n' "$line"
  if [[ -n ${TUI_COMPAT_RESULTS:-} ]]; then
    printf '%s\n' "$line" >>"$TUI_COMPAT_RESULTS"
  fi
}

# field reads one top-level number or string out of the --check JSON. The
# report is flat on purpose so a shell script needs nothing more than this.
field() {
  sed -n "s/.*\"$1\": \"\{0,1\}\([^\",]*\)\"\{0,1\},\{0,1\}$/\1/p" <<<"$2" | head -1
}

echo "--- tui-logs smoke on $(. /etc/os-release && echo "$PRETTY_NAME")"

if ! command -v journalctl >/dev/null; then
  echo "FAIL  no journalctl on this machine"
  exit 1
fi

echo "      $(journalctl --version | head -1)"

# Whether this account may open the system journal at all. It is a group
# question, not a root one: systemd-journal, adm and wheel all work, and on a
# lab guest the test user is in wheel.
if journalctl -n 1 --no-pager 2>&1 |
  grep -q "No journal files were opened due to insufficient permissions"; then
  permitted=no
else
  permitted=yes
fi
echo "      can open the system journal=$permitted"
if sudo -n true 2>/dev/null; then
  echo "      sudo -n=yes"
else
  echo "      sudo -n=no"
fi

report=$("$bin" --check 2>/dev/null)

# 1. The read path works at all and names the backend it drove.
check "check reads the journal" \
  "$bin --check" \
  '"backend": "journald"'

# 2. What it read, it read with a command it will show. The tool's whole claim
#    about its filters is that they are journalctl arguments, so the argv is in
#    the report and it has to look like one.
check "the report carries the command it read with" \
  "$bin --check" \
  '"command": "journalctl --no-pager -o json'

# 3. The boots agree with journalctl asked directly. This is the assertion that
#    covers the one version gate in the tool: on systemd 252 and newer the list
#    is read as JSON, below it from the table, and both must produce the same
#    number of boots.
want_boots=$(journalctl --list-boots --no-pager 2>/dev/null |
  grep -cE '^ *-?[0-9]+ +[0-9a-f]{32}')
if [[ ${want_boots:-0} -gt 0 ]]; then
  check "the $want_boots boots journalctl lists were parsed" \
    "$bin --check" \
    "\"boots\": $want_boots"
fi

# 4. The disk usage agrees with `journalctl --disk-usage`, which needs no
#    privileges at all.
want_usage=$(journalctl --disk-usage 2>/dev/null)
if [[ -n $want_usage ]]; then
  check "the disk usage matches journalctl" \
    "$bin --check" \
    "\"diskUsage\": \"$(sed 's/[][\\.*^$(){}?+|/]/\\&/g' <<<"$want_usage")\""
fi

# 5. Whether the journal survives a reboot is a directory, not a setting: a
#    machine with no /var/log/journal keeps it in /run and loses it every boot.
if [[ -d /var/log/journal ]]; then
  check "the journal is reported as persistent" "$bin --check" '"persistent": true'
else
  check "the journal is reported as volatile" "$bin --check" '"persistent": false'
fi

# 6. The unit list the picker is built from came from systemd, not from the
#    entries on screen.
want_units=$(systemctl list-units --all --plain --no-legend --no-pager 2>/dev/null | wc -l)
if [[ ${want_units:-0} -gt 0 ]]; then
  check "the unit list was read" "$bin --check" '"units": [1-9][0-9]*'
fi

# 7. The journald configuration in force was read. `systemd-analyze cat-config`
#    arrived in 239, which is this tool's minimum, so it is never optional.
check "the journald configuration was read" \
  "$bin --check" \
  '"confSettings": [1-9][0-9]*'

# 8. The counts are there whatever the machine has been up to. A guest that has
#    just booted may have no errors at all, so the assertion is on the field
#    rather than on a number.
check "the window was counted" "$bin --check" '"entries": [0-9]+'
check "the last hour was counted separately" "$bin --check" '"errorsLastHour": [0-9]+'

case "$permitted" in
  yes)
    # 9. With the journal open, the entries are the machine's. A guest always
    #    has some — it booted — so this one is a count, not a field.
    check "the system journal was opened" "$bin --check" '"denied": false'

    entries=$(field entries "$report")
    if [[ ${entries:-0} -gt 0 ]]; then
      check "the noisiest units were counted" "$bin --check" '"topUnits": \['
    fi

    # And the tool agrees with journalctl asked the same question directly.
    # `errorsLastHour` is its own narrow read — `-p err --since -1h` — so it
    # can be compared against exactly that command. A machine keeps logging
    # while the suite runs, so a couple of lines of drift is expected and a
    # count that is wildly different is not.
    want_errors=$(journalctl -p err --since -1h --no-pager -o json 2>/dev/null | wc -l)
    got_errors=$(field errorsLastHour "$report")
    drift=$((want_errors - ${got_errors:-0}))
    drift=${drift#-}
    if [[ $drift -le 5 ]]; then
      printf 'PASS  errors in the last hour match journalctl (%s vs %s)\n' \
        "$got_errors" "$want_errors"
      pass=$((pass + 1))
    else
      printf 'FAIL  errors in the last hour: tool said %s, journalctl %s\n' \
        "$got_errors" "$want_errors"
      fail=$((fail + 1))
    fi
    ;;

  no)
    # 9. Without it, what came back is this user's own entries. The tool must
    #    say so — a screen that showed nothing and claimed the machine was
    #    quiet would be the worst answer a log viewer can give.
    check "the tool says the system journal was not opened" \
      "$bin --check" \
      '"denied": true'

    check "and says what to do about it" \
      "$bin --check" \
      '"accessNote": ".*(systemd-journal|adm|wheel|root)'
    ;;
esac

# 10. --check must never change anything. The journal's size and the number of
#     files on disk must be what they were.
before_usage=$(journalctl --disk-usage 2>/dev/null)
before_files=$(find /var/log/journal /run/log/journal -name '*.journal*' 2>/dev/null | wc -l)
$bin --check >/dev/null 2>&1
after_usage=$(journalctl --disk-usage 2>/dev/null)
after_files=$(find /var/log/journal /run/log/journal -name '*.journal*' 2>/dev/null | wc -l)
if [[ "$before_usage" == "$after_usage" && "$before_files" == "$after_files" ]]; then
  printf 'PASS  --check left the journal untouched\n'
  pass=$((pass + 1))
else
  printf 'FAIL  --check changed the journal (%s→%s, %s→%s files)\n' \
    "$before_usage" "$after_usage" "$before_files" "$after_files"
  fail=$((fail + 1))
fi

# 11. And it prints no mutation: --check reports the read path, and a
#     housekeeping command line in its output would mean it had built one.
check_absent "--check builds no command" \
  "$bin --check" \
  'vacuum-size|vacuum-time|[-]-rotate|[-]-verify|install -m 600'

if [[ $fail -eq 0 ]]; then
  record_compat "$("$bin" --check 2>/dev/null)" pass
else
  record_compat "$("$bin" --check 2>/dev/null)" fail
fi

echo "--- tui-logs: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
