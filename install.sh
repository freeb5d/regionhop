#!/usr/bin/env bash
# Psiphon multi-region tunnel manager — installer / menu.
# Run as root on a Debian/Ubuntu (systemd) server. Everything it manages
# (the SOCKS proxies) is bound to 127.0.0.1 only; only the web panel and
# SSH management are meant to be reached remotely.
set -euo pipefail

PREFIX=/opt/psi-panel
ADMIN_DIR=/opt/regionhop-admin
REPO="freeb5d/regionhop"
REPO_URL="https://github.com/${REPO}.git"
CHECKOUT_DIR=/opt/regionhop-src
SERVICE_USER=psipanel
GO_VERSION=1.22.9
PANEL_ENV="$PREFIX/panel/panel.env"
REGISTRY="$PREFIX/data/tunnels.json"
VERSION_FILE="$PREFIX/VERSION"

if [[ $EUID -ne 0 ]]; then
  echo "Run this as root, e.g.:" >&2
  echo "  sudo bash <(curl -Ls https://raw.githubusercontent.com/freeb5d/regionhop/master/install.sh)" >&2
  exit 1
fi

# Resolve SRC_DIR: if this script is run from a real checkout (has sibling
# systemd/ and panel/ dirs), use that. If it's run standalone — e.g. via
#   bash <(curl -Ls https://raw.githubusercontent.com/freeb5d/regionhop/master/install.sh)
# — ${BASH_SOURCE[0]} points at a process-substitution fd with no siblings,
# so clone the repo first and use that checkout instead.
_candidate="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd || true)"
if [[ -n "$_candidate" && -d "$_candidate/systemd" && -d "$_candidate/panel" ]]; then
  SRC_DIR="$_candidate"
else
  if ! command -v git &>/dev/null; then
    apt-get update -y && apt-get install -y --no-install-recommends git ca-certificates
  fi
  if [[ -d "$CHECKOUT_DIR/.git" ]]; then
    git -C "$CHECKOUT_DIR" pull --ff-only
  else
    rm -rf "$CHECKOUT_DIR"
    git clone --depth 1 "$REPO_URL" "$CHECKOUT_DIR"
  fi
  SRC_DIR="$CHECKOUT_DIR"
fi

ensure_user() {
  if ! id "$SERVICE_USER" &>/dev/null; then
    useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
  fi
  # Lets the panel (running as this unprivileged user) read `journalctl -u
  # psi-tunnel@<name>` for the Logs page — without this, journalctl refuses
  # with "No journal files were opened due to insufficient permissions."
  usermod -aG systemd-journal "$SERVICE_USER"
  setup_sudo_control
  ensure_path_prefix
  fix_prefix_ownership
}

setup_sudo_control() {
  # The panel runs `sudo -n systemctl {enable --now|disable --now|restart}`
  # (and, for the Update button, sudo -n <self-update.sh>) as the
  # unprivileged psipanel user — grant that via a narrowly-scoped NOPASSWD
  # sudoers rule instead of polkit: polkit's JS rules.d format and default
  # authorization behavior differ enough across systemd/polkit versions
  # that a rule which works on one server silently no-ops on another
  # ("Interactive authentication required."); sudoers' command-matching
  # semantics are stable everywhere.
  local systemctl_path
  systemctl_path=$(command -v systemctl)

  # Remove a stale polkit rule from earlier regionhop versions, if present.
  rm -f /etc/polkit-1/rules.d/49-regionhop.rules

  install_self_update_script

  local sudoers_file=/etc/sudoers.d/regionhop-psipanel
  local tmp
  tmp=$(mktemp)
  cat > "$tmp" <<EOF
# Managed by regionhop's install.sh — do not edit by hand, it is
# regenerated on every install/update. Scoped to exactly the operations
# the panel needs on exactly the units/scripts it's meant to control.
Cmnd_Alias REGIONHOP_TUNNEL_ENABLE = $systemctl_path enable --now psi-tunnel@*
Cmnd_Alias REGIONHOP_TUNNEL_DISABLE = $systemctl_path disable --now psi-tunnel@*
Cmnd_Alias REGIONHOP_TUNNEL_RESTART = $systemctl_path restart psi-tunnel@*
Cmnd_Alias REGIONHOP_PANEL_RESTART = $systemctl_path restart psi-panel
Cmnd_Alias REGIONHOP_SELF_UPDATE = $ADMIN_DIR/self-update.sh
$SERVICE_USER ALL=(root) NOPASSWD: REGIONHOP_TUNNEL_ENABLE, REGIONHOP_TUNNEL_DISABLE, REGIONHOP_TUNNEL_RESTART, REGIONHOP_PANEL_RESTART, REGIONHOP_SELF_UPDATE
EOF
  if visudo -c -f "$tmp" &>/dev/null; then
    install -m 0440 -o root -g root "$tmp" "$sudoers_file"
  else
    echo "WARNING: generated sudoers rule failed validation, not installing it. Panel start/stop/restart/update will not work until this is fixed." >&2
    visudo -c -f "$tmp" >&2 || true
  fi
  rm -f "$tmp"
}

install_self_update_script() {
  # Fixed-content, root-owned script the panel is allowed to trigger via
  # sudo (no arguments, exact path — the whole point of scoping it this
  # narrowly in sudoers). It just re-runs the same update flow `psictl
  # update` already uses over SSH; the panel's "Update now" button is not a
  # separate/wider privilege surface than that.
  #
  # This MUST live outside $PREFIX: ensure_dirs() recursively chowns all of
  # $PREFIX to the unprivileged psipanel user, and Unix delete/replace
  # permission is governed by the *directory's* write bit, not the file's —
  # so a root-owned, mode-0700 script sitting inside a psipanel-owned
  # directory can still be deleted and recreated by psipanel with arbitrary
  # content, which sudo would then run as root unchanged (sudoers matches
  # by path, not by the file it pointed to when the rule was written). A
  # dedicated, root-owned, mode-0700 directory that psipanel can't even
  # list or enter is what actually keeps this scoped.
  mkdir -p "$ADMIN_DIR"
  chown root:root "$ADMIN_DIR"
  chmod 0700 "$ADMIN_DIR"
  # Clean up the old, vulnerable location from earlier regionhop versions
  # (it lived inside psipanel-owned $PREFIX/panel/ — see the comment below).
  rm -f "$PREFIX/panel/self-update.sh"
  cat > "$ADMIN_DIR/self-update.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
exec bash <(curl -Ls https://raw.githubusercontent.com/${REPO}/master/install.sh) update
EOF
  chown root:root "$ADMIN_DIR/self-update.sh"
  chmod 0700 "$ADMIN_DIR/self-update.sh"
}

ensure_dirs() {
  mkdir -p "$PREFIX"/{core,configs,data,panel}
  fix_prefix_ownership
}

# Ownership model for everything under $PREFIX, and the retrofit path for
# installs created before this scheme existed:
#   $PREFIX, $PREFIX/core, $PREFIX/panel   root:root, 0755 — psipanel can
#     read/execute (ConsoleClient, the psi-panel binary) but never write, so
#     it can't replace either binary out from under a later `sudo` call or a
#     root-run installer command (see set_panel_password below and the
#     self-update.sh comment above — same bug class, this is the general
#     form of that fix: nothing root directly executes, or a NOPASSWD sudo
#     rule points at, may live in a directory the unprivileged panel process
#     can write to).
#   $PANEL_ENV                              root:root, 0600 — systemd reads
#     EnvironmentFile= as root before dropping to User=psipanel, so the
#     panel process itself never needs to read this file directly.
#   $PREFIX/configs, $PREFIX/data           psipanel:psipanel — the running
#     panel legitimately writes here at runtime (tunnel configs, the
#     registry, your pasted Psiphon deployment config).
fix_prefix_ownership() {
  [[ -d "$PREFIX" ]] || return 0
  chown root:root "$PREFIX" "$PREFIX/core" "$PREFIX/panel" 2>/dev/null || true
  chmod 0755 "$PREFIX" "$PREFIX/core" "$PREFIX/panel" 2>/dev/null || true
  [[ -f "$PREFIX/core/ConsoleClient" ]] && { chown root:root "$PREFIX/core/ConsoleClient"; chmod 0755 "$PREFIX/core/ConsoleClient"; }
  [[ -f "$PREFIX/panel/psi-panel" ]] && { chown root:root "$PREFIX/panel/psi-panel"; chmod 0755 "$PREFIX/panel/psi-panel"; }
  [[ -f "$PREFIX/healthcheck.sh" ]] && { chown root:root "$PREFIX/healthcheck.sh"; chmod 0755 "$PREFIX/healthcheck.sh"; }
  [[ -f "$PANEL_ENV" ]] && { chown root:root "$PANEL_ENV"; chmod 0600 "$PANEL_ENV"; }
  mkdir -p "$PREFIX/configs" "$PREFIX/data"
  chown -R "$SERVICE_USER:$SERVICE_USER" "$PREFIX/configs" "$PREFIX/data"
  # Migrate extra-config.json out of the now-root-owned panel/ dir from
  # earlier regionhop versions, into data/ where the panel can still write
  # it at runtime.
  if [[ -f "$PREFIX/panel/extra-config.json" ]]; then
    mv -f "$PREFIX/panel/extra-config.json" "$PREFIX/data/extra-config.json"
    chown "$SERVICE_USER:$SERVICE_USER" "$PREFIX/data/extra-config.json"
  fi
}

ensure_go() {
  if command -v go &>/dev/null && go version | grep -q "go1\."; then
    return
  fi
  echo "Installing Go $GO_VERSION..."
  local arch
  arch=$(dpkg --print-architecture)
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${arch}.tar.gz" -o /tmp/go.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tar.gz
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
  rm -f /tmp/go.tar.gz
}

install_packages() {
  apt-get update -y
  apt-get install -y --no-install-recommends git curl ca-certificates ufw sudo
}

build_core() {
  echo "Fetching and building psiphon-tunnel-core (ConsoleClient)..."
  local build_dir=/tmp/psiphon-build
  rm -rf "$build_dir"
  git clone --depth 1 https://github.com/psiphon-labs/psiphon-tunnel-core.git "$build_dir"
  (cd "$build_dir/ConsoleClient" && go build -o "$PREFIX/core/ConsoleClient" .)
  chown root:root "$PREFIX/core/ConsoleClient"
  chmod 0755 "$PREFIX/core/ConsoleClient"
  echo "Core built at $PREFIX/core/ConsoleClient"
  echo
  echo "NOTE: edit propagation/sponsor IDs via menu option 'Set Psiphon IDs' before adding locations."
}

repo_version() {
  # Version string for this checkout (from VERSION file), used to stamp the
  # panel binary and to compare against GitHub's latest release.
  if [[ -f "$SRC_DIR/VERSION" ]]; then
    tr -d ' \t\n\r' < "$SRC_DIR/VERSION"
  else
    echo "0.0.0"
  fi
}

latest_release_tag() {
  # Prints e.g. "v1.1.0", empty on failure. Requires curl.
  curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null \
    | grep -o '"tag_name": *"[^"]*"' | head -1 | sed -E 's/.*"([^"]+)"$/\1/'
}

download_panel_binary() {
  # Fast path: fetch the prebuilt linux/amd64 panel binary from the given
  # release tag (e.g. "v1.1.0"). Returns non-zero if unavailable so callers
  # fall back to building from source.
  local tag="$1" arch
  arch=$(dpkg --print-architecture)
  [[ "$arch" == "amd64" ]] || return 1
  local url="https://github.com/${REPO}/releases/download/${tag}/regionhop-panel-linux-amd64"
  echo "Downloading prebuilt panel binary ($tag)..."
  curl -fsSL "$url" -o "$PREFIX/panel/psi-panel.new" || return 1
  chmod 0755 "$PREFIX/panel/psi-panel.new"
  chown root:root "$PREFIX/panel/psi-panel.new"
  mv "$PREFIX/panel/psi-panel.new" "$PREFIX/panel/psi-panel"
}

build_panel() {
  local tag
  tag="v$(repo_version)"
  if download_panel_binary "$tag"; then
    echo "Panel installed from prebuilt release binary."
  else
    echo "Prebuilt binary unavailable, building web panel from source..."
    (cd "$SRC_DIR/panel" && go mod tidy && go build -ldflags "-X main.CurrentVersion=$(repo_version)" -o "$PREFIX/panel/psi-panel" .)
    chown root:root "$PREFIX/panel/psi-panel"
    chmod 0755 "$PREFIX/panel/psi-panel"
  fi
  echo "$(repo_version)" > "$VERSION_FILE"
}

install_healthcheck() {
  # See healthcheck.sh's own header for what this does and why. Same
  # ownership treatment as the ConsoleClient/psi-panel binaries above: lives
  # in the root-owned $PREFIX tree even though psipanel (which runs it) only
  # ever needs to execute it, not write it.
  cp "$SRC_DIR/healthcheck.sh" "$PREFIX/healthcheck.sh"
  chown root:root "$PREFIX/healthcheck.sh"
  chmod 0755 "$PREFIX/healthcheck.sh"
}

install_units() {
  cp "$SRC_DIR/systemd/psi-tunnel@.service" /etc/systemd/system/
  cp "$SRC_DIR/systemd/psi-panel.service" /etc/systemd/system/
  cp "$SRC_DIR/systemd/psi-healthcheck.service" /etc/systemd/system/
  cp "$SRC_DIR/systemd/psi-healthcheck.timer" /etc/systemd/system/
  install_healthcheck
  systemctl daemon-reload
  systemctl enable --now psi-healthcheck.timer
}

setup_firewall() {
  # Defense-in-depth: SOCKS ports are already bound to 127.0.0.1 by config,
  # this just makes sure nothing external can ever reach that port range
  # even if a future config change forgot to bind to loopback.
  if command -v ufw &>/dev/null; then
    ufw deny in proto tcp from any to any port 19000:19999 comment 'psi-socks local-only' || true
  fi
}

random_port() {
  # avoid well-known ranges and the socks range
  local p
  while true; do
    p=$(( (RANDOM % 20000) + 20000 ))
    if [[ $p -lt 19000 || $p -gt 19999 ]]; then
      echo "$p"
      return
    fi
  done
}

set_panel_password() {
  read -rsp "New panel admin password: " pw1; echo
  read -rsp "Confirm password: " pw2; echo
  if [[ "$pw1" != "$pw2" || -z "$pw1" ]]; then
    echo "Passwords did not match or were empty." >&2
    return 1
  fi
  local hash
  hash=$("$PREFIX/panel/psi-panel" -hash-password "$pw1" 2>/dev/null || true)
  if [[ -z "$hash" ]]; then
    echo "Panel binary does not support -hash-password yet; build the panel first." >&2
    return 1
  fi
  set_env_var PANEL_ADMIN_HASH "$hash"
  echo "Password updated."
}

set_env_var() {
  local key="$1" val="$2"
  touch "$PANEL_ENV"
  if grep -q "^$key=" "$PANEL_ENV" 2>/dev/null; then
    sed -i "s#^$key=.*#$key=$val#" "$PANEL_ENV"
  else
    echo "$key=$val" >> "$PANEL_ENV"
  fi
  chmod 600 "$PANEL_ENV"
  # root:root, not psipanel: systemd reads EnvironmentFile= as root before
  # dropping to User=psipanel in the unit, so the panel process itself never
  # needs read access to this file, and giving it write access would let a
  # compromised panel rewrite its own PANEL_ADMIN_HASH.
  chown root:root "$PANEL_ENV"
}

random_path_prefix() {
  # A random path the panel is served under (e.g. /a1b2c3d4e5f6) so a port
  # scanner that finds the listening port still can't reach the login page
  # without also guessing this — on top of, not instead of, the actual auth.
  echo "/$(head -c 8 /dev/urandom | xxd -p -c 8)"
}

first_time_panel_setup() {
  [[ -f "$PANEL_ENV" ]] && return
  local port secret path_prefix
  port=$(random_port)
  secret=$(head -c 32 /dev/urandom | xxd -p -c 32)
  path_prefix=$(random_path_prefix)
  set_env_var PANEL_LISTEN "0.0.0.0:$port"
  set_env_var PANEL_SESSION_SECRET "$secret"
  set_env_var PANEL_PATH_PREFIX "$path_prefix"
  echo
  echo "Panel will listen on port $port (all interfaces) — set an admin password now."
  set_panel_password
  echo
  echo "=== Save this ==="
  echo "Panel URL:  http://$(server_ip):$port$path_prefix/"
  echo "================="
}

# Idempotent retrofit for installs from before PANEL_PATH_PREFIX existed:
# adds one if panel.env is present but doesn't have it yet, so `psictl
# update` picks this up on existing servers too, the same way the journal
# group and sudoers rule retrofits work.
ensure_path_prefix() {
  [[ -f "$PANEL_ENV" ]] || return 0
  grep -q '^PANEL_PATH_PREFIX=' "$PANEL_ENV" 2>/dev/null && return 0
  local path_prefix
  path_prefix=$(random_path_prefix)
  set_env_var PANEL_PATH_PREFIX "$path_prefix"
  local port
  port=$(sed -n 's/^PANEL_LISTEN=.*://p' "$PANEL_ENV")
  echo "Added a random path prefix to the panel URL: $path_prefix"
  echo "Your panel is now at: http://$(server_ip):${port}${path_prefix}/"
}

server_ip() {
  # Best-effort public IP detection for the "Panel URL:" line — falls back
  # through a few sources since not every box has outbound internet or the
  # same tools installed, and finally to a placeholder if all of them fail
  # (e.g. fully offline install) rather than hanging or erroring out.
  local ip
  ip=$(curl -fsSL --max-time 3 https://api.ipify.org 2>/dev/null) && [[ -n "$ip" ]] && { echo "$ip"; return; }
  ip=$(curl -fsSL --max-time 3 https://ifconfig.me 2>/dev/null) && [[ -n "$ip" ]] && { echo "$ip"; return; }
  ip=$(hostname -I 2>/dev/null | awk '{print $1}') && [[ -n "$ip" ]] && { echo "$ip"; return; }
  echo "<server-ip>"
}

set_psiphon_ids() {
  echo "Paste the full JSON object from your own Psiphon deployment config"
  echo "(PropagationChannelId, SponsorId, RemoteServerListUrl, signature public"
  echo "keys, NetworkID, etc.). End input with Ctrl-D:"
  local cfg
  cfg=$(cat)
  if [[ -z "$cfg" ]]; then
    echo "Nothing entered, leaving existing config unchanged." >&2
    return 1
  fi
  if ! echo "$cfg" | python3 -c 'import json,sys; json.load(sys.stdin)' 2>/dev/null \
     && ! echo "$cfg" | node -e 'JSON.parse(require("fs").readFileSync(0,"utf8"))' 2>/dev/null; then
    echo "WARNING: could not validate as JSON (no python3/node available to check) — saving as-is; the panel will reject it on next save if invalid." >&2
  fi
  echo "$cfg" > "$PREFIX/data/extra-config.json"
  chown "$SERVICE_USER:$SERVICE_USER" "$PREFIX/data/extra-config.json"
  echo "Saved. Existing location configs are not retroactively updated — remove and re-add them if needed."
}

start_panel() {
  systemctl enable --now psi-panel.service
  echo "Panel started."
}

status_all() {
  echo "--- Panel ---"
  systemctl status psi-panel.service --no-pager -l || true
  echo "--- Tunnels ---"
  systemctl list-units 'psi-tunnel@*' --no-pager || true
}

install_psictl() {
  cat > /usr/local/bin/psictl <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  list) systemctl list-units 'psi-tunnel@*' --no-pager ;;
  start) systemctl enable --now "psi-tunnel@$2" ;;
  stop) systemctl disable --now "psi-tunnel@$2" ;;
  restart) systemctl restart "psi-tunnel@$2" ;;
  logs) journalctl -u "psi-tunnel@$2" -n 200 --no-pager ;;
  panel-logs) journalctl -u psi-panel -n 200 --no-pager ;;
  panel-restart) systemctl restart psi-panel ;;
  update) bash <(curl -Ls https://raw.githubusercontent.com/freeb5d/regionhop/master/install.sh) update ;;
  check-update) bash <(curl -Ls https://raw.githubusercontent.com/freeb5d/regionhop/master/install.sh) check-update ;;
  *) echo "usage: psictl {list|start|stop|restart|logs} <name> | panel-logs | panel-restart | update | check-update" ;;
esac
EOF
  chmod +x /usr/local/bin/psictl
  echo "Installed 'psictl' — try: psictl list"
}

version_lt() {
  # true if $1 < $2, comparing dotted numeric versions
  [[ "$1" == "$2" ]] && return 1
  [[ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -1)" == "$1" ]]
}

check_update() {
  local installed latest
  installed=$([[ -f "$VERSION_FILE" ]] && cat "$VERSION_FILE" || echo "0.0.0")
  latest=$(latest_release_tag)
  latest=${latest#v}
  if [[ -z "$latest" ]]; then
    echo "Could not reach GitHub to check for updates."
    return 1
  fi
  echo "Installed: $installed"
  echo "Latest:    $latest"
  if version_lt "$installed" "$latest"; then
    echo "Update available. Run: sudo bash <(curl -Ls https://raw.githubusercontent.com/${REPO}/master/install.sh) update"
    return 2
  fi
  echo "Up to date."
}

self_update() {
  echo "Checking for updates..."
  local latest
  latest=$(latest_release_tag)
  if [[ -z "$latest" ]]; then
    echo "Could not reach GitHub to check for updates." >&2
    return 1
  fi
  echo "Updating checkout to $latest..."
  if [[ -d "$CHECKOUT_DIR/.git" ]]; then
    git -C "$CHECKOUT_DIR" fetch --depth 1 origin "$latest"
    git -C "$CHECKOUT_DIR" checkout -q "$latest" 2>/dev/null || git -C "$CHECKOUT_DIR" checkout -q master
  else
    rm -rf "$CHECKOUT_DIR"
    git clone --depth 1 --branch "$latest" "$REPO_URL" "$CHECKOUT_DIR" 2>/dev/null \
      || git clone --depth 1 "$REPO_URL" "$CHECKOUT_DIR"
  fi
  SRC_DIR="$CHECKOUT_DIR"
  ensure_user
  build_panel
  install_units
  systemctl restart psi-panel 2>/dev/null || true
  echo "Updated panel to $(repo_version)."
  echo "Note: the Psiphon core (ConsoleClient) is not touched by 'update' — rerun 'Rebuild core only' from the menu if you want to rebuild it against the latest psiphon-tunnel-core source too."
}

uninstall_all() {
  read -rp "This removes the panel, all tunnels, and their data. Type YES to continue: " c
  [[ "$c" == "YES" ]] || { echo "Aborted."; return; }
  systemctl disable --now psi-panel.service 2>/dev/null || true
  systemctl disable --now psi-healthcheck.timer 2>/dev/null || true
  for u in $(systemctl list-units --all 'psi-tunnel@*' --no-legend | awk '{print $1}'); do
    systemctl disable --now "$u" 2>/dev/null || true
  done
  rm -f /etc/systemd/system/psi-panel.service /etc/systemd/system/psi-tunnel@.service \
        /etc/systemd/system/psi-healthcheck.service /etc/systemd/system/psi-healthcheck.timer
  systemctl daemon-reload
  rm -f /etc/sudoers.d/regionhop-psipanel /etc/polkit-1/rules.d/49-regionhop.rules
  rm -rf "$PREFIX" "$ADMIN_DIR" /usr/local/bin/psictl
  userdel "$SERVICE_USER" 2>/dev/null || true
  echo "Removed."
}

menu() {
  PS3=$'\nSelect an option: '
  options=(
    "Full setup (packages, Go, build core+panel, firewall, units)"
    "Rebuild core only"
    "Rebuild panel only"
    "Set Psiphon PropagationChannelId/SponsorId"
    "Set/reset panel admin password"
    "Start/enable panel"
    "Install psictl (SSH CLI)"
    "Show status"
    "Check for updates"
    "Update to latest release"
    "Uninstall everything"
    "Quit"
  )
  select opt in "${options[@]}"; do
    case $REPLY in
      1)
        install_packages; ensure_user; ensure_dirs; ensure_go
        build_core; build_panel; install_units; setup_firewall
        first_time_panel_setup; start_panel; install_psictl
        ;;
      2) ensure_go; build_core ;;
      3) ensure_user; build_panel; systemctl restart psi-panel 2>/dev/null || true ;;
      4) set_psiphon_ids ;;
      5) set_panel_password; systemctl restart psi-panel 2>/dev/null || true ;;
      6) install_units; start_panel ;;
      7) install_psictl ;;
      8) status_all ;;
      9) check_update || true ;;
      10) self_update ;;
      11) uninstall_all ;;
      12) break ;;
      *) echo "Invalid option" ;;
    esac
  done
}

# Non-interactive entry points, so `psictl update` / `psictl check-update`
# (and the panel's "update available" banner) can drive this script without
# the interactive select menu:
#   bash install.sh update         (or piped via curl, see README)
#   bash install.sh check-update
case "${1:-}" in
  update) self_update; exit 0 ;;
  check-update) check_update; exit $? ;;
esac

menu
