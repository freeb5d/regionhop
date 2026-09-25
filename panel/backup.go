package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// backupFormatVersion guards against importing a file from an incompatible
// future export format; bump it only if the shape of backupFile changes in
// a way older code can't just ignore.
const backupFormatVersion = 1

// backupFile is the single-file, portable snapshot of everything needed to
// recreate a regionhop panel's locations on a freshly-installed server:
// the tunnel registry (names/regions/ports) and the user's pasted Psiphon
// deployment config. Deliberately excludes the admin password hash and
// session secret (panel.env) — those are host-specific secrets a fresh
// install already generates its own of, and carrying them across servers
// would be a step backward for security, not a convenience.
type backupFile struct {
	FormatVersion int      `json:"format_version"`
	ExportedAt    string   `json:"exported_at"`
	PanelVersion  string   `json:"panel_version"`
	Tunnels       []Tunnel `json:"tunnels"`
	ExtraConfig   string   `json:"extra_config_json"`
	// Pointer so a backup made before upstream support existed (field
	// absent) leaves the current server's upstream alone on import.
	Upstream *upstreamSettings `json:"upstream,omitempty"`
}

// handleBackupExport streams the current registry + Psiphon config as a
// single downloadable JSON file.
func (a *app) handleBackupExport(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	list, err := loadRegistry(registryPath)
	creds := a.creds
	upstream := loadUpstream()
	a.mu.Unlock()
	if err != nil {
		http.Error(w, "failed reading registry: "+err.Error(), 500)
		return
	}

	b := backupFile{
		FormatVersion: backupFormatVersion,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339),
		PanelVersion:  CurrentVersion,
		Tunnels:       list,
		ExtraConfig:   creds.ConfigJSON,
		Upstream:      &upstream,
	}
	body, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		http.Error(w, "failed encoding backup: "+err.Error(), 500)
		return
	}

	filename := fmt.Sprintf("regionhop-backup-%s.json", time.Now().UTC().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Write(body)
}

// backupImportResult summarizes what happened, shown back on the settings
// page — imports are best-effort per-location so one bad entry (e.g. a name
// that now collides with an existing location) doesn't abort the rest.
type backupImportResult struct {
	Added   []string
	Skipped map[string]string // name -> reason
}

// handleBackupImport restores a backup produced by handleBackupExport:
// the Psiphon config is applied first (so every restored location's config
// is written with it already merged in, matching how it originally
// happened one at a time through handleAdd), then each location not already
// present is recreated — its config file written, appended to the
// registry, and started — following exactly the same sequence handleAdd
// uses for a single new location. Existing locations with a name collision
// are left untouched and reported as skipped rather than overwritten.
func (a *app) handleBackupImport(w http.ResponseWriter, r *http.Request) {
	setLangCookie(w, r)
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}

	file, _, err := r.FormFile("backup_file")
	if err != nil {
		a.renderSettingsError(w, r, "backup: "+err.Error())
		return
	}
	defer file.Close()

	var b backupFile
	if err := json.NewDecoder(file).Decode(&b); err != nil {
		a.renderSettingsError(w, r, "backup: invalid file: "+err.Error())
		return
	}
	if b.FormatVersion != backupFormatVersion {
		a.renderSettingsError(w, r, fmt.Sprintf("backup: unsupported format version %d", b.FormatVersion))
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if err := validateExtraConfigJSON(b.ExtraConfig); err != nil {
		a.renderSettingsError(w, r, "backup: config in file: "+err.Error())
		return
	}
	if err := saveExtraConfigJSON(b.ExtraConfig); err != nil {
		a.renderSettingsError(w, r, "backup: failed saving config: "+err.Error())
		return
	}
	a.creds.ConfigJSON = b.ExtraConfig

	// Before recreating locations, so their configs are written with it.
	if b.Upstream != nil {
		s, err := normalizeUpstream(*b.Upstream)
		if err == nil {
			err = a.applyUpstream(s)
		}
		if err != nil {
			a.renderSettingsError(w, r, "backup: upstream: "+err.Error())
			return
		}
	}

	list, err := loadRegistry(registryPath)
	if err != nil {
		a.renderSettingsError(w, r, "backup: "+err.Error())
		return
	}
	existing := map[string]bool{}
	usedPorts := map[int]bool{}
	for _, t := range list {
		existing[t.Name] = true
		usedPorts[t.SocksPort] = true
	}

	result := backupImportResult{Skipped: map[string]string{}}
	var started []string
	for _, t := range b.Tunnels {
		if !validName(t.Name) {
			result.Skipped[t.Name] = "invalid name"
			continue
		}
		if existing[t.Name] {
			result.Skipped[t.Name] = "already exists"
			continue
		}
		if t.SocksPort < portBase || t.SocksPort > portMax || usedPorts[t.SocksPort] {
			result.Skipped[t.Name] = "port unavailable"
			continue
		}
		if err := writeTunnelConfig(configsDir, dataDir, t, a.creds); err != nil {
			result.Skipped[t.Name] = "config: " + err.Error()
			continue
		}
		list = append(list, t)
		existing[t.Name] = true
		usedPorts[t.SocksPort] = true
		result.Added = append(result.Added, t.Name)
		started = append(started, t.Name)
	}

	if err := saveRegistry(registryPath, list); err != nil {
		a.renderSettingsError(w, r, "backup: failed saving registry: "+err.Error())
		return
	}
	if len(started) > 0 {
		if err := refreshSudoersRule(); err != nil {
			log.Printf("refresh sudoers after backup import: %v", err)
		}
	}
	for _, name := range started {
		if err := startTunnel(name); err != nil {
			log.Printf("start %s after backup import: %v", name, err)
		}
	}

	data := credsTemplateData(a.creds, true, "")
	data["ImportResult"] = result
	tmpl.ExecuteTemplate(w, "settings.html", withLang(r, data))
}

func (a *app) renderSettingsError(w http.ResponseWriter, r *http.Request, msg string) {
	tmpl.ExecuteTemplate(w, "settings.html", withLang(r, credsTemplateData(a.creds, false, msg)))
}
