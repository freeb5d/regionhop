package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Upstream: an optional hop every ConsoleClient dials Psiphon's servers
// through, for hosts whose own network blocks or throttles direct
// connections. Psiphon's core supports this natively via UpstreamProxyUrl.
// "proxy" mode points that straight at a SOCKS/HTTP proxy; "v2ray" mode
// runs a bundled Xray (regionhop-upstream.service) with the user's outbound
// and a loopback-only SOCKS inbound, and points UpstreamProxyUrl at that.
const (
	upstreamSettingsPath = "/opt/psi-panel/data/upstream.json"
	upstreamXrayConfig   = "/opt/psi-panel/data/upstream-xray.json"
	xrayBinary           = "/opt/psi-panel/core/xray"
	upstreamUnit         = "regionhop-upstream.service"
	// Outside the tunnels' 19000-19999 range; loopback-only either way.
	upstreamXrayPort = 18999
)

const (
	upstreamNone  = "none"
	upstreamProxy = "proxy"
	upstreamV2Ray = "v2ray"
)

type upstreamSettings struct {
	Mode     string `json:"mode"`
	ProxyURL string `json:"proxy_url,omitempty"`
	V2Ray    string `json:"v2ray,omitempty"` // share link or Xray outbound JSON
}

func (u upstreamSettings) effectiveProxyURL() string {
	switch u.Mode {
	case upstreamProxy:
		return u.ProxyURL
	case upstreamV2Ray:
		return fmt.Sprintf("socks5://127.0.0.1:%d", upstreamXrayPort)
	}
	return ""
}

func loadUpstream() upstreamSettings {
	s := upstreamSettings{Mode: upstreamNone}
	b, err := os.ReadFile(upstreamSettingsPath)
	if err != nil {
		return s
	}
	if json.Unmarshal(b, &s) != nil || s.Mode == "" {
		s.Mode = upstreamNone
	}
	return s
}

func saveUpstream(s upstreamSettings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := upstreamSettingsPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, upstreamSettingsPath)
}

func normalizeUpstream(s upstreamSettings) (upstreamSettings, error) {
	s.ProxyURL = strings.TrimSpace(s.ProxyURL)
	s.V2Ray = strings.TrimSpace(s.V2Ray)
	switch s.Mode {
	case "", upstreamNone:
		s.Mode = upstreamNone
	case upstreamProxy:
		if s.ProxyURL == "" {
			return s, fmt.Errorf("proxy URL is required")
		}
		if err := validateUpstreamProxyURL(s.ProxyURL); err != nil {
			return s, err
		}
	case upstreamV2Ray:
		if s.V2Ray == "" {
			return s, fmt.Errorf("V2Ray link or config is required")
		}
		if _, err := parseV2RayOutbound(s.V2Ray); err != nil {
			return s, err
		}
	default:
		return s, fmt.Errorf("unknown upstream mode %q", s.Mode)
	}
	return s, nil
}

// applyUpstreamService brings regionhop-upstream.service in line with s:
// writes and syntax-checks the Xray config and (re)starts it in v2ray mode,
// stops and disables it otherwise.
func applyUpstreamService(s upstreamSettings) error {
	if s.Mode != upstreamV2Ray {
		// The config file only exists if v2ray mode was ever applied here.
		if _, err := os.Stat(upstreamXrayConfig); err != nil {
			return nil
		}
		if out, err := runSystemctlPrivileged("disable", "--now", upstreamUnit); err != nil {
			return wrapErr(out, err)
		}
		return os.Remove(upstreamXrayConfig)
	}
	if _, err := os.Stat(xrayBinary); err != nil {
		return fmt.Errorf("Xray is not installed on this server (no prebuilt bundle for this architecture) — use SOCKS/HTTP mode instead")
	}
	outbound, err := parseV2RayOutbound(s.V2Ray)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(buildXrayConfig(outbound), "", "  ")
	if err != nil {
		return err
	}
	// Must end in .json: Xray picks the config format from the extension.
	tmp := strings.TrimSuffix(upstreamXrayConfig, ".json") + ".new.json"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	if out, err := xrayTest(tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("Xray rejected the config: %s", strings.TrimSpace(lastLines(out, 3)))
	}
	if err := os.Rename(tmp, upstreamXrayConfig); err != nil {
		return err
	}
	if out, err := runSystemctlPrivileged("enable", "--now", upstreamUnit); err != nil {
		return wrapErr(out, err)
	}
	out, err := runSystemctlPrivileged("restart", upstreamUnit)
	return wrapErr(out, err)
}

// applyUpstream makes s the live upstream: service first (so a rejected
// Xray config leaves everything as it was), then settings, then every
// location's config rewritten and running tunnels restarted to pick it up.
// Caller must hold a.mu and pass an already-normalized s.
func (a *app) applyUpstream(s upstreamSettings) error {
	if err := applyUpstreamService(s); err != nil {
		return err
	}
	if err := saveUpstream(s); err != nil {
		return err
	}
	a.creds.UpstreamProxyURL = s.effectiveProxyURL()

	list, err := loadRegistry(registryPath)
	if err != nil {
		return err
	}
	for _, t := range list {
		if err := writeTunnelConfig(configsDir, dataDir, t, a.creds); err != nil {
			return fmt.Errorf("%s: %w", t.Name, err)
		}
		if st := tunnelStatus(t.Name); st == "active" || st == "activating" {
			if err := restartTunnel(t.Name); err != nil {
				return fmt.Errorf("restart %s: %w", t.Name, err)
			}
		}
	}
	return nil
}

func buildXrayConfig(outbound map[string]any) map[string]any {
	return map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": "psiphon-in", "listen": "127.0.0.1", "port": upstreamXrayPort,
			"protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{outbound},
	}
}

func xrayTest(path string) (string, error) {
	return xrayTestWith(xrayBinary, path)
}

func xrayTestWith(bin, path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "run", "-test", "-c", path).CombinedOutput()
	return string(out), err
}

func upstreamServiceState() string {
	out, _ := runSystemctl("is-active", upstreamUnit)
	return strings.TrimSpace(out)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// parseV2RayOutbound turns a share link (vless/vmess/trojan/ss) or pasted
// Xray JSON (a single outbound object, or a full config with "outbounds")
// into one Xray outbound object tagged "proxy".
func parseV2RayOutbound(in string) (map[string]any, error) {
	in = strings.TrimSpace(in)
	var ob map[string]any
	var err error
	switch {
	case strings.HasPrefix(in, "{"):
		ob, err = outboundFromJSON(in)
	case strings.HasPrefix(in, "vless://"):
		ob, err = parseVLESSOrTrojan(in, "vless")
	case strings.HasPrefix(in, "trojan://"):
		ob, err = parseVLESSOrTrojan(in, "trojan")
	case strings.HasPrefix(in, "vmess://"):
		ob, err = parseVMess(in)
	case strings.HasPrefix(in, "ss://"):
		ob, err = parseShadowsocks(in)
	default:
		return nil, fmt.Errorf("unsupported V2Ray input: paste a vless://, vmess://, trojan:// or ss:// link, or Xray outbound JSON")
	}
	if err != nil {
		return nil, err
	}
	ob["tag"] = "proxy"
	return ob, nil
}

func outboundFromJSON(in string) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal([]byte(in), &m); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if obs, ok := m["outbounds"].([]any); ok {
		for _, o := range obs {
			om, ok := o.(map[string]any)
			if !ok {
				continue
			}
			switch om["protocol"] {
			case "freedom", "blackhole", "dns", nil:
				continue
			}
			return om, nil
		}
		return nil, fmt.Errorf("no proxy outbound found in \"outbounds\"")
	}
	if _, ok := m["protocol"].(string); !ok {
		return nil, fmt.Errorf("JSON must be an Xray outbound (with \"protocol\") or a config with \"outbounds\"")
	}
	return m, nil
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return p, nil
}

func parseVLESSOrTrojan(link, proto string) (map[string]any, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("invalid %s link: %w", proto, err)
	}
	if u.User == nil || u.User.Username() == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid %s link: missing id/password or host", proto)
	}
	port, err := parsePort(u.Port())
	if err != nil {
		return nil, err
	}
	q := u.Query()
	var settings map[string]any
	if proto == "vless" {
		user := map[string]any{"id": u.User.Username(), "encryption": firstNonEmpty(q.Get("encryption"), "none")}
		if f := q.Get("flow"); f != "" {
			user["flow"] = f
		}
		settings = map[string]any{"vnext": []any{map[string]any{
			"address": u.Hostname(), "port": port, "users": []any{user},
		}}}
	} else {
		settings = map[string]any{"servers": []any{map[string]any{
			"address": u.Hostname(), "port": port, "password": u.User.Username(),
		}}}
	}
	defaultSec := "none"
	if proto == "trojan" {
		defaultSec = "tls"
	}
	stream := buildStream(streamParams{
		network: firstNonEmpty(q.Get("type"), "tcp"), security: firstNonEmpty(q.Get("security"), defaultSec),
		sni: q.Get("sni"), fp: q.Get("fp"), alpn: q.Get("alpn"), allowInsecure: q.Get("allowInsecure") == "1",
		pbk: q.Get("pbk"), sid: q.Get("sid"), spx: q.Get("spx"),
		path: q.Get("path"), host: q.Get("host"), headerType: q.Get("headerType"),
		serviceName: q.Get("serviceName"), mode: q.Get("mode"), seed: q.Get("seed"),
		defaultHost: u.Hostname(),
	})
	return map[string]any{"protocol": proto, "settings": settings, "streamSettings": stream}, nil
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("invalid base64")
}

func parseVMess(link string) (map[string]any, error) {
	raw, err := decodeB64(strings.TrimPrefix(link, "vmess://"))
	if err != nil {
		return nil, fmt.Errorf("invalid vmess link: %w", err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("invalid vmess link: %w", err)
	}
	str := func(k string) string {
		switch x := v[k].(type) {
		case string:
			return x
		case float64:
			return strconv.Itoa(int(x))
		}
		return ""
	}
	if str("add") == "" || str("id") == "" {
		return nil, fmt.Errorf("invalid vmess link: missing address or id")
	}
	port, err := parsePort(str("port"))
	if err != nil {
		return nil, err
	}
	aid, _ := strconv.Atoi(firstNonEmpty(str("aid"), "0"))
	security := "none"
	if t := str("tls"); t == "tls" || t == "reality" {
		security = t
	}
	stream := buildStream(streamParams{
		network: firstNonEmpty(str("net"), "tcp"), security: security,
		sni: str("sni"), fp: str("fp"), alpn: str("alpn"),
		path: str("path"), host: str("host"), headerType: str("type"),
		serviceName: str("path"), defaultHost: str("add"),
	})
	return map[string]any{
		"protocol": "vmess",
		"settings": map[string]any{"vnext": []any{map[string]any{
			"address": str("add"), "port": port,
			"users": []any{map[string]any{"id": str("id"), "alterId": aid, "security": firstNonEmpty(str("scy"), "auto")}},
		}}},
		"streamSettings": stream,
	}, nil
}

// ss://BASE64(method:password)@host:port#name (SIP002), or the legacy
// ss://BASE64(method:password@host:port)#name.
func parseShadowsocks(link string) (map[string]any, error) {
	body := strings.TrimPrefix(link, "ss://")
	if i := strings.IndexByte(body, '#'); i >= 0 {
		body = body[:i]
	}
	if i := strings.IndexAny(body, "/?"); i >= 0 {
		body = body[:i]
	}
	var method, password, hostport string
	if at := strings.LastIndexByte(body, '@'); at >= 0 {
		userinfo, _ := url.PathUnescape(body[:at])
		hostport = body[at+1:]
		if dec, err := decodeB64(userinfo); err == nil && strings.Contains(string(dec), ":") {
			userinfo = string(dec)
		}
		method, password, _ = strings.Cut(userinfo, ":")
	} else {
		dec, err := decodeB64(body)
		if err != nil {
			return nil, fmt.Errorf("invalid ss link: %w", err)
		}
		cred, hp, ok := strings.Cut(string(dec), "@")
		if !ok {
			return nil, fmt.Errorf("invalid ss link")
		}
		method, password, _ = strings.Cut(cred, ":")
		hostport = hp
	}
	u, err := url.Parse("ss://" + hostport)
	if err != nil || u.Hostname() == "" || method == "" || password == "" {
		return nil, fmt.Errorf("invalid ss link")
	}
	port, err := parsePort(u.Port())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"protocol": "shadowsocks",
		"settings": map[string]any{"servers": []any{map[string]any{
			"address": u.Hostname(), "port": port, "method": method, "password": password,
		}}},
	}, nil
}

type streamParams struct {
	network, security, sni, fp, alpn string
	allowInsecure                    bool
	pbk, sid, spx                    string
	path, host, headerType           string
	serviceName, mode, seed          string
	defaultHost                      string
}

func buildStream(p streamParams) map[string]any {
	network := p.network
	switch network {
	case "h2":
		network = "http"
	case "splithttp":
		network = "xhttp"
	}
	s := map[string]any{"network": network, "security": p.security}
	switch network {
	case "ws":
		s["wsSettings"] = map[string]any{"path": firstNonEmpty(p.path, "/"), "host": p.host}
	case "grpc":
		s["grpcSettings"] = map[string]any{"serviceName": p.serviceName, "multiMode": p.mode == "multi"}
	case "httpupgrade":
		s["httpupgradeSettings"] = map[string]any{"path": firstNonEmpty(p.path, "/"), "host": p.host}
	case "xhttp":
		x := map[string]any{"path": firstNonEmpty(p.path, "/"), "host": p.host}
		if p.mode != "" {
			x["mode"] = p.mode
		}
		s["xhttpSettings"] = x
	case "http":
		h := map[string]any{"path": firstNonEmpty(p.path, "/")}
		if p.host != "" {
			h["host"] = strings.Split(p.host, ",")
		}
		s["httpSettings"] = h
	case "kcp":
		k := map[string]any{"header": map[string]any{"type": firstNonEmpty(p.headerType, "none")}}
		if p.seed != "" {
			k["seed"] = p.seed
		}
		s["kcpSettings"] = k
	case "tcp":
		if p.headerType == "http" {
			req := map[string]any{"path": []any{firstNonEmpty(p.path, "/")}}
			if p.host != "" {
				req["headers"] = map[string]any{"Host": strings.Split(p.host, ",")}
			}
			s["tcpSettings"] = map[string]any{"header": map[string]any{"type": "http", "request": req}}
		}
	}
	sni := firstNonEmpty(p.sni, p.host, p.defaultHost)
	switch p.security {
	case "tls":
		t := map[string]any{"serverName": sni, "allowInsecure": p.allowInsecure}
		if p.fp != "" {
			t["fingerprint"] = p.fp
		}
		if p.alpn != "" {
			t["alpn"] = strings.Split(p.alpn, ",")
		}
		s["tlsSettings"] = t
	case "reality":
		s["realitySettings"] = map[string]any{
			"serverName": sni, "fingerprint": firstNonEmpty(p.fp, "chrome"),
			"publicKey": p.pbk, "shortId": p.sid, "spiderX": p.spx,
		}
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
