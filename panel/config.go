package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const extraConfigPath = "/opt/psi-panel/panel/extra-config.json"

// psiphonCreds holds the user-supplied Psiphon config, applied to every new
// location. It's a raw JSON object pasted in from the user's own legitimate
// Psiphon deployment config (PropagationChannelId, SponsorId,
// RemoteServerListUrl, signature public keys, NetworkID, whatever else they
// have) — regionhop doesn't supply, validate the authenticity of, or know
// the meaning of any of it. It's merged into the generated config, but
// protected fields in writeTunnelConfig are applied after the merge and
// always win: no pasted JSON can move a SOCKS port off loopback or off the
// port allocator's assignment.
type psiphonCreds struct {
	ConfigJSON string
}

func credsFromEnv() psiphonCreds {
	return psiphonCreds{ConfigJSON: loadExtraConfigJSON()}
}

func loadExtraConfigJSON() string {
	b, err := os.ReadFile(extraConfigPath)
	if err != nil {
		return ""
	}
	return string(b)
}

func saveExtraConfigJSON(s string) error {
	if strings.TrimSpace(s) == "" {
		os.Remove(extraConfigPath)
		return nil
	}
	tmp := extraConfigPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(s), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, extraConfigPath)
}

// validateExtraConfigJSON requires either an empty string or a JSON object
// (not an array/scalar) so it can be merged as key/value pairs.
func validateExtraConfigJSON(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return fmt.Errorf("must be a JSON object: %w", err)
	}
	return nil
}

// writeTunnelConfig renders the Psiphon JSON config for one tunnel instance:
// the user's pasted config JSON merged with regionhop's own required and
// protected fields. Protected fields are applied last and always win, so
// pasted JSON can never move a SOCKS port off 127.0.0.1 or override the
// port allocator's assignment — that guarantee doesn't depend on the
// content of ConfigJSON at all.
func writeTunnelConfig(configsDir, dataDir string, t Tunnel, creds psiphonCreds) error {
	dir := filepath.Join(dataDir, t.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	cfg := map[string]any{}
	if strings.TrimSpace(creds.ConfigJSON) != "" {
		if err := json.Unmarshal([]byte(creds.ConfigJSON), &cfg); err != nil {
			return fmt.Errorf("invalid config JSON: %w", err)
		}
	}

	// Sane defaults — only if the user's JSON didn't already set them.
	setDefault(cfg, "DisableLocalHTTPProxy", true)
	setDefault(cfg, "EmitDiagnosticNotices", true)
	setDefault(cfg, "UseIndistinguishableTLS", true)

	// Protected: never overridable by pasted JSON.
	cfg["EgressRegion"] = t.Region
	cfg["LocalSocksProxyPort"] = t.SocksPort
	cfg["ListenInterface"] = "lo"
	cfg["DisableLocalSocksProxy"] = false
	cfg["DataRootDirectory"] = dir

	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(configsDir, t.Name+".json")
	return os.WriteFile(path, body, 0o600)
}

func setDefault(m map[string]any, key string, val any) {
	if _, ok := m[key]; !ok {
		m[key] = val
	}
}

func removeTunnelConfig(configsDir string, name string) error {
	return os.Remove(filepath.Join(configsDir, name+".json"))
}
