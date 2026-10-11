# squi user guide

The [README](../README.md) covers installation. This guide is the full reference.

## Getting started

```sh
squi                      # opens the connection manager
squi --connect prod-api   # opens a saved connection directly
squi --connect 'postgres://app@db.internal/api?sslmode=require&ssh=bastion'
squi --connect ~/data/app.db   # a SQLite file
squi --no-mouse           # keep native terminal text selection
```

A connection URL is used as given and not saved; see [Connection URLs](#connection-urls).
`:open <name|url>` opens another one in a new tab, and `ctrl+u` in the connection
manager prompts for one.

In the connection manager press `ctrl+n` to add a connection, `ctrl+t` to test it,
`ctrl+s` to save, then `enter` to connect (`ctrl+l` on the form lists the server's
databases to pick from). In the editor, type SQL and press **ctrl+enter** or **F5**
(or `alt+enter`) to run the statement under the cursor.

Open tabs and their queries are saved when you quit (and every few seconds) and are
reopened on the next start; set `restore_session = false` in `settings.toml` to turn
this off.

`ctrl+enter` needs a terminal that reports modified keys — kitty, Ghostty, WezTerm,
iTerm2, foot, Alacritty and xterm do; macOS Terminal.app does not (use `F5`,
`alt+enter` or `ctrl+j` there). In tmux add `set -g extended-keys on`. Set
`SQUI_LEGACY_KEYS=1` to switch the extended key handling off.

Press **F1** (or `?`) at any time for the full key reference, and **ctrl+p** for the
command palette.

## Layout

```
┌ 1 prod-api ● │ 2 staging ──────────────────────────────── ctrl+t new ┐
│ Explorer        │ Query · NORMAL                                     │
│ ▾ app           │ SELECT * FROM orders WHERE status = 'paid'         │
│   ▾ public      ├────────────────────────────────────────────────────┤
│     ▾ Tables    │ Messages │ Result │ ⓘ orders │ Plan                │
│       orders    │  id │ user_id │ status │ total                     │
├─────────────────┴────────────────────────────────────────────────────┤
│ PRODUCTION  postgres prod-api · app · ssh connected  TX 12s  1000 rows│
└──────────────────────────────────────────────────────────────────────┘
```

Move focus with `tab` / `shift+tab` or `ctrl+w h/j/k/l`; `ctrl+w z` zooms a pane.

## Essential keys

| Where | Keys |
|---|---|
| Anywhere | `F1` help · `ctrl+p` palette · `:` command line · `ctrl+o` connections · `ctrl+t` new tab · `alt+1…9` switch tab · `ctrl+g` transaction menu · `ctrl+c` cancel query · `ctrl+q` quit |
| Editor | `ctrl+enter` / `F5` run statement / selection · `F6` run all · `F7`/`F8` explain / analyze · `F4` open in `$EDITOR` · `tab` complete · `ctrl+r` history · vim: `space r/a/e/h/s/f/E` |
| Explorer | `enter` open data · `t` / double-click open in new tab · `d` details · `D` DDL · `s` insert SELECT · `u` use database · `x` export · `i` import CSV · `/` filter · `*` pin to favourites · `r` refresh |
| Results | `enter` inspect cell · `v` record view · `y`/`Y`/`c` copy · `W` copy as `WHERE` · `/` filter (server-side on tables) · `f` filter loaded rows · `s` sort · `#` count rows · `>` follow foreign key · `<` referencing rows · `backspace` back · `*` bookmark view · `x` export · `F` load all · `e` edit · `o` add row · `dd` delete row · `u`/`U` revert · `ctrl+s` save · `[` `]` switch output tabs |
| Admin (`:admin`) | `1…4` processes / variables / users / sizes · `c` cancel query · `K` kill session · `a` auto-refresh |

Useful commands: `:use [db] [schema]`, `:commit`, `:rollback`, `:tx` (manual
transactions), `:timeout 2m` (this tab's statement timeout; `0` for none),
`:format`, `:edit`, `:open <name|url>`, `:consoles` (DataGrip queries), `:export`, `:snippet <name>`, `:snippets`,
`:keys`, `:set novim`, `:set fetch=5000`.

Every binding above can be changed; see [Key bindings](#key-bindings).

## Browsing tables

Opening a table from the explorer starts a **table view**. Unlike an ad-hoc query
result, its filter and sort run on the server, so they cover the whole table and
not just the rows loaded so far:

- `/` takes a `WHERE` condition (`status = 'paid' and total > 100`) and re-runs the
  query; the status bar shows that the view is filtered. The condition is checked
  by the same statement classifier as the editor, so it cannot smuggle in a second
  statement. `f` filters the loaded rows only, as in query results.
- `s` sorts by the column under the cursor with `ORDER BY`; pressing it again cycles
  through descending and unsorted.
- `>` on a foreign-key cell opens the referenced row; `<` lists the tables that
  reference this row and opens the matching rows. `backspace` goes back.
- `#` shows the row count: first the engine's statistics estimate (instant), then an
  exact `COUNT(*)` if you ask for it — on a large production table that can take a
  while, so it is never run on its own.
- `W` copies a `WHERE` predicate — for the cell (`"user_id" = 42`), the row's key, or
  the column's distinct loaded values (`"id" IN (1, 2, 3)`); pick one from the menu.
  It works in query results too.
- `*` bookmarks the view (table, filter and sort) in **★ Favorites** at the top of
  the explorer; `*` in the explorer pins a table there. Favourites are kept per
  connection in `favorites.json` in the state directory.

## Statement timeout

Each connection can stop statements that run too long: set *Statement timeout* in
the connection form (`30s`, `2m`, `0` for none). `prod` connections default to
**30s** when it is left empty; other environments have no limit. `:timeout <value>`
changes it for the current tab, `:timeout default` restores the profile's, and the
status bar shows `⏱` while one is active.

The timeout is enforced by squi: when it expires the statement is cancelled the same
way as `ctrl+c`, so it works the same on every engine and also covers statements a
server-side limit would not (MySQL's `max_execution_time` only applies to `SELECT`).

## Formatting and external editor

`:format` (`space f` in vim mode) reformats the statement under the cursor, or the
selection: keywords upper-cased, one clause per line, subqueries and CTEs indented.
It only changes whitespace and keyword case — strings, comments and identifiers are
kept byte for byte.

`F4` (`:edit`, `space E`) opens the editor's text in `$VISUAL` or `$EDITOR` (falling
back to `vi`, or `notepad` on Windows); when the editor exits, the text replaces the
buffer.

## Configuration

| File | Purpose |
|---|---|
| `~/.config/squi/connections.toml` | Connection profiles (no secrets) |
| `~/.config/squi/settings.toml` | Preferences: `vim_mode`, `fetch_size`, `max_column_width`, `explorer_width`, `manual_transactions`, `confirm_all_writes_on_prod`, `history_limit`, `restore_session` |
| `~/.config/squi/snippets.toml` | Saved SQL snippets |
| `~/.local/state/squi/history/` | Query history (JSONL) |
| `~/.local/state/squi/session.json` | Open tabs and their SQL, restored at start |
| `~/.local/state/squi/favorites.json` | Pinned tables and bookmarked table views |

`XDG_CONFIG_HOME` / `XDG_STATE_HOME` are honoured; `--config DIR` uses one
directory for everything.

**Passwords** are never written to the config. With *Save passwords* enabled they go
to the OS keychain (macOS Keychain, Secret Service on Linux, Windows Credential
Manager); otherwise squi asks when connecting and keeps them in memory for the
session. On headless servers without a keychain, set *Password env var*
(`password_env`) to read the password from an environment variable instead.

Profiles may reference environment variables as `${VAR}` or `${VAR:-default}` in
`host`, `user`, `database`, the TLS files, the SSH settings and `params`; they are
expanded when connecting, and the profile keeps the reference.

Other per-connection settings:

| Field | Meaning |
|---|---|
| `statement_timeout` | See [Statement timeout](#statement-timeout) |
| `label`, `color` | A short tag (`EU`) and a colour (`red`…`gray` or `#rrggbb`) for tabs and the status bar, to tell connections of the same environment apart |
| `params` | Extra driver parameters: Postgres connection keywords and runtime parameters, MySQL DSN parameters or session variables, ClickHouse settings, SQL Server connection-string keys, SQLite URI parameters |

```toml
[[connection]]
name = "orders-eu"
driver = "postgres"
host = "${ORDERS_DB_HOST:-db.internal}"
user = "readonly"
database = "orders"
env = "prod"
label = "EU"
color = "orange"
statement_timeout = "1m"
password_env = "ORDERS_DB_PASSWORD"
[connection.params]
application_name = "squi-${USER}"
```

### Connection URLs

`--connect`, `:open` and `ctrl+u` accept a URL wherever a connection name is
expected; `ctrl+u` in the connection form fills the form from one, to save it.

```
postgres://app@db.internal:5432/api?sslmode=require   (also postgresql://)
mysql://root@127.0.0.1/shop                           (also mariadb://)
clickhouse://default@ch:8123/logs                     (?secure=true for HTTPS)
sqlserver://sa@mssql:1433/shop                        (also mssql://; ?database= works too)
sqlite:app.db · sqlite:///var/lib/app/data.db · ./app.db
```

Query parameters set the profile: `sslmode`, `sslrootcert`, `sslcert`, `sslkey`,
`env`, `read_only`, `statement_timeout` (or `timeout`), `fetch_size`, `name`,
`label`, `color`, `host` (e.g. a Unix socket directory), and an SSH tunnel with
`ssh=[user@]host[:port]` (or an `~/.ssh/config` alias), `ssh_jump=a,b` and
`ssh_key=PATH`. Anything else becomes a driver parameter. A password in the URL is
used for that connection only and never written to disk.

### Importing from DataGrip

```sh
squi import datagrip --dry-run   # show what would be imported
squi import datagrip             # import, or update what was imported before
squi import datagrip --prune     # also remove those deleted in DataGrip
squi import datagrip --project ~/DataGripProjects/work   # a specific project
```

squi reads the newest DataGrip installation: its global data sources and those of
its recent projects (`.idea/dataSources.xml` and `dataSources.local.xml`), with the
SSH tunnels they use (`options/sshConfigs.xml`). `--project` also accepts the
options directory of another JetBrains IDE, e.g. PhpStorm's global data sources.
`ctrl+g` in the connection manager does the same after showing a preview.

- **Connections**: PostgreSQL, MySQL/MariaDB, ClickHouse (HTTP), SQL Server and
  SQLite data sources are converted from their JDBC URL; others are skipped with a
  reason. Names keep the DataGrip folder (`shop/orders`). JDBC parameters squi
  understands (SSL settings, `ApplicationName`, `currentSchema`, `databaseName`, …)
  are carried over; the rest are listed with `-v`.
- **SSH**: tunnels become the connection's SSH settings. *OpenSSH config and
  authentication agent* maps to `auto` (agent, `~/.ssh/config`, key files), *Key
  pair* to `key` and *Password* to `password`.
- **Environment**: new connections get one from their name — `prod`/`production`,
  `stage`/`staging`, `dev`/`test`, `local` — and SQLite files and plain `localhost`
  are `local`. Check it before relying on the prod guards.
- **Re-importing** matches connections by DataGrip's id. DataGrip's address, user,
  database, TLS, SSH and parameters win; squi's environment, label, colour,
  timeout, fetch size and password settings are kept, and read-only stays on once
  set on either side. Connections you created in squi are never touched.
- **Passwords** are not copied. When an imported connection has no password in
  squi, it reads the one DataGrip saved in the macOS Keychain
  (`IntelliJ Platform DB — <id>`) on connect; macOS asks to allow it — *Allow* asks
  again next time, *Always Allow* lets the `security` tool read that item without
  asking. The password is kept in memory for the session. On Linux and Windows,
  squi asks for the password instead.
- **Queries**: `:consoles` (`:dg`) lists the DataGrip query consoles of the current
  connection and the project files DataGrip attaches to it, newest first, read from
  DataGrip's files as they are now; picking one opens it in a new tab without
  running it.

### Key bindings

Override any binding in `settings.toml` by its action name; `:keys` lists every
action with its current keys.

```toml
[keys]
run = ["ctrl+r", "f5"]
"results.follow_fk" = ["gf", ">"]
analyze = []            # unbind
```

Global actions have plain names; actions of the result grid, the explorer and the
output tabs start with `results.`, `explorer.` and `output.`. Navigation keys
(`hjkl`, `gg`, arrows) and the editor's own keys are fixed. Help (`F1`), the command
palette and `:keys` show the bindings in effect, and conflicting bindings are
reported at start-up.

### SSH

A connection can tunnel through SSH. squi dials the database *through* the SSH
connection (like `ssh -W`), so no local port is opened.

- **Host alias**: any `Host` from `~/.ssh/config` — `HostName`, `User`, `Port`,
  `IdentityFile`, `ProxyJump` and `UserKnownHostsFile` are honoured. Explicit fields
  in the connection override the config.
- **Auth** (`auto` by default): ssh-agent → key files (passphrase prompted when
  needed) → password → keyboard-interactive.
- **Jump hosts**: `user@jump1:22, jump2` — dialed in order, each authenticated
  separately.
- **Host keys** are checked against `~/.ssh/known_hosts`. Unknown hosts show the
  fingerprint and ask before trusting; changed keys are refused.

```toml
[[connection]]
name = "prod-api"
driver = "postgres"
host = "db.internal"      # resolved on the bastion side
port = 5432
user = "app"
database = "api"
env = "prod"              # red status bar, confirm every write
read_only = true          # block writes entirely
[connection.ssh]
enabled = true
alias = "prod-bastion"    # from ~/.ssh/config
```

### ClickHouse

squi talks to ClickHouse over its **HTTP interface**: port `8123`, or `8443` with
TLS (set SSL mode to `require`, or `verify-full` to check the certificate). The
native protocol port `9000` is not used. Over SSH, point the connection at the HTTP
port as seen from the bastion.

Each tab is its own HTTP session, so `USE` and `SET` persist between statements.
ClickHouse has no transactions, so manual transactions, inline row editing and
`EXPLAIN ANALYZE` are not available; `F7` shows `EXPLAIN indexes = 1` as a tree.
CSV import sends one `INSERT … FORMAT TabSeparated` per batch, and rows from earlier
batches stay if a later one fails.

### SQL Server

squi connects to SQL Server and Azure SQL (port `1433`) with the pure-Go
`go-mssqldb` driver, through SSH like any other engine. SSL mode maps to the
driver's encryption settings: `disable` turns encryption off, empty / `prefer` /
`require` encrypt without checking the certificate (as SSMS's *Trust server
certificate*), and `verify-ca` / `verify-full` check it against *CA cert*.

- **Batches**: a line holding only `GO` (optionally `GO 5`) separates batches, as in
  SSMS. A batch starting with `DECLARE`, or creating a procedure, function or
  trigger, runs as one unit, so variables and routine bodies work; `BEGIN … END`
  blocks are kept whole.
- **Schemas**: the explorer shows databases, then schemas; unqualified names complete
  against the user's default schema (`dbo`).
- **Plans**: `F7` shows the estimated plan (`SHOWPLAN_XML`) as a tree, `F8` the actual
  one (`STATISTICS XML`), with costs, row estimates, warnings and missing-index
  hints in the details.
- **Admin**: sessions come from `sys.dm_exec_requests` / `sys.dm_exec_sessions` and
  can be killed (`KILL`); cancelling another session's query is not possible on SQL
  Server, so `c` reports that.
- SQL Server has no read-only session mode, so *Read-only* is enforced by squi's
  statement classifier only (use a login with read-only permissions as well on
  production, or `ApplicationIntent=ReadOnly` in params for a readable secondary).

### SQLite

A SQLite connection opens a local file (or `:memory:`); host, port, user and SSH do
not apply. The file must already exist, so a typo does not create an empty
database; add `?mode=rwc` to a `sqlite:` URL to create one. Foreign keys are
enforced and a 5-second busy timeout waits for other writers.

- `ATTACH 'other.db' AS aux` adds a database to the tab's session; the explorer
  lists the databases attached to its own connection (`main` and any attached
  there).
- *Read-only* opens the file read-only and sets `query_only`.
- `F7` shows `EXPLAIN QUERY PLAN` as a tree; SQLite has no `EXPLAIN ANALYZE`, so `F8`
  runs the statement and adds its time and row count to the plan.
- The admin views show pragmas and compile options (`:variables`) and per-table sizes
  from `dbstat` (`:sizes`); there are no sessions or users to list.
- Inline edits match rows by primary key. In a column declared without a type, a
  value like `1` is stored as an integer and does not match the text `'1'` squi
  generates; the edit then reports that no row was affected and nothing is saved.

## Safety model

- **Read-only connections** run every session with
  `SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY` (Postgres) /
  `SET SESSION TRANSACTION READ ONLY` (MySQL) / the `readonly = 2` setting
  (ClickHouse) / a read-only file with `query_only` (SQLite). squi also classifies
  each statement and blocks writes before they reach the server; on SQL Server this
  classification is the only guard.
- **Destructive statements** (`DROP`, `TRUNCATE`, `ALTER … DROP`, `DELETE`/`UPDATE`
  without `WHERE` or with an always-true `WHERE`, ClickHouse
  `ALTER TABLE … DELETE/UPDATE WHERE 1`) require typing the database name.
- **`prod` connections** ask for confirmation before any write, and cancel
  statements after 30 seconds unless a different timeout is set.
- **Manual transactions** (`ctrl+g m`): statements run inside a transaction until
  you commit (`ctrl+g c`) or roll back (`ctrl+g r`); the status bar shows the
  transaction age. Closing a tab or quitting with an open transaction asks first.
- **Inline edits** are shown as SQL before they run, executed in one transaction,
  and rolled back if any statement fails or does not affect exactly one row.

## Development

```sh
make dev-up            # Postgres 16, MySQL 8.4, MariaDB 11, ClickHouse 25.8, SQL Server 2022, two SSH bastions (docker)
make dev-sqlite        # build dev/squi.db from dev/seed/sqlite.sql (no server needed)
make dev               # run squi against them (dev/config/connections.toml; passwords "squi")
make test              # unit tests (SQLite tests always run)
make test-integration  # all tests against the playground, with -race
make dev-reset         # recreate databases from dev/seed/*.sql
```

| Package | Responsibility |
|---|---|
| `internal/db` | Driver-neutral interfaces (`Conn`, `Session`, `ResultStream`, `Admin`) |
| `internal/db/postgres`, `internal/db/mysql`, `internal/db/clickhouse`, `internal/db/mssql`, `internal/db/sqlite` | Engine drivers: catalog queries, DDL, streaming, cancel, explain, admin, row estimates |
| `internal/sshx` | SSH tunnels: auth chain, known_hosts, ssh_config, ProxyJump, keepalive/reconnect |
| `internal/sqltext` | Tokenizer, statement splitter, write/destructive classifier |
| `internal/sqlfmt` | SQL formatter on top of the `sqltext` tokens |
| `internal/schema` | Metadata cache and context-aware autocomplete |
| `internal/editsql` | DML generation for grid edits |
| `internal/dataio` | Export formats and CSV import |
| `internal/history` | Query history and snippets |
| `internal/config`, `internal/secrets` | Profiles, settings, OS keychain |
| `internal/datagrip` | DataGrip import: data sources, SSH configs, consoles, keychain passwords |
| `internal/ui` | Bubble Tea application; `internal/ui/components` holds the editor, grid, picker and form widgets; `internal/ui/keymap` the remappable key bindings |
