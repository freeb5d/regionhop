package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
)

// Tunnel is one location/region entry managed by the panel.
type Tunnel struct {
	Name      string `json:"name"`
	Region    string `json:"region"`
	SocksPort int    `json:"socks_port"`
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,30}$`)

func validName(name string) bool {
	return namePattern.MatchString(name)
}

// regionCodes are the ISO-3166-1 alpha-2 codes Psiphon accepts as EgressRegion.
// Keep this allowlist so arbitrary strings can't be injected into the config.
var regionCodes = map[string]string{
	"":   "Any (no preference)",
	"AT": "Austria",
	"BE": "Belgium",
	"BG": "Bulgaria",
	"CH": "Switzerland",
	"CZ": "Czechia",
	"DE": "Germany",
	"DK": "Denmark",
	"EE": "Estonia",
	"FI": "Finland",
	"FR": "France",
	"GB": "United Kingdom",
	"IT": "Italy",
	"LT": "Lithuania",
	"NL": "Netherlands",
	"NO": "Norway",
	"PL": "Poland",
	"RO": "Romania",
	"RS": "Serbia",
	"SE": "Sweden",
	"ES": "Spain",
	"IE": "Ireland",
	"IN": "India",
	"ID": "Indonesia",
	"JP": "Japan",
	"SG": "Singapore",
	"CA": "Canada",
	"US": "United States",
	"AU": "Australia",
}

// portAvailable reports whether nothing on this machine is listening on the
// TCP port right now. regionhop shares its host with other software (3x-ui
// panels and inbounds, notably, which pick random ports that can land in
// 19000-19999), so a port not used by another location isn't necessarily
// free. A variable so tests can substitute it.
var portAvailable = func(p int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

func loadRegistry(path string) ([]Tunnel, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []Tunnel{}, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Tunnel
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func saveRegistry(path string, list []Tunnel) error {
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func findFreePort(existing []Tunnel, base, max int) (int, error) {
	used := map[int]bool{}
	for _, t := range existing {
		used[t.SocksPort] = true
	}
	for p := base; p <= max; p++ {
		if !used[p] && portAvailable(p) {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free port in range %d-%d", base, max)
}
