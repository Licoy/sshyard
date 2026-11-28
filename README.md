<p align="left"><strong>English</strong> · <a href="README_ZH.md">简体中文</a> · <a href="README_ZH_TW.md">繁體中文</a> · <a href="README_KO.md">한국어</a> · <a href="README_JA.md">日本語</a></p>

<p align="center">
  <img src="resources/icon.png" alt="SSHYard logo" width="128" height="128">
</p>

<h1 align="center">SSHYard</h1>

SSHYard is a native desktop SSH / SFTP client for macOS, Windows and Linux, bringing terminals, file management and AI assistance into one app.

## Screenshots

<table>
  <tr>
    <td width="50%" align="center">
      <img src="docs/terminal-ai-assistant.webp" alt="Terminal and AI assistant" width="100%"><br>
      Terminal and AI assistant
    </td>
    <td width="50%" align="center">
      <img src="docs/sftp-file-manager.webp" alt="SFTP file manager" width="100%"><br>
      SFTP file manager
    </td>
  </tr>
  <tr>
    <td width="50%" align="center">
      <img src="docs/system-monitor.webp" alt="System monitor" width="100%"><br>
      System monitor
    </td>
    <td width="50%" align="center">
      <img src="docs/ai-assistant-settings.webp" alt="AI assistant settings" width="100%"><br>
      AI assistant settings
    </td>
  </tr>
</table>

## Features

- **Terminal**
  - Tabs, split panes and terminal search, with tabs you can reorder and rename
  - True colour, wide characters, input methods and mouse support for tmux, vim and htop
  - Ten built-in colour schemes plus `.itermcolors` import, keyword highlighting, line numbers and timestamps, and adjustable cursor, line height and character width
  - Many output encodings, set per host; ⌘ / Ctrl-click to open links in the terminal, ⌥ / Alt-drag for block selection
  - Command suggestions from the host's shell history as you type, accepted with → or End, and a list of the folder's files to complete paths from
  - Telnet, serial ports and local terminals besides SSH (PowerShell, CMD, Git Bash and WSL on Windows)
  - Windows remote desktops (RDP) in a tab or pane, at the pane's size, with clipboard text, TLS or Network Level Authentication, through a proxy or SSH jump hosts
  - Open a local terminal in a folder with `sshyard --path <folder>`, or choose SSHYard in Finder's Open With for a folder; running it again while the app is open uses the existing window
  - Shared connections across panes on the same host, with automatic reconnection that acts at once on a network change or wake from sleep and returns the shell to its folder

- **Workspace**
  - System monitor, process usage, host details, Docker, tmux, snippets, command history, broadcast commands and session logs are draggable panes that dock beside any pane or in another tab
  - tmux session manager that returns to your session after a reconnect
  - Docker container and image details with resource usage, plus starting and stopping containers
  - Broadcast commands with synchronised typing, and session logs recorded as text or raw output
  - Command palette and customisable shortcuts, with optional memory of window and split layouts

- **File management**
  - SFTP alongside the terminal with directory following, falling back to SCP when the server has no SFTP
  - Tabbed built-in editor with a folder tree
  - File access through sudo when you need it
  - Drag-and-drop support and folder transfers; uploads keep permissions and modification times
  - Copy files and folders straight from one server to another by drag-and-drop or copy and paste, streamed through the app without touching the local disk
  - Transfer queue with pause, resume and retry, keeping the computer awake while files transfer

- **AI assistant**
  - Run commands and edit files through a dedicated assistant for each host, over SSH, Telnet, serial or a local terminal, with conversations saved per host
  - Connect AI providers, or use ACP agents detected on this computer (Claude Code, Codex, Grok, Gemini CLI and more), several at once, each with the models and thinking levels it offers; their work stays on the remote host
  - Paste images and files as attachments
  - Choose Ask, Review or Full access mode, with a configurable review model, tool-call limit per turn and request timeout

- **Agent integrations**
  - Connect local AI agents through the open API, MCP and Skill
  - Browse hosts, establish connections and run commands without exposing saved credentials
  - One-click Skill setup for Claude Code and Codex; agent access is off by default

- **Hosts and credentials**
  - Import from SSH config, migrate from ten clients such as Xshell, MobaXterm and PuTTY, or paste a list of hosts as text; or connect quickly with `user@host:port`
  - Passwords, OpenSSH / PEM / PuTTY keys, OpenSSH certificates, SSH agent and password-less login support
  - Nested groups you can rearrange by dragging, with multi-select and latency tests
  - Provider, renewal schedule and notes for each host, with a reminder before renewal
  - Local encrypted credential storage, key generation and public-key installation; changed host keys are checked by fingerprint, and known hosts can be managed

- **Backup and restore**
  - Password-encrypted backups of hosts, credentials, snippets and settings, with restore on another device
  - Save locally or to WebDAV, S3-compatible storage, private GitHub Gists or a sync folder such as iCloud Drive, OneDrive or Dropbox
  - Manual or scheduled backups, with configurable retention

- **Networking**
  - Multi-hop jump hosts and HTTP / SOCKS5 proxies, which AI providers and update checks can use too
  - Local, remote and dynamic port forwarding

- **Everyday use**
  - Server metrics at a glance
  - Light and dark themes, with background images, translucency and interface scaling
  - Close to the tray and connect to hosts from the tray popover
  - English, Simplified Chinese, Traditional Chinese, Korean and Japanese

## Linux credentials and dependencies

Run SSHYard as your normal desktop user, without root or `sudo`. Saving and unlocking credentials requires libsecret 0.19 or newer (`libsecret-1.so.0`), your user's session D-Bus, and an available, unlocked [Secret Service](https://specifications.freedesktop.org/secret-service/latest/) provider, such as GNOME Keyring or another compatible provider configured for your desktop. Installing libsecret alone does not provide a running Secret Service. The system service stores SSHYard's master key; credentials remain encrypted locally in `secrets.enc`.

- **Debian / Ubuntu:** the `.deb` declares `libsecret-1-0 (>= 0.19)` alongside GTK; install through a dependency-resolving package manager, for example `sudo apt install ./sshyard_<version>_amd64.deb`, using the actual downloaded filename
- **Arch Linux:** the generated `PKGBUILD` declares `libsecret` and `gtk3`; MyGo's Debian-to-Arch mapping drops version constraints, so ensure libsecret is at least 0.19
- **Archive / `install.sh`:** installation is per-user under `~/.local` and does not automatically install libsecret, GTK or a Secret Service provider; arrange these dependencies first, and do not run the script as root

Missing system packages or a tenant policy disabling session D-Bus / Secret Service may require an administrator to resolve. Use your desktop's existing keyring manager to unlock its collection when needed. SSHYard uses native UI and does not require WebKitGTK.

Without a usable Secret Service, for example on a tenant account without administrator rights, SSHYard can protect the master key with a local master password instead: when unlocking fails and nothing is saved yet, choose **Use local master password…** in the error dialog. The master key is then sealed under that password (Argon2id) in `master.enc` beside `secrets.enc`, nothing is written to the system keychain, and you enter the password the first time saved secrets are needed after each launch. SSHYard never stores the password; if you forget it, saved secrets can only be recovered from a backup. **Settings → About → Secret storage** changes it, or moves the master key back to the system keychain once one is available. **Copy diagnostics** in the same dialog copies the credential service's own error message for a bug report; it contains no secrets.

For a read-only connectivity check, run this from a terminal in the **same desktop login session** as SSHYard, without `sudo` (requires `gdbus`):

```sh
gdbus call --session --dest org.freedesktop.secrets \
  --object-path /org/freedesktop/secrets \
  --method org.freedesktop.Secret.Service.ReadAlias default
```

A returned collection path means the service is reachable; `/` means it has no default collection. If a collection path is returned, replace `/returned/collection/path` below with that exact path to inspect its lock state:

```sh
gdbus call --session --dest org.freedesktop.secrets \
  --object-path /returned/collection/path \
  --method org.freedesktop.DBus.Properties.Get org.freedesktop.Secret.Collection Locked
```

`true` means locked and `false` means unlocked. These calls may activate the session service, but do not read passwords, unlock collections or modify/delete keyring entries. They only inspect connectivity and collection state; even an unlocked collection does not guarantee SSHYard can read or save its master key. A D-Bus connection error points to the login session or its access policy; `ServiceUnknown` usually means no Secret Service provider is available for activation. See the [ReadAlias API](https://specifications.freedesktop.org/secret-service/latest/org.freedesktop.Secret.Service.html#org.freedesktop.Secret.Service.ReadAlias) for its return values.

## Development

Built with [mygo](https://github.com/egoist/mygo). Requires Go 1.27.3 (`GOTOOLCHAIN` can download it). No cgo is needed.

```sh
# run
go run .

# run the development app; on macOS prefer an "Apple Development" identity
# stable signing can reduce keychain prompts; self-signing does not guarantee
# access across rebuilds, and macOS may still request authorization
scripts/run-dev-app.sh

# keep data in a separate directory
SSHYARD_DATA_DIR=/tmp/demo go run .

# a local SSH server for testing (macOS / Linux)
go run ./cmd/devsshd -addr 127.0.0.1:2222 -user demo -password demo -root /tmp/remote

# a local HTTP / SOCKS proxy for testing
go run ./cmd/devproxy -addr 127.0.0.1:3128

# tests and checks
go test -race -count=1 ./...
go vet ./...
gofmt -l .

# regenerate the locale catalog after editing locales/*.json
GOFLAGS=-mod=mod go run github.com/egoist/mygo/cmd/mygo generate

# package into dist/
go tool mygo build -platform linux/amd64,linux/arm64,windows/amd64,windows/arm64,darwin/arm64,darwin/amd64 -o dist
```

To try it against devsshd, add a host `127.0.0.1`, port `2222`, user `demo`, password `demo`.

Contributor rules are in [AGENTS.md](AGENTS.md).

## Community Support

- [LinuxDO](https://linux.do)

## License

SSHYard is licensed under the [GNU Affero General Public License v3.0](LICENSE).
