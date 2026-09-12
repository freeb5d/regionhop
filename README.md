<div align="center">

# regionhop

**Multi-region Psiphon tunnel manager for a single Linux server.**
Web panel + SSH CLI. SOCKS proxies stay local to the box — always.

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/panel-Go-00ADD8)](panel)
[![Platform](https://img.shields.io/badge/platform-Debian%2FUbuntu%20(systemd)-informational)](install.sh)

</div>

---

## What it does

regionhop runs several [`psiphon-tunnel-core`](https://github.com/psiphon-labs/psiphon-tunnel-core)
instances on one server, each configured to exit through a different Psiphon
region. Each instance exposes its own SOCKS5 proxy — bound to `127.0.0.1`
only, so it is **never reachable from outside the box**. A small Go web panel
lets you add, remove, start/stop, and watch logs for each region; the same
actions are available over SSH via a `psictl` command.

- 🌍 **Multiple regions, one server** — spin up as many location tunnels as you want, each isolated in its own systemd unit
- 🔒 **Local-only by design** — SOCKS ports are bound to loopback in the Psiphon config *and* blocked at the firewall as a second layer; nothing in the panel can expose them externally
- 🖥 **Web panel** — bcrypt-hashed password, signed session cookies, login-attempt lockout, random listen port chosen at install time
- ⌨️ **SSH-side control** — `psictl list|start|stop|restart|logs` for anyone who prefers the terminal
- ⚙️ **systemd-native** — every tunnel and the panel itself are ordinary systemd services: `systemctl status`, `journalctl`, auto-restart on failure, all work as expected

## Install

```bash
sudo bash <(curl -Ls https://raw.githubusercontent.com/freeb5d/regionhop/master/install.sh)
```

This opens an interactive menu. For a first run, pick **1) Full setup** — it
installs Go, downloads the prebuilt panel binary from the
[latest release](https://github.com/freeb5d/regionhop/releases/latest) (falls
back to building it from source if that's ever unavailable), builds
`psiphon-tunnel-core`'s `ConsoleClient` from source, installs the systemd
units, adds a firewall rule blocking external access to the SOCKS port range,
and walks you through setting the panel's admin password. It prints the
panel's URL at the end.

You can re-run the same command any time to reopen the menu (rebuild the
core, rotate the password, check status, uninstall, etc.) — it's idempotent.

> The panel ships as a prebuilt `linux/amd64` binary in each release, so
> installing it is just a download. `ConsoleClient` (Psiphon's own tunnel
> core) is a much larger codebase and is always built from source on your
> server during install/update.

## Before you add a location

Psiphon requires a `PropagationChannelId` and `SponsorId` issued to you by
[Psiphon Inc.](https://psiphon.ca) as a registered partner — regionhop has no
way to generate or fetch these, and doesn't ship any. Enter yours from the
panel's **Psiphon credentials** page (or via the installer's menu option)
before adding your first location; every location added afterwards picks
them up automatically.

## How it's laid out

| Path | Purpose |
|---|---|
| `install.sh` | Interactive installer / menu, safe to re-run |
| `panel/` | Go web panel (auth, dashboard, settings) |
| `systemd/psi-tunnel@.service` | One systemd template, instantiated per region as `psi-tunnel@<name>` |
| `systemd/psi-panel.service` | The panel's own systemd unit |
| `configs/_template.json` | Psiphon config template each new location is generated from |

On the server, everything lives under `/opt/psi-panel/`:

```
/opt/psi-panel/
├── core/ConsoleClient      # built Psiphon tunnel-core binary
├── configs/<name>.json     # one Psiphon config per location
├── data/<name>/            # per-location Psiphon data dir
├── data/tunnels.json       # panel's registry of locations
└── panel/psi-panel         # built panel binary
```

## Managing it over SSH

```bash
psictl list                 # show all location units
psictl start de-1           # enable + start a location
psictl stop de-1
psictl restart de-1
psictl logs de-1            # last 200 journal lines
psictl panel-restart
psictl panel-logs
psictl check-update    # compare installed vs. latest GitHub release
psictl update           # update the panel to the latest release, restart it
```

## Updating

The dashboard shows a banner when a newer release is published. To update:

```bash
psictl update
```

or, without `psictl` installed:

```bash
sudo bash <(curl -Ls https://raw.githubusercontent.com/freeb5d/regionhop/master/install.sh) update
```

This fetches the latest release, updates the panel (from the prebuilt
binary when available), reinstalls the systemd units, and restarts the
panel. It does **not** touch the already-built `ConsoleClient` — use
**Rebuild core only** from the installer menu if you also want to rebuild
the Psiphon core against its latest upstream source.

## Security model

- Each location's `LocalSocksProxyPort` is bound via `ListenInterface: "lo"` in
  its generated config — this value is never taken from panel input, only
  from an internal port allocator, so there is no path through the UI to bind
  a SOCKS port externally.
- A firewall rule additionally drops external traffic to the whole SOCKS port
  range (19000–19999) as defense-in-depth, independent of the config.
- The panel itself: bcrypt password hash, HMAC-signed session cookies,
  `HttpOnly`/`SameSite=Strict` cookies, and a 5-attempt login lockout per IP.
  Tunnel-manipulating routes all require an authenticated session.
- Each location's systemd service runs as an unprivileged `psipanel` user
  with `NoNewPrivileges`, `ProtectSystem=strict`, and a scoped
  `ReadWritePaths`.

## Uninstall

Re-run the installer and choose **Uninstall everything** — it stops and
removes every unit, deletes `/opt/psi-panel`, and removes the `psictl`
helper and the `psipanel` system user.

## License

[MIT](LICENSE)
