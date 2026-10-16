package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Test button in the Upstream section: tries the values currently in the
// form -- nothing is saved or applied -- by fetching the exit IP through them.
// A SOCKS/HTTP proxy is used directly; a V2Ray link is run in a short-lived
// Xray on a spare loopback port, so what is tested is exactly the config that
// saving would run.

type upstreamTestResult struct {
	OK        bool   `json:"ok"`
	LatencyMs int    `json:"latencyMs,omitempty"`
	IP        string `json:"ip,omitempty"`
	Error     string `json:"error,omitempty"`
}

const (
	upstreamTestRequestTimeout = 15 * time.Second
	xrayStartTimeout           = 8 * time.Second
	xrayTestOverallTimeout     = 75 * time.Second
)

// One test at a time: a double click must not start two Xray processes.
var upstreamTestMu sync.Mutex

func runUpstreamTest(s upstreamSettings) upstreamTestResult {
	if !upstreamTestMu.TryLock() {
		return upstreamTestResult{Error: "another test is still running"}
	}
	defer upstreamTestMu.Unlock()
	switch s.Mode {
	case upstreamProxy:
		return testUpstreamProxy(s.ProxyURL)
	case upstreamV2Ray:
		return testV2Ray(s.V2Ray)
	}
	return upstreamTestResult{Error: "Direct mode has no upstream to test"}
}

// testUpstreamProxy fetches the exit IP through a SOCKS5 or HTTP proxy. Go's
// own HTTP client speaks both (with user:pass in the URL), so there is no
// proxy protocol code here.
func testUpstreamProxy(proxy string) upstreamTestResult {
	u, err := url.Parse(proxy)
	if err != nil {
		return upstreamTestResult{Error: err.Error()}
	}
	if u.Scheme == "socks4a" {
		return upstreamTestResult{Error: "socks4a can't be tested from the panel (Psiphon itself supports it); use a socks5:// or http:// address to test"}
	}
	tr := &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: upstreamTestRequestTimeout}

	lastErr := "no IP-echo service answered"
	for _, t := range ipLookupTargets {
		scheme := "http"
		if t.tls {
			scheme = "https"
		}
		req, err := http.NewRequest("GET", scheme+"://"+net.JoinHostPort(t.host, strconv.Itoa(t.port))+"/", nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "regionhop")
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			lastErr = tidyProxyError(err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode != 200 {
			lastErr = fmt.Sprintf("the IP-echo service answered HTTP %d through the proxy", resp.StatusCode)
			continue
		}
		ip := net.ParseIP(strings.TrimSpace(string(body)))
		if ip == nil {
			lastErr = "the proxy answered, but not with an IP address (is it really a proxy?)"
			continue
		}
		return upstreamTestResult{OK: true, IP: ip.String(), LatencyMs: int(time.Since(start).Milliseconds())}
	}
	return upstreamTestResult{Error: lastErr}
}

// tidyProxyError drops Go's long `Get "https://...": ` prefix.
func tidyProxyError(err error) string {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err.Error()
	}
	return err.Error()
}

// testV2Ray runs the link/JSON in a throwaway Xray (SOCKS inbound on a spare
// loopback port, never the live upstream's port) and tests through that.
func testV2Ray(link string) upstreamTestResult {
	outbound, err := parseV2RayOutbound(link)
	if err != nil {
		return upstreamTestResult{Error: err.Error()}
	}
	if _, err := os.Stat(xrayBinary); err != nil {
		return upstreamTestResult{Error: "Xray is not installed on this server (no prebuilt bundle for its architecture), so a V2Ray link can't be tested or used here"}
	}
	port, err := freeLoopbackPort()
	if err != nil {
		return upstreamTestResult{Error: "no free local port: " + err.Error()}
	}
	dir, err := os.MkdirTemp("", "regionhop-xray-test-")
	if err != nil {
		return upstreamTestResult{Error: err.Error()}
	}
	defer os.RemoveAll(dir)
	cfg := filepath.Join(dir, "test.json") // Xray picks the format from the extension
	body, err := json.Marshal(buildXrayConfig(outbound, port))
	if err != nil {
		return upstreamTestResult{Error: err.Error()}
	}
	if err := os.WriteFile(cfg, body, 0o600); err != nil {
		return upstreamTestResult{Error: err.Error()}
	}

	ctx, cancel := context.WithTimeout(context.Background(), xrayTestOverallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, xrayBinary, "run", "-c", cfg)
	var out cappedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return upstreamTestResult{Error: "could not start Xray: " + err.Error()}
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	defer func() { cmd.Process.Kill(); <-exited }() // never leave it running

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if !waitListening(addr, xrayStartTimeout, exited) {
		select {
		case <-exited:
			return upstreamTestResult{Error: "Xray stopped right away: " + lastLines(out.String(), 3)}
		default:
			return upstreamTestResult{Error: "Xray did not start listening in time"}
		}
	}
	res := testUpstreamProxy("socks5://" + addr)
	if !res.OK && res.Error != "" {
		// Xray starts fine for a link that points at a dead or wrong server;
		// the failure then shows up as a failed request, so name the cause.
		res.Error = "Xray started, but traffic through the V2Ray server failed: " + res.Error
		if tail := lastLines(out.String(), 2); tail != "" {
			res.Error += " (" + tail + ")"
		}
	}
	return res
}

func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func waitListening(addr string, max time.Duration, exited <-chan struct{}) bool {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond); err == nil {
			c.Close()
			return true
		}
		select {
		case <-exited:
			return false
		case <-time.After(150 * time.Millisecond):
		}
	}
	return false
}

// cappedBuffer keeps only the first 8 KB of a child's output.
type cappedBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := 8192 - len(c.b); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		c.b = append(c.b, p[:room]...)
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.b)
}

func (a *app) handleUpstreamTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	limitBody(w, r, 1<<20)
	s, err := normalizeUpstream(upstreamSettings{
		Mode:     r.FormValue("upstream_mode"),
		ProxyURL: r.FormValue("upstream_proxy_url"),
		V2Ray:    r.FormValue("upstream_v2ray"),
	})
	var res upstreamTestResult
	if err != nil {
		res = upstreamTestResult{Error: err.Error()}
	} else {
		res = runUpstreamTest(s) // deliberately not under a.mu: it can take a while
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}
