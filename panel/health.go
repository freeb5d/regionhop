package main

import (
	"encoding/json"
	"os"
	"strconv"
	"time"
)

// healthEntry is one location's most recent result from healthcheck.sh
// (run every 5 minutes by psi-healthcheck.timer): an actual probe request
// sent through that location's own SOCKS proxy, independent of whatever
// real traffic a client may or may not have sent it. This is what lets the
// dashboard distinguish "tunnel connected but nothing has used it" from
// "tunnel connected but something's actually wrong" — TotalBytesTransferred
// in the tunnel's own journal can't tell those apart on its own.
type healthEntry struct {
	Result    string `json:"result"` // "ok" or "fail"
	Time      string `json:"time"`   // RFC3339 (UTC)
	ElapsedMs int    `json:"elapsedMs"`
}

const healthPath = dataDir + "/health.json"

// readHealth returns nil (not an error) when the file doesn't exist yet —
// e.g. right after install, before the timer's first run — so callers can
// just treat a missing entry as "no probe result yet" rather than failing.
func readHealth() map[string]healthEntry {
	b, err := os.ReadFile(healthPath)
	if err != nil {
		return nil
	}
	var m map[string]healthEntry
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// healthAge renders how long ago a probe ran, for display next to its
// result — "3m ago" reads a lot more usefully on a dashboard than a raw
// timestamp, and confirms at a glance that the probe is actually current.
func healthAge(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < 90*time.Second:
		return "just now"
	case d < 90*time.Minute:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	default:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	}
}
