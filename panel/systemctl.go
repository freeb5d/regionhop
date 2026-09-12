package main

import (
	"context"
	"encoding/json"
	"fmt"
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
		Region string `json:"region"`
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
// running at all. Both are read from a single backward scan of recent
// journal output: psiphon-tunnel-core emits
// {"noticeType":"Tunnels","data":{"count":N}} whenever its connected-tunnel
// count changes, and {"noticeType":"ConnectedServerRegion","data":{"region":"XX"}}
// once it lands on a server.
func tunnelStatusInfo(name string) tunnelInfo {
	svcState := tunnelStatus(name)
	if svcState != "active" {
		return tunnelInfo{State: svcState}
	}

	out, err := tunnelLogs(name, 200)
	if err != nil {
		return tunnelInfo{State: "connecting"}
	}

	state := ""
	region := ""
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0 && (state == "" || region == ""); i-- {
		idx := strings.IndexByte(lines[i], '{')
		if idx < 0 {
			continue
		}
		var n psiphonNotice
		if err := json.Unmarshal([]byte(lines[i][idx:]), &n); err != nil {
			continue
		}
		switch n.NoticeType {
		case "Tunnels":
			if state == "" {
				if n.Data.Count > 0 {
					state = "active"
				} else {
					state = "connecting"
				}
			}
		case "ConnectedServerRegion":
			if region == "" {
				region = n.Data.Region
			}
		}
	}
	if state == "" {
		state = "connecting"
	}
	return tunnelInfo{State: state, Region: region}
}

// countryFlag turns a 2-letter ISO country code into its flag emoji by
// mapping each letter to a Unicode regional indicator symbol.
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
