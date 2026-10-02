package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSecurityHeaders(t *testing.T) {
	basePath = "/x"
	defer func() { basePath = "" }()
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest("GET", "/x/", nil))
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"X-Frame-Options":        "DENY",
		"Cache-Control":          "no-store",
	} {
		if got := page.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if csp := page.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q", csp)
	}

	static := httptest.NewRecorder()
	h.ServeHTTP(static, httptest.NewRequest("GET", "/x/static/style.css", nil))
	if static.Header().Get("Cache-Control") == "no-store" {
		t.Error("static files should stay cacheable")
	}
}

func TestLimitBody(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("a", 1000)))
	limitBody(w, r, 100)
	if _, err := io.ReadAll(r.Body); err == nil {
		t.Fatal("expected an error reading past the limit")
	}
}

func TestValidRegionCode(t *testing.T) {
	for _, ok := range []string{"US", "DE", "ZZ"} {
		if !validRegionCode(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "U", "us", "USA", `" onerror=alert(1) x="`, "<s>", "1A"} {
		if validRegionCode(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestScanNoticesRejectsHostileRegion(t *testing.T) {
	line := `Sep 15 18:28:53 h ConsoleClient[1]: {"data":{"serverRegion":"\" onerror=alert(1) x=\""},"noticeType":"ConnectedServerRegion","timestamp":"2026-09-15T18:28:53.273Z"}`
	if _, region, _ := scanNotices(line); region != "" {
		t.Fatalf("hostile region accepted: %q", region)
	}
}

func TestLoginThrottleAndPrune(t *testing.T) {
	s := newSessionStore([]byte("k"))
	for i := 0; i < 5; i++ {
		s.recordFailure("1.2.3.4")
	}
	if s.allowAttempt("1.2.3.4") {
		t.Fatal("expected a lockout after 5 failures")
	}
	if !s.allowAttempt("5.6.7.8") {
		t.Fatal("other addresses must be unaffected")
	}
	s.recordSuccess("1.2.3.4")
	if !s.allowAttempt("1.2.3.4") {
		t.Fatal("success should clear the lockout")
	}

	// A flood of distinct addresses must not grow the map without bound.
	old := time.Now().Add(-2 * time.Hour)
	s.mu.Lock()
	for i := 0; i < 1500; i++ {
		s.failures[strings.Repeat("x", 1)+string(rune('a'+i%26))+time.Duration(i).String()] = &loginAttempts{count: 1, last: old}
	}
	s.mu.Unlock()
	s.recordFailure("9.9.9.9")
	s.mu.Lock()
	n := len(s.failures)
	s.mu.Unlock()
	if n > 10 {
		t.Fatalf("stale entries not pruned: %d left", n)
	}
}
