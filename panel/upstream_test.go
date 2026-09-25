package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Real public keys/ids aren't needed for `xray run -test` beyond being
// well-formed, so these are syntactically valid placeholders.
const (
	testUUID      = "11111111-2222-3333-4444-555555555555"
	testRealityPK = "Z84J2IelR9ch3k8VtlVhhs5ycBUlXA7wHBWcBrjqnAw"
)

func sampleLinks() []string {
	vm, _ := json.Marshal(map[string]any{
		"v": "2", "ps": "x", "add": "1.2.3.4", "port": "443", "id": testUUID, "aid": "0",
		"net": "ws", "type": "none", "host": "cdn.example.com", "path": "/ws", "tls": "tls", "sni": "cdn.example.com",
	})
	return []string{
		"vless://" + testUUID + "@1.2.3.4:443?type=tcp&security=reality&sni=www.google.com&fp=chrome&pbk=" + testRealityPK + "&sid=12ab&flow=xtls-rprx-vision#r",
		"vless://" + testUUID + "@ex.com:443?type=ws&security=tls&path=%2Fws&host=ex.com#ws",
		"vless://" + testUUID + "@ex.com:443?type=grpc&security=tls&serviceName=gs&mode=multi#grpc",
		"vless://" + testUUID + "@ex.com:443?type=xhttp&security=tls&path=%2Fx&mode=auto#xhttp",
		"vless://" + testUUID + "@ex.com:80?type=httpupgrade&path=%2Fh&host=ex.com#hu",
		"vless://" + testUUID + "@ex.com:80?type=tcp&headerType=http&path=%2F&host=ex.com#tcphttp",
		"trojan://secret@ex.com:443?sni=ex.com&type=tcp#t",
		"vmess://" + base64.StdEncoding.EncodeToString(vm),
		"ss://" + base64.RawURLEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:secret")) + "@5.6.7.8:8388#ss",
		"ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:pw@5.6.7.8:8388")) + "#legacy",
	}
}

func TestParseV2RayOutbound(t *testing.T) {
	for _, l := range sampleLinks() {
		if _, err := parseV2RayOutbound(l); err != nil {
			t.Errorf("%s: %v", l, err)
		}
	}
	for _, bad := range []string{"http://x", "vless://@:1", "ss://zzz", "{}", `{"outbounds":[{"protocol":"freedom"}]}`} {
		if _, err := parseV2RayOutbound(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestValidateUpstreamProxyURL(t *testing.T) {
	for _, u := range []string{"socks5://127.0.0.1:1080", "http://u:p@h:3128", "socks4a://h:1"} {
		if err := validateUpstreamProxyURL(u); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	for _, u := range []string{"ftp://h:1", "socks5://h", "socks5://h:1/x"} {
		if err := validateUpstreamProxyURL(u); err == nil {
			t.Errorf("expected error for %q", u)
		}
	}
}

// TestGeneratedConfigsAcceptedByXray runs real `xray run -test` against
// every sample; only when XRAY_BIN points at a binary (set in CI).
func TestGeneratedConfigsAcceptedByXray(t *testing.T) {
	bin := os.Getenv("XRAY_BIN")
	if bin == "" {
		t.Skip("XRAY_BIN not set")
	}
	dir := t.TempDir()
	for i, l := range sampleLinks() {
		ob, err := parseV2RayOutbound(l)
		if err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		body, _ := json.Marshal(buildXrayConfig(ob))
		path := filepath.Join(dir, "c"+string(rune('a'+i))+".json")
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := xrayTestWith(bin, path); err != nil {
			t.Errorf("xray rejected config for %s: %v\n%s", l, err, out)
		}
	}
}
