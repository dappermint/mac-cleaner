# ratatouille

`rat` for short.

macOS tells you "System Data: 235 GB" and then refuses to say another word about it. Storage
settings shows a coloured bar, a few categories it will not let you open, and a recommendation
to buy iCloud.

ratatouille answers the questions that bar refuses to, and then acts on them:

- **where did the space go**, down to the directory, with every byte accounted for
- **what is safe to remove**, with the evidence for each decision written down
- **is the filesystem healthy**, from the drive's own error counters up
- **what is the machine doing right now**

It never removes anything you did not mark, and everything it does is logged.

## the views

`v` cycles, or `1` `2` `3`.

**surface** is a complete accounting of physical storage. It starts at the APFS containers,
drops into each volume, then walks the data volume directory by directory. Every level sums to
its parent, and what does not earn a row of its own is still named:

```
 ▾ container disk3                    460 GiB   95.2%
   ▾ Macintosh HD - Data              410 GiB   89.1%
     ▾ Users                          278 GiB   67.8%
       ▾ <you>                        278 GiB   99.9%
         ▸ Library                    248 GiB   89.3%
         ▸ Downloads                  8.7 GiB    3.1%
         · smaller directories        4.9 GiB    1.8%
     ▸ Applications                    49 GiB   11.8%
     · home                        elsewhere    0.0%
     · unaccounted                     23 GiB    5.6%
     · unreadable entries            unknown       —
```

`unaccounted` is space the volume claims that no readable file explains: trees that refused
you, plus APFS metadata a file walk cannot see. `elsewhere` is a mount point for another
volume, counted under that volume instead of being folded in here.

Press `d` to mark a directory for Trash and `x` to remove the marked set. Marks cannot nest:
marking a directory inside a marked one is refused, marking an ancestor of one swallows it, so
the running total is the bytes you actually get back.

**actions** is the cleanup list, sorted by macOS storage category. Four risk levels: `safe` has
a supported command or proven evidence behind it, `review` needs a human look, `destructive`
needs an exact confirmation phrase, `protected` is visible but never selectable. A large game
library or VM image does not become "clutter" just because it is large.

Every path-based entry carries what proves the path is what it claims, and above the `safe`
tier, the sibling paths it deliberately leaves alone. A target whose owning app is running,
whose bundle holds credentials, or which you have whitelisted is skipped with the reason
attached. The same checks run again immediately before removal.

**health** answers whether there is reason to think the filesystem is damaged. SMART verdict,
NVMe media errors, controller error log, spare blocks, endurance, unsafe shutdowns, container
accounting, write headroom, and IO errors seen during the walk. Most of it costs nothing
because the walk gathers it anyway.

Only IO errors and directory loops are direct evidence. For a verdict,
`sudo rat surface --root --verify` runs a live `fsck_apfs` through diskutil. A live check
cannot repair anything; that still means recovery mode.

## use

```sh
rat                                    # the tui
sudo rat --root                        # adds System Data, macOS, other users

rat surface --depth 4                  # the accounting, no tui
rat surface ~/Downloads                # account for one path instead of the volume
rat surface /Volumes                   # external drives, which the default walk skips
rat surface ~ --files --min-size 1GiB  # the largest files, from the walk it already did

rat scan --deep --json                 # machine readable
rat clean --all-safe --dry-run         # cleanup, previewed

rat uninstall --list                   # apps, and the exact name each answers to
rat uninstall --dry-run Spotify        # the bundle plus everything it left behind
rat purge --dry-run                    # project build artifacts
rat installer --dry-run                # installer downloads

rat status --explain                   # live metrics, and how the load score was reached
rat history --since 7d                 # what it did, and where the files went
```

`--json` works on every command, and is implied when output is piped.

## safety

`internal/safety` is the only package allowed to remove or move a file. The `forbidigo` linter
fails the build on a bare `os.Remove`, `os.RemoveAll` or `os.Rename` anywhere else, including
inside `safety` itself; a deliberate bypass has to say why on the same line.

The path validator refuses relative paths, `..` components, control bytes, denied roots, and
bare roots whose children may be removable but which are not. Symlinks are resolved at the leaf
and at every ancestor, and resolution can only take permission away. A corpus of adversarial
paths and a fuzz target hold the line.

The funnel then opens the target's parent as an `os.Root` and removes the leaf relative to that
handle, comparing the directory's identity before and after. After validation the path is never
resolved by name again, so a symlink swapped in afterwards cannot redirect the removal.

Removals go to Trash by default, through Finder where it can give a real Put Back, falling back
to a rename that says it lost it. Every operation is recorded in
`~/Library/Logs/ratatouille/operations.jsonl` with the path, size, outcome and recoverability.
`RATATOUILLE_NO_OPLOG=1` turns that off, `RATATOUILLE_DRY_RUN=1` forces dry-run everywhere.

There is no undo command. Restoring an arbitrary path set automatically overwrites newer files
with older ones; `rat history` tells you exactly where things went and lets you decide.

`~/.config/ratatouille/whitelist` takes one target id or path pattern per line. It can only
remove something from a selection, never add one, and it cannot make a protected path
cleanable.

## config

`~/.config/ratatouille/config` is optional. Sections are `[name]`, entries are `key = value`,
`#` starts a comment.

```ini
keymap = vim          # default or vim
view   = surface      # which view opens first
depth  = 4            # default tree depth
colour = auto         # auto, always, never

[keys]
mark          = m     # rebinding replaces the default key, it does not add to it
execute-marks = X
```

`rat config show` prints every setting with whether it came from the file or a default, and
`rat config keys` prints the resolved bindings, so what it lists is what the interface will
actually do. The help overlay is generated from the same table, which is why rebinding
something never leaves it telling you to press a key that does nothing.

The `vim` keymap adds `gg`, `G`, `ctrl-d` and `ctrl-u`, plus two modes. `v` starts a visual
range: move to extend it, then `d` marks every row in it at once. `:` opens a command line
understanding `:surface`, `:actions`, `:health`, `:clear`, `:marks` and `:q`. `escape` leaves
either mode, and the mode is shown in the status line so you always know which one you are in.
The default keymap has no modes at all, so there is nothing to be stuck in.

## install

```sh
brew install dappermint/tap/ratatouille
```

```sh
nix profile install github:dappermint/ratatouille
```

Or as a flake input:

```nix
inputs.ratatouille.url = "github:dappermint/ratatouille";
environment.systemPackages = [ inputs.ratatouille.packages.${system}.default ];
```

Both `ratatouille` and `rat` are installed and run the same binary. `rat completion fish`
writes a completion script; zsh and bash work too.

Prebuilt universal darwin binaries are attached to every
[release](https://github.com/dappermint/ratatouille/releases), with checksums.

## develop

`direnv allow` picks up the dev shell, or `nix develop`. Then `just` lists everything and
`just verify` runs exactly what CI runs.

```
cmd/ratatouille      entry point
internal/safety      path validation, protection tables, the deletion funnel, the log
internal/storage     allocated blocks, mounts, APFS containers, SMART
internal/catalog     the cleanup targets, their guards, and what they leave alone
internal/scan        items, risk, the surface walker, health signals
internal/uninstall   app inventory, leftover evidence, the sibling guard, teardown
internal/purge       project build artifacts
internal/installer   installer downloads
internal/metrics     live cpu, memory, disk, network and power
internal/plist       binary and xml property lists, parsed in process
internal/history     reading the operation log back
internal/config      whitelists and search paths
internal/tui         renderer, the views, key handling
internal/text        display primitives
internal/cli         subcommands and non-interactive printers
```

Zero dependencies: the whole thing is the Go standard library.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the setup, the invariants the tests protect, and how
to add a cleanup target.

## licence

GPL-3.0-only. See [LICENSE](LICENSE).

## notes

Sizes are allocated blocks, not apparent file length, so sparse files do not show up as fake
wins. The whole-volume walk stops at device boundaries, so a nested volume never inflates the
directory it appears under; a scoped walk of a path you named does cross, because that is what
you asked for. Hard-linked files are counted once.

Trash is not free space until it is emptied, and the tool reports "to Trash" separately from
immediately reclaimable bytes. Local Time Machine snapshots are left alone; macOS reclaims
those itself under pressure. Nix garbage collection only removes paths unreachable from a GC
root, so rollback generations survive.
