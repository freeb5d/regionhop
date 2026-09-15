#!/usr/bin/env bash
# Psiphon multi-region tunnel manager — installer / menu.
# Run as root on a systemd Linux server with apt-get, dnf, or pacman
# (Debian/Ubuntu, Fedora, or Arch and their derivatives). Everything it
# manages (the SOCKS proxies) is bound to 127.0.0.1 only; only the web
# panel and SSH management are meant to be reached remotely.
set -euo pipefail

# detect_pkg_mgr echoes "apt", "dnf", or "pacman" for the package manager
# found on this system, or returns non-zero if none of the three is
# present — that's the concrete set of distro families this script knows
# how to install packages / a firewall rule on. (A distro without systemd,
# like Alpine, isn't supported regardless of package manager: the tunnel
# units, journalctl-based status/log reading, and sudoers-scoped systemctl
# control this whole project is built on all assume systemd is there.)
detect_pkg_mgr() {
  if command -v apt-get &>/dev/null; then
    echo apt
  elif command -v dnf &>/dev/null; then
    echo dnf
  elif command -v pacman &>/dev/null; then
    echo pacman
  else
    return 1
  fi
}

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

if ! command -v systemctl &>/dev/null; then
  echo "This server has no systemd — regionhop's tunnel units, log reading, and" >&2
  echo "privilege model all depend on it, so it isn't supported here." >&2
  exit 1
fi

if ! PKG_MGR=$(detect_pkg_mgr); then
  echo "No supported package manager found (looked for apt-get, dnf, pacman)." >&2
  echo "regionhop supports Debian/Ubuntu, Fedora, and Arch (and their derivatives)." >&2
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
    case "$PKG_MGR" in
      apt) apt-get update -y && apt-get install -y --no-install-recommends git ca-certificates ;;
      dnf) dnf install -y git ca-certificates ;;
      pacman) pacman -Sy --noconfirm git ca-certificates ;;
    esac
  fi
  if [[ -d "$CHECKOUT_DIR/.git" ]]; then
    # This checkout only ever mirrors upstream master — nothing local is
    # ever committed into it — so a hard reset to origin/master is always
    # the right outcome here, not a merge/rebase decision. Plain `pull
    # --ff-only` fails outright if upstream's history was ever rewritten
    # (a force-push changes every commit hash, so the old shallow clone and
    # the new origin aren't a fast-forward of each other at all), which
    # would otherwise turn one rewrite into a permanently broken `psictl
    # update` for every server that installed before it.
    git -C "$CHECKOUT_DIR" fetch --depth 1 origin master
    git -C "$CHECKOUT_DIR" reset --hard origin/master
  else
    rm -rf "$CHECKOUT_DIR"
    git clone --depth 1 "$REPO_URL" "$CHECKOUT_DIR"
  fi
  SRC_DIR="$CHECKOUT_DIR"
fi

ensure_user() {
  if ! id "$SERVICE_USER" &>/dev/null; then
    # nologin's path varies by distro (/usr/sbin on Debian, /sbin or
    # /usr/sbin on Fedora, /usr/bin on Arch) and root's PATH doesn't always
    # include sbin dirs, so check the concrete candidates directly instead
    # of relying on `command -v`.
    local nologin_shell=/bin/false
    for candidate in /usr/sbin/nologin /sbin/nologin /usr/bin/nologin; do
      [[ -x "$candidate" ]] && { nologin_shell="$candidate"; break; }
    done
    useradd --system --no-create-home --shell "$nologin_shell" "$SERVICE_USER"
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
  #
  # Each location's exact unit name is enumerated in the sudoers rule
  # rather than matched with a psi-tunnel@* wildcard — some sudo builds
  # are compiled with --disable-wildcards and reject ANY wildcard in a
  # Cmnd_Alias as a hard syntax error, which silently broke every
  # tunnel-control action on those builds (the whole rule failed
  # validation and visudo refused to install it at all, with no obvious
  # symptom beyond "tunnels never connect" and empty journals for units
  # that were never actually started). install_sudoers_refresh_script
  # writes the script that does the actual enumeration/regeneration; it's
  # run once here for the initial install, and the panel itself re-runs it
  # via that same sudo NOPASSWD rule whenever a location is added or
  # removed (see refreshSudoersRule in panel/systemctl.go), so the
  # allowlist always matches the current registry.

  # Remove a stale polkit rule from earlier regionhop versions, if present.
  rm -f /etc/polkit-1/rules.d/49-regionhop.rules

  install_self_update_script
  install_sudoers_refresh_script
  "$ADMIN_DIR/refresh-sudoers.sh"
}

install_sudoers_refresh_script() {
  # Root-owned, mode 0700 for the same reason self-update.sh is (see its
  # own comment above): psipanel must never be able to alter what a
  # sudo-triggered script actually does. The heredoc is fully quoted (no
  # variable expansion at generation time) and the handful of install-time
  # constants are substituted afterward via sed, to keep the generated
  # script's own runtime variables (which must stay literal) unambiguous.
  mkdir -p "$ADMIN_DIR"
  chown root:root "$ADMIN_DIR"
  chmod 0700 "$ADMIN_DIR"
  local systemctl_path
  systemctl_path=$(command -v systemctl)
  cat > "$ADMIN_DIR/refresh-sudoers.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
REGISTRY="__REGISTRY__"
SYSTEMCTL="__SYSTEMCTL__"
ADMIN_DIR="__ADMIN_DIR__"
SERVICE_USER="__SERVICE_USER__"
SUDOERS_FILE=/etc/sudoers.d/regionhop-psipanel

names=()
if [[ -f "$REGISTRY" ]]; then
  while IFS= read -r n; do names+=("$n"); done < <(grep -oP '"name"\s*:\s*"\K[a-z0-9][a-z0-9-]{1,30}(?=")' "$REGISTRY")
fi
# sudoers doesn't allow an empty Cmnd_Alias; "_none_" contains characters
# validName() never allows in a real location name, so it can never
# collide with one and is purely a placeholder for "no locations yet".
[[ ${#names[@]} -eq 0 ]] && names=("_none_")

enable_cmds="" disable_cmds="" restart_cmds=""
for n in "${names[@]}"; do
  enable_cmds+="${enable_cmds:+, }$SYSTEMCTL enable --now psi-tunnel@${n}.service"
  disable_cmds+="${disable_cmds:+, }$SYSTEMCTL disable --now psi-tunnel@${n}.service"
  restart_cmds+="${restart_cmds:+, }$SYSTEMCTL restart psi-tunnel@${n}.service"
done

tmp=$(mktemp)
{
  echo "# Managed by regionhop -- do not edit by hand. Regenerated whenever a"
  echo "# location is added or removed, and on every install/update. Enumerates"
  echo "# exact unit names instead of psi-tunnel@* -- some sudo builds reject a"
  echo "# wildcarded Cmnd_Alias outright as a syntax error."
  echo "Cmnd_Alias REGIONHOP_TUNNEL_ENABLE = $enable_cmds"
  echo "Cmnd_Alias REGIONHOP_TUNNEL_DISABLE = $disable_cmds"
  echo "Cmnd_Alias REGIONHOP_TUNNEL_RESTART = $restart_cmds"
  echo "Cmnd_Alias REGIONHOP_PANEL_RESTART = $SYSTEMCTL restart psi-panel"
  echo "Cmnd_Alias REGIONHOP_SELF_UPDATE = $ADMIN_DIR/self-update.sh"
  echo "Cmnd_Alias REGIONHOP_SUDOERS_REFRESH = $ADMIN_DIR/refresh-sudoers.sh"
  echo "$SERVICE_USER ALL=(root) NOPASSWD: REGIONHOP_TUNNEL_ENABLE, REGIONHOP_TUNNEL_DISABLE, REGIONHOP_TUNNEL_RESTART, REGIONHOP_PANEL_RESTART, REGIONHOP_SELF_UPDATE, REGIONHOP_SUDOERS_REFRESH"
} > "$tmp"

if visudo -c -f "$tmp" &>/dev/null; then
  install -m 0440 -o root -g root "$tmp" "$SUDOERS_FILE"
else
  echo "regionhop: regenerated sudoers rule failed validation, not installing it" >&2
  visudo -c -f "$tmp" >&2 || true
fi
rm -f "$tmp"
EOF
  sed -i \
    -e "s#__REGISTRY__#$REGISTRY#" \
    -e "s#__SYSTEMCTL__#$systemctl_path#" \
    -e "s#__ADMIN_DIR__#$ADMIN_DIR#" \
    -e "s#__SERVICE_USER__#$SERVICE_USER#" \
    "$ADMIN_DIR/refresh-sudoers.sh"
  chown root:root "$ADMIN_DIR/refresh-sudoers.sh"
  chmod 0700 "$ADMIN_DIR/refresh-sudoers.sh"
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

# detect_arch maps `uname -m` to the suffix used in this repo's release
# bundle filenames (regionhop-linux-<suffix>.tar.gz). uname is universal
# across every Linux distro, unlike Debian-specific `dpkg
# --print-architecture`. Echoes nothing (and returns non-zero) for anything
# we don't prebuild for, so callers fall back to building from source
# instead of trying to download a bundle that doesn't exist.
detect_arch() {
  case "$(uname -m)" in
    x86_64) echo "amd64" ;;
    aarch64) echo "arm64" ;;
    armv7l | armv6l) echo "armv7" ;;
    *) return 1 ;;
  esac
}

ensure_go() {
  if command -v go &>/dev/null && go version | grep -q "go1\."; then
    return
  fi
  echo "Installing Go $GO_VERSION..."
  # go.dev's own release archive names don't match detect_arch's suffixes
  # (its 32-bit ARM build in particular is published as "armv6l", covering
  # both v6 and v7 hardware in one build).
  local goarch
  case "$(detect_arch)" in
    amd64) goarch=amd64 ;;
    arm64) goarch=arm64 ;;
    armv7) goarch=armv6l ;;
    *) echo "No Go build available for this architecture ($(uname -m))." >&2; exit 1 ;;
  esac
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${goarch}.tar.gz" -o /tmp/go.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tar.gz
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
  rm -f /tmp/go.tar.gz
}

install_packages() {
  case "$PKG_MGR" in
    apt)
      apt-get update -y
      apt-get install -y --no-install-recommends git curl ca-certificates iptables sudo
      ;;
    dnf)
      dnf install -y git curl ca-certificates iptables sudo
      ;;
    pacman)
      pacman -Sy --noconfirm git curl ca-certificates iptables sudo
      ;;
  esac
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

# download_release_bundle fetches the single per-architecture release
# archive (regionhop-linux-<arch>.tar.gz, containing both the psi-panel and
# ConsoleClient binaries) for the given tag and installs both into place,
# root:root 0755. Returns non-zero, leaving both destinations untouched, if
# this architecture has no prebuilt bundle or the download/extract fails,
# so callers can fall back to a source build of whichever piece they need.
download_release_bundle() {
  local tag="$1" arch tmp
  arch=$(detect_arch) || return 1
  tmp=$(mktemp -d)
  local url="https://github.com/${REPO}/releases/download/${tag}/regionhop-linux-${arch}.tar.gz"
  if ! curl -fsSL "$url" -o "$tmp/bundle.tar.gz" || ! tar -xzf "$tmp/bundle.tar.gz" -C "$tmp"; then
    rm -rf "$tmp"
    return 1
  fi
  if [[ ! -f "$tmp/psi-panel" || ! -f "$tmp/ConsoleClient" ]]; then
    rm -rf "$tmp"
    return 1
  fi
  install -m 0755 -o root -g root "$tmp/psi-panel" "$PREFIX/panel/psi-panel"
  install -m 0755 -o root -g root "$tmp/ConsoleClient" "$PREFIX/core/ConsoleClient"
  # core-version.txt (the psiphon-tunnel-core commit it was built from,
  # shown in the panel footer) is optional -- older release bundles don't
  # have it, and its absence shouldn't fail the whole install.
  [[ -f "$tmp/core-version.txt" ]] && install -m 0644 -o root -g root "$tmp/core-version.txt" "$PREFIX/core/VERSION"
  rm -rf "$tmp"
}

# install_release installs both the panel and the core, together, from the
# single prebuilt bundle for this regionhop release when one exists for the
# server's architecture -- falling back to building each from source (and
# installing Go, only in that case) when it doesn't.
install_release() {
  local tag
  tag="v$(repo_version)"
  echo "Installing regionhop core + panel ($tag)..."
  if download_release_bundle "$tag"; then
    echo "Core + panel installed from the prebuilt release bundle."
  else
    echo "No prebuilt bundle for this architecture/release, building core + panel from source instead..."
    ensure_go
    local build_dir=/tmp/psiphon-build
    rm -rf "$build_dir"
    git clone --depth 1 https://github.com/psiphon-labs/psiphon-tunnel-core.git "$build_dir"
    (cd "$build_dir/ConsoleClient" && go build -o "$PREFIX/core/ConsoleClient" .)
    chown root:root "$PREFIX/core/ConsoleClient"
    chmod 0755 "$PREFIX/core/ConsoleClient"
    git -C "$build_dir" rev-parse --short HEAD > "$PREFIX/core/VERSION"
    chown root:root "$PREFIX/core/VERSION"
    chmod 0644 "$PREFIX/core/VERSION"
    (cd "$SRC_DIR/panel" && go mod tidy && go build -ldflags "-X main.CurrentVersion=$(repo_version)" -o "$PREFIX/panel/psi-panel" .)
    chown root:root "$PREFIX/panel/psi-panel"
    chmod 0755 "$PREFIX/panel/psi-panel"
  fi
  echo "$(repo_version)" > "$VERSION_FILE"
  echo
  echo "NOTE: edit propagation/sponsor IDs via menu option 'Set Psiphon IDs' before adding locations."
}

install_units() {
  cp "$SRC_DIR/systemd/psi-tunnel@.service" /etc/systemd/system/
  cp "$SRC_DIR/systemd/psi-panel.service" /etc/systemd/system/
  systemctl daemon-reload
  remove_healthcheck
}

# The traffic liveness probe (healthcheck.sh + psi-healthcheck.service/timer)
# shipped in v1.8.0-v1.8.4 and was removed by user request (unwanted CPU
# use, feature not needed). This tears it down on any server that installed
# one of those versions -- called from install_units so a plain `psictl
# update` actually stops it, not just skips installing it on new servers.
# Safe to call unconditionally: every step is a no-op if nothing is present.
remove_healthcheck() {
  systemctl disable --now psi-healthcheck.timer 2>/dev/null || true
  systemctl disable --now psi-healthcheck.service 2>/dev/null || true
  rm -f /etc/systemd/system/psi-healthcheck.service /etc/systemd/system/psi-healthcheck.timer
  systemctl daemon-reload
  rm -f "$PREFIX/healthcheck.sh" "$PREFIX/data/health.json"
}

setup_firewall() {
  # Defense-in-depth: SOCKS ports are already bound to 127.0.0.1 by config,
  # this just makes sure nothing external can ever reach that port range
  # even if a future config change forgot to bind to loopback. Uses
  # iptables directly (installed by install_packages on every distro this
  # script supports) instead of a distro-specific firewall manager
  # (ufw/firewalld/etc.), so one code path covers all of them. A raw
  # iptables rule doesn't otherwise survive a reboot, so a small systemd
  # unit reapplies it at boot — idempotent via the -C check first, so it's
  # safe to run on every boot rather than only the first.
  if ! command -v iptables &>/dev/null; then
    echo "WARNING: iptables not found, skipping the SOCKS-port firewall rule (SOCKS proxies are still bound to 127.0.0.1 only, by config)." >&2
    return 0
  fi
  iptables -C INPUT -p tcp --dport 19000:19999 -j DROP 2>/dev/null \
    || iptables -I INPUT -p tcp --dport 19000:19999 -j DROP

  cat > /etc/systemd/system/regionhop-firewall.service <<'EOF'
[Unit]
Description=Reapply regionhop's SOCKS-port firewall rule (not persisted across reboots otherwise)
After=network.target

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'iptables -C INPUT -p tcp --dport 19000:19999 -j DROP 2>/dev/null || iptables -I INPUT -p tcp --dport 19000:19999 -j DROP'
RemainAfterExit=true

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now regionhop-firewall.service

  # Best-effort cleanup of the ufw-based rule from installs before this
  # switched to iptables directly — harmless no-op if ufw isn't present.
  command -v ufw &>/dev/null && ufw delete deny in proto tcp from any to any port 19000:19999 2>/dev/null || true
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
usage() { echo "usage: psictl {list|start|stop|restart|logs} <name> | panel-logs | panel-restart | update | check-update" >&2; }
cmd="${1:-}"
case "$cmd" in
  start|stop|restart|logs)
    if [ -z "${2:-}" ]; then
      echo "psictl $cmd: missing <name>" >&2
      usage
      exit 1
    fi
    ;;
esac
case "$cmd" in
  list) systemctl list-units 'psi-tunnel@*' --no-pager ;;
  start) systemctl enable --now "psi-tunnel@$2" ;;
  stop) systemctl disable --now "psi-tunnel@$2" ;;
  restart) systemctl restart "psi-tunnel@$2" ;;
  logs) journalctl -u "psi-tunnel@$2" -n 200 --no-pager ;;
  panel-logs) journalctl -u psi-panel -n 200 --no-pager ;;
  panel-restart) systemctl restart psi-panel ;;
  update) bash <(curl -Ls https://raw.githubusercontent.com/freeb5d/regionhop/master/install.sh) update ;;
  check-update) bash <(curl -Ls https://raw.githubusercontent.com/freeb5d/regionhop/master/install.sh) check-update ;;
  *) usage; exit 1 ;;
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
    # -f: this checkout is never locally modified, so any conflict here can
    # only be an artifact of a prior upstream history rewrite — always take
    # the freshly fetched ref, the same reasoning as the reset --hard above.
    git -C "$CHECKOUT_DIR" checkout -qf "FETCH_HEAD" 2>/dev/null \
      || { git -C "$CHECKOUT_DIR" fetch --depth 1 origin master \
           && git -C "$CHECKOUT_DIR" checkout -qf "FETCH_HEAD"; }
  else
    rm -rf "$CHECKOUT_DIR"
    git clone --depth 1 --branch "$latest" "$REPO_URL" "$CHECKOUT_DIR" 2>/dev/null \
      || git clone --depth 1 "$REPO_URL" "$CHECKOUT_DIR"
  fi
  SRC_DIR="$CHECKOUT_DIR"
  ensure_user
  install_release
  install_units
  [[ -e /usr/local/bin/psictl ]] && install_psictl
  systemctl restart psi-panel 2>/dev/null || true
  echo "Updated core + panel to $(repo_version)."
  echo "Note: already-running tunnel processes keep using the old core binary until you restart them (Restart from the panel, or 'psictl restart <name>') — the new one only takes effect on their next start."
}

uninstall_all() {
  read -rp "This removes the panel, all tunnels, and their data. Type YES to continue: " c
  [[ "$c" == "YES" ]] || { echo "Aborted."; return; }
  systemctl disable --now psi-panel.service 2>/dev/null || true
  systemctl disable --now psi-healthcheck.timer 2>/dev/null || true
  systemctl disable --now regionhop-firewall.service 2>/dev/null || true
  for u in $(systemctl list-units --all 'psi-tunnel@*' --no-legend | awk '{print $1}'); do
    systemctl disable --now "$u" 2>/dev/null || true
  done
  rm -f /etc/systemd/system/psi-panel.service /etc/systemd/system/psi-tunnel@.service \
        /etc/systemd/system/psi-healthcheck.service /etc/systemd/system/psi-healthcheck.timer \
        /etc/systemd/system/regionhop-firewall.service
  systemctl daemon-reload
  command -v iptables &>/dev/null && iptables -D INPUT -p tcp --dport 19000:19999 -j DROP 2>/dev/null || true
  rm -f /etc/sudoers.d/regionhop-psipanel /etc/polkit-1/rules.d/49-regionhop.rules
  rm -rf "$PREFIX" "$ADMIN_DIR" /usr/local/bin/psictl
  userdel "$SERVICE_USER" 2>/dev/null || true
  echo "Removed."
}

menu() {
  PS3=$'\nSelect an option: '
  options=(
    "Full setup (packages, core+panel, firewall, units)"
    "Reinstall/rebuild core + panel"
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
        install_packages; ensure_user; ensure_dirs
        install_release; install_units; setup_firewall
        first_time_panel_setup; start_panel; install_psictl
        ;;
      2) ensure_user; install_release; systemctl restart psi-panel 2>/dev/null || true ;;
      3) set_psiphon_ids ;;
      4) set_panel_password; systemctl restart psi-panel 2>/dev/null || true ;;
      5) install_units; start_panel ;;
      6) install_psictl ;;
      7) status_all ;;
      8) check_update || true ;;
      9) self_update ;;
      10) uninstall_all ;;
      11) break ;;
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
