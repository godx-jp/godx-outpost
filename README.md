# Outpost

**Control your computer from your phone — terminal, files, and system monitor over a single secure WebSocket.** Think TeamViewer/Termius, but self-hosted: a small Go daemon (`outpost`) runs on the machine you want to reach, and a React Native app (iOS) pairs with it by scanning a QR code.

- 🖥️ **Native terminal** on the phone (SwiftTerm) — real `UITextInput`, so IME input (incl. Vietnamese Telex), native scrolling, truecolor. Renders Claude Code / Codex / vim / tmux correctly.
- ♻️ **Persistent sessions** — terminals are backed by [`dtach`](https://github.com/crigler/dtach), so they survive daemon restarts and reboots, and can be attached locally (`dtach -a`) with native scrollback. tmux/zellij sessions are first-class too.
- 🔐 **Pairing + per-device tokens** — scan a QR once; the device gets a long-lived token. Revoke/kick any device from the dashboard; it drops to the login screen immediately.
- 🌐 **Endpoint-agnostic networking** — the phone just needs a URL. Same Wi-Fi (LAN IP), anywhere (Tailscale), or a public domain (Cloudflare Tunnel). Swap the hostname, nothing else.
- 📁 **Files + 📊 monitor** channels, **multi-host** management, and a **local web dashboard** for pairing/device management.

> ⚠️ **Trust model:** a paired device gets a shell **as the user running `outpost`** — i.e. full access to that account. Treat pairing like handing out SSH access. Read [SECURITY.md](SECURITY.md) before exposing it.

---

## Install

`outpost` is a single static binary (pure Go, no cgo). Pick whichever fits the **host machine** you want to control:

### Homebrew (macOS)
```sh
brew install godx-jp/tap/outpost
```

### Linux / macOS — one-liner
```sh
curl -fsSL https://raw.githubusercontent.com/godx-jp/godx-outpost/main/scripts/install.sh | sh
```
Detects OS/arch, downloads the matching release asset, **verifies its SHA-256** against the release checksums, and installs to `/usr/local/bin` (or `~/.local/bin`).

### Debian/Ubuntu / Fedora/RHEL
Download the `.deb` / `.rpm` from [Releases](https://github.com/godx-jp/godx-outpost/releases):
```sh
sudo dpkg -i outpost_*_linux_amd64.deb     # Debian/Ubuntu
sudo rpm  -i outpost_*_linux_amd64.rpm      # Fedora/RHEL
```

### From source (Go ≥ 1.25)
```sh
go install github.com/godx-jp/godx-outpost/cmd/outpost@latest
```

**Optional but recommended:** install [`dtach`](https://github.com/crigler/dtach) on the host (`brew install dtach` / `apt install dtach`) so terminal sessions persist across restarts and can be attached locally. Without it, sessions live only for the daemon's lifetime.

---

## Quick start

On the **host** machine:

```sh
# Same Wi-Fi as the phone — advertise the machine's LAN IP:
outpost start --bind 0.0.0.0 --port 8722 --advertise ws://192.168.1.50:8722 --open
```

This prints a **pairing QR** in the terminal and (with `--open`) launches the local dashboard at `http://127.0.0.1:9722`. In the app: **Hosts → Scan QR**. Done — you have a terminal.

To run it as a background service that comes back after reboot:

```sh
outpost install --bind 0.0.0.0 --port 8722 --advertise ws://192.168.1.50:8722 --open
# launchd on macOS, systemd --user on Linux; sessions auto-restore (--restore).
```

---

## Networking — "just a URL"

`--advertise` is the URL embedded in the pairing QR; it's whatever the **phone** can reach. The daemon doesn't care which it is:

| Reach | `--advertise` | Notes |
|---|---|---|
| Same Wi-Fi/LAN | `ws://<lan-ip>:8722` | Simplest. Phone must be on the same network. |
| Anywhere (recommended) | `ws://<tailscale-ip>:8722` | Install [Tailscale](https://tailscale.com) on host + phone. No port-forwarding. |
| Public domain | `wss://outpost.example.com` | Put a TLS reverse proxy / Cloudflare Tunnel in front. Use `wss://`. |

Bind to `0.0.0.0` for LAN/Tailscale, or keep `127.0.0.1` and front it with a tunnel. The **dashboard** is always bound to `127.0.0.1` only.

---

## CLI reference

```
outpost start      Start the WebSocket server + print a pairing QR.
outpost pair       Print a fresh pairing QR without starting the server.
outpost status     Show device ID and config dir.
outpost devices    List paired devices.
outpost revoke [clientId]   Revoke one device (kicks it live), or all tokens.
outpost restore    Re-open saved sessions (after a reboot).
outpost install    Install as a login service (launchd / systemd --user).
outpost uninstall  Remove the login service.
outpost version
```

**`start` / `install` flags**

| Flag | Default | Purpose |
|---|---|---|
| `--bind` | `127.0.0.1` | Listen address. |
| `--port` | `8722` | Listen port. |
| `--advertise` | _(bind addr)_ | URL embedded in the pairing QR (what the phone connects to). |
| `--pair-ttl` | `2m` | Pairing-code lifetime. |
| `--restore` | `true` | Re-open saved sessions on startup. |
| `--open` | `false` | Run the local web dashboard (QR + devices). |
| `--dashboard-port` | `port+1000` | Dashboard port (127.0.0.1 only). |
| `--prompt` | _(start only)_ | Short shell prompt for sessions. |
| `--config-dir` | OS config dir | Identity/token/sessions dir. **Distinct dirs = distinct hosts.** |

Run several independent hosts on one machine by giving each its own `--config-dir` and `--port`.

---

## How it works

```
┌─────────────┐   one multiplexed WebSocket    ┌──────────────────────────┐
│  Mobile app │ ◀────── ctrl/term/fs/sys ─────▶ │  outpost daemon (host)   │
│ (Expo / RN) │   JSON envelopes + binary       │  PTYs · files · metrics  │
└─────────────┘   frames (term I/O)             └──────────────────────────┘
```

- **Protocol** (`internal/protocol`): text `Envelope {ch,type,id,data,err}` for control/RPC, and a compact `BinaryFrame [kind][idLen][id][payload]` for terminal I/O. Channels: `ctrl` (pairing/auth/ping), `term`, `fs`, `sys`, `api`.
- **Auth** (`internal/auth`): a stable per-host identity (device ID + HS256 signing key in `identity.json`, mode `0600`) created once and reused, so tokens survive restarts. Pairing mints a single-use, in-memory, short-lived **6-digit code**; redeeming it returns a short-lived **access** token (~15m) + long-lived **refresh** token (~1y). Each paired device has a record in SQLite, enabling **per-device revoke**.
- **Terminal** (`internal/term`): sessions are `dtach`-backed when available (survive restart, local `dtach -a`, native scroll), with an in-process fallback. Session metadata (cwd/shell/size) is persisted to SQLite for `outpost restore`. tmux/zellij sessions are listed and attachable.
- **Server** (`internal/server`): one WebSocket hub; the auth gate only lets `ctrl` through pre-auth; revoking a device **closes its live sockets immediately**.
- **Dashboard** (`internal/dashboard`): a `127.0.0.1`-only web UI for the pairing QR (auto-rotating code), paired devices (name/type/kick/rename) and live sessions, plus a browser terminal. Hardened against DNS-rebinding / cross-origin (Host + Origin checks).

New sessions open in the folder configured in the app (default `~/projects`); the daemon expands `~` and starts the shell there, so tmux/zellij launched inside inherit it.

---

## Mobile app

Expo (SDK 52) React Native app in [`mobile/`](mobile). The terminal uses a **native SwiftTerm view on iOS** (real keyboard/IME, native scroll) and falls back to xterm.js-in-WebView on web/Android.

```sh
cd mobile
npm install
npx expo run:ios --device     # build + install on a connected iPhone (needs Xcode + a signing team)
# or: npx expo start          # then open in the dev client
```

The SwiftTerm dependency is added via Swift Package Manager to the iOS app target (see `mobile/ios/`); a full dev-client rebuild is required after native changes.

---

## Building from source

```sh
go build ./...                                   # build everything
go build -o bin/outpost ./cmd/outpost            # the daemon
# cross-compile (static, cgo off):
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o outpost ./cmd/outpost
```

### Cutting a release
Releases are produced by [GoReleaser](https://goreleaser.com) on a `v*` tag (see `.goreleaser.yaml` + `.github/workflows/release.yml`): cross-builds, checksums, a Homebrew tap (`godx-jp/homebrew-tap`), and `.deb`/`.rpm`. The workflow needs a `TAP_GITHUB_TOKEN` repo secret (a fine-grained PAT with write access to the tap repo).

```sh
git tag v0.1.0 && git push origin v0.1.0          # CI runs goreleaser
# or locally:  TAP_GITHUB_TOKEN=<pat> goreleaser release --clean
```

---

## Project layout

```
cmd/outpost/        CLI entrypoint (start, pair, install, …)
internal/
  protocol/         envelope + binary frame wire format
  auth/             identity, pairing, JWT issue/verify, per-device revoke
  server/           WebSocket hub + auth gate + revoke-kick
  channel/          Conn / Handler interfaces
  term/             PTY sessions (dtach), tmux/zellij, cwd/restore
  fs/               file browse / read / write / upload
  sys/              system metrics
  dashboard/        local web UI (QR, devices, sessions, web terminal)
  store/            SQLite (devices + sessions)
  launcher/         shell/command launcher (sandbox seam)
mobile/             Expo React Native app (iOS native SwiftTerm terminal)
scripts/install.sh  curl|sh installer
```

---

## Security

A paired device gets shell access as your user. The daemon is designed to be bound to localhost or a private network (Tailscale) — see **[SECURITY.md](SECURITY.md)** for the trust model, what's hardened, known limitations, and how to report a vulnerability.

## License

MIT — see [LICENSE](LICENSE).
