# contributing

## getting set up

The only prerequisite is [nix](https://nixos.org/download). Everything else comes from the flake.

```sh
git clone https://github.com/dappermint/ratatouille
cd ratatouille
direnv allow      # or: nix develop
just setup
```

That gives you Go, gopls, golangci-lint, goreleaser, nixfmt and just, pinned to the same versions CI uses. `just` on its own lists every recipe.

## before you open a pull request

```sh
just verify
```

That is gofmt, `go vet`, golangci-lint, the race-enabled test suite and `nix flake check` — the same four things CI runs. If it passes locally it passes in CI.

## layout

```
cmd/ratatouille      entry point, nothing but flag plumbing
internal/storage     the filesystem underneath: allocated blocks, mounts,
                     APFS containers, SMART, running commands as another uid
internal/scan        the domain: items, risk, the surface walker, health signals
internal/tui         the full-screen interface: renderer, three views, key handling
internal/text        display primitives with no dependencies
internal/cli         subcommands and the non-interactive printers
```

Dependencies point one way: `storage` knows nothing about the rest, `scan` builds on `storage`, `tui` and `cli` build on both. Keep it that way — if something in `storage` needs to know about an `Item`, it belongs in `scan`.

## what the tests protect

- **Accounting invariants.** Every level of the surface tree must sum to its parent. `assertChildrenSumToParent` enforces this recursively; if you add a node kind, make sure it either carries bytes or reports zero.
- **Terminal geometry.** `TestEveryViewSurvivesEveryTerminalSize` renders all three views at every size from 1×1 to 48×160 and fails if any row exceeds the terminal width. Rendering changes must keep it green.
- **Real filesystem behaviour.** Some tests need real mounts or a real disk and skip when those are missing. A skip is fine; a failure is not.

## style

- No comments explaining what code does. Comment only what is genuinely non-obvious, such as a workaround and why it exists.
- Descriptive names over abbreviations. `candidate`, not `c`.
- Commits follow [conventional commits](https://www.conventionalcommits.org/en/v1.0.0/): `feat:`, `fix:`, `refactor:`, `chore:`, and so on, lowercase description.

## adding a cleanup action

Cleanup delegates to the tool that owns the data — never to a hand-rolled `rm`. A new action means a new collector in `internal/scan` that:

1. finds the tool with `exec.LookPath` and returns an empty result if it is absent,
2. asks the tool for an estimate, ideally through its own dry-run,
3. returns an `Item` whose `Action` invokes the tool's supported cleanup command.

Set `Risk` honestly. `RiskSafe` means the owning tool guarantees the data is reproducible. Anything a human might miss is `RiskReview`, and anything unrecoverable is `RiskDestructive` and needs a confirmation phrase.

## releasing

Tag and push. The release workflow runs goreleaser, which builds a universal darwin binary and attaches it with checksums.

```sh
git tag -a v0.89.0 -m v0.89.0
git push --follow-tags
```

Check it first with `just release-check`, which runs the whole pipeline into `dist/` without publishing.
