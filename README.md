# regionhop

Psiphon multi-region tunnel manager, local-only SOCKS.

Runs multiple `psiphon-tunnel-core` instances on **one server**, each exiting through
a different Psiphon region, each exposing a SOCKS5 proxy that is **bound to
127.0.0.1 only** — never reachable from outside the server. A small password-protected
web panel lets you add/remove regions and start/stop/monitor them; management is also
available from SSH via `psictl`.

## Important — read before using

- `psiphon-tunnel-core` is Anthropic-unrelated open-source code from the Psiphon
  project. Using it requires you to supply your own `PropagationChannelId` and
  `SponsorId` (obtained by registering as a Psiphon partner / using Psiphon's own
  published open-source client IDs). This installer ships **placeholder** values in
  `configs/_template.json` — you must fill in real, authorized values yourself. Do not
  use IDs you are not entitled to use.
- This setup is for **your own personal use of the server itself**. The SOCKS ports
  are firewalled to loopback (`127.0.0.1`) both in the Psiphon config and via a host
  firewall rule, specifically so that the tunnels cannot be turned into a public/shared
  proxy service for other people. Do not remove that binding to try to expose the
  ports externally.

## Layout

- `install.sh` — interactive installer/menu (run as root on the target Linux server)
- `systemd/psi-tunnel@.service` — one systemd service template, instantiated per region
- `configs/_template.json` — Psiphon config template (fill in your own IDs)
- `panel/` — Go web panel (auth-protected, random port, localhost-bound tunnels only)
- `psictl` — CLI helper installed to `/usr/local/bin` for SSH-side management

## Verify before relying on this

I could not run or network-fetch anything while building this (offline sandbox), so
two things are unverified and you should check them the first time you run
`install.sh` on the real server:

1. **`ConsoleClient` build path** — `install.sh` clones
   `github.com/psiphon-labs/psiphon-tunnel-core` and runs
   `go build ./ConsoleClient`. If a newer/older commit renamed or moved that
   directory, `build_core` will fail with a clear "no such directory" error —
   adjust the path in `install.sh`'s `build_core()` to match.
2. **Config field names** — `configs/_template.json` / `config.go` use the field
   names I know from this project's config schema
   (`PropagationChannelId`, `SponsorId`, `EgressRegion`, `LocalSocksProxyPort`,
   `ListenInterface`, `DataRootDirectory`, ...). If `ConsoleClient` refuses to
   start with an "unknown field" / "invalid config" error, check that instance's
   log (`psictl logs <name>`) and compare against the `Config` struct in the
   cloned repo's `psiphon/config.go`, then adjust `panel/config.go` accordingly.

Everything else (systemd wiring, auth, firewall rule, port allocation) is plain
Go/bash and was reviewed by hand, not executed against a live server.

## Quick start

```bash
sudo bash install.sh
```

Then follow the menu: `1) Install/build core` → `2) Add location` → `3) Start panel`.
The panel prints its URL, port, and login on first setup; change the password anytime
from the menu.
