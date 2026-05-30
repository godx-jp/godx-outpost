# Security

Outpost gives a paired phone a **shell on the host as the user running the daemon**. That is the whole point — and it means pairing a device is equivalent to granting that account's access. Read this before exposing it.

## Trust model

- **A paired device == account-level access.** The terminal runs as the daemon's user with no sandbox in v1 (every valid token maps to the "admin" profile). Don't run `outpost` as root unless you mean to hand out root. Sandboxed/guest profiles are planned (M5) but **not** shipped — do not rely on any isolation today.
- **Single-user host assumption.** State (signing key, sockets) is protected by file permissions (`identity.json` 0600, session dir 0700). On a shared/multi-user machine, a local user who can read those files or reach the dtach sockets can impersonate the host or attach to a session. Run it on a machine you control.
- **The signing key is the crown jewel.** `identity.json` holds the HS256 key that signs every token; anyone who reads it can forge admin tokens. It's stored 0600 in the config dir (outside the repo) and the mode is re-tightened on load. Back it up like an SSH private key.
- **Bind narrowly.** Prefer `--bind 127.0.0.1` + a tunnel, or a private network (Tailscale). Only bind `0.0.0.0` on a trusted LAN. The pairing code is the only thing standing between the network and access during a pairing window — keep windows short (`--pair-ttl`).
- **Encrypt the transport on untrusted networks.** Over a public hostname, serve `wss://` — either built-in (`--tls-cert`/`--tls-key`, CA-trusted cert, TLS 1.2+) or via a TLS-terminating proxy (Caddy/Cloudflare). The token handshake authenticates the client but does not encrypt the channel; `ws://` is acceptable only on loopback or an already-encrypted overlay (Tailscale/WireGuard). Self-signed certs are rejected by iOS — use a trusted cert.

## How access is controlled

- **Pairing:** a single-use, in-memory, time-limited 6-digit code (CSPRNG). Redeeming it returns an **access** token (~15 min) + **refresh** token (~1 year). After a configurable number of bad attempts the outstanding codes are invalidated.
- **Tokens:** HS256 JWTs. The verifier pins `HS256` and rejects `alg=none`/asymmetric confusion. Access tokens carry a per-device client id; refresh tokens carry a revocation generation + device binding (constant-time compared).
- **Revocation:** per-device (`outpost revoke <clientId>` / dashboard "Kick") and global (`outpost revoke`). Revoking a device **immediately closes its live WebSocket(s)** and a connection is re-checked right after it registers, so a revoke can't be lost to a handshake race. The mobile client, on losing auth, clears its tokens and returns to the login screen.
- **Dashboard** is bound to `127.0.0.1` only and validated on **both** the peer address and the `Host` header (blocks DNS-rebinding), and its WebSocket terminal bridge enforces a loopback `Origin` (blocks cross-origin pages from opening a shell).

## Known limitations / hardening roadmap

These are documented honestly rather than hidden. Most are mitigated by the "bind to localhost/Tailscale, single-user host" posture above.

- **Long-lived authenticated sockets aren't re-validated mid-session.** Access-token expiry only gates *new* connections; an open socket stays valid until it closes or the device is revoked (revoke does kick it). Keep sessions and revoke promptly; a periodic re-check is on the roadmap.
- **Dashboard mutating endpoints are GET.** The Host/Origin checks stop remote and cross-origin abuse, but full CSRF tokens + POST are still TODO. **Don't expose the dashboard port beyond loopback**, and don't proxy it without adding auth.
- **No sandbox for sessions (M5).** When guest/`Root` profiles land, the filesystem sandbox needs TOCTOU-safe path resolution (`openat2`/`O_NOFOLLOW`); the current lexical+symlink checks are not sufficient for hostile sandboxed clients. Until then, only pair devices you trust with full access.
- **Web build token storage.** On the web target, tokens fall back to `localStorage` (no secure enclave). Native iOS uses the Keychain. Prefer the native app for anything sensitive.
- **`InsecureSkipVerify` on the main app WebSocket.** The phone connects from arbitrary origins, so Origin isn't checked there; access is gated entirely by the in-band token handshake. Bind/tunnel accordingly.

## Reporting a vulnerability

Please **do not** open a public issue for security problems. Email **info@famgia.com** with details and reproduction steps. We'll acknowledge and work on a fix before any public disclosure.
