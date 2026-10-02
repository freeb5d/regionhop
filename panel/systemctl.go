package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
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

// sudoersRefreshScript regenerates /etc/sudoers.d/regionhop-psipanel from
// the current tunnel registry — see install.sh's
// install_sudoers_refresh_script for what it actually does and why:
// listing each location's exact unit name rather than a psi-tunnel@*
// wildcard, since some sudo builds reject any wildcard in a Cmnd_Alias
// outright. Triggered here whenever a location is added or removed, so a
// newly-added location's own control permission exists before startTunnel
// is called for it, and a removed one's is dropped again.
const sudoersRefreshScript = "/opt/regionhop-admin/refresh-sudoers.sh"

func refreshSudoersRule() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sudo", "-n", sudoersRefreshScript)
	out, err := cmd.CombinedOutput()
	return wrapErr(string(out), err)
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
	logPath := "/opt/psi-panel/data/update.log"
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
	invalidateNoticeCache(name)
	return wrapErr(out, err)
}

func stopTunnel(name string) error {
	out, err := runSystemctlPrivileged("disable", "--now", unitName(name))
	invalidateNoticeCache(name)
	return wrapErr(out, err)
}

func restartTunnel(name string) error {
	out, err := runSystemctlPrivileged("restart", unitName(name))
	invalidateNoticeCache(name)
	return wrapErr(out, err)
}

// invalidateNoticeCache forgets everything cached about a location -- called
// right after start/stop/restart so the dashboard's immediate post-action
// poll shows the new state instead of a cached pre-action one.
func invalidateNoticeCache(name string) {
	runCacheMu.Lock()
	delete(runCache, name)
	runCacheMu.Unlock()
	invalidateRows()
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
	Timestamp  string `json:"timestamp"` // RFC3339 with fixed-width fraction: compares correctly as a string
	Data       struct {
		Count  int    `json:"count"`
		Region string `json:"serverRegion"`
	} `json:"data"`
}

// tunnelInfo is what we know about a location right now: its real
// connection state (not just whether systemd is running it) and, once
// connected, which Psiphon server region it landed on.
type tunnelInfo struct {
	State  string // "active" (tunnel established), "connecting", or systemd's own state (inactive/failed/...)
	Region string // 2-letter code from ConnectedServerRegion, once known
}

// unitState is systemd's view of one location's unit.
type unitState struct {
	Active       string // is-active value: active, inactive, failed, activating, ...
	InvocationID string // identifies the unit's current run; changes on every (re)start
}

// loadUnitStates asks systemd about every location in ONE `systemctl show`
// call (instead of one `is-active` plus one `show` per location), and
// shares the answer for a few seconds between concurrent callers -- several
// open dashboards, or a page load racing a poll.
const unitStatesTTL = 3 * time.Second

var (
	unitStatesMu  sync.Mutex
	unitStatesAt  time.Time
	unitStatesKey string
	unitStatesVal map[string]unitState
)

func loadUnitStates(names []string) map[string]unitState {
	key := strings.Join(names, ",")
	unitStatesMu.Lock()
	defer unitStatesMu.Unlock()
	if key == unitStatesKey && time.Since(unitStatesAt) < unitStatesTTL {
		return unitStatesVal
	}
	states := map[string]unitState{}
	if len(names) > 0 {
		args := []string{"show", "-p", "Id", "-p", "ActiveState", "-p", "InvocationID"}
		for _, n := range names {
			args = append(args, unitName(n))
		}
		out, _ := runSystemctl(args...)
		states = parseUnitStates(out)
	}
	unitStatesKey, unitStatesAt, unitStatesVal = key, time.Now(), states
	return states
}

// parseUnitStates reads `systemctl show` output for several units: one
// block of Key=Value lines per unit, separated by blank lines. The result
// is keyed by location name (unit prefix and .service suffix removed).
func parseUnitStates(out string) map[string]unitState {
	states := map[string]unitState{}
	for _, block := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n\n") {
		var id string
		var st unitState
		for _, line := range strings.Split(block, "\n") {
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch k {
			case "Id":
				id = v
			case "ActiveState":
				st.Active = v
			case "InvocationID":
				st.InvocationID = v
			}
		}
		if name, ok := strings.CutSuffix(strings.TrimPrefix(id, unitPrefix), ".service"); ok && id != "" {
			states[name] = st
		}
	}
	return states
}

// tunnelStatusInfo reports what a location is actually doing, not just
// whether systemd is running it: "connecting" while ConsoleClient is still
// establishing a tunnel, "active" once it has reported a connected one, and
// systemd's own state (inactive/failed/activating/...) when it isn't running.
func tunnelStatusInfo(name string, st unitState) tunnelInfo {
	if st.Active == "" {
		return tunnelInfo{State: "unknown"}
	}
	if st.Active != "active" {
		return tunnelInfo{State: st.Active}
	}
	state, region := tunnelRunInfo(name, st.InvocationID)
	return tunnelInfo{State: state, Region: region}
}

// runInfo is the cached journal-derived answer for one run of one location.
type runInfo struct {
	invocationID string
	state        string
	region       string
	fetchedAt    time.Time
}

var (
	runCacheMu sync.Mutex
	runCache   = map[string]runInfo{}
)

// A connected tunnel rarely changes state, so it is re-read lazily; one that
// is still connecting is re-read quickly so it flips to active promptly.
// start/stop/restart clear the entry, and a restart also changes the
// invocation ID, so neither waits for these.
const (
	runInfoTTLActive     = 30 * time.Second
	runInfoTTLConnecting = 5 * time.Second
)

// tunnelRunInfo returns (state, exit region) for the unit's current run.
// One bounded journal read feeds both. The region is a once-per-connection
// notice that can scroll out of that window, so once found it is remembered
// for the rest of the run instead of being searched for again.
func tunnelRunInfo(name, invocationID string) (state, region string) {
	runCacheMu.Lock()
	e, ok := runCache[name]
	sameRun := ok && e.invocationID == invocationID
	if sameRun {
		ttl := runInfoTTLConnecting
		if e.state == "active" {
			ttl = runInfoTTLActive
		}
		if time.Since(e.fetchedAt) < ttl {
			runCacheMu.Unlock()
			return e.state, e.region
		}
	}
	prevRegion := ""
	if sameRun {
		prevRegion = e.region
	}
	runCacheMu.Unlock()

	state, region = "connecting", ""
	if out, err := tunnelLogs(name, 200); err == nil {
		var found bool
		state, region, found = scanNotices(out)
		if !found {
			// Heavy logging right after connect can push the one-time Tunnels
			// notice out of the window; search just this run for evidence.
			if s, ok := currentRunTunnelsState(invocationID); ok {
				state = s
			} else {
				state = "connecting"
			}
		}
	}
	if region == "" {
		region = prevRegion
	}
	if region == "" && state == "active" {
		region = currentRunRegion(invocationID)
	}

	runCacheMu.Lock()
	runCache[name] = runInfo{invocationID: invocationID, state: state, region: region, fetchedAt: time.Now()}
	runCacheMu.Unlock()
	return state, region
}

// journalGrep runs a newest-first, few-match search limited to one run.
func journalGrep(invocationID, pattern string) string {
	if invocationID == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "journalctl",
		"_SYSTEMD_INVOCATION_ID="+invocationID,
		"--grep", pattern, "-r", "-n", "3", "--no-pager").CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}

func currentRunTunnelsState(invocationID string) (string, bool) {
	state, _, found := scanNotices(journalGrep(invocationID, `"noticeType":"(Tunnels|TotalBytesTransferred)"`))
	return state, found
}

func currentRunRegion(invocationID string) string {
	_, region, _ := scanNotices(journalGrep(invocationID, `"noticeType":"ConnectedServerRegion"`))
	return region
}

// scanNotices reads journal text once and returns: the state implied by the
// most recent Tunnels / TotalBytesTransferred notice (found=false if there
// is none), and the region from the most recent ConnectedServerRegion.
//
// TotalBytesTransferred repeats every ~5 minutes but only from inside
// psiphon-tunnel-core's connected-tunnel loop, so its mere presence proves
// the tunnel was up -- unlike the Tunnels notice, which is emitted once per
// change and can age out of the journal. Notices are compared by their own
// timestamp rather than journalctl's output order, which is not reliable
// with --grep. Lines are pre-filtered with a cheap substring test so the
// many irrelevant log lines never reach the JSON decoder.
func scanNotices(out string) (state, region string, found bool) {
	var latest psiphonNotice
	var latestRegion psiphonNotice
	haveRegion := false
	for _, line := range strings.Split(out, "\n") {
		wantState := strings.Contains(line, `"noticeType":"Tunnels"`) || strings.Contains(line, `"noticeType":"TotalBytesTransferred"`)
		wantRegion := !wantState && strings.Contains(line, `"noticeType":"ConnectedServerRegion"`)
		if !wantState && !wantRegion {
			continue
		}
		n, ok := parseNoticeLine(line)
		if !ok {
			continue
		}
		switch {
		case wantRegion && n.NoticeType == "ConnectedServerRegion":
			if !haveRegion || n.Timestamp > latestRegion.Timestamp {
				latestRegion, haveRegion = n, true
			}
		case wantState && (n.NoticeType == "Tunnels" || n.NoticeType == "TotalBytesTransferred"):
			if !found || n.Timestamp > latest.Timestamp {
				latest, found = n, true
			}
		}
	}
	if haveRegion && validRegionCode(latestRegion.Data.Region) {
		region = latestRegion.Data.Region
	}
	if !found {
		return "", region, false
	}
	if latest.NoticeType == "TotalBytesTransferred" || latest.Data.Count > 0 {
		return "active", region, true
	}
	return "connecting", region, true
}

// validRegionCode accepts only a two-letter uppercase code. The region comes
// out of the tunnel process's log, so it is never trusted to be anything
// else before it is shown on the dashboard.
func validRegionCode(s string) bool {
	return len(s) == 2 && s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z'
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
