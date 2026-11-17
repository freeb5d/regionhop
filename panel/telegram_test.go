package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "123456:SECRET-test-token"

type tgCall1 struct {
	Method string
	Body   map[string]any
}

// fakeTelegram records every Bot API call and answers ok.
type fakeTelegram struct {
	mu    sync.Mutex
	calls []tgCall1
	files map[string]string // multipart fields of the last sendDocument
	srv   *httptest.Server
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	f := &fakeTelegram{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/bot"+testToken+"/") {
			w.WriteHeader(404)
			w.Write([]byte(`{"ok":false,"description":"Not Found"}`))
			return
		}
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		c := tgCall1{Method: method, Body: map[string]any{}}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			r.ParseMultipartForm(1 << 20)
			f.mu.Lock()
			f.files = map[string]string{"chat_id": r.FormValue("chat_id"), "caption": r.FormValue("caption")}
			if file, hdr, err := r.FormFile("document"); err == nil {
				b, _ := io.ReadAll(file)
				f.files["name"], f.files["content"] = hdr.Filename, string(b)
			}
			f.mu.Unlock()
		} else {
			json.NewDecoder(r.Body).Decode(&c.Body)
		}
		f.mu.Lock()
		f.calls = append(f.calls, c)
		n := len(f.calls)
		f.mu.Unlock()
		switch method {
		case "getMe":
			w.Write([]byte(`{"ok":true,"result":{"username":"rh_test_bot"}}`))
		case "sendMessage":
			w.Write([]byte(`{"ok":true,"result":{"message_id":` + string(rune('0'+n%10)) + `}}`))
		default:
			w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	old := telegramAPI
	telegramAPI = f.srv.URL
	t.Cleanup(func() { telegramAPI = old; f.srv.Close() })
	return f
}

func (f *fakeTelegram) byMethod(m string) []tgCall1 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tgCall1
	for _, c := range f.calls {
		if c.Method == m {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeTelegram) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func text(c tgCall1) string { s, _ := c.Body["text"].(string); return s }

func callbackData(c tgCall1) []string {
	var out []string
	kb, _ := c.Body["reply_markup"].(map[string]any)
	rows, _ := kb["inline_keyboard"].([]any)
	for _, r := range rows {
		for _, b := range r.([]any) {
			out = append(out, b.(map[string]any)["callback_data"].(string))
		}
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func admin1() telegramSettings {
	return telegramSettings{Enabled: true, Token: testToken, Admins: []int64{1}}
}

func msg(from int64, chatType, txt string, age time.Duration) tgUpdate {
	return tgUpdate{Message: &tgMessage{MessageID: 5, From: &tgUser{ID: from}, Chat: tgChat{ID: from, Type: chatType}, Date: time.Now().Add(-age).Unix(), Text: txt}}
}

func TestParseAdminIDs(t *testing.T) {
	ids, err := parseAdminIDs("123, 456\n123;789")
	if err != nil || len(ids) != 3 || ids[0] != 123 || ids[2] != 789 {
		t.Fatalf("%v %v", ids, err)
	}
	for _, bad := range []string{"@name", "0", "-5", "12x"} {
		if _, err := parseAdminIDs(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if ids, err := parseAdminIDs(""); err != nil || len(ids) != 0 {
		t.Errorf("empty: %v %v", ids, err)
	}
}

// Only admins, in a private chat, with a fresh message, are obeyed. A
// stranger only learns their own ID.
func TestTelegramAuthorisation(t *testing.T) {
	f := newFakeTelegram(t)
	a := &app{}
	s := admin1()

	a.tgHandle(s, msg(99, "private", "/backup", 0))
	if got := f.byMethod("sendMessage"); len(got) != 1 || !strings.Contains(text(got[0]), "99") || f.total() != 1 {
		t.Fatalf("stranger: %d calls", f.total())
	}
	if len(f.byMethod("sendDocument")) != 0 {
		t.Fatal("stranger got a backup")
	}

	before := f.total()
	a.tgHandle(s, msg(1, "group", "/help", 0))           // not a private chat
	a.tgHandle(s, msg(1, "private", "/help", time.Hour)) // stale message
	a.tgHandle(s, msg(1, "private", "hello", 0))         // not a command
	a.tgHandle(s, tgUpdate{CallbackQuery: &tgCallback{ID: "c", From: tgUser{ID: 99}, Data: "RY", Message: &tgMessage{MessageID: 2, Chat: tgChat{ID: 99, Type: "private"}}}})
	if f.total() != before {
		t.Fatalf("ignored updates produced %d calls", f.total()-before)
	}
}

func TestTelegramHelpRegionsAndPicker(t *testing.T) {
	f := newFakeTelegram(t)
	a := &app{}
	s := admin1()

	a.tgHandle(s, msg(1, "private", "/help@rh_test_bot", 0))
	got := f.byMethod("sendMessage")
	if len(got) != 1 || !strings.Contains(text(got[0]), "Help") || got[0].Body["parse_mode"] != "HTML" || !contains(callbackData(got[0]), "m") {
		t.Fatalf("help: %+v", got)
	}

	a.tgHandle(s, msg(1, "private", "/regions", 0))
	if r := f.byMethod("sendMessage"); !strings.Contains(text(r[len(r)-1]), "Brazil") {
		t.Fatal("regions list misses Brazil")
	}

	a.tgHandle(s, msg(1, "private", "/add", 0)) // no arguments: region picker with buttons
	r := f.byMethod("sendMessage")
	kb := callbackData(r[len(r)-1])
	if !contains(kb, "ar:BR") || !contains(kb, "ar:ANY") || !contains(kb, "m") {
		t.Fatalf("picker buttons: %v", kb)
	}
}

// Button presses edit the message they came from, and are acknowledged.
func TestTelegramCallbackEditsInPlace(t *testing.T) {
	f := newFakeTelegram(t)
	a := &app{}
	cb := &tgCallback{ID: "cb1", From: tgUser{ID: 1}, Data: "ar:BR", Message: &tgMessage{MessageID: 77, Chat: tgChat{ID: 1, Type: "private"}}}
	a.tgHandle(admin1(), tgUpdate{CallbackQuery: cb})

	if len(f.byMethod("answerCallbackQuery")) != 1 {
		t.Fatal("button press not acknowledged")
	}
	ed := f.byMethod("editMessageText")
	if len(ed) != 1 || ed[0].Body["message_id"].(float64) != 77 {
		t.Fatalf("edit: %+v", ed)
	}
	if !strings.Contains(text(ed[0]), "Brazil") || !strings.Contains(text(ed[0]), "<code>br</code>") || !contains(callbackData(ed[0]), "ay:BR") {
		t.Fatalf("confirm screen: %s %v", text(ed[0]), callbackData(ed[0]))
	}
	if len(f.byMethod("sendMessage")) != 0 {
		t.Fatal("should edit, not send a new message")
	}
}

func TestTelegramErrorsNeverContainTheToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	old := telegramAPI
	telegramAPI = url
	defer func() { telegramAPI = old }()
	err := tgSend(testToken, 1, "x", nil)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error must exist and hide the token: %v", err)
	}
}

func TestTelegramSendsBackupFile(t *testing.T) {
	f := newFakeTelegram(t)
	if err := tgSendFile(testToken, 42, "backup.json", []byte(`{"a":1}`), "cap"); err != nil {
		t.Fatal(err)
	}
	if f.files["chat_id"] != "42" || f.files["name"] != "backup.json" || f.files["content"] != `{"a":1}` || f.files["caption"] != "cap" {
		t.Fatalf("%v", f.files)
	}
}

func TestTelegramSettingsSave(t *testing.T) {
	f := newFakeTelegram(t)
	oldPath := telegramSettingsPath
	telegramSettingsPath = filepath.Join(t.TempDir(), "telegram.json")
	defer func() { telegramSettingsPath = oldPath }()
	a := &app{}

	post := func(form url.Values) string {
		r := httptest.NewRequest("POST", "/settings/telegram", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.handleTelegram(w, r)
		return w.Body.String()
	}

	// A good save: the bot name is read from Telegram, both admins get a hello,
	// and the token is never written back into the page.
	body := post(url.Values{"tg_enabled": {"1"}, "tg_token": {testToken}, "tg_admins": {"11, 22"}})
	if !strings.Contains(body, "@rh_test_bot") || strings.Contains(body, "SECRET") {
		t.Fatalf("page: bot name missing or token leaked")
	}
	s := loadTelegram()
	if !s.Enabled || s.Token != testToken || len(s.Admins) != 2 || s.BotName != "@rh_test_bot" {
		t.Fatalf("saved: %+v", s)
	}
	if n := len(f.byMethod("sendMessage")); n != 2 {
		t.Fatalf("expected a hello to each admin, got %d", n)
	}
	if fi, _ := os.Stat(telegramSettingsPath); fi.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Fatalf("settings file mode %v", fi.Mode().Perm())
	}

	// Saving again without a token keeps the saved one and doesn't greet again.
	post(url.Values{"tg_enabled": {"1"}, "tg_admins": {"11, 22"}})
	if loadTelegram().Token != testToken || len(f.byMethod("sendMessage")) != 2 {
		t.Fatal("blank token must keep the old one without re-greeting")
	}

	// A user name instead of a number is refused and nothing changes.
	body = post(url.Values{"tg_enabled": {"1"}, "tg_admins": {"@someone"}})
	if !strings.Contains(body, "not a numeric Telegram ID") || len(loadTelegram().Admins) != 2 {
		t.Fatal("bad admin id should be refused")
	}
}
