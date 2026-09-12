package main

import (
	"bufio"
	"os"
	"strings"
)

const panelEnvPath = "/opt/psi-panel/panel/panel.env"

// persistEnvVar updates (or appends) a KEY=value line in panel.env so the
// value survives a panel restart, without disturbing other lines/comments.
func persistEnvVar(path, key, value string) error {
	lines := []string{}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		f.Close()
	}

	prefix := key + "="
	found := false
	for i, l := range lines {
		if strings.HasPrefix(l, prefix) {
			lines[i] = prefix + value
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, prefix+value)
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, l := range lines {
		w.WriteString(l + "\n")
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	return os.Rename(tmp, path)
}
