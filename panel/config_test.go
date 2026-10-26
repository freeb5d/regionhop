package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureNoEstablishTimeout(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"EgressRegion":"NL"}`), 0o600)
	os.WriteFile(filepath.Join(dir, "b.json"), []byte(`{"EstablishTunnelTimeoutSeconds":60}`), 0o600)
	ensureNoEstablishTimeout(dir)
	for f, want := range map[string]string{"a.json": "0", "b.json": "60"} {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		cfg := map[string]any{}
		json.Unmarshal(b, &cfg)
		if got := fmt.Sprint(cfg["EstablishTunnelTimeoutSeconds"]); got != want {
			t.Errorf("%s: got %s want %s", f, got, want)
		}
	}
}
