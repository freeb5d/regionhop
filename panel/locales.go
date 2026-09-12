package main

import "net/http"

type langInfo struct {
	Code   string
	Native string
	RTL    bool
	Font   string // Google Fonts family name, or "" for the default UI font
}

var supportedLangs = []langInfo{
	{Code: "en", Native: "English"},
	{Code: "fa", Native: "فارسی", RTL: true, Font: "Vazirmatn"},
	{Code: "ar", Native: "العربية", RTL: true, Font: "Noto Sans Arabic"},
	{Code: "ru", Native: "Русский"},
	{Code: "zh", Native: "中文"},
}

func isSupportedLang(code string) bool {
	for _, l := range supportedLangs {
		if l.Code == code {
			return true
		}
	}
	return false
}

func langInfoFor(code string) langInfo {
	for _, l := range supportedLangs {
		if l.Code == code {
			return l
		}
	}
	return supportedLangs[0]
}

const langCookieName = "panel_lang"

// currentLang resolves the active language for a request: an explicit
// ?lang= always wins (and is what the language-switcher links use), then
// the panel_lang cookie from a previous visit, then English.
func currentLang(r *http.Request) string {
	if q := r.URL.Query().Get("lang"); isSupportedLang(q) {
		return q
	}
	if c, err := r.Cookie(langCookieName); err == nil && isSupportedLang(c.Value) {
		return c.Value
	}
	return "en"
}

// setLangCookie persists an explicit ?lang= choice so it sticks across
// pages without it needing to appear in every link.
func setLangCookie(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("lang")
	if !isSupportedLang(q) {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     langCookieName,
		Value:    q,
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   365 * 24 * 60 * 60,
	})
}

// t looks up a translation key for a language, falling back to English and
// then to the key itself so a missing string never breaks rendering.
func t(lang, key string) string {
	if m, ok := translations[lang]; ok {
		if s, ok := m[key]; ok {
			return s
		}
	}
	if s, ok := translations["en"][key]; ok {
		return s
	}
	return key
}

var translations = map[string]map[string]string{
	"en": {
		"nav.credentials":       "Credentials",
		"nav.dashboard":         "Dashboard",
		"nav.logout":            "Log out",
		"tunnels.title":         "Tunnels",
		"tunnels.desc":          "Each location runs its own Psiphon tunnel instance. SOCKS proxies are bound to 127.0.0.1 on this server only — never reachable from outside.",
		"add.title":             "Add location",
		"add.name":              "Name",
		"add.name.placeholder":  "e.g. de-1",
		"add.region":            "Region",
		"add.submit":            "Add + start",
		"add.note":              "Port is assigned automatically from the internal range and is always bound to loopback.",
		"table.name":            "Name",
		"table.region":          "Region",
		"table.socks":           "SOCKS",
		"table.status":          "Status",
		"table.flag":            "Flag",
		"table.actions":         "Actions",
		"table.empty":           "No locations yet — add one above to get started.",
		"action.restart":        "Restart",
		"action.stop":           "Stop",
		"action.remove":         "Remove",
		"action.remove.confirm": "Remove %s?",
		"action.logs":           "Logs",
		"status.active":         "active",
		"status.connecting":     "connecting",
		"status.inactive":       "inactive",
		"status.failed":         "failed",
		"status.unknown":        "unknown",
		"update.available":      "Update available: <strong>v%s</strong> (running v%s).",
		"update.now":            "Update now",
		"update.confirm":        "Update regionhop now? The panel will restart automatically.",
		"footer.credit":         "made with ❤️ by kaveh",
		"login.title":           "Admin password",
		"login.submit":          "Sign in",
		"login.error.locked":    "Too many attempts, try again later.",
		"login.error.wrong":     "Incorrect password.",
		"creds.title":           "Psiphon config",
		"creds.desc":            "Applied to every new location added from now on.",
		"creds.saved":           "Saved.",
		"creds.label":           "Config JSON",
		"creds.submit":          "Save",
		"creds.note1":           "Paste the full JSON object from your own Psiphon deployment config — PropagationChannelId, SponsorId, and anything else it includes. Applied to every location added from now on; not retroactively applied to existing ones — remove and re-add a location (or edit the config file directly) to update it.",
		"creds.note2":           "EgressRegion, LocalSocksProxyPort, ListenInterface, and DataRootDirectory always come from regionhop itself and cannot be overridden here, no matter what's pasted — that's what keeps every SOCKS proxy bound to 127.0.0.1. regionhop does not supply, validate, or know the meaning of anything you put in this box.",
		"logs.title":            "Logs: %s",
		"logs.desc":             "Last 200 journal lines for this location's service.",
		"logs.breadcrumb":       "Tunnels",
		"updating.title":        "Update started. The panel will rebuild/redownload itself and restart — this page will retry automatically in a few seconds.",
		"updating.back":         "Back to dashboard now",
	},
	"fa": {
		"nav.credentials":       "تنظیمات Psiphon",
		"nav.dashboard":         "داشبورد",
		"nav.logout":            "خروج",
		"tunnels.title":         "تونل‌ها",
		"tunnels.desc":          "هر لوکیشن، نمونهٔ تونل Psiphon مخصوص به خودش را اجرا می‌کند. پراکسی‌های SOCKS فقط روی 127.0.0.1 این سرور باز هستند — هرگز از بیرون در دسترس نیستند.",
		"add.title":             "افزودن لوکیشن",
		"add.name":              "نام",
		"add.name.placeholder":  "مثلاً de-1",
		"add.region":            "منطقه",
		"add.submit":            "افزودن و شروع",
		"add.note":              "پورت به‌صورت خودکار از بازهٔ داخلی اختصاص می‌یابد و همیشه روی loopback است.",
		"table.name":            "نام",
		"table.region":          "منطقه",
		"table.socks":           "SOCKS",
		"table.status":          "وضعیت",
		"table.flag":            "پرچم",
		"table.actions":         "عملیات",
		"table.empty":           "هنوز لوکیشنی اضافه نشده — یکی از بالا اضافه کنید.",
		"action.restart":        "ری‌استارت",
		"action.stop":           "توقف",
		"action.remove":         "حذف",
		"action.remove.confirm": "%s حذف شود؟",
		"action.logs":           "لاگ‌ها",
		"status.active":         "فعال",
		"status.connecting":     "در حال اتصال",
		"status.inactive":       "غیرفعال",
		"status.failed":         "خطا",
		"status.unknown":        "نامشخص",
		"update.available":      "نسخهٔ جدید موجود است: <strong>v%s</strong> (نسخهٔ فعلی v%s).",
		"update.now":            "به‌روزرسانی",
		"update.confirm":        "regionhop همین حالا به‌روزرسانی شود؟ پنل به‌طور خودکار ری‌استارت می‌شود.",
		"footer.credit":         "ساخته‌شده با ❤️ توسط kaveh",
		"login.title":           "رمز عبور مدیر",
		"login.submit":          "ورود",
		"login.error.locked":    "تعداد تلاش‌ها زیاد بود، بعداً دوباره امتحان کنید.",
		"login.error.wrong":     "رمز عبور نادرست است.",
		"creds.title":           "تنظیمات Psiphon",
		"creds.desc":            "از این پس روی هر لوکیشن جدیدی که اضافه می‌شود اعمال می‌شود.",
		"creds.saved":           "ذخیره شد.",
		"creds.label":           "JSON تنظیمات",
		"creds.submit":          "ذخیره",
		"creds.note1":           "شیء JSON کامل تنظیمات دیپلوی Psiphon خودتان را وارد کنید — PropagationChannelId، SponsorId و هرچیز دیگری که شامل می‌شود. از این پس روی هر لوکیشن اضافه‌شده اعمال می‌شود؛ روی لوکیشن‌های قبلی به‌صورت خودکار اعمال نمی‌شود — برای به‌روزرسانی آن‌ها، لوکیشن را حذف و دوباره اضافه کنید (یا فایل تنظیمات را مستقیماً ویرایش کنید).",
		"creds.note2":           "مقادیر EgressRegion، LocalSocksProxyPort، ListenInterface و DataRootDirectory همیشه از خود regionhop می‌آیند و با هیچ‌چیزی که اینجا وارد کنید قابل بازنویسی نیستند — همین موضوع تضمین می‌کند هر پراکسی SOCKS روی 127.0.0.1 باقی بماند. regionhop هیچ‌چیزی که اینجا وارد می‌کنید را تأمین، اعتبارسنجی یا تفسیر نمی‌کند.",
		"logs.title":            "لاگ‌ها: %s",
		"logs.desc":             "۲۰۰ خط آخر لاگ این سرویس.",
		"logs.breadcrumb":       "تونل‌ها",
		"updating.title":        "به‌روزرسانی آغاز شد. پنل خودش را بازسازی/دانلود و ری‌استارت می‌کند — این صفحه به‌زودی خودکار دوباره تلاش می‌کند.",
		"updating.back":         "بازگشت به داشبورد",
	},
	"ar": {
		"nav.credentials":       "إعدادات Psiphon",
		"nav.dashboard":         "لوحة التحكم",
		"nav.logout":            "تسجيل الخروج",
		"tunnels.title":         "الأنفاق",
		"tunnels.desc":          "يشغّل كل موقع نسخته الخاصة من نفق Psiphon. وكلاء SOCKS مرتبطون فقط بـ 127.0.0.1 على هذا الخادم — لا يمكن الوصول إليهم من الخارج أبدًا.",
		"add.title":             "إضافة موقع",
		"add.name":              "الاسم",
		"add.name.placeholder":  "مثال: de-1",
		"add.region":            "المنطقة",
		"add.submit":            "إضافة وتشغيل",
		"add.note":              "يتم تعيين المنفذ تلقائيًا من النطاق الداخلي ويبقى دائمًا على loopback.",
		"table.name":            "الاسم",
		"table.region":          "المنطقة",
		"table.socks":           "SOCKS",
		"table.status":          "الحالة",
		"table.flag":            "العلم",
		"table.actions":         "إجراءات",
		"table.empty":           "لا توجد مواقع بعد — أضف واحدًا أعلاه للبدء.",
		"action.restart":        "إعادة تشغيل",
		"action.stop":           "إيقاف",
		"action.remove":         "إزالة",
		"action.remove.confirm": "إزالة %s؟",
		"action.logs":           "السجلات",
		"status.active":         "نشط",
		"status.connecting":     "جارٍ الاتصال",
		"status.inactive":       "غير نشط",
		"status.failed":         "فشل",
		"status.unknown":        "غير معروف",
		"update.available":      "يتوفر تحديث: <strong>v%s</strong> (الإصدار الحالي v%s).",
		"update.now":            "التحديث الآن",
		"update.confirm":        "هل تريد تحديث regionhop الآن؟ ستعيد اللوحة التشغيل تلقائيًا.",
		"footer.credit":         "صُنع بـ ❤️ بواسطة kaveh",
		"login.title":           "كلمة مرور المسؤول",
		"login.submit":          "تسجيل الدخول",
		"login.error.locked":    "محاولات كثيرة جدًا، حاول مرة أخرى لاحقًا.",
		"login.error.wrong":     "كلمة مرور غير صحيحة.",
		"creds.title":           "إعدادات Psiphon",
		"creds.desc":            "تُطبَّق على كل موقع جديد يُضاف من الآن فصاعدًا.",
		"creds.saved":           "تم الحفظ.",
		"creds.label":           "إعدادات JSON",
		"creds.submit":          "حفظ",
		"creds.note1":           "الصق كائن JSON الكامل من إعدادات نشر Psiphon الخاصة بك — PropagationChannelId وSponsorId وأي شيء آخر يتضمنه. يُطبَّق على كل موقع يُضاف من الآن فصاعدًا؛ لا يُطبَّق بأثر رجعي على المواقع الحالية — احذف الموقع وأضفه مجددًا (أو عدّل ملف الإعدادات مباشرة) لتحديثه.",
		"creds.note2":           "قيم EgressRegion وLocalSocksProxyPort وListenInterface وDataRootDirectory تأتي دائمًا من regionhop نفسه ولا يمكن تجاوزها هنا مهما كان ما تلصقه — وهذا ما يبقي كل وكيل SOCKS مرتبطًا بـ 127.0.0.1. لا يقدّم regionhop أو يتحقق من صحة أو يفهم معنى أي شيء تضعه في هذا المربع.",
		"logs.title":            "السجلات: %s",
		"logs.desc":             "آخر 200 سطر من سجل هذه الخدمة.",
		"logs.breadcrumb":       "الأنفاق",
		"updating.title":        "بدأ التحديث. ستعيد اللوحة بناء/تنزيل نفسها والتشغيل — ستعيد هذه الصفحة المحاولة تلقائيًا خلال ثوانٍ.",
		"updating.back":         "العودة إلى لوحة التحكم الآن",
	},
	"ru": {
		"nav.credentials":       "Конфигурация Psiphon",
		"nav.dashboard":         "Панель",
		"nav.logout":            "Выйти",
		"tunnels.title":         "Туннели",
		"tunnels.desc":          "Каждая локация запускает собственный туннель Psiphon. SOCKS-прокси привязаны только к 127.0.0.1 этого сервера — недоступны снаружи.",
		"add.title":             "Добавить локацию",
		"add.name":              "Имя",
		"add.name.placeholder":  "напр. de-1",
		"add.region":            "Регион",
		"add.submit":            "Добавить и запустить",
		"add.note":              "Порт назначается автоматически из внутреннего диапазона и всегда привязан к loopback.",
		"table.name":            "Имя",
		"table.region":          "Регион",
		"table.socks":           "SOCKS",
		"table.status":          "Статус",
		"table.flag":            "Флаг",
		"table.actions":         "Действия",
		"table.empty":           "Пока нет локаций — добавьте одну выше.",
		"action.restart":        "Перезапуск",
		"action.stop":           "Остановить",
		"action.remove":         "Удалить",
		"action.remove.confirm": "Удалить %s?",
		"action.logs":           "Логи",
		"status.active":         "активен",
		"status.connecting":     "подключение",
		"status.inactive":       "неактивен",
		"status.failed":         "ошибка",
		"status.unknown":        "неизвестно",
		"update.available":      "Доступно обновление: <strong>v%s</strong> (установлено v%s).",
		"update.now":            "Обновить сейчас",
		"update.confirm":        "Обновить regionhop сейчас? Панель перезапустится автоматически.",
		"footer.credit":         "сделано с ❤️ автором kaveh",
		"login.title":           "Пароль администратора",
		"login.submit":          "Войти",
		"login.error.locked":    "Слишком много попыток, попробуйте позже.",
		"login.error.wrong":     "Неверный пароль.",
		"creds.title":           "Конфигурация Psiphon",
		"creds.desc":            "Применяется ко всем новым локациям, добавленным с этого момента.",
		"creds.saved":           "Сохранено.",
		"creds.label":           "JSON конфигурации",
		"creds.submit":          "Сохранить",
		"creds.note1":           "Вставьте полный JSON-объект из вашей собственной конфигурации развёртывания Psiphon — PropagationChannelId, SponsorId и всё остальное, что она включает. Применяется ко всем локациям, добавленным после этого; не применяется задним числом к уже существующим — удалите и заново добавьте локацию (или отредактируйте файл конфигурации напрямую), чтобы обновить её.",
		"creds.note2":           "Значения EgressRegion, LocalSocksProxyPort, ListenInterface и DataRootDirectory всегда задаются самим regionhop и не могут быть переопределены здесь, что бы вы ни вставили — именно это гарантирует, что каждый SOCKS-прокси остаётся привязан к 127.0.0.1. regionhop не предоставляет, не проверяет и не интерпретирует содержимое этого поля.",
		"logs.title":            "Логи: %s",
		"logs.desc":             "Последние 200 строк журнала этого сервиса.",
		"logs.breadcrumb":       "Туннели",
		"updating.title":        "Обновление запущено. Панель пересоберёт/перезагрузит себя и перезапустится — эта страница автоматически повторит попытку через несколько секунд.",
		"updating.back":         "Вернуться на панель сейчас",
	},
	"zh": {
		"nav.credentials":       "Psiphon 配置",
		"nav.dashboard":         "仪表盘",
		"nav.logout":            "退出登录",
		"tunnels.title":         "隧道",
		"tunnels.desc":          "每条线路都运行自己独立的 Psiphon 隧道实例。SOCKS 代理仅绑定在本服务器的 127.0.0.1 上——永远无法从外部访问。",
		"add.title":             "添加线路",
		"add.name":              "名称",
		"add.name.placeholder":  "例如 de-1",
		"add.region":            "地区",
		"add.submit":            "添加并启动",
		"add.note":              "端口会自动从内部范围分配，且始终绑定在回环地址。",
		"table.name":            "名称",
		"table.region":          "地区",
		"table.socks":           "SOCKS",
		"table.status":          "状态",
		"table.flag":            "国旗",
		"table.actions":         "操作",
		"table.empty":           "还没有线路 — 在上方添加一条开始使用。",
		"action.restart":        "重启",
		"action.stop":           "停止",
		"action.remove":         "删除",
		"action.remove.confirm": "删除 %s？",
		"action.logs":           "日志",
		"status.active":         "已连接",
		"status.connecting":     "连接中",
		"status.inactive":       "未运行",
		"status.failed":         "失败",
		"status.unknown":        "未知",
		"update.available":      "有可用更新：<strong>v%s</strong>（当前运行 v%s）。",
		"update.now":            "立即更新",
		"update.confirm":        "现在更新 regionhop？面板将自动重启。",
		"footer.credit":         "由 kaveh 用 ❤️ 制作",
		"login.title":           "管理员密码",
		"login.submit":          "登录",
		"login.error.locked":    "尝试次数过多，请稍后再试。",
		"login.error.wrong":     "密码错误。",
		"creds.title":           "Psiphon 配置",
		"creds.desc":            "自此以后添加的每一条新线路都会应用此配置。",
		"creds.saved":           "已保存。",
		"creds.label":           "配置 JSON",
		"creds.submit":          "保存",
		"creds.note1":           "粘贴你自己的 Psiphon 部署配置的完整 JSON 对象——PropagationChannelId、SponsorId 以及其中包含的任何其他内容。自此以后添加的线路都会应用此配置；不会追溯应用到已有线路——如需更新，请删除后重新添加该线路（或直接编辑配置文件）。",
		"creds.note2":           "EgressRegion、LocalSocksProxyPort、ListenInterface 和 DataRootDirectory 始终由 regionhop 自身决定，无论你粘贴什么内容都无法在此覆盖——这正是保证每个 SOCKS 代理始终绑定在 127.0.0.1 的原因。regionhop 不提供、不校验、也不解读你在此框中填写的任何内容。",
		"logs.title":            "日志：%s",
		"logs.desc":             "该线路服务最近 200 行日志。",
		"logs.breadcrumb":       "隧道",
		"updating.title":        "更新已开始。面板将重新构建/下载自身并重启——本页面将在几秒后自动重试。",
		"updating.back":         "立即返回仪表盘",
	},
}
