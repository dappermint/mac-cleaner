# mac-cleaner

macOS tells you "System Data: 235 GB" and then refuses to say another word about it. Storage settings shows a coloured bar, a few categories it will not let you open, and a recommendation to buy iCloud.

mac-cleaner is a terminal app that answers the three questions that bar refuses to:

- **where did the space go**, down to the directory, with every byte accounted for
- **what is actually safe to delete**, using each tool's own cleanup command rather than guessing
- **is the filesystem itself healthy**, from the drive's own error counters up

It never deletes anything you did not mark. Path-based cleanup moves things to Trash so it stays recoverable.

## the three views

`v` cycles, or `1` `2` `3`.

**surface** is a complete accounting of physical storage. It starts at the APFS containers, drops into each volume, then walks the data volume directory by directory. Every level sums to its parent, and what does not earn a row of its own is still named:

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

`unaccounted` is space the volume claims that no readable file explains: trees that refused you, plus APFS metadata a file walk cannot see. `elsewhere` is a mount point for another volume, counted under that volume instead of being folded in here. The header prints walked against claimed, so the share the tool actually explains is stated rather than implied.

**actions** is the cleanup list, sorted by macOS storage category. Four risk levels: `safe` has a supported command behind it, `review` needs a human look, `destructive` needs an exact confirmation phrase, `protected` is visible but never selectable. A large game library or VM image does not become "clutter" just because it is large.

**health** answers whether there is reason to think the filesystem is damaged. SMART verdict, NVMe media errors, controller error log, spare blocks, endurance, unsafe shutdowns, container accounting, write headroom, and IO errors seen during the walk. Most of it costs nothing because the walk gathers it anyway.

Only IO errors and directory loops are direct evidence. For a verdict, `sudo mac-cleaner surface --root --verify` runs a live `fsck_apfs` through diskutil. A live check cannot repair anything; that still means recovery mode.

## use

```sh
mac-cleaner                            # the tui
sudo mac-cleaner --root                # adds System Data, macOS, other users

mac-cleaner surface --depth 4          # the accounting, no tui
mac-cleaner scan --deep --json         # machine readable
mac-cleaner clean --all-safe --dry-run
```

The surface walk is the slow part, about a minute for two million files. `scan` leaves it off unless you pass `--surface`; the TUI always runs it.

## install

```sh
nix profile install github:dappermint/mac-cleaner
```

Or as a flake input:

```nix
inputs.mac-cleaner.url = "github:dappermint/mac-cleaner";
environment.systemPackages = [ inputs.mac-cleaner.packages.${system}.default ];
```

## develop

`direnv allow` picks up the dev shell. Then `just` lists everything.

```sh
just check        # fmt, vet, test
just surface      # run the accounting against your own disk
just nix-check    # every flake output evaluates and builds
```

## notes

Sizes are allocated blocks, not apparent file length, so sparse files do not show up as fake wins. The walk stops at device boundaries, so a nested volume never inflates the directory it appears under. Hard-linked files are counted once.

Trash is not free space until it is emptied, and the tool reports "to Trash" separately from immediately reclaimable bytes. Local Time Machine snapshots are left alone; macOS reclaims those itself under pressure. Nix garbage collection only removes paths unreachable from a GC root, so rollback generations survive.
