package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Auto restart: restart every running location on a user-chosen schedule, so
// long-lived connections don't go stale over days of uptime. The schedule
// lives in a small file and is checked once a minute by a goroutine inside
// the panel -- no systemd timer or cron entry to install or clean up.
// It restarts through restartTunnel, i.e. the same narrowly-scoped sudo rule
// the Restart button already uses.
var autoRestartPath = "/opt/psi-panel/data/auto-restart.json"

const (
	autoRestartTick    = time.Minute
	autoRestartStagger = 3 * time.Second // pause after each restart so they don't all reconnect at once
	autoRestartMaxDays = 365
)

type autoRestartSettings struct {
	Enabled bool   `json:"enabled"`
	Every   int    `json:"every"`
	Unit    string `json:"unit"`               // "hours", "days" or "weeks"
	LastRun int64  `json:"last_run,omitempty"` // unix seconds of the last run (or of when the schedule was armed)
	// Only limits the schedule to these locations. Empty means every
	// location, which is also what a schedule saved before this field
	// existed gets.
	Only []string `json:"only,omitempty"`

	// Quality gates (see autorestart_gates.go): after a restart the location
	// must pass every ticked check, or it is restarted again, up to MaxTries.
	MaxLatencyMs int  `json:"max_latency_ms,omitempty"` // real delay must be at most this; 0 = not checked
	NewIP        bool `json:"new_ip,omitempty"`         // exit IP must differ from before the restart
	Gemini       bool `json:"gemini,omitempty"`         // Gemini must not be blocked on the exit IP
	MaxTries     int  `json:"max_tries,omitempty"`      // 0 = defaultMaxTries
}

// includes reports whether the schedule covers the named location.
func (s autoRestartSettings) includes(name string) bool {
	if len(s.Only) == 0 {
		return true
	}
	for _, n := range s.Only {
		if n == name {
			return true
		}
	}
	return false
}

func defaultAutoRestart() autoRestartSettings {
	return autoRestartSettings{Every: 1, Unit: "days"}
}

func (s autoRestartSettings) interval() time.Duration {
	switch s.Unit {
	case "hours":
		return time.Duration(s.Every) * time.Hour
	case "days":
		return time.Duration(s.Every) * 24 * time.Hour
	case "weeks":
		return time.Duration(s.Every) * 7 * 24 * time.Hour
	}
	return 0
}

func validateAutoRestart(s autoRestartSettings) error {
	switch s.Unit {
	case "hours", "days", "weeks":
	default:
		return fmt.Errorf("choose hours, days or weeks")
	}
	if s.Every < 1 {
		return fmt.Errorf("the interval must be at least 1")
	}
	if s.interval() > autoRestartMaxDays*24*time.Hour {
		return fmt.Errorf("the interval can be at most %d days", autoRestartMaxDays)
	}
	return validateGates(s)
}

func loadAutoRestart() autoRestartSettings {
	s := defaultAutoRestart()
	b, err := os.ReadFile(autoRestartPath)
	if err != nil {
		return s
	}
	var loaded autoRestartSettings
	if json.Unmarshal(b, &loaded) != nil || validateAutoRestart(loaded) != nil {
		return s
	}
	return loaded
}

func saveAutoRestart(s autoRestartSettings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := autoRestartPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, autoRestartPath)
}

// autoRestartStep is the whole scheduling decision, kept free of I/O so it
// can be tested: given the saved settings and the current time, it returns
// the settings to save and whether a restart wave should run now.
func autoRestartStep(s autoRestartSettings, now time.Time) (autoRestartSettings, bool) {
	if !s.Enabled || s.interval() == 0 {
		return s, false
	}
	if s.LastRun == 0 {
		// First sight of an enabled schedule: start the countdown instead of
		// restarting immediately.
		s.LastRun = now.Unix()
		return s, false
	}
	if now.Unix() >= s.LastRun+int64(s.interval()/time.Second) {
		s.LastRun = now.Unix()
		return s, true
	}
	return s, false
}

func (a *app) startAutoRestart() {
	go func() {
		t := time.NewTicker(autoRestartTick)
		defer t.Stop()
		for now := range t.C {
			a.autoRestartTick(now)
		}
	}()
}

func (a *app) autoRestartTick(now time.Time) {
	// Under a.mu so this can't overwrite a schedule the admin is saving at
	// the same moment.
	a.mu.Lock()
	s := loadAutoRestart()
	next, run := autoRestartStep(s, now)
	if next.LastRun != s.LastRun { // the step only ever changes LastRun
		if err := saveAutoRestart(next); err != nil {
			a.mu.Unlock()
			log.Printf("auto-restart: cannot save schedule, skipping this round: %v", err)
			return
		}
	}
	a.mu.Unlock()
	if run {
		a.restartAllRunning(next)
	}
}

// restartAllRunning restarts every location the schedule covers that is
// currently running, one at a time. Stopped locations stay stopped: someone stopped them on purpose.
// a.mu is held only around each single restart (not the whole wave), so
// adding or removing a location never waits minutes behind this -- and
// because the registry is re-read under the lock each time, a location
// removed mid-wave is skipped instead of being started again.
func (a *app) restartAllRunning(s autoRestartSettings) {
	list, err := loadRegistry(registryPath)
	if err != nil {
		log.Printf("auto-restart: cannot read the location list: %v", err)
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	restarted, covered := 0, 0
	for _, t := range list {
		if !s.includes(t.Name) {
			continue
		}
		covered++
		n, verdict := restartUntilGood(s, a.realGateDeps(t.Name, fmt.Sprintf("127.0.0.1:%d", t.SocksPort), s))
		logGateOutcome(t.Name, n, verdict)
		if n > 0 {
			restarted++
			time.Sleep(autoRestartStagger)
		}
	}
	log.Printf("auto-restart: restarted %d of %d covered locations", restarted, covered)
}

func (a *app) restartIfRunning(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	current, err := loadRegistry(registryPath)
	if err != nil {
		return false
	}
	still := false
	for _, t := range current {
		if t.Name == name {
			still = true
			break
		}
	}
	if !still {
		return false
	}
	if st := tunnelStatus(name); st != "active" && st != "activating" {
		return false
	}
	if err := restartTunnel(name); err != nil {
		log.Printf("auto-restart: %s: %v", name, err)
		return false
	}
	return true
}

func formatScheduleTime(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format("2006-01-02 15:04 UTC")
}

type autoRestartLocation struct {
	Name    string
	Region  string
	Checked bool
}

// autoRestartLocations lists the current locations for the picker, with the
// ones the schedule covers ticked.
func autoRestartLocations(s autoRestartSettings) []autoRestartLocation {
	list, err := loadRegistry(registryPath)
	if err != nil {
		return nil
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	out := make([]autoRestartLocation, 0, len(list))
	for _, t := range list {
		out = append(out, autoRestartLocation{
			Name:    t.Name,
			Region:  regionLabel(t.Region),
			Checked: len(s.Only) > 0 && s.includes(t.Name),
		})
	}
	return out
}

// autoRestartTemplateData is what the settings page shows about the schedule.
func autoRestartTemplateData() map[string]any {
	s := loadAutoRestart()
	next := ""
	if s.Enabled && s.LastRun != 0 {
		next = formatScheduleTime(s.LastRun + int64(s.interval()/time.Second))
	}
	return map[string]any{
		"AutoRestart":          s,
		"ArSelected":           len(s.Only) > 0,
		"AutoRestartLocations": autoRestartLocations(s),
		"AutoRestartLast":      formatScheduleTime(s.LastRun),
		"AutoRestartNext":      next,
	}
}

func (a *app) handleAutoRestart(w http.ResponseWriter, r *http.Request) {
	setLangCookie(w, r)
	if r.Method != http.MethodPost {
		http.Redirect(w, r, urlFor("/settings"), http.StatusSeeOther)
		return
	}
	limitBody(w, r, 64<<10)
	a.mu.Lock()
	defer a.mu.Unlock()

	old := loadAutoRestart()
	every, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("ar_every")))
	s := autoRestartSettings{
		Enabled: r.FormValue("ar_enabled") == "on",
		Every:   every,
		Unit:    r.FormValue("ar_unit"),
	}
	if r.FormValue("ar_latency") == "on" {
		s.MaxLatencyMs, _ = strconv.Atoi(strings.TrimSpace(r.FormValue("ar_max_latency")))
		if s.MaxLatencyMs == 0 {
			s.MaxLatencyMs = -1 // ticked but empty/invalid: let validation explain
		}
	}
	s.NewIP = r.FormValue("ar_new_ip") == "on"
	s.Gemini = r.FormValue("ar_gemini") == "on"
	s.MaxTries, _ = strconv.Atoi(strings.TrimSpace(r.FormValue("ar_tries")))
	scopeSelected := r.FormValue("ar_scope") == "selected"
	if scopeSelected {
		existing := map[string]bool{}
		if list, lerr := loadRegistry(registryPath); lerr == nil {
			for _, t := range list {
				existing[t.Name] = true
			}
		}
		seen := map[string]bool{}
		for _, n := range r.PostForm["ar_name"] {
			if validName(n) && existing[n] && !seen[n] {
				seen[n] = true
				s.Only = append(s.Only, n)
			}
		}
		sort.Strings(s.Only)
	}
	err := validateAutoRestart(s)
	if err == nil && scopeSelected && len(s.Only) == 0 {
		err = fmt.Errorf("choose at least one location, or switch to all locations")
	}
	if err != nil && !s.Enabled {
		// Turning it off must always work, even if the form was left in a
		// state that wouldn't be a valid schedule.
		s.Every, s.Unit, s.Only, err = old.Every, old.Unit, old.Only, nil
		s.MaxLatencyMs, s.NewIP, s.Gemini, s.MaxTries = old.MaxLatencyMs, old.NewIP, old.Gemini, old.MaxTries
		scopeSelected = len(old.Only) > 0
	}
	if err == nil {
		// Changing what the schedule means restarts the countdown from now;
		// re-saving the same schedule leaves it running.
		if s.Enabled && (!old.Enabled || old.Every != s.Every || old.Unit != s.Unit || old.LastRun == 0) {
			s.LastRun = time.Now().Unix()
		} else {
			s.LastRun = old.LastRun
		}
		err = saveAutoRestart(s)
	}

	data := credsTemplateData(a.creds, false, "")
	if err != nil {
		data["AutoRestart"] = s // keep what was typed
		data["ArSelected"] = scopeSelected
		data["AutoRestartLocations"] = autoRestartLocations(s)
		data["AutoRestartError"] = err.Error()
	} else {
		for k, v := range autoRestartTemplateData() {
			data[k] = v
		}
		data["AutoRestartSaved"] = true
	}
	tmpl.ExecuteTemplate(w, "settings.html", withLang(r, data))
}
