package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Telegram bot: manage the panel from Telegram. The panel polls Telegram
// (getUpdates), so it only needs outbound HTTPS -- no public address or
// webhook. Only the numeric Telegram user IDs saved as admins, writing to the
// bot in a private chat, are obeyed; everyone else just gets told their own
// ID (which is how an admin finds it) and nothing else.
var (
	telegramAPI          = "https://api.telegram.org"
	telegramSettingsPath = "/opt/psi-panel/data/telegram.json"
)

// Messages older than this are ignored, so a restart never replays old commands.
const tgMaxAge = 5 * time.Minute

type telegramSettings struct {
	Enabled   bool    `json:"enabled"`
	Token     string  `json:"token"`
	Admins    []int64 `json:"admins"`
	BotName   string  `json:"bot_name,omitempty"` // @username, read from Telegram with the token
	LastError string  `json:"last_error,omitempty"`
}

var tgMu sync.Mutex // serializes writes of the settings file

func loadTelegram() telegramSettings {
	var s telegramSettings
	if b, err := os.ReadFile(telegramSettingsPath); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func saveTelegram(s telegramSettings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := telegramSettingsPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, telegramSettingsPath)
}

func isTGAdmin(s telegramSettings, id int64) bool {
	for _, a := range s.Admins {
		if a == id {
			return true
		}
	}
	return false
}

// parseAdminIDs reads numeric Telegram user IDs separated by commas, spaces or newlines.
func parseAdminIDs(raw string) ([]int64, error) {
	var ids []int64
	seen := map[int64]bool{}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
		id, err := strconv.ParseInt(f, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q is not a numeric Telegram ID (a user name like @name doesn't work; send /start to your bot to see your number)", f)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) > 20 {
		return nil, fmt.Errorf("at most 20 admins")
	}
	return ids, nil
}

// ---- Telegram Bot API ----

var tgHTTP = &http.Client{Timeout: 45 * time.Second}

// scrub keeps the bot token out of any error text (Go puts the request URL,
// which contains the token, into transport errors).
func scrub(token, s string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "<token>")
}

func tgDo(token, method string, req *http.Request, out any) error {
	resp, err := tgHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("can't reach Telegram: %s", scrub(token, err.Error()))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if json.Unmarshal(body, &env) != nil {
		return fmt.Errorf("unexpected answer from Telegram (HTTP %d)", resp.StatusCode)
	}
	if !env.OK {
		if env.Description == "" {
			env.Description = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("%s", env.Description)
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

func tgCall(token, method string, params any, out any) error {
	body, _ := json.Marshal(params)
	req, err := http.NewRequest(http.MethodPost, telegramAPI+"/bot"+token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s", scrub(token, err.Error()))
	}
	req.Header.Set("Content-Type", "application/json")
	return tgDo(token, method, req, out)
}

func tgSend(token string, chat int64, text string, markup any) error {
	p := map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if markup != nil {
		p["reply_markup"] = markup
	}
	return tgCall(token, "sendMessage", p, nil)
}

func tgSendFile(token string, chat int64, name string, data []byte, caption string) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("chat_id", strconv.FormatInt(chat, 10))
	mw.WriteField("caption", caption)
	fw, _ := mw.CreateFormFile("document", name)
	fw.Write(data)
	mw.Close()
	req, err := http.NewRequest(http.MethodPost, telegramAPI+"/bot"+token+"/sendDocument", &buf)
	if err != nil {
		return fmt.Errorf("%s", scrub(token, err.Error()))
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return tgDo(token, "sendDocument", req, nil)
}

func tgBotName(token string) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	if err := tgCall(token, "getMe", map[string]any{}, &me); err != nil {
		return "", err
	}
	return "@" + me.Username, nil
}

type tgUser struct {
	ID int64 `json:"id"`
}
type tgChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
type tgMessage struct {
	MessageID int64   `json:"message_id"`
	From      *tgUser `json:"from"`
	Chat      tgChat  `json:"chat"`
	Date      int64   `json:"date"`
	Text      string  `json:"text"`
}
type tgCallback struct {
	ID      string     `json:"id"`
	From    tgUser     `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}
type tgUpdate struct {
	UpdateID      int64       `json:"update_id"`
	Message       *tgMessage  `json:"message"`
	CallbackQuery *tgCallback `json:"callback_query"`
}

// ---- polling loop ----

func (a *app) startTelegram() { go a.telegramLoop() }

func (a *app) telegramLoop() {
	var offset int64
	lastToken := ""
	lastErr := ""
	setErr := func(msg string) {
		if msg == lastErr {
			return
		}
		lastErr = msg
		tgMu.Lock()
		s := loadTelegram()
		s.LastError = msg
		saveTelegram(s)
		tgMu.Unlock()
	}
	for {
		s := loadTelegram()
		if !s.Enabled || s.Token == "" {
			offset, lastToken = 0, ""
			time.Sleep(3 * time.Second)
			continue
		}
		if s.Token != lastToken {
			offset, lastToken = 0, s.Token
			tgCall(s.Token, "setMyCommands", map[string]any{"commands": tgCommands}, nil)
		}
		var ups []tgUpdate
		err := tgCall(s.Token, "getUpdates", map[string]any{
			"offset": offset, "timeout": 25, "allowed_updates": []string{"message", "callback_query"},
		}, &ups)
		if err != nil {
			setErr(err.Error())
			time.Sleep(10 * time.Second)
			continue
		}
		setErr("")
		for _, u := range ups {
			offset = u.UpdateID + 1
			a.tgHandleSafe(s, u)
		}
	}
}

func (a *app) tgHandleSafe(s telegramSettings, u tgUpdate) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("telegram: panic handling update: %v", r)
		}
	}()
	a.tgHandle(s, u)
}

// ---- handling ----

type tgBtn struct{ Text, Data string }

// tgKB builds an inline keyboard ("glass buttons") from rows of buttons.
func tgKB(rows ...[]tgBtn) map[string]any {
	out := make([][]map[string]string, 0, len(rows))
	for _, r := range rows {
		row := make([]map[string]string, 0, len(r))
		for _, b := range r {
			row = append(row, map[string]string{"text": b.Text, "callback_data": b.Data})
		}
		out = append(out, row)
	}
	return map[string]any{"inline_keyboard": out}
}

// tgCtx is one conversation step. msg is the message a screen is drawn into:
// once known, every later screen edits it in place instead of adding a new
// message, so the chat stays one tidy panel.
type tgCtx struct {
	a     *app
	token string
	chat  int64
	msg   int64
}

func (c *tgCtx) show(text string, markup any) {
	p := map[string]any{"chat_id": c.chat, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true}
	if markup != nil {
		p["reply_markup"] = markup
	}
	if c.msg != 0 {
		p["message_id"] = c.msg
		err := tgCall(c.token, "editMessageText", p, nil)
		if err == nil || strings.Contains(err.Error(), "not modified") {
			return
		}
		delete(p, "message_id") // too old or deleted: send a fresh one
	}
	var sent struct {
		MessageID int64 `json:"message_id"`
	}
	if err := tgCall(c.token, "sendMessage", p, &sent); err != nil {
		log.Printf("telegram: send: %v", err)
		return
	}
	c.msg = sent.MessageID
}

func (c *tgCtx) toast(id, text string) {
	p := map[string]any{"callback_query_id": id}
	if text != "" {
		p["text"] = text
	}
	tgCall(c.token, "answerCallbackQuery", p, nil)
}

func (a *app) tgHandle(s telegramSettings, u tgUpdate) {
	if cb := u.CallbackQuery; cb != nil {
		if cb.Message == nil || cb.Message.Chat.Type != "private" || !isTGAdmin(s, cb.From.ID) {
			return
		}
		c := &tgCtx{a, s.Token, cb.Message.Chat.ID, cb.Message.MessageID}
		a.tgCallback(c, cb.ID, cb.Data)
		return
	}
	m := u.Message
	if m == nil || m.From == nil || m.Chat.Type != "private" {
		return
	}
	if time.Since(time.Unix(m.Date, 0)) > tgMaxAge {
		return
	}
	c := &tgCtx{a: a, token: s.Token, chat: m.Chat.ID}
	if !isTGAdmin(s, m.From.ID) {
		c.show(fmt.Sprintf("🔒 <b>Not an admin</b>\nYour Telegram ID is <code>%d</code>.\nTo use this bot, add that number in the panel under <i>Settings → Telegram bot</i>.", m.From.ID), nil)
		return
	}
	text := strings.TrimSpace(m.Text)
	if !strings.HasPrefix(text, "/") {
		return
	}
	f := strings.Fields(text)
	cmd := strings.ToLower(strings.TrimPrefix(f[0], "/"))
	if i := strings.Index(cmd, "@"); i >= 0 { // /list@mybot
		cmd = cmd[:i]
	}
	a.tgCommand(c, cmd, f[1:])
}

// tgCommands is the list Telegram shows in the bot's "/" menu.
var tgCommands = []map[string]string{
	{"command": "menu", "description": "Open the menu"},
	{"command": "list", "description": "Locations and their status"},
	{"command": "add", "description": "Add a new location"},
	{"command": "backup", "description": "Send the backup file"},
	{"command": "help", "description": "How to use this bot"},
}

const tgHelp = `❓ <b>Help</b>

Use the buttons, or these commands:
/menu — main menu
/list — locations and their status
/add — pick a region and add a location
/add <i>name</i> <i>REGION</i> — e.g. <code>/add uk GB</code>
/restart <i>name</i> — new IP for a location
/on <i>name</i> · /stop <i>name</i>
/test <i>name</i> — delay and exit IP
/google <i>name</i> — is Google/Gemini open from its IP
/remove <i>name</i>
/regions — region codes
/backup — send the backup file here`

func (a *app) tgCommand(c *tgCtx, cmd string, args []string) {
	switch cmd {
	case "start", "menu":
		a.showMenu(c)
	case "help":
		c.show(tgHelp, tgKB([]tgBtn{{"⬅ Menu", "m"}}))
	case "list", "status":
		a.showList(c)
	case "regions":
		c.show(tgRegions(), tgKB([]tgBtn{{"⬅ Menu", "m"}}))
	case "backup":
		c.sendBackup()
	case "add":
		if len(args) == 0 {
			a.showRegionPicker(c)
			return
		}
		if len(args) < 2 {
			c.show("Use: <code>/add name REGION</code>, like <code>/add uk GB</code>, or just /add to pick from a list.", nil)
			return
		}
		region := strings.ToUpper(args[1])
		if region == "ANY" {
			region = ""
		}
		if _, err := a.addLocation(args[0], region); err != nil {
			c.show("❌ "+html.EscapeString(err.Error()), nil)
			return
		}
		a.showDetail(c, args[0], "✅ Added and started. Connecting usually takes about a minute: press Refresh.")
	case "restart", "on", "stop", "test", "google", "remove":
		if len(args) < 1 || !validName(args[0]) {
			c.show("Give a location name, like <code>/"+cmd+" nl</code>. See /list.", nil)
			return
		}
		if _, ok := locationInfo(args[0]); !ok {
			c.show("There is no location named <code>"+html.EscapeString(args[0])+"</code>. See /list.", nil)
			return
		}
		act := map[string]string{"restart": "r", "on": "o", "stop": "s", "test": "t", "google": "g", "remove": "x"}[cmd]
		a.tgAction(c, act, args[0])
	default:
		c.show("Unknown command. See /help.", tgKB([]tgBtn{{"⬅ Menu", "m"}}))
	}
}

func (a *app) tgCallback(c *tgCtx, id, data string) {
	act, arg, _ := strings.Cut(data, ":")
	switch act {
	case "m":
		c.toast(id, "")
		a.showMenu(c)
	case "l":
		c.toast(id, "")
		a.showList(c)
	case "h":
		c.toast(id, "")
		c.show(tgHelp, tgKB([]tgBtn{{"⬅ Menu", "m"}}))
	case "b":
		c.toast(id, "Sending the backup…")
		c.sendBackup()
	case "a":
		c.toast(id, "")
		a.showRegionPicker(c)
	case "ar": // region picked: confirm
		c.toast(id, "")
		a.showAddConfirm(c, arg)
	case "ay": // region confirmed: add
		c.toast(id, "Adding…")
		a.doAdd(c, arg)
	case "R":
		c.toast(id, "")
		a.showRestartAllConfirm(c)
	case "RY":
		c.toast(id, "Restarting all…")
		a.doRestartAll(c)
	case "d", "r", "o", "s", "t", "g", "x", "X":
		if !validName(arg) {
			c.toast(id, "")
			return
		}
		if _, ok := locationInfo(arg); !ok {
			c.toast(id, "That location no longer exists.")
			a.showList(c)
			return
		}
		c.toast(id, map[string]string{"r": "Restarting…", "t": "Testing…", "g": "Checking Google…", "o": "Starting…", "s": "Stopping…"}[act])
		a.tgAction(c, act, arg)
	default:
		c.toast(id, "")
	}
}

// tgAction runs one action on one location and draws its card.
func (a *app) tgAction(c *tgCtx, act, name string) {
	switch act {
	case "d":
		a.showDetail(c, name, "")
	case "r":
		if err := restartTunnel(name); err != nil {
			a.showDetail(c, name, "❌ "+html.EscapeString(err.Error()))
			return
		}
		a.showDetail(c, name, "🔄 Restarting for a new IP. It reconnects in a few seconds: press Refresh.")
	case "o":
		if err := startTunnel(name); err != nil {
			a.showDetail(c, name, "❌ "+html.EscapeString(err.Error()))
			return
		}
		a.showDetail(c, name, "▶ Started.")
	case "s":
		if err := stopTunnel(name); err != nil {
			a.showDetail(c, name, "❌ "+html.EscapeString(err.Error()))
			return
		}
		a.showDetail(c, name, "⏹ Stopped.")
	case "t", "g":
		mode, label := "real", "🧪 Testing…"
		if act == "g" {
			mode, label = "google", "🔎 Checking Google/Gemini…"
		}
		a.showDetail(c, name, label)
		go func() { a.showDetail(c, name, probeNote(mode, name)) }()
	case "x":
		t, _ := locationInfo(name)
		c.show("🗑 <b>Remove "+tgFlag(t.Region)+" "+html.EscapeString(name)+"?</b>\nThis stops it and deletes the location.",
			tgKB([]tgBtn{{"🗑 Yes, remove", "X:" + name}, {"✖ Cancel", "d:" + name}}))
	case "X":
		a.removeLocation(name)
		c.show("🗑 Removed <b>"+html.EscapeString(name)+"</b>.", tgKB([]tgBtn{{"📋 Locations", "l"}, {"⬅ Menu", "m"}}))
	}
}

func probeNote(mode, name string) string {
	t, ok := locationInfo(name)
	if !ok {
		return ""
	}
	r := runProbe(mode, fmt.Sprintf("127.0.0.1:%d", t.SocksPort))
	ip := ""
	if r.IP != "" {
		ip = " · exit IP <code>" + html.EscapeString(r.IP) + "</code>"
	}
	switch {
	case !r.OK:
		return "❌ " + html.EscapeString(r.Error)
	case mode == "google" && r.Google == "blocked":
		return "🚫 Google/Gemini is <b>blocked</b> from this IP" + ip + "\n<i>" + html.EscapeString(r.Detail) + "</i>\nTip: 🔄 New IP and test again."
	case mode == "google":
		return "✅ Google/Gemini is <b>open</b>" + ip
	}
	return fmt.Sprintf("✅ <b>%d ms</b>%s", r.LatencyMs, ip)
}

// ---- screens ----

func locationInfo(name string) (Tunnel, bool) {
	list, err := loadRegistry(registryPath)
	if err != nil {
		return Tunnel{}, false
	}
	for _, t := range list {
		if t.Name == name {
			return t, true
		}
	}
	return Tunnel{}, false
}

func tgFlag(code string) string {
	if code == "" {
		return "🌍"
	}
	if f := countryFlag(code); f != "" {
		return f
	}
	return "🌍"
}

func statusIcon(class string) string {
	switch class {
	case "active":
		return "🟢"
	case "connecting":
		return "🟡"
	case "inactive":
		return "🔴"
	}
	return "⚪"
}

func (a *app) showMenu(c *tgCtx) {
	rows, err := a.buildRows()
	if err != nil {
		c.show("❌ "+html.EscapeString(err.Error()), nil)
		return
	}
	var on, wait, off int
	for _, r := range rows {
		switch r.StatusClass {
		case "active":
			on++
		case "connecting":
			wait++
		default:
			off++
		}
	}
	text := "🌐 <b>regionhop</b>\n"
	if len(rows) == 0 {
		text += "No locations yet. Tap <b>Add location</b> to create the first one."
	} else {
		text += fmt.Sprintf("%d locations · 🟢 %d online · 🟡 %d connecting · 🔴 %d off", len(rows), on, wait, off)
	}
	c.show(text, tgKB(
		[]tgBtn{{"📋 Locations", "l"}, {"➕ Add location", "a"}},
		[]tgBtn{{"🔄 Restart all", "R"}, {"💾 Backup", "b"}},
		[]tgBtn{{"❓ Help", "h"}},
	))
}

func (a *app) showList(c *tgCtx) {
	rows, err := a.buildRows()
	if err != nil {
		c.show("❌ "+html.EscapeString(err.Error()), nil)
		return
	}
	if len(rows) == 0 {
		c.show("📋 <b>Locations</b>\nNone yet.", tgKB([]tgBtn{{"➕ Add location", "a"}}, []tgBtn{{"⬅ Menu", "m"}}))
		return
	}
	var sb strings.Builder
	sb.WriteString("📋 <b>Locations</b>\n")
	var kb [][]tgBtn
	var pair []tgBtn
	for _, r := range rows {
		t, _ := locationInfo(r.Name)
		fmt.Fprintf(&sb, "\n%s %s <b>%s</b> · %s", statusIcon(r.StatusClass), tgFlag(t.Region), html.EscapeString(r.Name), html.EscapeString(r.Status))
		if r.ExitRegion != "" {
			fmt.Fprintf(&sb, " · exit %s", html.EscapeString(r.ExitRegion))
		}
		pair = append(pair, tgBtn{statusIcon(r.StatusClass) + " " + tgFlag(t.Region) + " " + r.Name, "d:" + r.Name})
		if len(pair) == 2 {
			kb = append(kb, pair)
			pair = nil
		}
	}
	if len(pair) > 0 {
		kb = append(kb, pair)
	}
	kb = append(kb, []tgBtn{{"➕ Add", "a"}, {"↻ Refresh", "l"}, {"⬅ Menu", "m"}})
	sb.WriteString("\n\nTap a location to manage it.")
	c.show(sb.String(), tgKB(kb...))
}

func (a *app) showDetail(c *tgCtx, name, note string) {
	t, ok := locationInfo(name)
	if !ok {
		a.showList(c)
		return
	}
	rows, _ := a.buildRows()
	var cur row
	for _, r := range rows {
		if r.Name == name {
			cur = r
		}
	}
	label := "Any region"
	if t.Region != "" {
		label = regionCodes[t.Region]
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s <b>%s</b> · %s\n", tgFlag(t.Region), html.EscapeString(name), html.EscapeString(label))
	fmt.Fprintf(&sb, "%s %s", statusIcon(cur.StatusClass), html.EscapeString(capFirst(cur.Status)))
	if cur.ExitRegion != "" {
		fmt.Fprintf(&sb, " · exit %s", html.EscapeString(cur.ExitRegion))
	}
	fmt.Fprintf(&sb, "\n🔌 SOCKS <code>127.0.0.1:%d</code>", t.SocksPort)
	if note != "" {
		sb.WriteString("\n\n" + note)
	}
	toggle := tgBtn{"⏹ Stop", "s:" + name}
	if cur.StatusClass == "inactive" {
		toggle = tgBtn{"▶ Start", "o:" + name}
	}
	c.show(sb.String(), tgKB(
		[]tgBtn{{"🔄 New IP", "r:" + name}, {"🧪 Test", "t:" + name}, {"🔎 Google", "g:" + name}},
		[]tgBtn{toggle, {"↻ Refresh", "d:" + name}},
		[]tgBtn{{"🗑 Remove", "x:" + name}},
		[]tgBtn{{"⬅ Locations", "l"}},
	))
}

func (a *app) showRegionPicker(c *tgCtx) {
	type reg struct{ code, label string }
	var regs []reg
	for code, label := range regionCodes {
		if code != "" {
			regs = append(regs, reg{code, label})
		}
	}
	sort.Slice(regs, func(i, j int) bool { return regs[i].label < regs[j].label })
	kb := [][]tgBtn{{{"🌍 Any (Psiphon chooses)", "ar:ANY"}}}
	var pair []tgBtn
	for _, r := range regs {
		pair = append(pair, tgBtn{tgFlag(r.code) + " " + r.label, "ar:" + r.code})
		if len(pair) == 2 {
			kb = append(kb, pair)
			pair = nil
		}
	}
	if len(pair) > 0 {
		kb = append(kb, pair)
	}
	kb = append(kb, []tgBtn{{"⬅ Menu", "m"}})
	c.show("➕ <b>Add a location</b>\nWhere should its exit IP be?", tgKB(kb...))
}

// autoName picks a free location name from a region code: gb, gb-2, gb-3…
func autoName(code string) string {
	base := strings.ToLower(code)
	if code == "ANY" || code == "" {
		base = "any"
	}
	name := base
	for i := 2; i < 100; i++ {
		if _, taken := locationInfo(name); !taken {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

func regionOf(arg string) (code string, ok bool) {
	if arg == "ANY" {
		return "", true
	}
	_, ok = regionCodes[arg]
	return arg, ok && arg != ""
}

func (a *app) showAddConfirm(c *tgCtx, arg string) {
	code, ok := regionOf(arg)
	if !ok {
		a.showRegionPicker(c)
		return
	}
	label := "Any region"
	if code != "" {
		label = regionCodes[code]
	}
	c.show(fmt.Sprintf("➕ <b>Add a location</b>\n%s <b>%s</b>\nName: <code>%s</code>\n\n<i>Want another name? Use /add name %s</i>",
		tgFlag(code), html.EscapeString(label), autoName(arg), html.EscapeString(arg)),
		tgKB([]tgBtn{{"✅ Add", "ay:" + arg}, {"✖ Cancel", "a"}}))
}

func (a *app) doAdd(c *tgCtx, arg string) {
	code, ok := regionOf(arg)
	if !ok {
		a.showRegionPicker(c)
		return
	}
	name := autoName(arg)
	if _, err := a.addLocation(name, code); err != nil {
		c.show("❌ "+html.EscapeString(err.Error()), tgKB([]tgBtn{{"⬅ Menu", "m"}}))
		return
	}
	a.showDetail(c, name, "✅ Added and started. Connecting usually takes about a minute: press Refresh.")
}

func (a *app) showRestartAllConfirm(c *tgCtx) {
	rows, _ := a.buildRows()
	if len(rows) == 0 {
		c.show("There are no locations to restart.", tgKB([]tgBtn{{"⬅ Menu", "m"}}))
		return
	}
	c.show(fmt.Sprintf("🔄 <b>Restart all %d locations?</b>\nEach one reconnects with a new IP.", len(rows)),
		tgKB([]tgBtn{{"✅ Yes, restart all", "RY"}, {"✖ Cancel", "m"}}))
}

func (a *app) doRestartAll(c *tgCtx) {
	rows, _ := a.buildRows()
	c.show(fmt.Sprintf("🔄 Restarting %d locations…", len(rows)), nil)
	go func() {
		failed := 0
		for i, r := range rows {
			if err := restartTunnel(r.Name); err != nil {
				failed++
			}
			if i < len(rows)-1 {
				time.Sleep(time.Second)
			}
		}
		text := fmt.Sprintf("✅ Restarted %d locations. They reconnect in a minute.", len(rows)-failed)
		if failed > 0 {
			text += fmt.Sprintf("\n❌ %d could not be restarted.", failed)
		}
		c.show(text, tgKB([]tgBtn{{"📋 Locations", "l"}, {"⬅ Menu", "m"}}))
	}()
}

func tgRegions() string {
	var codes []string
	for code := range regionCodes {
		if code != "" {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	parts := make([]string, len(codes))
	for i, code := range codes {
		parts[i] = tgFlag(code) + " <code>" + code + "</code> " + regionCodes[code]
	}
	return "🗺 <b>Region codes</b> (or <code>ANY</code>)\n" + strings.Join(parts, "\n")
}

func (c *tgCtx) sendBackup() {
	body, name, err := c.a.buildBackup()
	if err != nil {
		c.show("❌ "+html.EscapeString(err.Error()), nil)
		return
	}
	if err := tgSendFile(c.token, c.chat, name, body, "💾 regionhop backup. It contains your Psiphon config, so keep it private."); err != nil {
		c.show("❌ "+html.EscapeString(err.Error()), nil)
	}
}

func capFirst(s string) string {
	if s == "" {
		return "Unknown"
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ---- settings page ----

type telegramView struct {
	Enabled   bool
	HasToken  bool
	BotName   string
	Admins    string
	LastError string
}

func telegramTemplateData() telegramView {
	s := loadTelegram()
	ids := make([]string, len(s.Admins))
	for i, id := range s.Admins {
		ids[i] = strconv.FormatInt(id, 10)
	}
	return telegramView{Enabled: s.Enabled, HasToken: s.Token != "", BotName: s.BotName, Admins: strings.Join(ids, ", "), LastError: s.LastError}
}

func (a *app) handleTelegram(w http.ResponseWriter, r *http.Request) {
	setLangCookie(w, r)
	if r.Method != http.MethodPost {
		http.Redirect(w, r, urlFor("/settings"), http.StatusSeeOther)
		return
	}
	limitBody(w, r, 64<<10)

	tgMu.Lock()
	old := loadTelegram()
	s := old
	s.Enabled = r.FormValue("tg_enabled") != ""
	if t := strings.TrimSpace(r.FormValue("tg_token")); t != "" {
		s.Token = t
	}
	var err error
	if ids, perr := parseAdminIDs(r.FormValue("tg_admins")); perr == nil {
		s.Admins = ids
	} else if s.Enabled {
		err = perr
	}
	if err == nil && s.Enabled {
		switch {
		case s.Token == "":
			err = fmt.Errorf("paste the bot token from @BotFather")
		case len(s.Admins) == 0:
			err = fmt.Errorf("add your numeric Telegram ID as an admin")
		default:
			if s.BotName, err = tgBotName(s.Token); err != nil {
				err = fmt.Errorf("Telegram refused this token: %v", err)
			}
		}
	}
	if err == nil {
		s.LastError = ""
		err = saveTelegram(s)
	}
	tgMu.Unlock()

	data := credsTemplateData(a.creds, false, "")
	if err != nil {
		data["TGError"] = err.Error()
	} else {
		data["TGSaved"] = true
		// Tell the admins it works, so a wrong ID or a bot that was never
		// started shows up right now rather than at the first command.
		if s.Enabled && (!old.Enabled || old.Token != s.Token || fmt.Sprint(old.Admins) != fmt.Sprint(s.Admins)) {
			var warns []string
			for _, id := range s.Admins {
				if e := tgSend(s.Token, id, "✅ regionhop is connected to this bot. Send /help to see what it can do.", nil); e != nil {
					warns = append(warns, fmt.Sprintf("%d: %s", id, scrub(s.Token, e.Error())))
				}
			}
			if len(warns) > 0 {
				data["TGWarn"] = strings.Join(warns, "; ")
			}
		}
	}
	data["TG"] = telegramTemplateData()
	tmpl.ExecuteTemplate(w, "settings.html", withLang(r, data))
}
