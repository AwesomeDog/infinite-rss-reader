# Go Rewrite Plan

## Naming

| Usage | Old | New |
|-------|-----|-----|
| Product name | RSS HTTP API / Infinite RSS Reader | **Infinite RSS Reader** (display name) |
| Binary name | `rss_bridge` / `rss_bridge.py` | **`infrss`** / **`infrss.exe`** |
| Extension ID | `rss_bridge@example.org` | **`infrss@awesomedog.github.io`** |
| Native messaging name | `rss_bridge` | **`infrss`** |
| Manifest filename | `rss_bridge.json` | **`infrss.json`** |
| Log file prefix | `thunderbird_rss_bridge_` | **`infrss_`** |
| Homebrew formula | - | **`infrss`** |
| Winget ID | - | **`AwesomeDog.InfRSS`** |

## Problem

The current installation process has too much friction for ordinary users:

1. **Python required** — macOS 12.3+ removed built-in Python; Windows never shipped it.
2. **Git required** — `git clone` is not something non-developers know.
3. **Terminal required** — running `python install.py` is intimidating.
4. **Separate installer** — `install.py` is a separate step from the actual app.
5. **Multiple moving parts** — `.py` script, `.bat` wrapper (Windows, required because Thunderbird native messaging needs an executable — `.py` isn't one), `.json` manifest, `.xpi` extension, all wired together.

The goal: **users download and double-click one file, done.**

## Solution

Rewrite `rss_bridge.py` and `install.py` into a **single Go binary (`infrss`)** that:

- **Eliminates Python entirely** — no runtime dependency.
- **Embeds everything** — `index.html`, add-on files, manifest template all baked into the binary via `//go:embed`.
- **Self-installs on first run** — no separate installer step.
- **Self-updates on every launch** — keeps the `.xpi` in sync with the binary version.
- **Requires zero command-line flags** — auto-detects whether launched by user or by Thunderbird.

## Architecture

### Two-Mode Binary (Auto-Detected)

The binary detects how it was launched — no CLI flags needed:

- **`--help`** → prints usage information and exits.
- **`--version`** → prints version string (e.g. `infrss 1.0.0`) and exits.
- **Thunderbird launches it** → passes the extension ID (`infrss@awesomedog.github.io`) as an argument → binary enters **Bridge Mode**.
- **User double-clicks it** → no arguments → binary enters **Install Mode**.

### Install Mode (user double-clicks the binary)

1. Determine install path: `~/.local/bin/infrss` (or `infrss.exe` on Windows)
2. Copy self to install path
   - Create `~/.local/bin/` if it doesn't exist
   - Skip if already running from that path
   - If target exists, run `~/.local/bin/infrss --version` to get installed version; overwrite if it differs from own version
3. Find Thunderbird profile directory
   - macOS: `~/Library/Thunderbird/Profiles/*.default*`
   - Windows: `%APPDATA%/Thunderbird/Profiles/*.default*`
   - Linux: `~/snap/thunderbird/common/.thunderbird/*.default*` (Snap) or `~/.thunderbird/*.default*`
4. Build `.xpi` from embedded add-on files (ZIP in memory)
5. Write `.xpi` to `<profile>/extensions/infrss@awesomedog.github.io.xpi`
6. Clear `<profile>/startupCache/` to force Thunderbird to reload
7. Write native messaging manifest (`infrss.json`) — `path` always points to `~/.local/bin/infrss`
   - macOS: copy to `~/Library/Mozilla/NativeMessagingHosts/`
   - Windows: write to disk + register in `HKCU\Software\Mozilla\NativeMessagingHosts\infrss`
   - Linux: copy to `~/.mozilla/native-messaging-hosts/`
8. Print success message + next steps, wait for Enter key, exit

### Bridge Mode (Thunderbird launches the binary)

1. Set up logging (date-based log files, auto-cleanup older than 7 days)
2. Silently check if embedded `.xpi` version > installed version → update if needed
3. Start HTTP server on `:7654` in a goroutine
4. Run native messaging stdin/stdout loop on main goroutine

## What Gets Embedded

```go
//go:embed embed/index.html
var indexHTML []byte

//go:embed add-on/*
var addonFS embed.FS

//go:embed embed/infrss.json
var manifestTemplate []byte
```

The distributed binary is a **single file** containing everything.

## Components to Port

Same functionality as `rss_bridge.py` + `install.py`. Refer to existing Python code for behavior details.

| Component | Go approach |
|-----------|-------------|
| Native Messaging | `encoding/binary` + `os.Stdin` / `os.Stdout` |
| BridgeState | `sync.Mutex` + channels (replace `threading.Event`) |
| HTTP Server (5 routes) | `net/http`, serve embedded `index.html`, CORS `*` |
| Logging | `<temp_dir>/infrss_YYYYMMDD.log`, auto-cleanup > 7 days |
| Installer (cross-platform) | See table below |
| Self-Update (Bridge Mode) | Compare embedded vs installed `.xpi` version, overwrite + clear `startupCache` if stale |

### Platform-Specific Installer Steps

| Step | macOS | Windows | Linux |
|------|-------|---------|-------|
| Copy binary | → `~/.local/bin/infrss` | → `~/.local/bin/infrss.exe` | → `~/.local/bin/infrss` |
| Find profiles dir | `~/Library/Thunderbird/Profiles` | `%APPDATA%/Thunderbird/Profiles` | `~/snap/thunderbird/common/.thunderbird` |
| Install `.xpi` | Copy to `<profile>/extensions/` | Same | Same |
| Clear startup cache | Remove `<profile>/startupCache/` | Same | Same |
| Get binary's own path | `os.Executable()` + `filepath.EvalSymlinks()` | Same | Same |
| Manifest `path` value | `/Users/X/.local/bin/infrss` | `C:\Users\X\.local\bin\infrss.exe` | `/home/X/.local/bin/infrss` |
| Write manifest | Copy to `~/Library/Mozilla/NativeMessagingHosts/` | Write file + `reg add` via `os/exec` | Copy to `~/.mozilla/native-messaging-hosts/` |

## Project Structure

```
infinite-rss-reader/
├── main.go              # Entry point, mode detection, flag-free dispatch
├── bridge.go            # BridgeState, channels, mutex
├── messaging.go         # Native messaging read/write (stdin/stdout)
├── server.go            # HTTP server, 5 route handlers
├── installer.go         # Install mode: profile detection, xpi build, manifest registration
├── xpisync.go           # Bridge mode: silent .xpi version check + update
├── logging.go           # Log setup + old log cleanup
├── embed.go             # //go:embed declarations
├── embed/               # Embedded static resources
│   ├── index.html       # Web UI (embedded)
│   └── infrss.json      # Native messaging manifest template (embedded)
├── add-on/              # Thunderbird extension files (embedded)
│   ├── manifest.json
│   ├── background.js
│   └── icons/
│       └── rss.svg
└── go.mod
```

Or if simplicity is preferred, a single `main.go` (~400-500 lines) is perfectly viable for this codebase size.

## Dependencies

**Standard library only.** Zero third-party dependencies on all platforms.

Windows registry writes use `os/exec` to call `reg add` instead of `golang.org/x/sys/windows/registry`.

## Version

A single `var version = "dev"` in `main.go`, injected at build time via `-ldflags`:

```bash
VERSION=$(git describe --tags --always --dirty)
go build -ldflags "-X main.version=${VERSION}" -o infrss .
```

- `infrss --version` prints `infrss <version>` and exits.
- CI extracts version from git tag automatically — no hardcoded value to forget.
- When building the `.xpi` in memory, the code patches `add-on/manifest.json`'s `"version"` field to match the binary's own version. The repo copy keeps a placeholder `"0.0.0"` — no CI sync step needed.

## Cross-Compilation

Build all targets from one machine:

```bash
VERSION=$(git describe --tags --always --dirty)
LDFLAGS="-X main.version=${VERSION}"

# macOS (Apple Silicon)
GOOS=darwin GOARCH=arm64 go build -ldflags "${LDFLAGS}" -o bin/infrss-macos-arm64 .

# macOS (Intel)
GOOS=darwin GOARCH=amd64 go build -ldflags "${LDFLAGS}" -o bin/infrss-macos-amd64 .

# Windows
GOOS=windows GOARCH=amd64 go build -ldflags "${LDFLAGS}" -o bin/infrss-windows.exe .

# Linux
GOOS=linux GOARCH=amd64 go build -ldflags "${LDFLAGS}" -o bin/infrss-linux-amd64 .
```

## Distribution

GitHub Releases with bare binaries (no ZIP — single file, nothing to extract):

```
infrss-macos-arm64
infrss-macos-amd64
infrss-linux-amd64
infrss-windows.exe
```

## User Experience (End Result)

### First Time

1. Download binary from GitHub Releases
2. Double-click `infrss` (macOS/Linux) or `infrss.exe` (Windows)
3. Binary copies itself to `~/.local/bin/`, installs extension + manifest
4. See "✅ Installation complete!" message
5. Open Thunderbird → set `xpinstall.signatures.required = false` in Config Editor → restart

### Every Subsequent Launch

Thunderbird auto-launches `~/.local/bin/infrss`. User just opens `http://localhost:7654`. Updates are automatic when the binary is replaced.

### Updating

1. Download new binary, double-click it
2. Binary runs `~/.local/bin/infrss --version`, detects version mismatch → overwrites `~/.local/bin/infrss` + updates `.xpi` + clears startup cache
3. Thunderbird picks up changes on next restart

## Update Strategy

### Distribution Channels

| Channel | Platforms | Update Method | User Action |
|---------|-----------|---------------|-------------|
| **GitHub Releases** | All | Download new binary, double-click it | Binary auto-detects version mismatch and overwrites old installation |
| **Homebrew** | macOS, Linux | `brew upgrade infrss` | Run one command |
| **Winget** | Windows | `winget upgrade` | Run one command |
