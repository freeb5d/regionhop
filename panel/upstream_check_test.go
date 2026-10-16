package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func ipEchoServer(ip string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, ip) }))
}

func TestUpstreamTestSocks5(t *testing.T) {
	web := ipEchoServer("198.51.100.20\n")
	defer web.Close()
	socks := fakeSocks(t, map[string]string{"ip.test:80": web.Listener.Addr().String()})
	withIPTargets(t, []ipLookupTarget{{"ip.test", 80, false}})

	r := testUpstreamProxy("socks5://" + socks)
	if !r.OK || r.IP != "198.51.100.20" || r.Error != "" {
		t.Fatalf("%+v", r)
	}
	// credentials in the URL must be accepted (the stand-in just ignores them)
	if r := testUpstreamProxy("socks5://user:pass@" + socks); !r.OK {
		t.Fatalf("with credentials: %+v", r)
	}
}

func TestUpstreamTestHTTPProxy(t *testing.T) {
	// a plain-HTTP "proxy" receives absolute-URI requests and answers them itself
	var gotHost string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		fmt.Fprint(w, "203.0.113.31")
	}))
	defer proxy.Close()
	withIPTargets(t, []ipLookupTarget{{"ip.test", 80, false}})

	r := testUpstreamProxy("http://" + proxy.Listener.Addr().String())
	if !r.OK || r.IP != "203.0.113.31" {
		t.Fatalf("%+v", r)
	}
	if gotHost != "ip.test:80" {
		t.Errorf("the request should have been sent through the proxy for ip.test:80, proxy saw host %q", gotHost)
	}
}

func TestUpstreamTestFailures(t *testing.T) {
	withIPTargets(t, []ipLookupTarget{{"ip.test", 80, false}})
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := l.Addr().String()
	l.Close()

	cases := map[string]struct{ proxy, contains string }{
		"nothing listening":    {"socks5://" + dead, ""},
		"http proxy dead":      {"http://" + dead, ""},
		"socks4a not testable": {"socks4a://127.0.0.1:1080", "socks4a can't be tested"},
	}
	for name, c := range cases {
		r := testUpstreamProxy(c.proxy)
		if r.OK || r.Error == "" || !strings.Contains(r.Error, c.contains) {
			t.Errorf("%s: %+v", name, r)
		}
	}

	// A server that answers, but not like an IP-echo service
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>captive portal</html>") }))
	defer web.Close()
	if r := testUpstreamProxy("http://" + web.Listener.Addr().String()); r.OK || !strings.Contains(r.Error, "not with an IP address") {
		t.Errorf("non-IP answer: %+v", r)
	}
	// ...and one that answers with an error status
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 407) }))
	defer bad.Close()
	if r := testUpstreamProxy("http://" + bad.Listener.Addr().String()); r.OK || !strings.Contains(r.Error, "HTTP 407") {
		t.Errorf("407: %+v", r)
	}
}

func TestUpstreamTestFallsBackToTheSecondService(t *testing.T) {
	good := ipEchoServer("192.0.2.77")
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 500) }))
	defer bad.Close()
	socks := fakeSocks(t, map[string]string{"a.test:80": bad.Listener.Addr().String(), "b.test:80": good.Listener.Addr().String()})
	withIPTargets(t, []ipLookupTarget{{"a.test", 80, false}, {"b.test", 80, false}})
	if r := testUpstreamProxy("socks5://" + socks); !r.OK || r.IP != "192.0.2.77" {
		t.Fatalf("%+v", r)
	}
}

func TestRunUpstreamTestDispatch(t *testing.T) {
	if r := runUpstreamTest(upstreamSettings{Mode: upstreamNone}); r.OK || !strings.Contains(r.Error, "no upstream to test") {
		t.Errorf("none: %+v", r)
	}
	// a second test while one is running is refused, not queued
	upstreamTestMu.Lock()
	r := runUpstreamTest(upstreamSettings{Mode: upstreamProxy, ProxyURL: "socks5://127.0.0.1:1"})
	upstreamTestMu.Unlock()
	if r.OK || !strings.Contains(r.Error, "another test") {
		t.Errorf("busy: %+v", r)
	}
}

func TestV2RayTestErrorsBeforeStartingAnything(t *testing.T) {
	old := xrayBinary
	defer func() { xrayBinary = old }()

	if r := testV2Ray("not a link"); r.OK || !strings.Contains(r.Error, "unsupported V2Ray input") {
		t.Errorf("bad input: %+v", r)
	}
	xrayBinary = "/definitely/not/here/xray"
	r := testV2Ray("vless://11111111-2222-3333-4444-555555555555@1.2.3.4:443?type=tcp&security=tls#x")
	if r.OK || !strings.Contains(r.Error, "Xray is not installed") {
		t.Errorf("missing binary: %+v", r)
	}
}

func TestCappedBuffer(t *testing.T) {
	var b cappedBuffer
	b.Write([]byte(strings.Repeat("x", 6000)))
	b.Write([]byte(strings.Repeat("y", 6000)))
	if n := len(b.String()); n != 8192 {
		t.Fatalf("kept %d bytes", n)
	}
}

// Real Xray, only where a binary is provided (CI sets XRAY_BIN): the whole
// lifecycle -- spare port, start, test through it, stop -- against a V2Ray
// "server" that is just a local SOCKS stand-in.
func TestV2RayTestWithRealXray(t *testing.T) {
	bin := os.Getenv("XRAY_BIN")
	if bin == "" {
		t.Skip("XRAY_BIN not set")
	}
	old := xrayBinary
	xrayBinary = bin
	defer func() { xrayBinary = old }()

	web := ipEchoServer("203.0.113.55")
	defer web.Close()
	upstream := fakeSocks(t, map[string]string{"ip.test:80": web.Listener.Addr().String()})
	host, port, _ := net.SplitHostPort(upstream)
	withIPTargets(t, []ipLookupTarget{{"ip.test", 80, false}})

	good := fmt.Sprintf(`{"protocol":"socks","settings":{"servers":[{"address":"%s","port":%s}]}}`, host, port)
	for i := 0; i < 2; i++ { // twice: the first run must not leave a process or port behind
		r := testV2Ray(good)
		if !r.OK || r.IP != "203.0.113.55" {
			t.Fatalf("run %d: %+v", i+1, r)
		}
	}

	// Xray starts happily for a server that does not exist; the traffic then
	// fails, and the message says so.
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadPort := dead.Addr().(*net.TCPAddr).Port
	dead.Close()
	bad := fmt.Sprintf(`{"protocol":"socks","settings":{"servers":[{"address":"127.0.0.1","port":%d}]}}`, deadPort)
	r := testV2Ray(bad)
	if r.OK || !strings.Contains(r.Error, "traffic through the V2Ray server failed") {
		t.Fatalf("dead server: %+v", r)
	}

	// A config Xray refuses outright is reported with Xray's own words.
	r = testV2Ray(`{"protocol":"vless","settings":{"vnext":[{"address":"1.2.3.4","port":443,"users":[{"id":"not-a-uuid"}]}]},"streamSettings":{"network":"tcp","security":"tls"}}`)
	if r.OK || r.Error == "" {
		t.Fatalf("invalid config accepted: %+v", r)
	}
}
