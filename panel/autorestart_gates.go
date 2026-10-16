package main

import (
	"fmt"
	"log"
	"time"
)

// Quality gates for auto restart: after restarting a location, check that it
// came back good, and if not restart it again -- up to a limit, because a
// gate that can never be met (every exit is slow, Gemini blocks the whole
// region) must not turn into an endless restart loop.

const (
	defaultMaxTries  = 5
	maxMaxTries      = 20
	minGateLatencyMs = 20
	maxGateLatencyMs = 10000
	reconnectTimeout = 120 * time.Second
)

// Tries is how many restarts a location gets before the schedule gives up on it.
func (s autoRestartSettings) Tries() int {
	if s.MaxTries > 0 {
		return s.MaxTries
	}
	return defaultMaxTries
}

func (s autoRestartSettings) hasGates() bool {
	return s.MaxLatencyMs > 0 || s.NewIP || s.Gemini
}

func validateGates(s autoRestartSettings) error {
	if s.MaxLatencyMs != 0 && (s.MaxLatencyMs < minGateLatencyMs || s.MaxLatencyMs > maxGateLatencyMs) {
		return fmt.Errorf("the maximum real delay must be between %d and %d ms", minGateLatencyMs, maxGateLatencyMs)
	}
	if s.MaxTries < 0 || s.MaxTries > maxMaxTries {
		return fmt.Errorf("tries per location must be between 1 and %d", maxMaxTries)
	}
	return nil
}

// gateDeps is everything restartUntilGood needs from the outside world, so
// the retry logic can be tested without a tunnel.
type gateDeps struct {
	restart   func() bool        // false: the location was stopped or removed
	connected func() bool        // waits until the location carries traffic again
	latency   func() probeResult // real-delay probe, best of two
	exitIP    func() string
	gemini    func() geminiResult
}

// restartUntilGood restarts one location and, when gates are configured,
// keeps restarting it until it passes them or the tries run out. It returns
// how many restarts were done and why it stopped (empty = all good).
func restartUntilGood(s autoRestartSettings, d gateDeps) (restarts int, verdict string) {
	before := ""
	if s.NewIP {
		before = d.exitIP() // the address to differ from: read before the first restart
	}
	for try := 1; try <= s.Tries(); try++ {
		if !d.restart() {
			return restarts, "stopped or removed meanwhile"
		}
		restarts++
		if !s.hasGates() {
			return restarts, ""
		}
		if !d.connected() {
			verdict = "did not reconnect in time"
			continue
		}
		ok, why := checkGates(s, before, d)
		if ok {
			return restarts, ""
		}
		verdict = why
	}
	return restarts, fmt.Sprintf("gave up after %d tries: %s", s.Tries(), verdict)
}

// checkGates runs the ticked checks, cheapest first.
func checkGates(s autoRestartSettings, before string, d gateDeps) (bool, string) {
	ip := ""
	if s.MaxLatencyMs > 0 {
		r := d.latency()
		if !r.OK {
			return false, "the real-delay test failed: " + r.Error
		}
		if r.LatencyMs > s.MaxLatencyMs {
			return false, fmt.Sprintf("real delay %d ms is over %d ms", r.LatencyMs, s.MaxLatencyMs)
		}
		ip = r.IP
	}
	// Without a known "before" address there is nothing to compare with, and
	// if the lookup service is down now, restarting again would not help --
	// in both cases the check is skipped rather than looping on it.
	if s.NewIP && before != "" {
		if ip == "" {
			ip = d.exitIP()
		}
		if ip != "" && ip == before {
			return false, "the exit IP did not change (" + ip + ")"
		}
	}
	if s.Gemini {
		if g := d.gemini(); !g.OK {
			return false, "Gemini is blocked: " + g.Reason
		}
	}
	return true, ""
}

func (a *app) realGateDeps(name, socksAddr string, s autoRestartSettings) gateDeps {
	return gateDeps{
		restart:   func() bool { return a.restartIfRunning(name) },
		connected: func() bool { return waitSocksUp(socksAddr, reconnectTimeout) },
		latency:   func() probeResult { return bestRealDelay(socksAddr, s.MaxLatencyMs) },
		exitIP:    func() string { return fetchExitIP(socksAddr) },
		gemini:    func() geminiResult { return checkGemini(socksAddr) },
	}
}

// waitSocksUp polls until a connection through the location's proxy works.
func waitSocksUp(socksAddr string, max time.Duration) bool {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if probeTCP(socksAddr).OK {
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

// bestRealDelay measures the real delay (with the exit IP), and measures once
// more if the first sample failed or was over the limit, keeping the better
// one: a single slow sample right after a reconnect shouldn't cost a restart.
func bestRealDelay(socksAddr string, maxMs int) probeResult {
	r := runProbe("real", socksAddr)
	if r.OK && r.LatencyMs <= maxMs {
		return r
	}
	if r2 := runProbe("real", socksAddr); r2.OK && (!r.OK || r2.LatencyMs < r.LatencyMs) {
		return r2
	}
	return r
}

func logGateOutcome(name string, restarts int, verdict string) {
	switch {
	case restarts == 0:
	case verdict != "":
		log.Printf("auto-restart: %s: %d restart(s), %s", name, restarts, verdict)
	case restarts > 1:
		log.Printf("auto-restart: %s: good after %d restarts", name, restarts)
	}
}
