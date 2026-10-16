package main

import (
	"strings"
	"testing"
)

// fake simulates a location whose results are scripted per restart.
type fake struct {
	calls     []string
	restarts  int
	delays    []int    // real delay after restart #1, #2, ... (0 = probe fails)
	ips       []string // exit IP after restart #1, #2, ...
	blocked   []bool   // Gemini blocked after restart #1, #2, ...
	ipBefore  string
	stopAfter int  // restart() returns false once this many restarts happened (0 = never)
	neverUp   bool // connected() always false
}

func at[T any](xs []T, i int) T {
	var zero T
	if len(xs) == 0 {
		return zero
	}
	if i >= len(xs) {
		return xs[len(xs)-1]
	}
	return xs[i]
}

func (f *fake) deps() gateDeps {
	return gateDeps{
		restart: func() bool {
			if f.stopAfter > 0 && f.restarts >= f.stopAfter {
				return false
			}
			f.restarts++
			f.calls = append(f.calls, "restart")
			return true
		},
		connected: func() bool { f.calls = append(f.calls, "connected"); return !f.neverUp },
		latency: func() probeResult {
			f.calls = append(f.calls, "latency")
			d := at(f.delays, f.restarts-1)
			if d == 0 {
				return probeResult{Error: "timeout"}
			}
			return probeResult{OK: true, LatencyMs: d, IP: at(f.ips, f.restarts-1)}
		},
		exitIP: func() string {
			if f.restarts == 0 {
				f.calls = append(f.calls, "ip-before")
				return f.ipBefore
			}
			f.calls = append(f.calls, "ip")
			return at(f.ips, f.restarts-1)
		},
		gemini: func() geminiResult {
			f.calls = append(f.calls, "gemini")
			if at(f.blocked, f.restarts-1) {
				return geminiResult{Reached: true, Reason: "HTTP 403: Error 403 (Forbidden)!!1"}
			}
			return geminiResult{OK: true, Reached: true}
		},
	}
}

func TestNoGatesIsASingleRestart(t *testing.T) {
	f := &fake{}
	n, verdict := restartUntilGood(autoRestartSettings{MaxTries: 5}, f.deps())
	if n != 1 || verdict != "" || strings.Join(f.calls, ",") != "restart" {
		t.Fatalf("n=%d verdict=%q calls=%v", n, verdict, f.calls)
	}
}

func TestRestartsAgainUntilDelayIsUnderTheLimit(t *testing.T) {
	f := &fake{delays: []int{450, 320, 180}}
	n, verdict := restartUntilGood(autoRestartSettings{MaxLatencyMs: 200}, f.deps())
	if n != 3 || verdict != "" {
		t.Fatalf("n=%d verdict=%q calls=%v", n, verdict, f.calls)
	}
	// every restart is followed by a wait-for-reconnect and a delay test
	if got := strings.Join(f.calls, ","); got != "restart,connected,latency,restart,connected,latency,restart,connected,latency" {
		t.Errorf("calls: %s", got)
	}
}

func TestDelayExactlyAtTheLimitPasses(t *testing.T) {
	f := &fake{delays: []int{200}}
	if n, v := restartUntilGood(autoRestartSettings{MaxLatencyMs: 200}, f.deps()); n != 1 || v != "" {
		t.Fatalf("n=%d v=%q", n, v)
	}
}

func TestGivesUpAfterTheTriesInsteadOfLoopingForever(t *testing.T) {
	f := &fake{delays: []int{900}} // never good
	n, verdict := restartUntilGood(autoRestartSettings{MaxLatencyMs: 200, MaxTries: 4}, f.deps())
	if n != 4 || !strings.HasPrefix(verdict, "gave up after 4 tries") || !strings.Contains(verdict, "900 ms is over 200") {
		t.Fatalf("n=%d verdict=%q", n, verdict)
	}
	// the default limit applies when none is set
	f = &fake{delays: []int{900}}
	if n, _ := restartUntilGood(autoRestartSettings{MaxLatencyMs: 200}, f.deps()); n != defaultMaxTries {
		t.Errorf("default tries: %d", n)
	}
}

func TestFailedDelayTestCountsAsNotGood(t *testing.T) {
	f := &fake{delays: []int{0, 150}}
	if n, v := restartUntilGood(autoRestartSettings{MaxLatencyMs: 200}, f.deps()); n != 2 || v != "" {
		t.Fatalf("n=%d v=%q", n, v)
	}
}

func TestNewIPReadsTheOldAddressBeforeTheFirstRestart(t *testing.T) {
	f := &fake{ipBefore: "1.1.1.1", ips: []string{"1.1.1.1", "1.1.1.1", "2.2.2.2"}}
	n, verdict := restartUntilGood(autoRestartSettings{NewIP: true}, f.deps())
	if n != 3 || verdict != "" {
		t.Fatalf("n=%d verdict=%q calls=%v", n, verdict, f.calls)
	}
	if f.calls[0] != "ip-before" || f.calls[1] != "restart" {
		t.Errorf("the baseline must be read first: %v", f.calls)
	}
}

func TestNewIPSkippedWhenTheOldAddressIsUnknown(t *testing.T) {
	f := &fake{ipBefore: "", ips: []string{"1.1.1.1"}}
	if n, v := restartUntilGood(autoRestartSettings{NewIP: true}, f.deps()); n != 1 || v != "" {
		t.Fatalf("an unknown baseline must not cause restarts: n=%d v=%q", n, v)
	}
}

func TestNewIPNotEnforcedWhenTheLookupIsDownAfterwards(t *testing.T) {
	f := &fake{ipBefore: "1.1.1.1", ips: []string{""}}
	if n, v := restartUntilGood(autoRestartSettings{NewIP: true}, f.deps()); n != 1 || v != "" {
		t.Fatalf("a lookup outage must not cause restart loops: n=%d v=%q", n, v)
	}
}

func TestGeminiBlockedRestartsAgain(t *testing.T) {
	f := &fake{blocked: []bool{true, true, false}}
	n, verdict := restartUntilGood(autoRestartSettings{Gemini: true}, f.deps())
	if n != 3 || verdict != "" {
		t.Fatalf("n=%d verdict=%q", n, verdict)
	}
	f = &fake{blocked: []bool{true}}
	n, verdict = restartUntilGood(autoRestartSettings{Gemini: true, MaxTries: 2}, f.deps())
	if n != 2 || !strings.Contains(verdict, "Gemini is blocked: HTTP 403") {
		t.Fatalf("n=%d verdict=%q", n, verdict)
	}
}

func TestAllGatesTogetherMustAllPass(t *testing.T) {
	// restart 1: slow. 2: fast but same IP. 3: fast, new IP, Gemini blocked. 4: everything good.
	f := &fake{
		ipBefore: "1.1.1.1",
		delays:   []int{400, 100, 100, 100},
		ips:      []string{"3.3.3.3", "1.1.1.1", "4.4.4.4", "5.5.5.5"},
		blocked:  []bool{false, false, true, false},
	}
	n, verdict := restartUntilGood(autoRestartSettings{MaxLatencyMs: 200, NewIP: true, Gemini: true}, f.deps())
	if n != 4 || verdict != "" {
		t.Fatalf("n=%d verdict=%q calls=%v", n, verdict, f.calls)
	}
}

func TestStopsWhenTheLocationIsStoppedOrRemoved(t *testing.T) {
	f := &fake{delays: []int{900}, stopAfter: 2}
	n, verdict := restartUntilGood(autoRestartSettings{MaxLatencyMs: 200, MaxTries: 10}, f.deps())
	if n != 2 || verdict != "stopped or removed meanwhile" {
		t.Fatalf("n=%d verdict=%q", n, verdict)
	}
}

func TestNeverReconnectingGivesUp(t *testing.T) {
	f := &fake{neverUp: true}
	n, verdict := restartUntilGood(autoRestartSettings{Gemini: true, MaxTries: 3}, f.deps())
	if n != 3 || !strings.Contains(verdict, "did not reconnect in time") {
		t.Fatalf("n=%d verdict=%q", n, verdict)
	}
	for _, c := range f.calls {
		if c == "gemini" || c == "latency" {
			t.Errorf("no check should run against a location that is not up: %v", f.calls)
		}
	}
}

func TestValidateGates(t *testing.T) {
	base := autoRestartSettings{Every: 1, Unit: "days"}
	good := []autoRestartSettings{
		base,
		{Every: 1, Unit: "days", MaxLatencyMs: 200},
		{Every: 1, Unit: "days", MaxLatencyMs: 20, MaxTries: 1},
		{Every: 1, Unit: "days", MaxLatencyMs: 10000, MaxTries: 20, NewIP: true, Gemini: true},
	}
	for _, s := range good {
		if err := validateAutoRestart(s); err != nil {
			t.Errorf("%+v should be valid: %v", s, err)
		}
	}
	bad := []autoRestartSettings{
		{Every: 1, Unit: "days", MaxLatencyMs: -1},
		{Every: 1, Unit: "days", MaxLatencyMs: 19},
		{Every: 1, Unit: "days", MaxLatencyMs: 10001},
		{Every: 1, Unit: "days", MaxTries: 21},
		{Every: 1, Unit: "days", MaxTries: -2},
	}
	for _, s := range bad {
		if err := validateAutoRestart(s); err == nil {
			t.Errorf("%+v should be rejected", s)
		}
	}
}

func TestOldSettingsFileHasNoGates(t *testing.T) {
	s := autoRestartSettings{Enabled: true, Every: 1, Unit: "days"}
	if s.hasGates() || s.Tries() != defaultMaxTries {
		t.Errorf("a schedule saved before the gates existed must behave as before: %+v", s)
	}
}
