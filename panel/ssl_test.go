package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func selfSigned(t *testing.T) *tls.Certificate {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}
}

func TestMixedPortServesTLSAndRedirectsPlain(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "hello") })
	srv := func(h http.Handler) *http.Server { return &http.Server{Addr: addr, Handler: h} }
	go serveMixed(addr, srv(redirectWhenSSL(h)), srv(h))
	time.Sleep(200 * time.Millisecond)

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

	// SSL off: plain works.
	sslEnabled.Store(false)
	sslCert.Store(nil)
	resp, err := noFollow.Get("http://" + addr + "/x")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(resp.Body); string(b) != "hello" {
		t.Fatalf("plain with SSL off: %q", b)
	}

	// SSL on: plain is redirected, TLS is served, on the same port.
	sslCert.Store(selfSigned(t))
	sslEnabled.Store(true)
	t.Cleanup(func() { sslEnabled.Store(false); sslCert.Store(nil) })
	resp, err = noFollow.Get("http://" + addr + "/x?a=1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != "https://"+addr+"/x?a=1" {
		t.Fatalf("redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, err = noFollow.Get("https://" + addr + "/x")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(resp.Body); string(b) != "hello" {
		t.Fatalf("https: %q", b)
	}
}

func TestXUICertPaths(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/b/panel/api/setting/all" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"success":true,"obj":{"webCertFile":"/root/cert/x/fullchain.pem","webKeyFile":"/root/cert/x/privkey.pem"}}`))
	}))
	defer srv.Close()
	c, k, err := xuiCertPaths(xuiSettings{URL: srv.URL + "/b", Token: "tok"})
	if err != nil || c != "/root/cert/x/fullchain.pem" || k != "/root/cert/x/privkey.pem" {
		t.Fatalf("%q %q %v", c, k, err)
	}
	if _, _, err := xuiCertPaths(xuiSettings{}); err == nil || !strings.Contains(err.Error(), "3x-ui") {
		t.Fatalf("unconfigured: %v", err)
	}
}

func TestInstallCertRejectsRelativePaths(t *testing.T) {
	if _, _, err := installCert("cert.pem", "/k.pem"); err == nil {
		t.Fatal("relative path must be rejected")
	}
}
