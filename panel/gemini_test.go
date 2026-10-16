package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The 403 page Google served to a blocked exit IP (seen in a browser).
const gemini403Page = `<!DOCTYPE html><html lang=en><meta charset=utf-8><title>Error 403 (Forbidden)!!1</title>` +
	`<p><b>403.</b> <ins>That’s an error.</ins><p>Your client does not have permission to get URL <code>/</code> from this server. <ins>That’s all we know.</ins>`

const geminiOKPage = "<!doctype html><html lang=\"en-US\"><head><base href=\"https://gemini.google.com/\"><title>‎Google Gemini</title></head><body>app shell</body></html>"

func TestHTMLTitle(t *testing.T) {
	for in, want := range map[string]string{
		geminiOKPage:       "Google Gemini", // the real page has an invisible LRM before the title
		gemini403Page:      "Error 403 (Forbidden)!!1",
		"<TITLE>x</TITLE>": "x",
		"no title here":    "",
	} {
		if got := htmlTitle(in); got != want {
			t.Errorf("htmlTitle = %q, want %q", got, want)
		}
	}
}

func TestClassifyGemini(t *testing.T) {
	u, _ := url.Parse("https://gemini.google.com/")
	cases := []struct {
		name     string
		status   int
		location string
		body     string
		ok       bool
		contains string
		csp      string
	}{
		{"normal page", 200, "", geminiOKPage, true, "", ""},
		{"real 403 from a blocked exit", 403, "", gemini403Page, false, "HTTP 403: Error 403", ""},
		{"429", 429, "", "<title>Sorry...</title>", false, "429", ""},
		{"captcha redirect", 302, "https://www.google.com/sorry/index?continue=x", "", false, "CAPTCHA", ""},
		{"consent redirect still means served", 302, "https://consent.google.com/m?continue=x", "", true, "consent", ""},
		{"redirect somewhere else", 302, "https://example.org/x", "", false, "redirected to example.org/x", ""},
		{"redirect without a location", 302, "", "", false, "no usable address", ""},
		{"200 but a captcha page", 200, "", "<title>Gemini</title>Our systems have detected unusual traffic", false, "unusual traffic", ""},
		{"200 but says unsupported", 200, "", "<title>Gemini</title>Gemini isn't supported in your country", false, "supported in your", ""},
		{"200 but not Gemini at all", 200, "", "<title>Welcome to nginx!</title>", false, "unexpected page: Welcome to nginx!", ""},
		{"200 with no title", 200, "", "hello", false, "unexpected page", ""},
		{"500", 500, "", "<title>Server Error</title>", false, "HTTP 500", ""},
		// The real page has its <title> ~757 KB in; only the top 64 KB is read.
		{"real page: no title in view, og:site_name marks it", 200, "", `<head><meta property="og:site_name" content="Gemini"><script>`, true, "", ""},
		{"real page: only the CSP header marks it", 200, "", "<head><script>", true, "", "report-uri /_/BardChatUi/cspreport;default-src 'none'"},
		{"not Gemini and no markers", 200, "", "<head><script>var x</script>", false, "unexpected page", ""},
	}
	for _, c := range cases {
		ok, why := classifyGemini(c.status, u, c.location, c.csp, c.body)
		if ok != c.ok || !strings.Contains(why, c.contains) {
			t.Errorf("%s: got (%v, %q), want ok=%v containing %q", c.name, ok, why, c.ok, c.contains)
		}
	}
	// A same-host redirect that ended on a CAPTCHA path.
	su, _ := url.Parse("https://gemini.google.com/sorry/index")
	if ok, _ := classifyGemini(200, su, "", "", "<title>Gemini</title>"); ok {
		t.Error("a /sorry final page must be blocked")
	}
}

func withGeminiURL(t *testing.T, u string) {
	old := geminiURL
	geminiURL = u
	t.Cleanup(func() { geminiURL = old })
}

// End to end through a fake SOCKS5 proxy, against local servers standing in
// for Google.
func TestCheckGeminiThroughSocks(t *testing.T) {
	hits := map[string]int{}
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, geminiOKPage)
		case "/blocked":
			w.WriteHeader(403)
			fmt.Fprint(w, gemini403Page)
		case "/limited":
			w.WriteHeader(429)
		case "/captcha":
			http.Redirect(w, r, "https://www.google.com/sorry/index?continue=x", 302)
		case "/consent":
			http.Redirect(w, r, "https://consent.google.com/m", 302)
		case "/elsewhere":
			http.Redirect(w, r, "http://evil.test/steal", 302)
		case "/hop1":
			http.Redirect(w, r, "/", 302)
		case "/loop":
			http.Redirect(w, r, "/loop", 302)
		}
	}))
	defer web.Close()
	socks := fakeSocks(t, map[string]string{"gem.test:80": web.Listener.Addr().String()})

	cases := []struct {
		path     string
		ok       bool
		reached  bool
		contains string
	}{
		{"/", true, true, ""},
		{"/blocked", false, true, "HTTP 403"},
		{"/limited", false, true, "429"},
		{"/captcha", false, true, "CAPTCHA"},
		{"/consent", true, true, "consent"},
		{"/elsewhere", false, true, "redirected to evil.test"},
		{"/hop1", true, true, ""},
		{"/loop", false, true, "redirected to gem.test"},
	}
	for _, c := range cases {
		withGeminiURL(t, "http://gem.test"+c.path)
		g := checkGemini(socks)
		if g.OK != c.ok || g.Reached != c.reached || !strings.Contains(g.Reason, c.contains) {
			t.Errorf("%s: %+v, want ok=%v reached=%v containing %q", c.path, g, c.ok, c.reached, c.contains)
		}
	}
	if hits["/loop"] > 5 {
		t.Errorf("a redirect loop was followed %d times", hits["/loop"])
	}
}

func TestCheckGeminiUnreachable(t *testing.T) {
	withGeminiURL(t, "http://gem.test/")
	g := checkGemini("127.0.0.1:1") // nothing listens there
	if g.OK || g.Reached || !strings.Contains(g.Reason, "unreachable") {
		t.Fatalf("%+v", g)
	}
}

func TestProbeGoogleResult(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/blocked" {
			w.WriteHeader(403)
			fmt.Fprint(w, gemini403Page)
			return
		}
		fmt.Fprint(w, geminiOKPage)
	}))
	defer web.Close()
	socks := fakeSocks(t, map[string]string{"gem.test:80": web.Listener.Addr().String()})
	withIPTargets(t, nil) // no IP lookup in this test

	withGeminiURL(t, "http://gem.test/")
	if r := probeGoogle(socks); !r.OK || r.Google != "ok" || r.Error != "" {
		t.Errorf("ok page: %+v", r)
	}
	withGeminiURL(t, "http://gem.test/blocked")
	if r := probeGoogle(socks); !r.OK || r.Google != "blocked" || !strings.Contains(r.Detail, "403") {
		t.Errorf("blocked page must still count as reachable (so the IP is shown): %+v", r)
	}
	if r := probeGoogle("127.0.0.1:1"); r.OK || r.Google != "blocked" || r.Error == "" {
		t.Errorf("unreachable: %+v", r)
	}
}

// Regression for a real bug: the real page is ~800 KB with its <title> at
// byte ~757,000, but only the first 64 KB is read, so judging by the title
// reported a perfectly good Gemini page as "unexpected page".
func TestCheckGeminiWhenTheTitleIsBeyondTheReadWindow(t *testing.T) {
	filler := strings.Repeat("/* long inline script */ var a=1;\n", 4000) // ~130 KB
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/real": // marker near the top, title far below
			fmt.Fprint(w, `<!doctype html><head><meta property="og:site_name" content="Gemini"><script>`+filler+`</script><title>Google Gemini</title>`)
		case "/other": // a non-Gemini page that is also long, with its own title far down
			fmt.Fprint(w, `<!doctype html><head><script>`+filler+`</script><title>Google Gemini</title>`)
		}
	}))
	defer web.Close()
	socks := fakeSocks(t, map[string]string{"gem.test:80": web.Listener.Addr().String()})

	withGeminiURL(t, "http://gem.test/real")
	if g := checkGemini(socks); !g.OK {
		t.Errorf("real-shaped page judged %+v", g)
	}
	// With no marker in the part that was read, a far-away title proves nothing.
	withGeminiURL(t, "http://gem.test/other")
	if g := checkGemini(socks); g.OK || !strings.Contains(g.Reason, "unexpected page") {
		t.Errorf("a page without any marker in view must not count as OK: %+v", g)
	}
}
