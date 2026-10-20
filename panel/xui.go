package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// 3x-ui integration: with an API token from the 3x-ui panel (Settings ->
// Security -> API Token) regionhop keeps one SOCKS outbound per location in
// 3x-ui's Xray config, tagged "regionhop-<name>" and pointing at
// 127.0.0.1:<socks port>, so routing rules there can use it right away.
// Only outbounds with that tag prefix are ever touched. This assumes 3x-ui
// runs on the same server (the SOCKS ports are loopback-only).
const (
	xuiSettingsPath = "/opt/psi-panel/data/xui.json"
	xuiTagPrefix    = "regionhop-"
)

type xuiSettings struct {
	Enabled   bool   `json:"enabled"`
	URL       string `json:"url"` // panel base, including its web base path
	Token     string `json:"token"`
	LastSync  int64  `json:"last_sync,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// xuiMu serializes every read-modify-write of 3x-ui's config (and of the
// settings file), so concurrent syncs can't overwrite each other.
var xuiMu sync.Mutex

func loadXUI() xuiSettings {
	var s xuiSettings
	if b, err := os.ReadFile(xuiSettingsPath); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func saveXUI(s xuiSettings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := xuiSettingsPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, xuiSettingsPath)
}

// normalizeXUIURL accepts what people paste (a trailing slash, or the
// /panel page itself) and returns the base the API paths are appended to.
func normalizeXUIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("enter the 3x-ui panel address, like http://127.0.0.1:2053/yourpath")
	}
	u.RawQuery, u.Fragment = "", ""
	p := strings.TrimRight(u.Path, "/")
	p = strings.TrimSuffix(p, "/panel")
	u.Path = p
	return u.String(), nil
}

func xuiClient(base string) *http.Client {
	tr := &http.Transport{}
	if u, err := url.Parse(base); err == nil {
		h := u.Hostname()
		// 3x-ui is often served with a self-signed certificate. That's only
		// accepted for loopback, where there is no one to impersonate it.
		if ip := net.ParseIP(h); h == "localhost" || (ip != nil && ip.IsLoopback()) {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
	}
	return &http.Client{
		Timeout:       20 * time.Second,
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// xuiCall POSTs a form to a 3x-ui API path with the Bearer token and returns
// the "obj" of the {success,msg,obj} envelope.
func xuiCall(s xuiSettings, path string, form url.Values) (json.RawMessage, error) {
	req, err := http.NewRequest(http.MethodPost, s.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := xuiClient(s.URL).Do(req)
	if err != nil {
		return nil, fmt.Errorf("can't reach 3x-ui: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var env struct {
		Success bool            `json:"success"`
		Msg     string          `json:"msg"`
		Obj     json.RawMessage `json:"obj"`
	}
	if json.Unmarshal(body, &env) != nil {
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode/100 == 3 {
			return nil, fmt.Errorf("3x-ui answered %d: check the address and its web path, and that 3x-ui is a recent version with API tokens", resp.StatusCode)
		}
		return nil, fmt.Errorf("that isn't a 3x-ui API response (HTTP %d): check the address and the token", resp.StatusCode)
	}
	if !env.Success {
		if env.Msg == "" {
			env.Msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("3x-ui refused the request: %s", env.Msg)
	}
	return env.Obj, nil
}

// unquoteJSON returns the JSON inside a value that 3x-ui sometimes sends as a
// JSON string holding JSON.
func unquoteJSON(raw json.RawMessage) json.RawMessage {
	var str string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &str) == nil {
		return json.RawMessage(str)
	}
	return raw
}

// xuiGetConfig reads the Xray config template and the outbound test URL.
func xuiGetConfig(s xuiSettings) (map[string]json.RawMessage, string, error) {
	obj, err := xuiCall(s, "/panel/api/xray/", url.Values{})
	if err != nil {
		return nil, "", err
	}
	var resp struct {
		XraySetting     json.RawMessage `json:"xraySetting"`
		OutboundTestURL string          `json:"outboundTestUrl"`
	}
	if err := json.Unmarshal(unquoteJSON(obj), &resp); err != nil || len(resp.XraySetting) == 0 {
		return nil, "", fmt.Errorf("unexpected answer from 3x-ui when reading its Xray config")
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(unquoteJSON(resp.XraySetting), &cfg); err != nil {
		return nil, "", fmt.Errorf("unexpected Xray config format from 3x-ui")
	}
	return cfg, resp.OutboundTestURL, nil
}

func xuiTag(name string) string { return xuiTagPrefix + name }

func xuiOutbound(name string, port int) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"tag":      xuiTag(name),
		"protocol": "socks",
		"settings": map[string]any{"address": "127.0.0.1", "port": port},
	})
	return b
}

func outboundMatches(raw json.RawMessage, port int) bool {
	var o struct {
		Protocol string `json:"protocol"`
		Settings struct {
			Address string `json:"address"`
			Port    int    `json:"port"`
		} `json:"settings"`
	}
	return json.Unmarshal(raw, &o) == nil && o.Protocol == "socks" &&
		o.Settings.Address == "127.0.0.1" && o.Settings.Port == port
}

// mergeOutbounds returns outbounds with exactly one "regionhop-*" entry per
// wanted location (name -> port): missing ones are appended (never inserted
// first, since the first outbound is Xray's default route), wrong ones
// replaced, stale ones dropped. Everything else is kept untouched.
func mergeOutbounds(existing []json.RawMessage, want map[string]int) ([]json.RawMessage, bool) {
	out := make([]json.RawMessage, 0, len(existing)+len(want))
	have := map[string]bool{}
	changed := false
	for _, raw := range existing {
		var o struct {
			Tag string `json:"tag"`
		}
		json.Unmarshal(raw, &o)
		if !strings.HasPrefix(o.Tag, xuiTagPrefix) {
			out = append(out, raw)
			continue
		}
		name := strings.TrimPrefix(o.Tag, xuiTagPrefix)
		port, ok := want[name]
		switch {
		case !ok || have[name]:
			changed = true // stale or duplicate
		case outboundMatches(raw, port):
			have[name] = true
			out = append(out, raw)
		default:
			have[name] = true
			out = append(out, xuiOutbound(name, port))
			changed = true
		}
	}
	for name, port := range want {
		if !have[name] {
			out = append(out, xuiOutbound(name, port))
			changed = true
		}
	}
	return out, changed
}

// xuiApply makes 3x-ui's outbounds match want. It writes only when something
// differs, because saving restarts 3x-ui's Xray.
func xuiApply(s xuiSettings, want map[string]int) (bool, error) {
	cfg, testURL, err := xuiGetConfig(s)
	if err != nil {
		return false, err
	}
	var existing []json.RawMessage
	if raw, ok := cfg["outbounds"]; ok {
		if err := json.Unmarshal(raw, &existing); err != nil {
			return false, fmt.Errorf("unexpected outbounds format in the 3x-ui config")
		}
	}
	merged, changed := mergeOutbounds(existing, want)
	if !changed {
		return false, nil
	}
	cfg["outbounds"], _ = json.Marshal(merged)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cfg); err != nil {
		return false, err
	}
	form := url.Values{"xraySetting": {strings.TrimSpace(buf.String())}}
	if testURL != "" {
		form.Set("outboundTestUrl", testURL)
	}
	if _, err := xuiCall(s, "/panel/api/xray/update", form); err != nil {
		return false, err
	}
	return true, nil
}

// xuiSync brings 3x-ui in line with the current locations and records the
// result in the settings file. A no-op when the integration is off.
func xuiSync() (changed bool, err error) {
	xuiMu.Lock()
	defer xuiMu.Unlock()
	s := loadXUI()
	if !s.Enabled {
		return false, nil
	}
	defer func() {
		s.LastSync = time.Now().Unix()
		s.LastError = ""
		if err != nil {
			s.LastError = err.Error()
		}
		saveXUI(s)
	}()
	list, lerr := loadRegistry(registryPath)
	if lerr != nil {
		return false, lerr
	}
	want := make(map[string]int, len(list))
	for _, t := range list {
		want[t.Name] = t.SocksPort
	}
	return xuiApply(s, want)
}

// xuiSyncAsync is what add/remove/startup call: it must never slow down or
// fail the request that triggered it.
func xuiSyncAsync() {
	go func() {
		if _, err := xuiSync(); err != nil {
			log.Printf("3x-ui sync: %v", err)
		}
	}()
}
