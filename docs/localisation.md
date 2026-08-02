# localisation

ratatouille ships `en_GB` only for now. The localisation boundary is in place so contributors
can add a complete language without rewriting presentation code or changing machine output.

## what gets translated

translate text a person reads: headings, prompts, explanations, errors, status labels, and TUI
help. Do not translate commands, flags, target IDs, configuration keys, JSON keys, schema names,
log event IDs, paths, or values supplied by macOS.

## adding a language

1. Copy `internal/i18n/locales/en_GB.json` to a file named with its locale, for example
   `nl_NL.json`.
2. Keep every message key and translate values only. Preserve placeholders such as `%s`, `%d`,
   and `%.1f` exactly.
3. Add the locale to the registry in `internal/i18n/i18n.go`. A partial locale must not be
   registered or selected.
4. Run `just i18n-check`, then `just verify`.
5. Open a pull request naming a native reviewer. Include terminal screenshots for changed TUI
   text and mention any wording that depends on macOS terminology.

Locale aliases use BCP 47 spelling at the boundary, so `en-GB` normalises to `en_GB`. The app
does not auto-select the operating-system language yet. `locale = en_GB` in the config is the
only shipped choice, and an unavailable locale falls back to `en_GB` with a diagnostic.
