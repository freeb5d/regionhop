package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type xuiView struct {
	Enabled   bool
	URL       string
	HasToken  bool
	LastSync  string
	LastError string
}

func xuiTemplateData() xuiView {
	s := loadXUI()
	v := xuiView{Enabled: s.Enabled, URL: s.URL, HasToken: s.Token != "", LastError: s.LastError}
	if s.LastSync > 0 {
		v.LastSync = time.Unix(s.LastSync, 0).Format("2006-01-02 15:04")
	}
	return v
}

// xuiFormSettings builds settings from the form; a blank token keeps the
// saved one, so the secret never has to be sent back to the page.
func xuiFormSettings(r *http.Request) (xuiSettings, error) {
	s := loadXUI()
	s.Enabled = r.FormValue("xui_enabled") != ""
	base, err := normalizeXUIURL(r.FormValue("xui_url"))
	if err != nil {
		return s, err
	}
	s.URL = base
	if t := strings.TrimSpace(r.FormValue("xui_token")); t != "" {
		s.Token = t
	}
	return s, nil
}

type xuiResult struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Changed bool   `json:"changed,omitempty"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (a *app) handleXUI(w http.ResponseWriter, r *http.Request) {
	setLangCookie(w, r)
	if r.Method != http.MethodPost {
		http.Redirect(w, r, urlFor("/settings"), http.StatusSeeOther)
		return
	}
	limitBody(w, r, 64<<10)
	var errMsg string
	s, err := xuiFormSettings(r)
	switch {
	case err != nil && r.FormValue("xui_enabled") == "":
		// switched off with a blank/bad address: just turn it off
		s = loadXUI()
		s.Enabled = false
		err = nil
		fallthrough
	case err == nil:
		if s.Enabled && s.Token == "" {
			err = errString("paste the API token from 3x-ui")
		}
		if err == nil {
			xuiMu.Lock()
			err = saveXUI(s)
			xuiMu.Unlock()
		}
		if err == nil && s.Enabled {
			if _, serr := xuiSync(); serr != nil {
				errMsg = serr.Error()
			}
		}
	}
	data := credsTemplateData(a.creds, false, "")
	if err != nil {
		data["XUIError"] = err.Error()
	} else if errMsg != "" {
		data["XUIError"] = errMsg
	} else {
		data["XUISaved"] = true
	}
	data["XUI"] = xuiTemplateData()
	tmpl.ExecuteTemplate(w, "settings.html", withLang(r, data))
}

func (a *app) handleXUITest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	limitBody(w, r, 64<<10)
	s, err := xuiFormSettings(r)
	if err == nil && s.Token == "" {
		err = errString("paste the API token from 3x-ui")
	}
	if err == nil {
		_, _, err = xuiGetConfig(s)
	}
	if err != nil {
		writeJSON(w, xuiResult{Error: err.Error()})
		return
	}
	writeJSON(w, xuiResult{OK: true})
}

func (a *app) handleXUISync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	changed, err := xuiSync()
	if err != nil {
		writeJSON(w, xuiResult{Error: err.Error()})
		return
	}
	writeJSON(w, xuiResult{OK: true, Changed: changed})
}

type errString string

func (e errString) Error() string { return string(e) }
