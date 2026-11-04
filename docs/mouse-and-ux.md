# squi: full mouse support, UX fixes and layout features

## Context

squi is a Bubble Tea TUI (`bubbletea` v1.3.10). Mouse capture is on by default, but only the tab bar, pane focus, explorer rows and some wheel scrolling respond. The user asked for desktop-like mouse work plus UX and functionality improvements, and chose:

- **Mouse:** click, wheel and double-click on every surface, drag to resize, right-click context menus.
- **Drag:** in-app selection (editor, text views, grid cell ranges), copied through the existing clipboard path.
- **UX fixes:** feedback and states, discoverability, keyboard consistency, safety and data loss.
- **Features:** persistent layout, tab bar upgrade, grid column resize/autofit/freeze/header sort, clickable status bar.

Everything stays reachable by keyboard. `--no-mouse` keeps working.

## Decisions worth vetoing

1. **`READ-ONLY` click does not turn writes on.** It opens a small menu with an explanation and "Edit connection". `cfg.ReadOnly` also reaches the driver (`internal/ui/conn.go:172`), so it can't be flipped safely at runtime, and one stray click must not enable writes.
2. **Double-clicking a grid cell inspects, not edits.** Edit is in the context menu and on `e`. Avoids accidental edits on production.
3. **Single click runs an item in menus and pickers**, as in a desktop menu. There is no hover highlight, because cell motion reports only while a button is held. Connection manager rows need a double-click to connect.
4. **Outside click closes only menus, pickers and text views.** Confirm, typed-confirm, prompt and form dialogs stay open so nothing typed or guarded is lost.
5. **Typed confirmation stays typed.** The mouse can cancel it or submit a matching value, never bypass it.
6. **Keybinding changes** (listed in step 8): `F5` removed from `results.rerun` and `explorer.refresh`, where global `run` already shadows it; `H`/`L` go to the grid in detail and admin panels, with `<`/`>` and `1…9` switching sub-tabs. (`[`/`]` were the first idea, but they already switch output tabs.)

## Design

### Hit-testing: record geometry while rendering

Add `internal/ui/mouse.go`. `View` records a `frame` on the model each render:

- outer rects for explorer, editor and output (absent when hidden or zoomed out)
- spans for tabs and their close buttons, output sub-tabs, inner sub-tabs and status segments
- the top modal's rect

Update and View run on one goroutine, and widgets already take sizes during `View`, so this fits the code. It replaces derived coordinates in `handleMouse`, which is how the zoom bug arose: `layout()` ignores `m.zoom` (`app.go:963`). Widgets get local coordinates and expose their own hit-test.

### Input state

- **Click count:** a tracker on `Model` turns presses into single/double/triple by position, button and time. Reuse `doubleClick` (`explorer.go:528`).
- **Drag capture:** `m.drag` is set on press and receives motion and release wherever the pointer goes. Kinds: explorer split, editor split, editor selection, grid range, column resize, tab reorder, text selection. A new press or any key ends a drag whose release was lost.
- **Autoscroll:** while a selection drag sits past a pane edge, a 60 ms tick keeps scrolling.
- **Optional interfaces:** add `mousePanel` next to `scroller` (`output.go:23`), so panels without mouse logic don't change. Modals keep `update(m, tea.Msg)` and receive box-local coordinates. A small `dismissible` interface marks modals closed by an outside click.

### Prerequisite bug: split mouse reports

`incompleteCSI` (`internal/termkeys/termkeys.go:88`) rejects `<`, so an SGR mouse report split across two reads isn't buffered. The tail can reach the editor as typed text such as `10;5M`. Drag floods make this likely over SSH, so it gets fixed first: accept `<` after `ESC[`, and buffer a trailing `ESC[`. squi binds nothing to `alt+[`.

## Steps

Each step is one Conventional Commit through `/commit`.

### 1. Foundation
- Fix `incompleteCSI`, with a byte-by-byte split test in `termkeys_test.go`.
- Add `mouse.go`: `frame`, click tracker, drag capture.
- Rewrite `handleMouse` (`app.go:881`):
  - use `frame`, honour zoom, treat the status row separately
  - return modal `cmd`s, which are dropped today unless `done` (`app.go:884-888`)
  - route to the connection manager, which is skipped today (`app.go:890`)
  - cancel cmdline and `winPrefix` on click
- Add a "Toggle mouse capture" palette command and `ctrl+w m`, using `tea.DisableMouse` and `tea.EnableMouseCellMotion`, so native selection is available without restarting.

### 2. Splits and persistent layout
- Drag the explorer/editor border and the editor/output border. Double-click resets to defaults.
- One clamp shared by keys and drag, which also fixes `>` growing past the `width/3` cap with no effect (`app.go:790`, `app.go:967`). Raise the cap to `width/2`, keeping room for the right pane.
- New `internal/config/layout.go` writes `layout.json` in `StateDir` using `writeFile`: explorer width, editor percent, explorer hidden. It is separate from `session.json` because `saveSession` is skipped when `restore_session` is off (`session.go:45`). Save on drag release, on `ctrl+w` resize and on quit. `explorer_width` remains the default when no file exists.

### 3. Tab bar
- Rework `mainView` tab rendering (`app.go:1024-1029`): `m.tabScroll` keeps the active tab visible, `‹`/`›` when overflowing, `×` per tab, `+` in place of the `ctrl+t new` text.
- Middle-click closes, wheel scrolls, drag reorders.
- Close goes through the existing guarded path around `app.go:825-833`, never straight to `closeTab`.
- Right-click menu: close, close others, new tab.

### 4. Editor
New `components/editor_mouse.go`:
- `PosAt` is the inverse of `displayCol` (wide runes), using `gutterWidth`, `top` and `left`.
- Click moves the cursor without changing vim mode. Double-click selects a word (reuse the word bounds near `editor.go:1296`), triple-click a line.
- Drag selects: `selecting`/`anchor` in standard mode, `ModeVisual` in vim mode, respecting its inclusive end in `selRange`. Copy uses `ctrl+c`/`y`, which already raise `ActYank`.
- Wheel scrolls the view without moving the cursor. `SetSize` calls `scroll()` every render (`editor.go:123`) and would snap it back, so add `viewDetached`, set by wheel and cleared by any key.
- Extract completion popup geometry from `overlayCompletion` (`editor.go:1454`): click accepts, wheel scrolls, click elsewhere closes.
- Right-click menu: cut, copy, paste, select all, run, format.

### 5. Grid
New `components/grid_mouse.go`:
- Extract `visibleColumns()` from `View` (`grid.go:334`), then add `HitTest(x, y)` returning header, column edge, gutter or cell.
- **Wheel:** moves `top` and keeps the cursor inside the viewport. `AtBottom` (`grid.go:234`) also becomes true when the viewport nears the end; without that, lazy loading through `maybeFetch` (`results.go:285`) stalls. Horizontal wheel and alt+wheel scroll columns (terminals often take shift+wheel).
- **Click:** cell selects, gutter selects the row, header selects and sorts. Sort reuses `browseSort` for server-side tables and `ToggleSort` otherwise.
- **Double-click:** cell runs `inspectCell`.
- **Resize:** drag the `│` in the header row. Overrides survive `AppendRows` and reset in `SetData`. Double-click autofits from loaded rows, capped at pane width and ignoring `MaxColWidth`.
- **Freeze:** `frozen` count, heavy separator after frozen columns, `left` never below `frozen`, and `colsWidth`/`clamp` account for frozen width. New `results.freeze` key action and a menu item.
- **Range:** drag selects a rectangle in view coordinates (works with filter and sort), highlighted with `SelBg`. Esc clears. The copy menu and `y` use the range when present, through `dataio.FormatRows`. Inline edit stays single-cell.
- **Right-click menu:** copy cell, row and range, filter by value, inspect, record, edit, follow FK, referencing rows, freeze. Items run the same functions as keys, so read-only and edit guards still apply.

### 6. Panels and explorer
- **Explorer:**
  - wheel moves `top` and clamps `ix` into view; `top` is only updated in `view` today
  - fix the filter off-by-one (`explorer.go:577` runs before `h--` at `:583`)
  - right-click menu from `action` (`explorer.go:450`)
  - `click` stays as the entry point
- **Output sub-tabs:** click selects, middle-click closes. Pull the dirty-guarded close out of `output.update` (`output.go:108`) so both routes share it.
- **Inner sub-tabs:** `subTabs` (`panels.go:19`) returns spans, so clicks work even when labels are abbreviated.
- **Explain:** click toggles a node. Move the toggle out of `update` (`panels.go:371`).
- **`TextView`:** drag selection by line and column. The selected span renders from raw text with `SelBg`. `y` copies the selection when there is one, otherwise everything, as it does today.

### 7. Modals, forms, manager and context menus
- `menuModal` gets an optional anchor so `View` (`app.go:993`) can place it at the pointer, flipped to stay on screen. This is the context menu.
- Add `f10` (`context_menu`) to open the same menu for the focused item, because some terminals keep right-click for themselves.
- Clickable `[ yes ]`/`[ no ]` and `[ ok ]`/`[ cancel ]` in confirm, typed-confirm and prompt, respecting decision 5. Click in a prompt input places the caret.
- **Picker** (`picker.go:76`): click runs, wheel scrolls, handling `MultiLine` rows and `top`.
- **Form** (`form.go`): record a line-to-field map at render, which is the only way to handle sections and `Visible`. Click focuses via `FocusKey`, toggles bools and cycles selects.
- **Connection manager:** click selects, double-click connects, wheel scrolls, right-click for edit, test, duplicate and delete, using existing actions.

### 8. Status bar and UX fixes
**Status bar** (`app.go:1075`), with segments recorded as they are built:

| Segment | Click |
|---|---|
| connection | switch connection picker |
| database | database picker |
| `READ-ONLY` | decision 1 |
| timeout | timeout prompt |
| `TX` / manual tx | `tx_menu` |
| toast / error | open Messages panel |
| busy | cancel |
| help | help |

**Feedback and states**
- Show toasts on the connection manager, plus a spinner with elapsed time while connecting. Today toasts render only in `statusBar` (`app.go:1122`).
- Editor placeholder also shows when focused and empty (`editor.go:1368`).
- Explorer no-match shows "no matches" instead of "loading…" (`explorer.go:585`).
- `r` retries a failed DDL load (`panels.go:174-179`).
- First `d` shows a "press again" hint (`results.go:261`).
- Remove the duplicate `r.status()`, shown in both the panel footer and the status bar (`results.go:158`, `app.go:1138`).

**Discoverability**
- Replace hard-coded key names with `m.keys.First`: `app.go:349`, `tab.go:111`, `results.go:163`, `results.go:427`, `output.go:266`, `run.go:85`, `browse.go:57`.
- Key hints on detail, plan and text panels.
- Help gains a mouse section and the missing Messages `C` and panel `e` (`commands.go:332`).
- Manager footer wraps instead of cutting `esc back` (`connections.go:557`).

**Keyboard consistency**
- Remove the shadowed `f5` defaults.
- Add `f2` to `focus_next`, because `tab` stays indent/completion while typing (`typingKey`, `app.go:716`).
- Esc in the explorer with no filter returns to the editor, matching output.
- `<`/`>` and `1…9` switch sub-tabs, and `H`/`L` reach the grid in detail and admin.

**Safety**
- Esc on an edited connection form asks before discarding (`connections.go:277`).
- Validation errors focus the bad field (`connections.go:426`).

### 9. Docs
- `docs/USAGE.md` mouse section and changed keys. The website renders this file (`site/main.go:62`).
- README feature bullet and key table.
- `CHANGELOG.md` under `Unreleased`, including key changes.
- Copy this plan to `docs/mouse-and-ux.md`.
- No version tag or push unless asked.

## Testing and verification

**Unit tests**, new `internal/ui/mouse_test.go` following `ui_test.go`:
- Add `click`, `drag` and `wheel` helpers using `newTestModel` and `drain`, always calling `m.View()` first, because geometry is set during rendering.
- Add `locate(m, "text")`, which finds text in the ANSI-stripped `View()` and clicks there, so tests fail if geometry drifts from rendering.
- Cover: zoomed clicks, every surface, drag past edges, lost release, wide runes in the editor, vim and standard selection, column resize across `AppendRows`, freeze with horizontal scroll, ranges under filter and sort, wheel-driven lazy loading, tab reorder keeping the right active tab, guarded closes, outside-click rules, typed-confirm not bypassed, layout round trip, and narrow sizes through `TestViewAtSizes`.

**Commands**
- `gofmt -l .`
- `make vet`
- `go test ./...`
- `make build`
- `make test-integration` is not required, since no driver changes. Run it if any `internal/db` file changes, reusing running containers per global rule 1.

**Real app**
- Run in tmux at 120×40, 80×24 and 50×14 against `dev/squi.db` with `dev/config`.
- Send SGR mouse reports with `tmux send-keys -l` and check `capture-pane`, including one report split into two writes.

**Not verifiable by me:** feel in real terminals. The final report will ask the user to try iTerm2, Terminal.app and tmux, covering right-click and shift+drag passthrough, which vary by terminal.
