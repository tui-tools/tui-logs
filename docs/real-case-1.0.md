# Real case for 1.0: reading a server's journal, and capping a journal

Point 5 of the family's [stability bar](https://github.com/tui-tools/tui-kit/blob/main/docs/stability.md):
the tool has driven a real machine end to end for its main purpose, and what
that surfaced was fixed or filed.

## The machines, plainly

Reading the journal, the tool's main purpose, was done on a real machine in
daily use: the project's own server, Fedora 44 Workstation with systemd 259
and a persistent journal of about 4 GB, as an ordinary user in a terminal
reached over SSH. Only reads were done there; nothing on that machine was
changed, and no journal line from it is reproduced here.

The changes (retention and vacuum) were made on a lab virtual machine instead:
Fedora 44 Cloud Edition, booted by [tui-lab](https://github.com/tui-tools/tui-lab)
under KVM on the same server. The build was `main` on 2026-11-18 in both.

## What was done, end to end

On the server (read-only):

1. Opened: `journal: open   entries: 500   errors: 15   on disk: 4.0G`, read
   as the ordinary user without escalating (the account may read the journal).
2. `p`, `err`, then `t`, `today`: the title became `err and worse · since
   today` and the line under the list showed the command it ran,
   `journalctl --no-pager -o json -p 3 --since today -n 500`.
3. `2`: the stats said the window hit the read limit of 500 and that the
   counts describe the window, not the whole day.
4. `3`: disk usage, persistence (`/var/log/journal exists`), the two boots
   the journal holds and the effective `journald.conf`, each key with where
   it came from (on that machine, `(compiled-in default)` for every key on
   screen).
5. `1`, `enter` on an entry: every field journald stored for it.
6. `tui-logs --check` exited 0 with the same counts.

On the lab machine:

1. `S`: `SystemMaxUse` 200M, `MaxRetentionSec` 2week, `Storage` persistent.
   The dialog said what the restart would delete (nothing at 32M) and showed
   the whole `/etc/systemd/journald.conf.d/50-tui-logs.conf`, installed with
   `install -D -m 644` and applied with `systemctl restart systemd-journald`.
   `systemd-analyze cat-config systemd/journald.conf` then printed
   `SystemMaxUse=200M` and `MaxRetentionSec=2week`, and screen `3` named the
   drop-in and its line for both.
2. `v`, 200M: `journalctl --vacuum-size=200M`, previewed and confirmed.

## What it surfaced

- After the vacuum, the status line read "freed 0B of archived journals from
  /run/log/journal": `journalctl` prints one line per directory and the tool
  keeps the first, which is not the persistent journal's. Filed as
  [#34](https://github.com/tui-tools/tui-logs/issues/34).
- Nothing in the read path: the counts, the filters, the command line shown
  and `--check` agreed with `journalctl` on both machines.

## What it did not cover

Export (`x`), rotate (`o`) and verify (`y`) were not exercised on these
machines; the guide on tui.tools walks through them on an Ubuntu container.
