package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CurrentVersion is set at build time via -ldflags "-X main.CurrentVersion=x.y.z"
// by install.sh; it falls back to "dev" for local/manual builds.
var CurrentVersion = "dev"

const releasesAPI = "https://api.github.com/repos/freeb5d/regionhop/releases/latest"

type updateStatus struct {
	mu        sync.Mutex
	latest    string
	checkedAt time.Time
	checkErr  string
}

var updates = &updateStatus{}

func (u *updateStatus) snapshot() (latest string, available bool, checkedAt time.Time, errMsg string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	avail := CurrentVersion != "dev" && versionLess(CurrentVersion, u.latest)
	return u.latest, avail, u.checkedAt, u.checkErr
}

// versionLess reports whether a < b for dotted numeric version strings
// (e.g. "1.2.0" < "1.10.0"). Non-numeric or malformed parts sort as 0.
func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var an, bn int
		if i < len(as) {
			an, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bn, _ = strconv.Atoi(bs[i])
		}
		if an != bn {
			return an < bn
		}
	}
	return false
}

// checkForUpdateNow synchronously refreshes the cached "latest release"
// value; used both by the periodic ticker and the panel's "Check for
// updates" button.
func checkForUpdateNow() {
	latest, err := fetchLatestVersion()
	updates.mu.Lock()
	updates.checkedAt = time.Now()
	if err != nil {
		updates.checkErr = err.Error()
	} else {
		updates.latest = latest
		updates.checkErr = ""
	}
	updates.mu.Unlock()
}

// startUpdateChecker polls GitHub's "latest release" API on an interval and
// caches the result; the dashboard reads the cache so page loads never block
// on a network call to GitHub.
func startUpdateChecker(interval time.Duration) {
	checkForUpdateNow()
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			checkForUpdateNow()
		}
	}()
}

type ghRelease struct {
	TagName string `json:"tag_name"`
}

func fetchLatestVersion() (string, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest(http.MethodGet, releasesAPI, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "regionhop-panel")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	return strings.TrimPrefix(rel.TagName, "v"), nil
}
