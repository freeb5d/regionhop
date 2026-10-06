<div align="center">

**[English](README.md) · [Русский](README.ru.md) · فارسی · [中文](README.zh.md)**

# regionhop

</div>

<div dir="rtl">

<div align="center">

**مدیریت چندین تونل چندمنطقه‌ای Psiphon روی یک سرور لینوکسی.**
پنل وب + خط فرمان SSH. پراکسی‌های SOCKS همیشه فقط روی خود سرور باقی می‌مانند.

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/panel-Go-00ADD8)](panel)
[![Platform](https://img.shields.io/badge/platform-Debian%2FUbuntu%20(systemd)-informational)](install.sh)
[![Release](https://img.shields.io/github/v/release/freeb5d/regionhop)](https://github.com/freeb5d/regionhop/releases/latest)

</div>

---

## فهرست

- [کاری که regionhop انجام می‌دهد](#کاری-که-regionhop-انجام-می‌دهد)
- [پیش‌نیازها](#پیش‌نیازها)
- [نصب](#نصب)
- [پیش از افزودن یک لوکیشن](#پیش-از-افزودن-یک-لوکیشن)
- [نحوهٔ استفاده](#نحوهٔ-استفاده)
- [مناطق موجود](#مناطق-موجود)
- [منطقهٔ خروجی](#منطقهٔ-خروجی)
- [Upstream](#upstream)
- [مدیریت از طریق SSH](#مدیریت-از-طریق-ssh)
- [به‌روزرسانی](#به‌روزرسانی)
- [ساختار ریپازیتوری](#ساختار-ریپازیتوری)
- [مدل امنیتی](#مدل-امنیتی)
- [حذف کامل](#حذف-کامل)
- [مجوز](#مجوز)

## کاری که regionhop انجام می‌دهد

regionhop چند نمونه از
[`psiphon-tunnel-core`](https://github.com/psiphon-labs/psiphon-tunnel-core)
را روی یک سرور اجرا می‌کند؛ هرکدام برای خروج از یک منطقهٔ متفاوت Psiphon
تنظیم شده‌اند. هر نمونه پراکسی SOCKS5 مخصوص به خودش را روی `127.0.0.1`
باز می‌کند — یعنی **هرگز از بیرون سرور در دسترس نیست**. یک پنل وب کوچک به
زبان Go امکان افزودن، حذف، روشن/خاموش‌کردن و مشاهدهٔ لاگ هر منطقه، و همچنین
دیدن این‌که واقعاً کدام‌یک متصل است را فراهم می‌کند؛ همین کارها از طریق SSH و
با دستور `psictl` هم در دسترس‌اند.

- 🌍 **چند منطقه روی یک سرور** — هر تعداد تونل که بخواهید، هرکدام در یک سرویس مجزای systemd
- 🔒 **از پایه فقط لوکال** — پورت‌های SOCKS در تنظیمات Psiphon روی loopback بسته شده‌اند و علاوه‌بر آن با فایروال هم مسدود می‌شوند؛ هیچ‌چیز در پنل نمی‌تواند آن‌ها را به بیرون باز کند
- 🖥 **پنل وب** — رمز عبور به‌صورت هش bcrypt، کوکی‌های نشست امضاشده، قفل‌شدن پس از تلاش‌های ناموفق ورود، پورت تصادفی هنگام نصب
- 📡 **وضعیت واقعی اتصال** — داشبورد بر اساس اعلان‌های خود تونل وضعیت *connecting* یا *active* را نشان می‌دهد، به‌همراه منطقهٔ خروجی و پرچم آن پس از اتصال
- 🔀 **آپ‌استریم** — روی سرورهایی که شبکه‌شان Psiphon را مسدود می‌کند، همهٔ تونل‌ها ابتدا از یک پراکسی SOCKS/HTTP یا یک لینک V2Ray (‏VLESS/VMess/Trojan/Shadowsocks؛ با Xray داخلی اجرا می‌شود) عبور می‌کنند — بخش [Upstream](#upstream) را ببینید
- ⌨️ **کنترل از طریق SSH** — دستور `psictl list|start|stop|restart|logs` برای کسانی که ترمینال را ترجیح می‌دهند
- ⚙️ **کاملاً مبتنی بر systemd** — هر تونل و خودِ پنل یک سرویس معمولی systemd هستند: `systemctl status`، `journalctl`، راه‌اندازی مجدد خودکار هنگام خطا
- 🔄 **به‌روزرسانی خودکار** — با یک دستور آخرین نسخه دریافت و پنل ری‌استارت می‌شود؛ داشبورد خودش اعلام می‌کند که نسخهٔ جدیدی موجود است

## پیش‌نیازها

- یک سرور با systemd و یکی از مدیرهای بسته `apt-get`، `dnf` یا `pacman`، قابل دسترسی از طریق SSH به‌عنوان root (یا کاربری که به `sudo` دسترسی دارد). انتظار می‌رود روی این‌ها کار کند: Debian 11/12 و جدیدتر، Ubuntu 20.04/22.04/24.04 و جدیدتر، Fedora، Arch Linux (و مشتقاتی مثل Manjaro). توزیع‌های **بدون systemd** (مثل Alpine) پشتیبانی **نمی‌شوند**
- معماری `amd64`، `arm64` یا `armv7` برای مسیر سریع (یک بستهٔ آماده، بدون نیاز به Go)؛ در معماری‌های دیگر Go نصب می‌شود و همه‌چیز به‌صورت خودکار از سورس ساخته می‌شود — بقیهٔ مراحل نصب یکسان است
- تنظیمات (config) دیپلوی Psiphon مخصوص خودتان (به بخش [پیش از افزودن یک لوکیشن](#پیش-از-افزودن-یک-لوکیشن) مراجعه کنید)

## نصب

```bash
bash <(curl -Ls https://raw.githubusercontent.com/freeb5d/regionhop/master/install.sh)
```

این دستور یک منوی تعاملی باز می‌کند. برای اولین اجرا گزینهٔ **۱) Full setup**
را انتخاب کنید — معماری سرور شما را تشخیص می‌دهد و یک بستهٔ واحد را از
[آخرین انتشار](https://github.com/freeb5d/regionhop/releases/latest) دانلود
می‌کند که هم پنل و هم `ConsoleClient` مربوط به `psiphon-tunnel-core` در آن
آماده است (روی `amd64`/`arm64`/`armv7` نیازی به Go نیست؛ در معماری‌های دیگر Go
نصب می‌شود و همه‌چیز از سورس ساخته می‌شود)، سرویس‌های systemd را نصب می‌کند،
قوانین فایروال را برای مسدودکردن دسترسی بیرونی به پورت SOCKS هر لوکیشن اضافه
می‌کند، و از شما می‌خواهد رمز عبور مدیریتی پنل را تنظیم کنید. در انتها آدرس
پنل چاپ می‌شود — شامل یک پیشوند مسیر تصادفی (مثلاً
`http://1.2.3.4:34521/a1b2c3d4e5f6/`)، نه فقط یک پورت تصادفی. کل آدرس را
نگه دارید؛ بدون مسیر پنل باز نمی‌شود.

**آدرس را گم کرده‌اید؟** روی سرور دستور `sudo psictl panel-url` را اجرا کنید،
یا منوی نصب‌کننده را باز کنید و **Show panel address** (گزینهٔ ۱۱) را
انتخاب کنید. آدرس از `PANEL_LISTEN` و `PANEL_PATH_PREFIX` در
`/opt/psi-panel/panel/panel.env` ساخته می‌شود. (`psictl panel-url` یک دستور
شل است، نه چیزی برای تایپ در منو — منو فقط عدد می‌پذیرد.)

می‌توانید همین دستور را هر زمان دوباره اجرا کنید تا منو دوباره باز شود (بازساخت
هستهٔ تونل، تغییر رمز عبور، بررسی وضعیت، حذف کامل و غیره) — این اسکریپت
ایدمپوتنت (بی‌اثر در اجرای تکراری) است.

> هم پنل و هم `ConsoleClient` (هستهٔ تونل Psiphon) در هر انتشار با هم در یک
> بستهٔ `regionhop-linux-<arch>.tar.gz` عرضه می‌شوند — برای `amd64`،
> `arm64` و `armv7`. هر انتشار regionhop نسخهٔ `ConsoleClient` را ثابت
> می‌کند و `psictl update` همان بستهٔ ثابت را نصب می‌کند، نه آخرین سورس
> Psiphon را.

## پیش از افزودن یک لوکیشن

Psiphon به یک تنظیمات (`PropagationChannelId`، `SponsorId` و معمولاً
`RemoteServerListUrl`/کلیدهای عمومی امضا) نیاز دارد که باید توسط
[Psiphon Inc.](https://psiphon.ca) به شما به‌عنوان یک شریک ثبت‌شده اعطا شده
باشد — regionhop هیچ راهی برای تولید یا دریافت این‌ها ندارد و هیچ‌کدام را
همراه خودش عرضه نمی‌کند. تنظیمات خودتان را به‌صورت یک شیء JSON در صفحهٔ
**Psiphon config** پنل (یا از طریق گزینهٔ منوی نصب‌کننده با عنوان
**Set Psiphon PropagationChannelId/SponsorId**) وارد کنید، پیش از افزودن
اولین لوکیشن؛ هر لوکیشنی که پس از آن اضافه شود، این تنظیمات را به‌طور خودکار
دریافت می‌کند.

مقادیر `EgressRegion`، `LocalSocksProxyPort`، `ListenInterface` و
`DataRootDirectory` همیشه توسط خود regionhop تعیین می‌شوند و با هیچ‌چیزی که
شما وارد کنید قابل بازنویسی نیستند — همین موضوع تضمین می‌کند که هر پراکسی
SOCKS، بدون توجه به محتوای تنظیماتی که وارد کرده‌اید، روی `127.0.0.1` باقی
بماند.

## نحوهٔ استفاده

آدرس پنل که در پایان نصب چاپ شده را باز کنید، وارد شوید، و سپس:

۱. **Psiphon config** (بالا-راست) — تنظیمات دیپلوی خودتان را یک‌بار وارد کنید
۲. **Add location** — یک نام و یک منطقه؛ یک پورت SOCKS به‌طور خودکار اختصاص می‌یابد
۳. تغییر وضعیت از **connecting** به **active** را ببینید و ستون پرچم را که با منطقه‌ای که Psiphon واقعاً به آن متصل شده پر می‌شود مشاهده کنید
۴. در صورت نیاز روی هر لوکیشن **Restart** / **Stop** / **Remove** / **Logs** بزنید

## مناطق موجود

این‌ها مناطقی هستند که هنگام افزودن لوکیشن می‌توانید انتخاب کنید، به‌علاوهٔ **Any (no preference)** که انتخاب را به Psiphon می‌سپارد.

**اروپا 🌍**

| | منطقه | کد |
|---|---|---|
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1e6-1f1f9.svg" width="20" alt="AT"> | Austria | `AT` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1e7-1f1ea.svg" width="20" alt="BE"> | Belgium | `BE` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1e7-1f1ec.svg" width="20" alt="BG"> | Bulgaria | `BG` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1e8-1f1ff.svg" width="20" alt="CZ"> | Czechia | `CZ` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1e9-1f1f0.svg" width="20" alt="DK"> | Denmark | `DK` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1ea-1f1ea.svg" width="20" alt="EE"> | Estonia | `EE` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1eb-1f1ee.svg" width="20" alt="FI"> | Finland | `FI` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1eb-1f1f7.svg" width="20" alt="FR"> | France | `FR` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1e9-1f1ea.svg" width="20" alt="DE"> | Germany | `DE` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1ee-1f1ea.svg" width="20" alt="IE"> | Ireland | `IE` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1ee-1f1f9.svg" width="20" alt="IT"> | Italy | `IT` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1f1-1f1f9.svg" width="20" alt="LT"> | Lithuania | `LT` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1f3-1f1f1.svg" width="20" alt="NL"> | Netherlands | `NL` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1f3-1f1f4.svg" width="20" alt="NO"> | Norway | `NO` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1f5-1f1f1.svg" width="20" alt="PL"> | Poland | `PL` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1f7-1f1f4.svg" width="20" alt="RO"> | Romania | `RO` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1f7-1f1f8.svg" width="20" alt="RS"> | Serbia | `RS` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1ea-1f1f8.svg" width="20" alt="ES"> | Spain | `ES` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1f8-1f1ea.svg" width="20" alt="SE"> | Sweden | `SE` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1e8-1f1ed.svg" width="20" alt="CH"> | Switzerland | `CH` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1ec-1f1e7.svg" width="20" alt="GB"> | United Kingdom | `GB` |

**آسیا 🌏**

| | منطقه | کد |
|---|---|---|
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1ee-1f1f3.svg" width="20" alt="IN"> | India | `IN` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1ee-1f1e9.svg" width="20" alt="ID"> | Indonesia | `ID` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1ef-1f1f5.svg" width="20" alt="JP"> | Japan | `JP` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1f8-1f1ec.svg" width="20" alt="SG"> | Singapore | `SG` |

**آمریکای شمالی 🌎**

| | منطقه | کد |
|---|---|---|
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1e8-1f1e6.svg" width="20" alt="CA"> | Canada | `CA` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1fa-1f1f8.svg" width="20" alt="US"> | United States | `US` |

**اقیانوسیه 🌏**

| | منطقه | کد |
|---|---|---|
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1e6-1f1fa.svg" width="20" alt="AU"> | Australia | `AU` |

Psiphon فقط کشور خروجی را انتخاب می‌کند، نه یک شهر مشخص.

## منطقهٔ خروجی

به‌محض این‌که یک لوکیشن وضعیت **active** را نشان دهد، ستون پرچم در داشبورد،
منطقهٔ سروری از Psiphon که تونل واقعاً به آن متصل شده را همراه با پرچمش نشان
می‌دهد. این اطلاعات از اعلان اختصاصی `ConnectedServerRegion` خودِ
`psiphon-tunnel-core` در لاگ سیستم خوانده می‌شود — بدون هیچ درخواست خروجی و
بدون هیچ سرویس شخص‌ثالثی.

## Upstream

اگر شبکهٔ سرور Psiphon را مسدود یا کند می‌کند، در صفحهٔ **Psiphon config** پنل یک upstream تنظیم کنید. از آن به بعد هر لوکیشن از طریق آن به Psiphon وصل می‌شود و لوکیشن‌های در حال اجرا برای اعمال تغییر ری‌استارت می‌شوند:

- **پراکسی SOCKS / HTTP** — آدرسی مانند `socks5://127.0.0.1:1080` یا `http://user:pass@host:3128` (به پارامتر `UpstreamProxyUrl` خودِ Psiphon داده می‌شود).
- **V2Ray** — یک لینک `vless://`، `vmess://`، `trojan://` یا `ss://`، یا JSON خروجی Xray را وارد کنید. regionhop آن را با Xray داخلی (`regionhop-upstream.service`) پشت یک ورودی SOCKS فقط روی `127.0.0.1:18999` اجرا می‌کند و پیش از اعمال، کانفیگ را با `xray run -test` بررسی می‌کند.

## مدیریت از طریق SSH

```bash
psictl list                 # نمایش همهٔ سرویس‌های لوکیشن‌ها
psictl start de-1           # فعال و روشن‌کردن یک لوکیشن
psictl stop de-1
psictl restart de-1
psictl logs de-1            # ۲۰۰ خط آخر لاگ
psictl panel-url             # نمایش دوبارهٔ آدرس پنل (با root اجرا کنید)
psictl panel-restart
psictl panel-logs
psictl check-update          # مقایسهٔ نسخهٔ نصب‌شده با آخرین انتشار در GitHub
psictl update                # به‌روزرسانی پنل به آخرین انتشار و ری‌استارت آن
```

## به‌روزرسانی

داشبورد وقتی انتشار جدیدی منتشر شود یک بنر نشان می‌دهد (بررسی به‌صورت
ساعتی، یا به‌صورت دستی با دکمهٔ **Check for updates** پایین صفحه)، و همان‌جا
دکمهٔ **Update now** هم قرار دارد — با کلیک روی آن، به‌روزرسانی در پس‌زمینه
اجرا و پنل به‌طور خودکار ری‌استارت می‌شود.

این دکمه عمداً **تنها** کاری است که پنل می‌تواند به‌عنوان root اجرا کند: فقط
یک اسکریپت ثابت که مالکیتش با root است را از طریق یک قانون `sudoers` با
NOPASSWD اجرا می‌کند که دقیقاً به همان مسیر محدود شده است (به بخش
[مدل امنیتی](#مدل-امنیتی) مراجعه کنید) — این یعنی دسترسی باز به شل نیست.
همان روندی را طی می‌کند که این دستور طی می‌کند:

```bash
psictl update
```

یا بدون نصب `psictl`:

```bash
bash <(curl -Ls https://raw.githubusercontent.com/freeb5d/regionhop/master/install.sh) update
```

هرکدام از این سه روش، آخرین انتشار را دریافت می‌کند، پنل را (در صورت امکان
از باینری آماده) به‌روزرسانی می‌کند، سرویس‌های systemd را دوباره نصب می‌کند
و پنل را ری‌استارت می‌کند. هیچ‌کدام از این‌ها به `ConsoleClient`ی که قبلاً
ساخته شده دست نمی‌زند — اگر می‌خواهید هستهٔ Psiphon را هم بر اساس آخرین
سورس بازسازی کنید، از گزینهٔ منوی نصب‌کننده با عنوان **Rebuild core only**
استفاده کنید.

## ساختار ریپازیتوری

| مسیر | کاربرد |
|---|---|
| `install.sh` | نصب‌کنندهٔ تعاملی/منو، اجرای دوباره‌اش بی‌خطر است |
| `panel/` | پنل وب به زبان Go (احراز هویت، داشبورد، تنظیمات) |
| `systemd/psi-tunnel@.service` | یک قالب سرویس systemd که برای هر منطقه به‌صورت `psi-tunnel@<name>` نمونه‌سازی می‌شود |
| `systemd/psi-panel.service` | سرویس systemd مخصوص خودِ پنل |

روی سرور، همه‌چیز در `/opt/psi-panel/` قرار دارد:

```
/opt/psi-panel/
├── core/ConsoleClient       # باینری ساخته‌شدهٔ Psiphon tunnel-core
├── configs/<name>.json      # تنظیمات تولیدشدهٔ Psiphon برای هر لوکیشن
├── data/<name>/             # پوشهٔ دادهٔ Psiphon مخصوص هر لوکیشن
├── data/tunnels.json        # فهرست لوکیشن‌های پنل
├── panel/psi-panel          # باینری پنل (دانلودشده یا ساخته‌شده)
├── panel/extra-config.json  # تنظیمات دیپلوی Psiphon که شما وارد کرده‌اید
└── VERSION                  # نسخهٔ نصب‌شدهٔ فعلی regionhop
```

## مدل امنیتی

- مقادیر `LocalSocksProxyPort` و `ListenInterface: "lo"` هر لوکیشن *پس از*
  ترکیب‌شدن با تنظیمات Psiphon شمایی که وارد کرده‌اید اعمال می‌شوند، بنابراین
  هیچ‌چیزی که وارد کنید نمی‌تواند پورت SOCKS را از loopback خارج کند — این
  تضمین اصلاً به محتوای تنظیمات شما وابسته نیست.
- قوانین فایروال هم به‌صورت مستقل از تنظیمات، ترافیک بیرونی به پورت SOCKS
  هر لوکیشن را به‌عنوان یک لایهٔ دفاعی اضافه مسدود می‌کنند. این قوانین فقط
  پورت‌هایی را پوشش می‌دهند که خودِ regionhop اختصاص داده (در زنجیرهٔ
  جداگانهٔ iptables به نام `REGIONHOP-SOCKS`)، بنابراین نرم‌افزارهای دیگر
  روی همان سرور — مثلاً پنل یا inbound برنامهٔ 3x-ui روی پورتی در بازهٔ
  ۱۹۰۰۰ تا ۱۹۹۹۹ — هرگز مسدود نمی‌شوند. لوکیشن‌های جدید هم پورت‌هایی را که
  برنامهٔ دیگری روی آن‌ها گوش می‌دهد رد می‌کنند.
- خودِ پنل: هش bcrypt برای رمز عبور، کوکی‌های نشست امضاشده با HMAC،
  `HttpOnly`/`SameSite=Strict`، و قفل‌شدن پس از ۵ تلاش ناموفق ورود از هر
  IP. هر مسیری که تونل‌ها را تغییر می‌دهد نیاز به نشست احرازهویت‌شده دارد.
- فرآیند پنل با کاربر سیستمی غیرمجاز `psipanel` اجرا می‌شود. این کاربر
  هیچ دسترسی دائمی به root ندارد. هر کاری که به‌عنوان root می‌تواند انجام
  دهد، محدود به یک قانون بسیار مشخص در `sudoers` است: دقیقاً
  `systemctl {enable --now|disable --now|restart}` برای واحد هر لوکیشن با نام دقیق آن (به‌صورت صریح فهرست شده، نه با الگوی `psi-tunnel@*`)،
  `systemctl restart psi-panel`، و اجرای یک اسکریپت ثابت با مالکیت root
  به‌نام `self-update.sh` (بدون آرگومان، با مسیر دقیق) برای دکمهٔ
  **Update now** — چیز دیگری روی سرور، ازجمله دسترسی باز به شل، از این
  طریق در دسترس نیست.
- سرویس systemd هر لوکیشن هم علاوه‌بر این با `NoNewPrivileges`،
  `ProtectSystem=strict` و `ReadWritePaths` محدودشده اجرا می‌شود.
- **پنل روی HTTP ساده سرو می‌شود.** رمز عبور و کوکی نشست شما بدون رمزنگاری از شبکه عبور می‌کند و هر کسی در مسیر میان شما و سرور می‌تواند آن‌ها را بخواند. فقط از شبکه‌ای که به آن اعتماد دارید استفاده کنید، یا یک reverse proxy با HTTPS جلوی آن بگذارید، یا آن را روی `127.0.0.1` ببندید (`PANEL_LISTEN` در `/opt/psi-panel/panel/panel.env`) و از طریق تونل SSH واردش شوید.

## حذف کامل

نصب‌کننده را دوباره اجرا کنید و گزینهٔ **Uninstall everything** را انتخاب
کنید — همهٔ سرویس‌ها متوقف و حذف می‌شوند، `/opt/psi-panel` پاک می‌شود، و
ابزار `psictl`، قانون sudoers، و کاربر سیستمی `psipanel` نیز حذف می‌شوند.

## مجوز

[MIT](LICENSE)

</div>
