# squi user guide

The [README](../README.md) covers installation. This guide is the full reference.

## Getting started

```sh
squi                      # opens the connection manager
squi --connect prod-api   # opens a saved connection directly
squi --no-mouse           # keep native terminal text selection
```

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
| Editor | `ctrl+enter` / `F5` run statement / selection · `F6` run all · `F7`/`F8` explain / analyze · `tab` complete · `ctrl+r` history · vim: `space r/a/e/h/s` |
| Explorer | `enter` open data · `t` / double-click open in new tab · `d` details · `D` DDL · `s` insert SELECT · `u` use database · `x` export · `i` import CSV · `/` filter · `r` refresh |
| Results | `enter` inspect cell · `v` record view · `y`/`Y`/`c` copy · `/` filter · `s` sort · `x` export · `F` load all · `e` edit · `o` add row · `dd` delete row · `u`/`U` revert · `ctrl+s` save · `[` `]` switch output tabs |
| Admin (`:admin`) | `1…4` processes / variables / users / sizes · `c` cancel query · `K` kill session · `a` auto-refresh |

Useful commands: `:use [db] [schema]`, `:commit`, `:rollback`, `:tx` (manual
transactions), `:export`, `:snippet <name>`, `:snippets`, `:set novim`,
`:set fetch=5000`.

## Configuration

| File | Purpose |
|---|---|
| `~/.config/squi/connections.toml` | Connection profiles (no secrets) |
| `~/.config/squi/settings.toml` | Preferences: `vim_mode`, `fetch_size`, `max_column_width`, `explorer_width`, `manual_transactions`, `confirm_all_writes_on_prod`, `history_limit`, `restore_session` |
| `~/.config/squi/snippets.toml` | Saved SQL snippets |
| `~/.local/state/squi/history/` | Query history (JSONL) |
| `~/.local/state/squi/session.json` | Open tabs and their SQL, restored at start |

`XDG_CONFIG_HOME` / `XDG_STATE_HOME` are honoured; `--config DIR` uses one
directory for everything.

**Passwords** are never written to the config. With *Save passwords* enabled they go
to the OS keychain (macOS Keychain, Secret Service on Linux, Windows Credential
Manager); otherwise squi asks when connecting and keeps them in memory for the
session.

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

## Safety model

- **Read-only connections** run every session with
  `SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY` (Postgres) /
  `SET SESSION TRANSACTION READ ONLY` (MySQL). squi also classifies each statement
  and blocks writes before they reach the server.
- **Destructive statements** (`DROP`, `TRUNCATE`, `ALTER … DROP`, `DELETE`/`UPDATE`
  without `WHERE` or with an always-true `WHERE`) require typing the database name.
- **`prod` connections** ask for confirmation before any write.
- **Manual transactions** (`ctrl+g m`): statements run inside a transaction until
  you commit (`ctrl+g c`) or roll back (`ctrl+g r`); the status bar shows the
  transaction age. Closing a tab or quitting with an open transaction asks first.
- **Inline edits** are shown as SQL before they run, executed in one transaction,
  and rolled back if any statement fails or does not affect exactly one row.

## Development

```sh
make dev-up            # Postgres 16, MySQL 8.4, MariaDB 11, two SSH bastions (docker)
make dev               # run squi against them (dev/config/connections.toml; passwords "squi")
make test              # unit tests
make test-integration  # all tests against the playground, with -race
make dev-reset         # recreate databases from dev/seed/*.sql
```

| Package | Responsibility |
|---|---|
| `internal/db` | Driver-neutral interfaces (`Conn`, `Session`, `ResultStream`, `Admin`) |
| `internal/db/postgres`, `internal/db/mysql` | Engine drivers: catalog queries, DDL, streaming, cancel, explain, admin |
| `internal/sshx` | SSH tunnels: auth chain, known_hosts, ssh_config, ProxyJump, keepalive/reconnect |
| `internal/sqltext` | Tokenizer, statement splitter, write/destructive classifier |
| `internal/schema` | Metadata cache and context-aware autocomplete |
| `internal/editsql` | DML generation for grid edits |
| `internal/dataio` | Export formats and CSV import |
| `internal/history` | Query history and snippets |
| `internal/config`, `internal/secrets` | Profiles, settings, OS keychain |
| `internal/ui` | Bubble Tea application; `internal/ui/components` holds the editor, grid, picker and form widgets |
