#!/usr/bin/env bash
# Psiphon multi-region tunnel manager — installer / menu.
# Run as root on a Debian/Ubuntu (systemd) server. Everything it manages
# (the SOCKS proxies) is bound to 127.0.0.1 only; only the web panel and
# SSH management are meant to be reached remotely.
set -euo pipefail

PREFIX=/opt/psi-panel
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
  setup_polkit
}

setup_polkit() {
  # The panel runs `systemctl start/stop/restart/enable/disable` as the
  # unprivileged psipanel user (NoNewPrivileges=true in its unit, so sudo/
  # setuid can't be used to escalate). Grant it permission via polkit,
  # scoped to only the units it's meant to control — everything else on the
  # box stays out of its reach.
  if ! command -v pkaction &>/dev/null && ! dpkg -s policykit-1 &>/dev/null 2>&1; then
    apt-get install -y --no-install-recommends policykit-1 || true
  fi
  mkdir -p /etc/polkit-1/rules.d
  cat > /etc/polkit-1/rules.d/49-regionhop.rules <<'EOF'
polkit.addRule(function(action, subject) {
    // start/stop/restart need manage-units; enable/disable (used by the
    // panel's --now flag) need manage-unit-files — grant both, scoped to
    // exactly the units the panel is meant to control.
    var scoped = (action.id == "org.freedesktop.systemd1.manage-units" ||
                  action.id == "org.freedesktop.systemd1.manage-unit-files") &&
                 subject.user == "psipanel";
    if (!scoped) {
        return polkit.Result.NOT_HANDLED;
    }
    var unit = action.lookup("unit");
    if (unit && (/^psi-tunnel@.*\.service$/.test(unit) || unit == "psi-panel.service")) {
        return polkit.Result.YES;
    }
    return polkit.Result.NOT_HANDLED;
});
EOF
  systemctl try-restart polkit 2>/dev/null || systemctl try-restart polkitd 2>/dev/null || true
}

ensure_dirs() {
  mkdir -p "$PREFIX"/{core,configs,data,panel}
  chown -R "$SERVICE_USER:$SERVICE_USER" "$PREFIX"
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
  apt-get install -y --no-install-recommends git curl ca-certificates ufw policykit-1
}

build_core() {
  echo "Fetching and building psiphon-tunnel-core (ConsoleClient)..."
  local build_dir=/tmp/psiphon-build
  rm -rf "$build_dir"
  git clone --depth 1 https://github.com/psiphon-labs/psiphon-tunnel-core.git "$build_dir"
  (cd "$build_dir/ConsoleClient" && go build -o "$PREFIX/core/ConsoleClient" .)
  chown "$SERVICE_USER:$SERVICE_USER" "$PREFIX/core/ConsoleClient"
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
  chmod +x "$PREFIX/panel/psi-panel.new"
  mv "$PREFIX/panel/psi-panel.new" "$PREFIX/panel/psi-panel"
  chown "$SERVICE_USER:$SERVICE_USER" "$PREFIX/panel/psi-panel"
}

build_panel() {
  local tag
  tag="v$(repo_version)"
  if download_panel_binary "$tag"; then
    echo "Panel installed from prebuilt release binary."
  else
    echo "Prebuilt binary unavailable, building web panel from source..."
    (cd "$SRC_DIR/panel" && go mod tidy && go build -ldflags "-X main.CurrentVersion=$(repo_version)" -o "$PREFIX/panel/psi-panel" .)
    chown "$SERVICE_USER:$SERVICE_USER" "$PREFIX/panel/psi-panel"
  fi
  echo "$(repo_version)" > "$VERSION_FILE"
}

install_units() {
  cp "$SRC_DIR/systemd/psi-tunnel@.service" /etc/systemd/system/
  cp "$SRC_DIR/systemd/psi-panel.service" /etc/systemd/system/
  systemctl daemon-reload
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
  chown "$SERVICE_USER:$SERVICE_USER" "$PANEL_ENV"
}

first_time_panel_setup() {
  [[ -f "$PANEL_ENV" ]] && return
  local port secret
  port=$(random_port)
  secret=$(head -c 32 /dev/urandom | xxd -p -c 32)
  set_env_var PANEL_LISTEN "0.0.0.0:$port"
  set_env_var PANEL_SESSION_SECRET "$secret"
  echo
  echo "Panel will listen on port $port (all interfaces) — set an admin password now."
  set_panel_password
  echo
  echo "=== Save this ==="
  echo "Panel URL:  http://<server-ip>:$port/"
  echo "================="
}

set_psiphon_ids() {
  read -rp "PropagationChannelId: " pcid
  read -rp "SponsorId: " sid
  set_env_var PSIPHON_PROPAGATION_CHANNEL_ID "$pcid"
  set_env_var PSIPHON_SPONSOR_ID "$sid"
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
  for u in $(systemctl list-units --all 'psi-tunnel@*' --no-legend | awk '{print $1}'); do
    systemctl disable --now "$u" 2>/dev/null || true
  done
  rm -f /etc/systemd/system/psi-panel.service /etc/systemd/system/psi-tunnel@.service
  systemctl daemon-reload
  rm -rf "$PREFIX" /usr/local/bin/psictl
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
