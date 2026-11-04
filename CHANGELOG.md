# Changelog

All notable changes to squi. Versions follow [semantic versioning](https://semver.org).

## 0.7.0 — 2026-11-04

### Added
- Update checker: squi looks for a new release at most once a day in the background and
  shows it in the status bar and connection manager. `:update` checks now. The check
  sends GitHub the usual request details, such as the machine's IP address; turn it off
  with `check_updates = false` or `SQUI_NO_UPDATE_CHECK=1`.
- Self-update for downloaded binaries: after you confirm, squi downloads the release,
  verifies it against the release's checksums, checks that it runs, and replaces the
  binary. Homebrew, Scoop, `go install` and system package installs are never replaced;
  squi shows the command to run or points to the release page instead.
- Full mouse support: click, double-click, drag, wheel and right-click on tabs, panes,
  editor, result grid, explorer, output tabs, dialogs, forms, the connection manager
  and the status bar.
- Selection by dragging in the editor, text views and the result grid (cell ranges and
  whole rows), copied with `y`, `ctrl+c` or the copy menu.
- Resize panes by dragging their borders; sizes and the hidden explorer are remembered
  in `layout.json`.
- Tab bar with close buttons, a new-tab button, drag to reorder and scrolling when tabs
  do not fit.
- Result grid: drag or double-click a column edge to resize or fit it, click a header to
  sort, and freeze leading columns (`z`).
- Context menus on right-click or `F10`.
- `ctrl+w m` and `:mouse` turn mouse capture on and off, leaving text selection to the
  terminal.
- `F2` moves focus, also out of the editor while typing.

### Changed
- `H` and `L` move within the grid in detail and admin panels, like everywhere else.
  Sub-tabs switch with `<`, `>` or `1…9` as before.
- `F5` is no longer listed for `results.rerun` and `explorer.refresh`: it always ran the
  query, so it never did either. Use `r`.
- The mouse wheel scrolls the grid and explorer view and keeps the cursor on screen. In
  the editor it scrolls without moving the cursor.
- `esc` in the explorer returns to the editor when there is no filter to clear.
- `explorer_width` is the default until a size has been saved; drag or `ctrl+w <`/`>`
  sets the size used from then on.
- The explorer can now be resized to half the terminal's width.
- Hints show your own key bindings, and detail, plan and text panels list their keys.
- Dialogs show clickable buttons. Prompt help text moved below them.

### Fixed
- A mouse report split across reads could be typed into the editor as text, most often
  over SSH.
- Clicking while a pane was zoomed could switch to another pane.
- `esc` on an edited connection form now asks before discarding it, and a failed save
  focuses the field at fault.
- The connection manager shows messages and connection progress. It also no longer hides
  its last list row or cuts off its key hints on narrow terminals.
- A failed DDL load can be retried with `r`.
- The explorer no longer says "loading…" when a filter matches nothing, and keeps the
  selection on screen while filtered.
- The editor's placeholder also shows while it has focus.
- Sort marks fit in narrow columns.
- `squi --version` reports the real version for `go install …@version` builds.

## 0.6.0 — 2026-10-31

### Added
- Query parameters: statements with `:name`, `$1` or `?` placeholders open a form
  for their values before running, and remember them for the session. Turn it off
  with `query_parameters = false`.
- `squi diff SOURCE TARGET` compares two schemas (tables, columns, primary keys,
  indexes, foreign keys, views) and writes the statements that align TARGET with
  SOURCE. Drops stay commented out unless `-drop` is given; `-exit-code` fails CI
  when schemas differ.
- Website with a user guide at https://farvisun.github.io/squi/.
- Security policy, issue templates and Dependabot for Go modules and GitHub Actions.

### Changed
- CI and release workflows use the current major versions of their actions.
- Dependencies updated, including Bubbles 1.0, pgx 5.11 and the MySQL driver 1.10.
- Release notes come from this changelog.

## 0.5.0 — 2026-10-11

### Added
- `squi import datagrip` imports DataGrip data sources with their SSH tunnels and
  keeps them in sync; saved passwords and query consoles are read from DataGrip.

## 0.4.0 — 2026-10-11

### Added
- SQL Server and Azure SQL (with `GO` batches and graphical-plan details) and SQLite.
- Server-side filtering and sorting of whole tables, foreign key navigation and
  referencing rows.

## 0.3.1 — 2026-10-11

### Fixed
- Unparseable `known_hosts` lines are skipped, as OpenSSH does.

## 0.3.0 — 2026-10-11

### Added
- ClickHouse over its HTTP interface.

## 0.2.1 — 2026-10-11

### Fixed
- Key runes the terminal delivers in one batch are treated as text.

### Added
- Demo GIF and preview image.

## 0.2.0 — 2026-10-11

- First release: PostgreSQL, MySQL and MariaDB, over SSH, with a safety model for
  production. Homebrew and Scoop packages.
