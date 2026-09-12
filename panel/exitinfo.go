package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// exitInfo is what a tunnel's SOCKS proxy reports about its own egress IP
// once it's actually connected — queried by making an HTTP request through
// the tunnel's own local SOCKS5 port, never by asking Psiphon or regionhop
// for it directly.
type exitInfo struct {
	IP          string
	Country     string
	CountryCode string
	CheckedAt   time.Time
	Refreshing  bool
}

const exitInfoTTL = 5 * time.Minute

var (
	exitInfoMu    sync.Mutex
	exitInfoCache = map[string]exitInfo{}
)

// exitInfoFor returns the cached exit info for a location (zero value if
// none yet) and, if the tunnel is active and the cache is missing or stale,
// kicks off a background refresh — never blocks the caller.
func exitInfoFor(name string, socksPort int, active bool) exitInfo {
	exitInfoMu.Lock()
	info, ok := exitInfoCache[name]
	stale := !ok || time.Since(info.CheckedAt) > exitInfoTTL
	alreadyRefreshing := ok && info.Refreshing
	if active && stale && !alreadyRefreshing {
		exitInfoCache[name] = exitInfo{Refreshing: true, CheckedAt: info.CheckedAt}
	}
	exitInfoMu.Unlock()

	if active && stale && !alreadyRefreshing {
		go refreshExitInfo(name, socksPort)
	}
	if !active {
		return exitInfo{}
	}
	return info
}

func refreshExitInfo(name string, socksPort int) {
	ip, country, code, err := queryExitInfo(socksPort)
	exitInfoMu.Lock()
	defer exitInfoMu.Unlock()
	if err != nil {
		// Keep whatever we had before (if anything) rather than blanking it
		// out on a transient failure; just clear Refreshing and back off by
		// leaving CheckedAt as-is only if we have no prior value.
		prev := exitInfoCache[name]
		prev.Refreshing = false
		if prev.IP == "" {
			prev.CheckedAt = time.Now().Add(-exitInfoTTL + 20*time.Second)
		}
		exitInfoCache[name] = prev
		return
	}
	exitInfoCache[name] = exitInfo{IP: ip, Country: country, CountryCode: code, CheckedAt: time.Now()}
}

type ipapiResponse struct {
	IP          string `json:"ip"`
	CountryName string `json:"country_name"`
	CountryCode string `json:"country_code"`
	Error       bool   `json:"error"`
	Reason      string `json:"reason"`
}

// queryExitInfo dials the tunnel's own local SOCKS5 proxy (127.0.0.1 only,
// same as every other connection through it) and asks a geolocation API
// what IP/country that traffic is exiting from.
func queryExitInfo(socksPort int) (ip, country, code string, err error) {
	dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", socksPort), nil, proxy.Direct)
	if err != nil {
		return "", "", "", err
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return "", "", "", fmt.Errorf("socks dialer does not support context")
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return contextDialer.DialContext(ctx, network, addr)
			},
		},
	}

	resp, err := client.Get("https://ipapi.co/json/")
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()

	var r ipapiResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", "", "", err
	}
	if r.Error {
		return "", "", "", fmt.Errorf("%s", r.Reason)
	}
	return r.IP, r.CountryName, r.CountryCode, nil
}

// countryFlag turns a 2-letter ISO country code into its flag emoji by
// mapping each letter to a Unicode regional indicator symbol.
func countryFlag(code string) string {
	if len(code) != 2 {
		return ""
	}
	a, b := code[0], code[1]
	if a < 'A' || a > 'Z' || b < 'A' || b > 'Z' {
		return ""
	}
	return string(rune(0x1F1E6+int(a-'A'))) + string(rune(0x1F1E6+int(b-'A')))
}
