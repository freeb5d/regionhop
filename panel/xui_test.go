package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeXUI mimics the two 3x-ui endpoints regionhop uses.
type fakeXUI struct {
	cfg     string // xray config JSON
	updates int
	srv     *httptest.Server
}

func newFakeXUI(t *testing.T, cfg string) *fakeXUI {
	f := &fakeXUI{cfg: cfg}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			w.Write([]byte(`{"success":false,"msg":"unauthorized"}`))
			return
		}
		switch r.URL.Path {
		case "/base/panel/api/xray/":
			obj, _ := json.Marshal(map[string]any{"xraySetting": json.RawMessage(f.cfg), "outboundTestUrl": "https://t.example/204"})
			s, _ := json.Marshal(string(obj)) // 3x-ui sends obj as a JSON string
			w.Write([]byte(`{"success":true,"msg":"","obj":` + string(s) + `}`))
		case "/base/panel/api/xray/update":
			r.ParseForm()
			f.cfg = r.PostForm.Get("xraySetting")
			if r.PostForm.Get("outboundTestUrl") != "https://t.example/204" {
				t.Errorf("test URL not preserved")
			}
			f.updates++
			w.Write([]byte(`{"success":true,"msg":"ok"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func tags(t *testing.T, cfg string) []string {
	var c struct{ Outbounds []struct{ Tag string } }
	if err := json.Unmarshal([]byte(cfg), &c); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, o := range c.Outbounds {
		out = append(out, o.Tag)
	}
	return out
}

func TestXUIApply(t *testing.T) {
	f := newFakeXUI(t, `{"log":{"loglevel":"warning"},"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"regionhop-old","protocol":"socks","settings":{"address":"127.0.0.1","port":1}},{"tag":"mine","protocol":"vless"}]}`)
	s := xuiSettings{Enabled: true, URL: f.srv.URL + "/base", Token: "tok"}

	changed, err := xuiApply(&s, map[string]int{"de": 19000, "us": 19001})
	if err != nil || !changed {
		t.Fatalf("first apply: %v %v", changed, err)
	}
	got := strings.Join(tags(t, f.cfg), ",")
	if !strings.HasPrefix(got, "direct,mine,") || strings.Contains(got, "old") || !strings.Contains(got, "regionhop-de") || !strings.Contains(got, "regionhop-us") {
		t.Fatalf("outbounds: %s", got)
	}
	if !strings.Contains(f.cfg, `"loglevel":"warning"`) {
		t.Fatalf("other config lost: %s", f.cfg)
	}

	// Same state again: nothing may be written (a save restarts 3x-ui's Xray).
	if changed, err = xuiApply(&s, map[string]int{"de": 19000, "us": 19001}); err != nil || changed || f.updates != 1 {
		t.Fatalf("second apply: changed=%v err=%v updates=%d", changed, err, f.updates)
	}
	// A moved port is corrected, a removed location is dropped.
	if changed, err = xuiApply(&s, map[string]int{"de": 19005}); err != nil || !changed {
		t.Fatalf("third apply: %v %v", changed, err)
	}
	if got := strings.Join(tags(t, f.cfg), ","); strings.Contains(got, "us") || !strings.Contains(f.cfg, "19005") {
		t.Fatalf("after change: %s", f.cfg)
	}
}

func TestXUIBadToken(t *testing.T) {
	f := newFakeXUI(t, `{"outbounds":[]}`)
	_, _, err := xuiGetConfig(&xuiSettings{URL: f.srv.URL + "/base", Token: "nope"})
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("want unauthorized, got %v", err)
	}
	_, _, err = xuiGetConfig(&xuiSettings{URL: f.srv.URL + "/wrong", Token: "tok"})
	if err == nil {
		t.Fatal("wrong path should fail")
	}
}

func TestNormalizeXUIURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:2053/abc/": "http://127.0.0.1:2053/abc",
		"https://h:1/abc/panel/":     "https://h:1/abc",
		" http://127.0.0.1:2053 ":    "http://127.0.0.1:2053",
	} {
		if got, err := normalizeXUIURL(in); err != nil || got != want {
			t.Errorf("%q -> %q, %v", in, got, err)
		}
	}
	if _, err := normalizeXUIURL("127.0.0.1:2053"); err == nil {
		t.Error("scheme-less address should be rejected")
	}
}

func TestSettingsPageRendersXUICard(t *testing.T) {
	for _, lang := range []string{"en", "fa", "ar", "ru", "zh"} {
		r := httptest.NewRequest("GET", "/settings?lang="+lang, nil)
		w := httptest.NewRecorder()
		data := withLang(r, credsTemplateData(psiphonCreds{}, false, ""))
		data["XUI"] = xuiView{Enabled: true, URL: "http://127.0.0.1:2053/p", HasToken: true, LastSync: "2026-10-20 10:00", LastError: "boom"}
		if err := tmpl.ExecuteTemplate(w, "settings.html", data); err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
		body := w.Body.String()
		for _, want := range []string{`id="xui-form"`, `value="http://127.0.0.1:2053/p"`, `/settings/xui/sync`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %q", lang, want)
			}
		}
		if strings.Contains(body, "tok") && strings.Contains(body, `name="xui_token" value=`) {
			t.Errorf("%s: token must never be sent back to the page", lang)
		}
	}
}

// 3x-ui with SSL answers plain http with a redirect to https on the same
// address; the call must follow that once and keep the https address.
func TestXUIUpgradesToHTTPS(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	fake := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"success":true,"obj":{"webCertFile":"/c","webKeyFile":"/k"}}`))
	})
	srv := func(h http.Handler) *http.Server { return &http.Server{Addr: addr, Handler: h} }
	go serveMixed(addr, srv(redirectWhenSSL(fake)), srv(fake))
	time.Sleep(200 * time.Millisecond)
	sslCert.Store(selfSigned(t))
	sslEnabled.Store(true)
	t.Cleanup(func() { sslEnabled.Store(false); sslCert.Store(nil) })

	s := xuiSettings{URL: "http://" + addr + "/base", Token: "tok"}
	c, k, err := xuiCertPaths(&s)
	if err != nil || c != "/c" || k != "/k" {
		t.Fatalf("%q %q %v", c, k, err)
	}
	if !strings.HasPrefix(s.URL, "https://"+addr) {
		t.Fatalf("URL not upgraded: %s", s.URL)
	}
}

// A request 3x-ui can't authenticate must produce a readable error, not a bare status code.
func TestXUIRedirectAndUnauthorizedMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/redir") {
			http.Redirect(w, r, "/login/", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(401)
		w.Write([]byte(`{"success":false,"msg":"Please login again"}`))
	}))
	defer srv.Close()
	_, _, err := xuiGetConfig(&xuiSettings{URL: srv.URL + "/redir", Token: "x"})
	if err == nil || !strings.Contains(err.Error(), "redirected") || !strings.Contains(err.Error(), "/login/") {
		t.Fatalf("redirect: %v", err)
	}
	_, _, err = xuiGetConfig(&xuiSettings{URL: srv.URL, Token: "x"})
	if err == nil || !strings.Contains(err.Error(), "token was not accepted") {
		t.Fatalf("401: %v", err)
	}
}
