package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const unitPrefix = "psi-tunnel@"

func unitName(tunnelName string) string {
	return unitPrefix + tunnelName + ".service"
}

func runSystemctl(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runSystemctlPrivileged is for the mutating calls (enable/disable/restart)
// that require root: the panel runs as the unprivileged psipanel user, so
// these go through `sudo -n`, authorized by a narrowly-scoped NOPASSWD
// sudoers rule (see install.sh's setup_sudo_control()) limited to exactly
// the psi-tunnel@*.service and psi-panel.service units. `-n` makes sudo
// fail fast with a clear error instead of hanging if that rule is missing.
func runSystemctlPrivileged(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fullArgs := append([]string{"-n", "systemctl"}, args...)
	cmd := exec.CommandContext(ctx, "sudo", fullArgs...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Deliberately outside /opt/psi-panel: that whole tree is owned by the
// unprivileged psipanel user this process runs as, and directory-write
// permission (not file permission) is what governs delete/replace — a
// root-owned, mode-0700 script sitting in a psipanel-owned directory could
// still be swapped out by psipanel for arbitrary content that sudo would
// then run as root unchanged. /opt/regionhop-admin is root-owned and
// mode 0700, so psipanel can't even list or enter it, let alone touch this
// file — see install.sh's install_self_update_script for the write side.
const selfUpdateScript = "/opt/regionhop-admin/self-update.sh"

// triggerSelfUpdate kicks off the update in the background and returns
// immediately: the update flow restarts psi-panel itself partway through,
// which would otherwise cut off the HTTP response mid-request. Output goes
// to a log file (not the panel's own stdout/journal, which is about to
// vanish along with this process) so a failure is diagnosable afterward via
// `psictl panel-logs` or by reading the file directly.
func triggerSelfUpdate() error {
	logPath := "/opt/psi-panel/panel/update.log"
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command("sudo", "-n", selfUpdateScript)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return err
	}
	go func() {
		cmd.Wait()
		logFile.Close()
	}()
	return nil
}

// wrapErr folds systemctl's own stderr/stdout into the returned error so
// callers don't just see the useless "exit status 1" from os/exec.
func wrapErr(out string, err error) error {
	if err == nil {
		return nil
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return err
	}
	return fmt.Errorf("%s", out)
}

func startTunnel(name string) error {
	out, err := runSystemctlPrivileged("enable", "--now", unitName(name))
	return wrapErr(out, err)
}

func stopTunnel(name string) error {
	out, err := runSystemctlPrivileged("disable", "--now", unitName(name))
	return wrapErr(out, err)
}

func restartTunnel(name string) error {
	out, err := runSystemctlPrivileged("restart", unitName(name))
	return wrapErr(out, err)
}

func tunnelStatus(name string) string {
	out, err := runSystemctl("is-active", unitName(name))
	out = strings.TrimSpace(out)
	if err != nil && out == "" {
		return "unknown"
	}
	return out
}

func tunnelLogs(name string, lines int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "journalctl", "-u", unitName(name), "-n", fmt.Sprintf("%d", lines), "--no-pager")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

type psiphonNotice struct {
	NoticeType string `json:"noticeType"`
	Data       struct {
		Count  int    `json:"count"`
		Region string `json:"serverRegion"`
	} `json:"data"`
}

// tunnelInfo is what a single journal scan determines about a location:
// its real connection state (not just whether systemd is running it) and,
// once connected, which Psiphon server region it landed on.
type tunnelInfo struct {
	State  string // "active" (tunnel established), "connecting", or systemd's own state (inactive/failed/...)
	Region string // 2-letter code from ConnectedServerRegion, once known
}

// tunnelStatusInfo reports what the process is actually doing, not just
// whether systemd is running it, and which server region it's connected to.
// "connecting" while ConsoleClient is still establishing a tunnel,
// "active" only once it's actually reported a connected tunnel, and
// systemd's own state (inactive/failed/activating/...) when the unit isn't
// running at all.
//
// Connection state comes from a backward scan of a small recent journal
// window: psiphon-tunnel-core emits {"noticeType":"Tunnels","data":
// {"count":N}} every time its connected-tunnel count changes, so the most
// recent one is always fresh. The exit region comes from a separate,
// full-history `journalctl --grep` lookup instead of that same small
// window: {"noticeType":"ConnectedServerRegion","data":
// {"serverRegion":"XX"}} is emitted exactly once right after connecting,
// so on a long-running tunnel it can scroll out of a bounded recent-lines
// window long before the tunnel itself disconnects.
func tunnelStatusInfo(name string) tunnelInfo {
	svcState := tunnelStatus(name)
	if svcState != "active" {
		return tunnelInfo{State: svcState}
	}

	out, err := tunnelLogs(name, 100)
	if err != nil {
		return tunnelInfo{State: "connecting"}
	}

	state := ""
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0 && state == ""; i-- {
		n, ok := parseNoticeLine(lines[i])
		if !ok || n.NoticeType != "Tunnels" {
			continue
		}
		if n.Data.Count > 0 {
			state = "active"
		} else {
			state = "connecting"
		}
	}
	if state == "" {
		state = "connecting"
	}

	return tunnelInfo{State: state, Region: connectedServerRegion(name)}
}

func parseNoticeLine(line string) (psiphonNotice, bool) {
	idx := strings.IndexByte(line, '{')
	if idx < 0 {
		return psiphonNotice{}, false
	}
	var n psiphonNotice
	if err := json.Unmarshal([]byte(line[idx:]), &n); err != nil {
		return psiphonNotice{}, false
	}
	return n, true
}

// connectedServerRegion searches the whole journal for this unit for its
// (single, one-time-per-connection) ConnectedServerRegion notice, returning
// the most recent one. Uses journalctl's own indexed --grep instead of
// pulling N lines client-side, so it stays cheap and correct no matter how
// long the tunnel has been running or how noisy its log is.
func connectedServerRegion(name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "journalctl",
		"-u", unitName(name),
		"--grep", `"noticeType":"ConnectedServerRegion"`,
		"-n", "5", "--no-pager")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	lines := strings.Split(string(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		n, ok := parseNoticeLine(lines[i])
		if ok && n.NoticeType == "ConnectedServerRegion" && n.Data.Region != "" {
			return n.Data.Region
		}
	}
	return ""
}

// countryFlag turns a 2-letter ISO country code into its flag emoji by
// mapping each letter to a Unicode regional indicator symbol. Not used for
// display any more (Windows' fonts don't render flag emoji at all — they
// fall back to showing the two raw regional-indicator letters, which look
// just like plain text), kept for anywhere the emoji form is still useful
// (window/tab titles, log lines, etc.).
func countryFlag(code string) string {
	if len(code) != 2 {
		return ""
	}
	a, b := code[0], code[1]
	if a < 'A' || a > 'Z' || b < 'A' || b > 'Z' {
		return ""
	}
	return string(rune(0x1F1E6+int(a-'A'))) + string(rune(0x1F1E6+int(b-'A')))
}

// flagImageURL returns a Twemoji SVG flag image URL for a 2-letter ISO
// country code — rendered as a real <img>, this shows correctly on every
// OS/browser (including Windows, which has no flag glyphs in its emoji
// font at all, unlike the Unicode emoji character itself).
func flagImageURL(code string) string {
	if len(code) != 2 {
		return ""
	}
	a, b := code[0], code[1]
	if a < 'A' || a > 'Z' || b < 'A' || b > 'Z' {
		return ""
	}
	return fmt.Sprintf("https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/%x-%x.svg",
		0x1F1E6+int(a-'A'), 0x1F1E6+int(b-'A'))
}
