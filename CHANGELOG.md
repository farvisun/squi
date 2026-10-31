# Changelog

All notable changes to squi. Versions follow [semantic versioning](https://semver.org).

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
