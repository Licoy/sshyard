# AGENTS.md

Global rules for anyone (human or agent) changing SSHYard. Read this before you write code.

## Hard constraints

- **Pure native UI.** Everything is drawn with MyGo `ui` (v0.9.1). No webview, no HTML, no xterm.js, and no HTML fallback of any kind.
- **Brand colour #1984FC.** Take colours from the theme (`c.Theme()`, `internal/theme`), never hard-coded hex in views. Every view must work in light and dark.
- **Every user-visible string in all five languages: `en`, `zh-Hans`, `zh-Hant`, `ko` and `ja`**: labels, tooltips, accessible labels, errors and menu items. Add the key to all five `locales/*.json` catalogs, regenerate the catalog and use the generated accessor (`m.OpenFilesHere()`). These are the supported languages.
- **Secrets use a local encrypted store** (`internal/keychain`): the operating system keychain, through `mygo.Secrets`, contains only one random 32-byte master key (`master/v1`) per OS user and application environment (development and production use separate services); passwords, private keys, passphrases, proxy/forward passwords, snippet bodies, hosts' startup commands, AI keys, backup location passwords/secret keys/tokens, backup location keys, the default backup key, the device MCP token and the app lock's record and one-time code secret belong in `secrets.enc`, encrypted with that master key. Never write secret plaintext to JSON, settings, logs or disk test fixtures. One-run secrets remain in memory. Startup reads metadata while holding the credential store lifetime lock without requesting system authorization, then tries a silent unlock (`TryUnlock`) that succeeds only where the system keychain hands over the master key without asking (macOS after "Always Allow", Windows) and otherwise stays locked without showing an error; the native app remains usable with secrets locked. Only explicit user actions needing saved secrets request an asynchronous unlock that may ask; a failed or denied request shows Retry/Cancel, and Cancel preserves drafts and leaves the app usable with secrets locked. Automatic forwards, device MCP and automatic backups wait for an unlock. Production has no memory fallback. The only fallback is a local master password, chosen by the user from the unlock failure while the system keychain cannot be used and nothing is stored yet, never automatically and never in place of a master key that already protects secrets: the master key is sealed under it with Argon2id (`internal/vault`) in `master.enc` beside `secrets.enc`, the system keychain then holds nothing, the password is never stored, silent unlock stays locked and every explicit unlock asks for it; the About page changes it or moves the master key back to the system keychain. Deleting an owner deletes its encrypted secret entries. No migration or compatibility with the former per-secret keychain layout is provided.
- **Portable secrets leave only in an end-to-end encrypted vault** (`internal/vault`): backup files are a compressed archive (`internal/backup`) sealed with a random vault key wrapped under an Argon2id-derived user password, with all compression, encryption and derivation performed locally; the former JSON payload still restores. Never persist the backup password. The only exceptions are the default backup password, set once on the Backup page for exports and locations to choose, and a location's own backup password, shared by its manual and automatic backups: only the derived key (never the password) is kept, in `secrets.enc`, and files it seals still open with that password. Automatic backups stay opt-in. Backup locations (WebDAV, S3 with self-implemented SigV4, GitHub gist via a personal access token) receive only sealed files, never write over another device's files, and prune only this device's. Backups include secrets by default, with an option to omit them, but never include the local master key (nor `master.enc`), the device MCP token, the backup locations and their credentials, the app lock (`applock.json`, its record and its one-time code secret), or anything about this computer's SSH keys (remembered key passphrases and `localkeys.json`, which notes the key each host last logged in with). Restore accepts only secrets belonging to entries in that backup and re-encrypts them under the destination device's master key. Secret batches are committed in one encrypted-file write; metadata failures trigger compensating rollback, without claiming crash atomicity across `hosts.json` and `secrets.enc`.
- **The app lock hides the windows, not the data** (`internal/applock`): it opens with a password, a TOTP code (RFC 6238, the secret in `secrets.enc`) or both, and lives in `applock.json` beside the data, never in settings or a backup; only a vault-sealed verifier of the password is kept, never the password. It locks at startup, after the chosen idle minutes, on screen lock or sleep and on request (`actLock`). While locked every window draws only its lock screen and the tray, shortcuts and host-naming notifications run nothing, while sessions, forwards, transfers and backups go on. Failed attempts are counted in the file with a capped back-off. What opens the lock (method and verifier) is also a record in `secrets.enc` (`keychain.AppLock`), written before the file; once the store unlocks the record wins, so a deleted, replaced or unreadable file is restored and the app locks again, and a check against the replaced lock opens nothing. A file that cannot be read keeps the app locked until then. The only way out of a forgotten lock is the reset on the lock screen, which first deletes every saved secret (`Keychain.Wipe`, which works while the store is locked), then the lock, keeps the lock screen and restarts the app; the next start deletes the rest of the data folder (hosts, settings, layout, known hosts, AI history and audit, backup locations, backgrounds, `master.enc`) before reading it (`finishReset`), so nothing the lock hid is shown. A new data file must be added to `resetFiles`.
- **Never commit key files** (`*.key` is ignored; the updater signing key stays outside the repo).
- **The version lives only in `mygo.json`.**

## Icons: Remix Icon for actions, LobeHub for AI brands, Material Icon Theme for file content

- All interface/action icons, excluding file/folder content artwork below, come from [Remix Icon](https://remixicon.com/) **v4.8.0**, vendored in `internal/icons/svg/` (Apache-2.0; `internal/icons/LICENSE` and `NOTICE` must stay). Do not upgrade past 4.8.0 without checking the licence: 4.9+ uses the "Remix Icon License v1.0".
- AI provider and model brand icons come exclusively from [LobeHub Icons](https://icons.lobehub.com/), vendored in `internal/aibrands/png/` with a pinned upstream version, `LICENSE` and `NOTICE`, and scaled by `internal/aibrands/gen.go` to the size the interface draws (see `NOTICE`). Use the official `-color` variant whenever available; brands without one retain the official light/dark artwork. Render native bitmaps without theme tinting, decode them on first use, never fetch artwork at runtime.
- Use `aibrands.Model(modelID, provider)` for models, matching their names before falling back to `aibrands.Provider(provider)` for unknown models. Select `Light`/`Dark` for the current theme; matching must never rewrite the model ID sent to the API. Add new brands and model families to this shared package, never per-view matching.
- File and folder content icons come exclusively from [Material Icon Theme](https://github.com/material-extensions/vscode-material-icon-theme) **v5.40.0**, vendored unchanged in `internal/fileicons/svg/` with local embedding; lookup uses `fileicons.Lookup(basename, isDir)`, with upstream defaults for unknown entries. Render its `Light`/`Dark` SVG using `ui.Image` to preserve original colors, never theme-tint or download artwork at runtime. This exception applies only to file/folder content, not interface actions. Keep all MIT, applicable Apache-2.0, Pictogrammers and provenance notices in `resources/licenses/material-icon-theme/`, which ships with the app.
- No other icon set, no icon fonts, no hand-drawn SVG paths in views.
- For Remix action icons, use the `-line` style. Use the matching `-fill` only for an active state, passed as `IconButton.On`.
- Do not add tests for icon/logo artwork, icon catalogs or icon lookup mappings (see "Tests and checks": logic tests only).
- To add an icon:
  1. Copy `icons/<Category>/<name>.svg` from the RemixIcon `v4.8.0` tag into `internal/icons/svg/`, unchanged.
  2. Add a variable in `internal/icons/icons.go` named by meaning, not by shape: `Upload = load("upload-2-line")`.

## App icon

- The source is `icon/logo.jpg`. `resources/icon.png` (Windows, Linux and the `.icns` fallback), `icon/Assets.car` (macOS, `macos.icon`), `resources/icon-dev.png` (the `mygo dev` app, not shipped) and `icon/logo-ui.png` (the logo the interface draws) are generated from it: never edit them by hand. After changing the logo, run `go run icon/gen.go` on a Mac with Xcode and commit the logo and every generated file.
- Keep sources out of `resources/`: everything there ships with the app.

## IconButton vs text Button

- Every icon-only action is an `IconButton` (`internal/app/iconbutton.go`). Do not build ad-hoc clickable icons with `ui.ButtonBase` or `ui.Icon` + click handling.
- `Label` is required and comes from the locale catalog. It is both the tooltip and the accessible label, so it names the action, not the icon.
- Set `Active` (and `On`) for a state that is on, `Toggle` for a toggle, `Disabled` when the action cannot run, `Danger` for destructive actions, `Size: Small` in dense rows.
- Hover, press, active and focus styles come from the theme accent. Do not restyle per call site.
- A text `Button` / `PrimaryButton` is only for the primary actions of a dialog or form.

## Right rail: everything it opens is a draggable pane

- Unless a task explicitly says otherwise, every window the right rail opens (system monitor, process usage, host details, Docker, tmux, snippets, command history, broadcast, session logs and any new one) is a workspace pane, like the files pane: never a fixed column, drawer or stacked panel beside the panes.
- Such a tool is a `layout.Kind` listed in `isTool` (`internal/app/toolpane.go`), named in `nameTools`, with icons in `kindIcon`, and drawn by `toolPaneView`. Its header drags it onto any pane's edge or another tab, and split dividers resize it. The header holds its name, its own actions and the close button; the body has no title bar of its own.
- The rail toggles it with `ToggleTool` and lights it with `toolHere`; code that must only show it (a notification, a link) uses `ShowTool`. There is one of each per workspace: toggling it from another tab brings it beside the focused pane. It never keeps the tab's focus, so it works on `a.focused()`. The layout saves where it is (a pane on no host, one of each), never what it shows.

## Design system

- Reference: macOS 27 (Liquid Glass). The accent is always #1984FC.
- Type scale only (`Caption 11, Footnote 12, Body 13, Title3 15, Title2 17, Title1 22`); spacing only via `theme.Space(n)` (4px grid); radii only via the `Radius*` tokens; shadows only via the `Shadow.*` tokens. New tokens need a test in `internal/theme/theme_test.go`.
- Identity tints come from `theme.Tint(id)` / `TintAt(i)`. Banners are rounded, tinted at α .12 with a matching border and a Remix icon.
- Sheets and dialogs use `sheet()` in `internal/app/style.go`. Their first field must autofocus and use a `focusHold` so typing works without a click. Buttons in sheets and settings are pills (`Capsule`).
- Glass without blur: MyGo has no backdrop blur except macOS vibrancy, so glass is approximated with alpha fills, hairlines and soft shadows. Do not fake blur with images or a webview.

## Keyboard

- App-wide shortcuts use `appMods` (⌘ on macOS, Ctrl+Shift elsewhere) so the terminal can handle plain Ctrl+letter. A new app shortcut must go through `isAppShortcut` so the terminal yields it.
- On Windows, the terminal uses Ctrl+V to paste and Ctrl+C to copy a selection. With no selection, Ctrl+C reaches the shell.
- Do not take a platform's standard editing shortcuts (⌘A, ⌘C, ⌘V and their equivalents).
- Users can record their own shortcuts (`settings.Shortcuts`). A new app shortcut is an entry in `actions` (`internal/app/keymap.go`), which the dispatcher, `isAppShortcut`, the menus, the Shortcuts page and the command palette all read; never claim its key with a hard-coded `c.Shortcut`. Recorded shortcuts must hold `shortcutMods`, with one exception: the tab actions (`selectTab1`–`selectTab9`) may be recorded as Alt (⌥ on macOS) and a digit alone (`altDigit`), giving that key to the app instead of the shell; the terminal drops the character the key types after it. Their defaults stay on `appMods`.

## Tests and checks

- **Documentation-only changes: do not run any tests.** Review the documentation diff instead.
- **Code changes: unless the current task explicitly requires broader verification, run only the unit tests covering the modified or newly added behavior.** Select the affected packages and test names with `go test -race -count=1 ./path/to/package -run '^(TestAffected|TestNew)$'`; replace the example package and names with the actual relevant tests. Do not run the full test suite (`go test ./...`, `go test -race ./...`, or equivalent), or unrelated tests. A commit or push alone is not a reason to broaden the test scope.
- If relevant unit tests are missing, add focused coverage where appropriate and report any remaining coverage gap; do not substitute a full test run.
- **Logic tests only; no UI or image tests.** Do not write tests about how the interface looks or is laid out: no screenshots or pixel sampling (`tt.Image()`, `RGBAAt`), colours, sizes, positions or bounds, wrapping, scroll offsets, tooltips or accessible labels, cursors, hover/focus/press styles, icons, or rendering across languages and themes. A UI-only change needs no test. Test behaviour and state instead: persisted data, secrets and their rollback, authorization and unlock, connections, commands sent or withheld, transfers, errors, and security gates. Prefer calling the logic or the handler directly; drive a flow through `ui.Tester` only when the behaviour cannot be reached otherwise, and then assert on state, not on what is drawn. A field bound to a TextInput is overwritten from the input's own state on the next frame; drive such flows by calling the handler rather than setting the field and clicking.
- Keep changed Go files formatted with `gofmt`; scope any required `go vet` checks to affected packages. Do not run repository-wide or cross-platform checks unless explicitly required by the current task.
- After editing `locales/*.json`, regenerate the catalog:

```sh
GOFLAGS=-mod=mod go run github.com/egoist/mygo/cmd/mygo generate
```

- Build only with MyGo's own `go tool mygo build`, never hand-rolled packaging:

```sh
MYGO_UPDATER_PRIVATE_KEY="$(cat path/to/mygo-update.key)" \
go tool mygo build \
  -platform linux/amd64,linux/arm64,windows/amd64,windows/arm64,darwin/arm64,darwin/amd64 -o dist
```

## CI and releases

- CI is GitHub Actions (`.github/workflows/ci.yml`, `release.yml`); keep both green. `ci.yml` runs the full checks, tests and builds on every push to `main`, on `v*` tags and by hand; pushes to `main` touching only `website/`, `docs/`, Markdown, `LICENSE` or `website.yml` are skipped, a newer push to `main` cancels the older run, other branches and pull requests run nothing, and `bump.sh`'s release commit is checked only on its tag (private repo, billed minutes). A tag whose commit differs from one with a passed run on `main` only in `mygo.json`'s version and skipped files reuses that run (`.github/ci-reuse.sh`, waiting for it if still running): no tests, and for a stable tag no builds either, since `release.yml` builds and signs; keep its file list in step with `paths-ignore`. Local verification before a push therefore still follows the change-scoped rules above, including the documentation-only exemption; a release tag also waits for `ci.yml` on that tag.
- `v<version>` is a stable release: `release.yml` builds, signs and verifies every platform, waits for `ci.yml` to pass on the same tag, then publishes the release as latest with all assets, `SHA256SUMS` and the notes (signing secrets: `.github/RELEASING.md`). A failed CI or build publishes nothing: re-run the failed jobs.
- Release notes come from the conventional commits since the previous stable tag, through `cliff.toml` (git-cliff): the release body adds download links, the update manifests (the in-app update window) get the notes alone.
- `v<version>-beta-<yyyymmddhhmmss>` is an internal beta: only `ci.yml` runs, it never makes a Release and beta builds have no update channel. The timestamp must be a real UTC time in 2020–2099.
- The base `<version>` of any tag must equal `mygo.json`'s version, or the build fails.
- To release: run the local checks, then `./bump.sh -v <x.y.z> -p`: it sets `mygo.json`'s version, commits `chore(release): bump version to <x.y.z>` (left out of the notes), tags `v<x.y.z>` and pushes both. Agents never push tags themselves.
