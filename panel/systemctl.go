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

// invalidateNoticeCache drops any cached ConnectedServerRegion lookup for a
// location — called right after start/stop/restart so the dashboard's
// immediate post-action poll (see dashboard.html's __regionhopPoll call)
// doesn't keep showing a pre-action exit region that's still within
// noticeCacheTTL. (Status itself no longer goes through this cache at all —
// see latestTunnelsState — so there's nothing to invalidate for it.)
func invalidateNoticeCache(name string) {
	noticeCacheMu.Lock()
	delete(noticeCache, name+"|ConnectedServerRegion")
	noticeCacheMu.Unlock()
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
	Timestamp  string `json:"timestamp"` // RFC3339; compared as a string to pick the truly most recent match, see latestNotice
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
func tunnelStatusInfo(name string) tunnelInfo {
	svcState := tunnelStatus(name)
	if svcState != "active" {
		return tunnelInfo{State: svcState}
	}

	return tunnelInfo{State: latestTunnelsState(name), Region: connectedServerRegion(name)}
}

// latestTunnelsState reads a small bounded recent-lines window (the same
// call the Logs page already uses) instead of a full-history journalctl
// --grep, and looks for the most recent of two notice types:
// {"noticeType":"Tunnels","data":{"count":N}} (emitted once, only when the
// connected-tunnel count *changes*) or {"noticeType":"TotalBytesTransferred"}
// (emitted repeatedly, every ~5 minutes, but ONLY from inside
// psiphon-tunnel-core's connected-tunnel operate loop -- so its mere
// presence at all proves the tunnel was active as of that timestamp,
// regardless of the byte count it reports).
//
// This used to be a full-history search for the Tunnels notice alone,
// which is fine in principle (that notice really is only emitted once) but
// broke in practice: confirmed in the field, on a busy server with several
// tunnels and other software logging heavily, the journal rotates old
// entries out over time, and once that one-time notice ages out there's no
// way to recover it -- a tunnel that had been solidly connected for 40+
// minutes with zero interruptions (confirmed via TotalBytesTransferred
// climbing every 5 minutes) showed as permanently stuck on "connecting"
// because the *original* connect-time notice was gone. Since
// TotalBytesTransferred repeats for as long as the tunnel stays connected,
// a bounded recent window will always contain fresh evidence for any
// tunnel that's actually still up, with no full-journal scan needed at all.
func latestTunnelsState(name string) string {
	out, err := tunnelLogs(name, 200)
	if err != nil {
		return "connecting"
	}
	var latest psiphonNotice
	found := false
	for _, line := range strings.Split(out, "\n") {
		n, ok := parseNoticeLine(line)
		if !ok || (n.NoticeType != "Tunnels" && n.NoticeType != "TotalBytesTransferred") {
			continue
		}
		if !found || n.Timestamp > latest.Timestamp {
			latest = n
			found = true
		}
	}
	if !found {
		return "connecting"
	}
	if latest.NoticeType == "TotalBytesTransferred" {
		return "active"
	}
	if latest.Data.Count > 0 {
		return "active"
	}
	return "connecting"
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

// noticeCacheTTL bounds how often latestNotice actually spawns journalctl
// per (tunnel, notice type) pair. Without this, a full-history --grep scan
// ran on every dashboard poll (every 3s) for every tunnel, twice over (once
// for status, once for exit region) -- cheap on a fresh journal, but its
// cost grows with journal size, and confirmed in the field as a real,
// sustained double-digit-%CPU journalctl process on a server with several
// long-running tunnels. Caching means a status/region change can lag by up
// to this long before showing up, which is an acceptable trade for not
// re-scanning the whole journal several times a second.
const noticeCacheTTL = 15 * time.Second

type noticeCacheEntry struct {
	notice    psiphonNotice
	found     bool
	fetchedAt time.Time
}

var (
	noticeCacheMu sync.Mutex
	noticeCache   = map[string]noticeCacheEntry{}
)

// latestNotice returns the most recent notice of the given type for a
// location, searched across the unit's full journal history (not a bounded
// recent window — see tunnelStatusInfo's comment for why that matters for
// once-only notices like Tunnels and ConnectedServerRegion), through a
// short-lived cache (see noticeCacheTTL) so repeated callers within the TTL
// share one journalctl invocation instead of each triggering their own.
//
// Deliberately does NOT trust journalctl's own output ordering to find
// "most recent" — combined with --grep, journalctl has been observed to
// print matches newest-first rather than the oldest-first order a plain
// `journalctl -u <unit>` gives, and relying on that silently picked a STALE
// match instead of the current one (e.g. an old "Tunnels":{"count":0} from
// hours before a tunnel connected, overriding the current "count":1 and
// showing "connecting" forever on a tunnel that was actually fine). Each
// notice carries its own "timestamp" field, so every candidate line is
// parsed and compared by that instead — correct regardless of what order
// journalctl happens to print them in on any given system/version.
func latestNotice(name, noticeType string) (psiphonNotice, bool) {
	key := name + "|" + noticeType

	noticeCacheMu.Lock()
	if e, ok := noticeCache[key]; ok && time.Since(e.fetchedAt) < noticeCacheTTL {
		noticeCacheMu.Unlock()
		return e.notice, e.found
	}
	noticeCacheMu.Unlock()

	notice, found := fetchLatestNotice(name, noticeType)

	noticeCacheMu.Lock()
	noticeCache[key] = noticeCacheEntry{notice: notice, found: found, fetchedAt: time.Now()}
	noticeCacheMu.Unlock()

	return notice, found
}

func fetchLatestNotice(name, noticeType string) (psiphonNotice, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "journalctl",
		"-u", unitName(name),
		"--grep", `"noticeType":"`+noticeType+`"`,
		"-n", "5", "--no-pager")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return psiphonNotice{}, false
	}
	var latest psiphonNotice
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		n, ok := parseNoticeLine(line)
		if !ok || n.NoticeType != noticeType {
			continue
		}
		if !found || n.Timestamp > latest.Timestamp {
			latest = n
			found = true
		}
	}
	return latest, found
}

// connectedServerRegion searches the whole journal for this unit for its
// (single, one-time-per-connection) ConnectedServerRegion notice, returning
// the most recent one. Uses journalctl's own indexed --grep instead of
// pulling N lines client-side, so it stays cheap and correct no matter how
// long the tunnel has been running or how noisy its log is.
func connectedServerRegion(name string) string {
	n, ok := latestNotice(name, "ConnectedServerRegion")
	if !ok {
		return ""
	}
	return n.Data.Region
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
