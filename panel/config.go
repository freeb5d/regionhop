package main

import (
	"fmt"
	"os"
	"path/filepath"
)

const psiphonConfigTemplate = `{
  "PropagationChannelId": %q,
  "SponsorId": %q,
  "EgressRegion": %q,
  "LocalSocksProxyPort": %d,
  "ListenInterface": "lo",
  "DisableLocalHTTPProxy": true,
  "DisableLocalSocksProxy": false,
  "DataRootDirectory": %q,
  "EmitDiagnosticNotices": true,
  "UseIndistinguishableTLS": true
}
`

// writeTunnelConfig renders the Psiphon JSON config for one tunnel instance.
// LocalSocksProxyPort + ListenInterface "lo" is what keeps the SOCKS listener
// bound to 127.0.0.1: this value is never taken from user input, only from
// the port allocator, so the panel can't be used to open the proxy externally.
func writeTunnelConfig(configsDir, dataDir string, t Tunnel, propagationChannelID, sponsorID string) error {
	dir := filepath.Join(dataDir, t.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	body := fmt.Sprintf(psiphonConfigTemplate, propagationChannelID, sponsorID, t.Region, t.SocksPort, dir)
	path := filepath.Join(configsDir, t.Name+".json")
	return os.WriteFile(path, []byte(body), 0o600)
}

func removeTunnelConfig(configsDir string, name string) error {
	return os.Remove(filepath.Join(configsDir, name+".json"))
}
