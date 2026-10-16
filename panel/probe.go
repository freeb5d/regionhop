package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// probeResult is what a latency test reports back to the dashboard.
type probeResult struct {
	OK        bool   `json:"ok"`
	LatencyMs int    `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
	IP        string `json:"ip,omitempty"`     // exit address as seen by websites; empty if the lookup failed
	Google    string `json:"google,omitempty"` // google mode only: "ok" or "blocked"
	Detail    string `json:"detail,omitempty"` // google mode only: why it was judged blocked
}

// probeTargetHost is a single fixed, well-known endpoint used for every
// probe — nothing user-supplied, so there's no way to point a probe at an
// arbitrary internal address.
const probeTargetHost = "example.com"

const probeTimeout = 10 * time.Second

// socks5Connect performs a minimal SOCKS5 CONNECT handshake (no auth, the
// only mode Psiphon's local proxy speaks) through the proxy at socksAddr to
// host:port, returning the established connection. Written by hand instead
// of pulling in a SOCKS client library — the CONNECT handshake is small
// enough that a dependency isn't worth it for three read-only probe modes.
func socks5Connect(ctx context.Context, socksAddr, host string, port int) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", socksAddr)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		conn.Close()
		return nil, err
	}
	greetResp := make([]byte, 2)
	if _, err := io.ReadFull(conn, greetResp); err != nil {
		conn.Close()
		return nil, err
	}
	if greetResp[0] != 0x05 || greetResp[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("SOCKS5 proxy rejected the no-auth handshake")
	}

	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, []byte(host)...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, err
	}

	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		conn.Close()
		return nil, err
	}
	if head[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("SOCKS5 CONNECT failed (code %d)", head[1])
	}
	var addrLen int
	switch head[3] {
	case 0x01:
		addrLen = 4
	case 0x04:
		addrLen = 16
	case 0x03:
		lb := make([]byte, 1)
		if _, err := io.ReadFull(conn, lb); err != nil {
			conn.Close()
			return nil, err
		}
		addrLen = int(lb[0])
	default:
		conn.Close()
		return nil, fmt.Errorf("SOCKS5 proxy returned an unknown address type")
	}
	if _, err := io.ReadFull(conn, make([]byte, addrLen+2)); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// probeTCP measures only the time to open a TCP connection through the
// tunnel to the probe target — the cheapest, fastest check, good for a
// quick "is this location's proxy alive at all" read.
func probeTCP(socksAddr string) probeResult {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	start := time.Now()
	conn, err := socks5Connect(ctx, socksAddr, probeTargetHost, 443)
	elapsed := time.Since(start)
	if err != nil {
		return probeResult{Error: err.Error()}
	}
	conn.Close()
	return probeResult{OK: true, LatencyMs: int(elapsed.Milliseconds())}
}

// probeHTTP sends a real plain-HTTP GET through the tunnel and waits for a
// response, measuring the full request/response round trip (not just the
// connection setup).
func probeHTTP(socksAddr string) probeResult {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	start := time.Now()
	conn, err := socks5Connect(ctx, socksAddr, probeTargetHost, 80)
	if err != nil {
		return probeResult{Error: err.Error()}
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	req := "GET / HTTP/1.1\r\nHost: " + probeTargetHost + "\r\nConnection: close\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return probeResult{Error: err.Error()}
	}
	if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
		return probeResult{Error: err.Error()}
	}
	elapsed := time.Since(start)
	return probeResult{OK: true, LatencyMs: int(elapsed.Milliseconds())}
}

// probeReal measures the full cost of actually loading something over
// HTTPS through the tunnel — SOCKS handshake, TCP connect, and TLS
// handshake together — the closest of the three modes to what a real user
// would experience opening a page.
func probeReal(socksAddr string) probeResult {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	start := time.Now()
	conn, err := socks5Connect(ctx, socksAddr, probeTargetHost, 443)
	if err != nil {
		return probeResult{Error: err.Error()}
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	tlsConn := tls.Client(conn, &tls.Config{ServerName: probeTargetHost})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return probeResult{Error: err.Error()}
	}
	req := "GET / HTTP/1.1\r\nHost: " + probeTargetHost + "\r\nConnection: close\r\n\r\n"
	if _, err := tlsConn.Write([]byte(req)); err != nil {
		return probeResult{Error: err.Error()}
	}
	if _, err := bufio.NewReader(tlsConn).ReadString('\n'); err != nil {
		return probeResult{Error: err.Error()}
	}
	elapsed := time.Since(start)
	return probeResult{OK: true, LatencyMs: int(elapsed.Milliseconds())}
}

func runProbe(mode, socksAddr string) probeResult {
	var r probeResult
	switch mode {
	case "tcp":
		r = probeTCP(socksAddr)
	case "http":
		r = probeHTTP(socksAddr)
	case "real":
		r = probeReal(socksAddr)
	case "google":
		r = probeGoogle(socksAddr)
	default:
		return probeResult{Error: "unknown probe mode"}
	}
	// Only after the latency has been measured, and never part of it: the
	// lookup is an extra request, so it must not inflate the number above.
	if r.OK {
		r.IP = fetchExitIP(socksAddr)
	}
	return r
}

// ipLookupTarget is one fixed IP-echo service. Like the latency target, these
// are constants -- nothing user-supplied ever reaches the dial.
type ipLookupTarget struct {
	host string
	port int
	tls  bool
}

// Two independent services, so one being blocked from some exit country
// doesn't leave the column empty. They are variables only so tests can
// point them at a local server.
var ipLookupTargets = []ipLookupTarget{
	{"api.ipify.org", 443, true},
	{"checkip.amazonaws.com", 443, true},
}

const ipLookupTimeout = 8 * time.Second

// fetchExitIP asks an IP-echo service, through the tunnel, which address it
// sees -- i.e. the address websites see for traffic leaving this location.
// Returns "" if every service fails; the latency result stands without it.
// Only a successfully parsed IP address is ever returned, because the text
// comes from a third party and ends up on the dashboard.
func fetchExitIP(socksAddr string) string {
	for _, t := range ipLookupTargets {
		if ip := fetchExitIPFrom(socksAddr, t); ip != "" {
			return ip
		}
	}
	return ""
}

func fetchExitIPFrom(socksAddr string, t ipLookupTarget) string {
	ctx, cancel := context.WithTimeout(context.Background(), ipLookupTimeout)
	defer cancel()
	conn, err := socks5Connect(ctx, socksAddr, t.host, t.port)
	if err != nil {
		return ""
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	var rw io.ReadWriter = conn
	if t.tls {
		tc := tls.Client(conn, &tls.Config{ServerName: t.host})
		if err := tc.HandshakeContext(ctx); err != nil {
			return ""
		}
		rw = tc
	}
	req := "GET / HTTP/1.1\r\nHost: " + t.host + "\r\nUser-Agent: regionhop\r\nAccept: text/plain\r\nConnection: close\r\n\r\n"
	if _, err := rw.Write([]byte(req)); err != nil {
		return ""
	}
	raw, _ := io.ReadAll(io.LimitReader(rw, 4096))
	return parseIPEchoResponse(string(raw))
}

// parseIPEchoResponse accepts a 200 response whose body is a bare IP address
// and returns it in canonical form; anything else yields "".
func parseIPEchoResponse(raw string) string {
	head, body, ok := strings.Cut(raw, "\r\n\r\n")
	if !ok || !strings.HasPrefix(head, "HTTP/1.") || !strings.Contains(strings.SplitN(head, "\r\n", 2)[0], " 200") {
		return ""
	}
	if strings.Contains(strings.ToLower(head), "transfer-encoding: chunked") {
		// Some servers chunk even tiny bodies: "<hex size>\r\n<data>\r\n0\r\n\r\n".
		if _, rest, ok := strings.Cut(body, "\r\n"); ok {
			body, _, _ = strings.Cut(rest, "\r\n")
		}
	}
	ip := net.ParseIP(strings.TrimSpace(body))
	if ip == nil {
		return ""
	}
	return ip.String()
}

// geminiURL is the one fixed page the Google check loads; a variable only so
// tests can point it at a local server.
var geminiURL = "https://gemini.google.com/"

const (
	geminiTimeout = 15 * time.Second
	// A browser User-Agent: Google answers Go's default one differently.
	browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

type geminiResult struct {
	OK        bool
	Reached   bool // an HTTP answer came back at all (false = the request itself failed)
	Reason    string
	LatencyMs int
}

// checkGemini loads the Gemini web app through the tunnel and decides whether
// Google is serving it to this exit IP. "OK" needs positive evidence -- a 200
// page titled Gemini -- so a CAPTCHA, a 403 ("Your client does not have
// permission to get URL / from this server", observed from a blocked exit),
// or any other page counts as blocked, with the reason kept for the tooltip.
//
// Only the first 64 KB is read (the real page is ~800 KB; a block page is a
// few KB), and redirects are followed only within the same host, so nothing a
// remote server says can make the panel dial somewhere else.
func checkGemini(socksAddr string) geminiResult {
	ctx, cancel := context.WithTimeout(context.Background(), geminiTimeout)
	defer cancel()
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			host, portStr, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			port, err := strconv.Atoi(portStr)
			if err != nil {
				return nil, err
			}
			return socks5Connect(ctx, socksAddr, host, port)
		},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 4 || req.URL.Host != via[0].URL.Host {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, "GET", geminiURL, nil)
	if err != nil {
		return geminiResult{Reason: err.Error()}
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return geminiResult{Reason: "unreachable: " + err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	ok, why := classifyGemini(resp.StatusCode, resp.Request.URL, resp.Header.Get("Location"), resp.Header.Get("Content-Security-Policy"), string(body))
	return geminiResult{OK: ok, Reached: true, Reason: why, LatencyMs: int(time.Since(start).Milliseconds())}
}

// Phrases that never appear on the normal Gemini page (checked against it)
// but do on Google's block / unsupported pages.
var geminiBlockPhrases = []string{
	"unusual traffic", "captcha",
	"not supported in your", "isn't supported in your",
	"not available in your", "isn't available in your",
}

var titleRE = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

func htmlTitle(body string) string {
	m := titleRE.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	t := strings.TrimSpace(strings.Trim(html.UnescapeString(m[1]), "\u200e\u200f \t\r\n"))
	if len(t) > 80 {
		t = t[:80]
	}
	return t
}

// classifyGemini turns one HTTP answer into (served?, reason).
//
// "Served" needs positive evidence that it is the real Gemini app. The page's
// <title> can't be that evidence: in the real ~800 KB page it comes at byte
// ~757,000, far past what is read. Two things near the top do work: the
// og:site_name meta tag, and the Content-Security-Policy header, whose
// report-uri names the app ("BardChatUi"). The title still counts if it
// happens to be in the part that was read.
func classifyGemini(status int, u *url.URL, location, csp, body string) (bool, string) {
	title := htmlTitle(body)
	suffix := ""
	if title != "" {
		suffix = ": " + title
	}
	if status >= 300 && status < 400 {
		loc, err := u.Parse(location)
		if err != nil || location == "" {
			return false, fmt.Sprintf("HTTP %d redirect with no usable address", status)
		}
		host := strings.ToLower(loc.Hostname())
		switch {
		case strings.HasPrefix(loc.Path, "/sorry"):
			return false, "Google is asking for a CAPTCHA (unusual traffic from this IP)"
		case strings.HasPrefix(host, "consent."):
			return true, "Google served its consent page"
		}
		return false, fmt.Sprintf("redirected to %s%s (HTTP %d)", host, loc.Path, status)
	}
	if strings.HasPrefix(u.Path, "/sorry") {
		return false, "Google is asking for a CAPTCHA (unusual traffic from this IP)"
	}
	if status == 429 {
		return false, "HTTP 429: too many requests / unusual traffic" + suffix
	}
	if status >= 400 {
		return false, fmt.Sprintf("HTTP %d%s", status, suffix)
	}
	lower := strings.ToLower(body)
	for _, p := range geminiBlockPhrases {
		if strings.Contains(lower, p) {
			return false, "page says \"" + p + "\"" + suffix
		}
	}
	isGemini := strings.Contains(strings.ToLower(title), "gemini") ||
		strings.Contains(lower, `og:site_name" content="gemini"`) ||
		strings.Contains(csp, "BardChatUi")
	if !isGemini {
		return false, "unexpected page" + suffix
	}
	return true, ""
}

// probeGoogle is the dashboard's "Google" test: OK means the request got an
// answer (so the exit IP can be looked up), Google says whether Gemini is
// being served to that IP.
func probeGoogle(socksAddr string) probeResult {
	g := checkGemini(socksAddr)
	if !g.Reached {
		return probeResult{Error: g.Reason, Google: "blocked", Detail: g.Reason}
	}
	r := probeResult{OK: true, LatencyMs: g.LatencyMs, Detail: g.Reason, Google: "blocked"}
	if g.OK {
		r.Google = "ok"
	}
	return r
}
