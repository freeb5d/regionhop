package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// HTTPS for the panel. One port serves both: the first byte of each
// connection tells TLS (0x16) from plain HTTP, so turning SSL on or off needs
// no restart and old http:// bookmarks keep working (they're redirected to
// https while SSL is on).
//
// The certificate and key come from files the user names -- either typed in
// or read from the 3x-ui panel's settings. The panel runs unprivileged and
// such files are usually root-only, so a fixed root-owned script
// (install-cert.sh, run through sudo) validates them and copies them into
// sslDir, where the panel loads them. They're re-copied every 12 hours so a
// renewed certificate is picked up.
var (
	sslSettingsPath  = "/opt/psi-panel/data/ssl.json"
	sslDir           = "/opt/psi-panel/data/ssl"
	sslInstallScript = "/opt/regionhop-admin/install-cert.sh"
)

type sslSettings struct {
	Enabled   bool   `json:"enabled"`
	CertSrc   string `json:"cert_src,omitempty"`
	KeySrc    string `json:"key_src,omitempty"`
	Subject   string `json:"subject,omitempty"`
	Expires   string `json:"expires,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

var (
	sslMu      sync.Mutex // serializes settings file + cert installs
	sslEnabled atomic.Bool
	sslCert    atomic.Pointer[tls.Certificate]
)

func loadSSL() sslSettings {
	var s sslSettings
	if b, err := os.ReadFile(sslSettingsPath); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func saveSSL(s sslSettings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := sslSettingsPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, sslSettingsPath)
}

// loadSSLCert reads the installed pair from sslDir into memory.
func loadSSLCert() error {
	c, err := tls.LoadX509KeyPair(filepath.Join(sslDir, "cert.pem"), filepath.Join(sslDir, "key.pem"))
	if err != nil {
		return err
	}
	sslCert.Store(&c)
	return nil
}

func sslActive() bool { return sslEnabled.Load() && sslCert.Load() != nil }

func getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if c := sslCert.Load(); c != nil {
		return c, nil
	}
	return nil, fmt.Errorf("no certificate installed")
}

// installCert has the root helper validate and copy the pair, then loads it.
// Returns the helper's "subject=... / notAfter=..." lines.
func installCert(certPath, keyPath string) (subject, expires string, err error) {
	for _, p := range []string{certPath, keyPath} {
		if !filepath.IsAbs(p) || strings.ContainsAny(p, "\n\r\x00") {
			return "", "", fmt.Errorf("use full paths, like /root/cert/example.com/fullchain.pem")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, cerr := exec.CommandContext(ctx, "sudo", "-n", sslInstallScript, certPath, keyPath).CombinedOutput()
	if cerr != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = cerr.Error()
		}
		return "", "", fmt.Errorf("%s", msg)
	}
	if err := loadSSLCert(); err != nil {
		return "", "", err
	}
	for _, ln := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(ln, "subject="):
			subject = strings.TrimSpace(strings.TrimPrefix(ln, "subject="))
		case strings.HasPrefix(ln, "notAfter="):
			expires = strings.TrimSpace(strings.TrimPrefix(ln, "notAfter="))
		}
	}
	return subject, expires, nil
}

// startSSL restores the saved state at startup and keeps the copy fresh.
func startSSL() {
	s := loadSSL()
	if loadSSLCert() == nil {
		sslEnabled.Store(s.Enabled)
	}
	go func() {
		for range time.Tick(12 * time.Hour) {
			sslRefresh()
		}
	}()
}

func sslRefresh() {
	sslMu.Lock()
	defer sslMu.Unlock()
	s := loadSSL()
	if !s.Enabled || s.CertSrc == "" || s.KeySrc == "" {
		return
	}
	subj, exp, err := installCert(s.CertSrc, s.KeySrc)
	s.LastError = ""
	if err != nil {
		s.LastError = err.Error()
		log.Printf("ssl refresh: %v", err)
	} else {
		s.Subject, s.Expires = subj, exp
	}
	saveSSL(s)
}

// ---- one port, TLS and plain HTTP ----

type chanListener struct {
	ch   chan net.Conn
	addr net.Addr
	once sync.Once
	done chan struct{}
}

func newChanListener(a net.Addr) *chanListener {
	return &chanListener{ch: make(chan net.Conn, 64), addr: a, done: make(chan struct{})}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *chanListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *chanListener) Addr() net.Addr { return l.addr }

type peekConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// serveMixed listens on addr and hands TLS connections to tlsSrv and the rest
// to plainSrv. It blocks until the listener fails.
func serveMixed(addr string, plainSrv, tlsSrv *http.Server) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	plainL, tlsL := newChanListener(ln.Addr()), newChanListener(ln.Addr())
	go tlsSrv.Serve(tls.NewListener(tlsL, &tls.Config{GetCertificate: getCertificate, MinVersion: tls.VersionTLS12}))
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				plainL.Close()
				return
			}
			go func() {
				c.SetReadDeadline(time.Now().Add(10 * time.Second))
				br := bufio.NewReader(c)
				b, err := br.Peek(1)
				if err != nil {
					c.Close()
					return
				}
				c.SetReadDeadline(time.Time{})
				pc := &peekConn{Conn: c, r: br}
				target := plainL
				if b[0] == 0x16 {
					target = tlsL
				}
				select {
				case target.ch <- pc:
				default:
					c.Close()
				}
			}()
		}
	}()
	return plainSrv.Serve(plainL)
}

// redirectWhenSSL sends plain-HTTP requests to https while SSL is on.
func redirectWhenSSL(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sslActive() {
			http.Redirect(w, r, "https://"+r.Host+r.RequestURI, http.StatusPermanentRedirect)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ---- reading the paths from 3x-ui ----

func xuiCertPaths(s xuiSettings) (cert, key string, err error) {
	if s.URL == "" || s.Token == "" {
		return "", "", fmt.Errorf("set up the 3x-ui connection first (the 3x-ui section above)")
	}
	obj, err := xuiCall(s, "/panel/api/setting/all", url.Values{})
	if err != nil {
		return "", "", err
	}
	var v struct {
		WebCertFile string `json:"webCertFile"`
		WebKeyFile  string `json:"webKeyFile"`
	}
	if err := json.Unmarshal(unquoteJSON(obj), &v); err != nil {
		return "", "", fmt.Errorf("unexpected answer from 3x-ui")
	}
	if v.WebCertFile == "" || v.WebKeyFile == "" {
		return "", "", fmt.Errorf("3x-ui has no certificate configured (Settings → Panel → certificate paths)")
	}
	return v.WebCertFile, v.WebKeyFile, nil
}

// ---- handlers ----

type sslView struct {
	Enabled   bool
	Active    bool
	HasCert   bool
	CertSrc   string
	KeySrc    string
	Subject   string
	Expires   string
	LastError string
}

func sslTemplateData() sslView {
	s := loadSSL()
	return sslView{Enabled: s.Enabled, Active: sslActive(), HasCert: sslCert.Load() != nil,
		CertSrc: s.CertSrc, KeySrc: s.KeySrc, Subject: s.Subject, Expires: s.Expires, LastError: s.LastError}
}

func (a *app) handleSSL(w http.ResponseWriter, r *http.Request) {
	setLangCookie(w, r)
	if r.Method != http.MethodPost {
		http.Redirect(w, r, urlFor("/settings"), http.StatusSeeOther)
		return
	}
	limitBody(w, r, 64<<10)
	enable := r.FormValue("ssl_enabled") != ""
	cert, key := strings.TrimSpace(r.FormValue("ssl_cert")), strings.TrimSpace(r.FormValue("ssl_key"))

	sslMu.Lock()
	s := loadSSL()
	var err error
	if enable {
		switch {
		case cert != "" || key != "":
			if cert == "" || key == "" {
				err = fmt.Errorf("give both the certificate file and the key file")
				break
			}
			var subj, exp string
			if subj, exp, err = installCert(cert, key); err == nil {
				s.CertSrc, s.KeySrc, s.Subject, s.Expires, s.LastError = cert, key, subj, exp, ""
			}
		case sslCert.Load() == nil:
			err = fmt.Errorf("give the certificate file and the key file")
		}
	}
	if err == nil {
		s.Enabled = enable
		if err = saveSSL(s); err == nil {
			sslEnabled.Store(enable)
		}
	}
	sslMu.Unlock()

	data := credsTemplateData(a.creds, false, "")
	if err != nil {
		data["SSLError"] = err.Error()
	} else {
		data["SSLSaved"] = true
	}
	tmpl.ExecuteTemplate(w, "settings.html", withLang(r, data))
}

func (a *app) handleSSLFromXUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	cert, key, err := xuiCertPaths(loadXUI())
	res := map[string]any{"ok": err == nil}
	if err != nil {
		res["error"] = err.Error()
	} else {
		res["cert"], res["key"] = cert, key
	}
	writeJSON(w, res)
}
