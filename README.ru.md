<div align="center">

**[English](README.md) · Русский · [فارسی](README.fa.md) · [中文](README.zh.md)**

# regionhop

**Менеджер многорегиональных туннелей Psiphon для одного Linux-сервера.**
Веб-панель + SSH CLI. SOCKS-прокси всегда остаются локальными.

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/panel-Go-00ADD8)](panel)
[![Platform](https://img.shields.io/badge/platform-Debian%2FUbuntu%20(systemd)-informational)](install.sh)
[![Release](https://img.shields.io/github/v/release/freeb5d/regionhop)](https://github.com/freeb5d/regionhop/releases/latest)

</div>

---

## Содержание

- [Что делает regionhop](#что-делает-regionhop)
- [Требования](#требования)
- [Установка](#установка)
- [Перед добавлением локации](#перед-добавлением-локации)
- [Использование](#использование)
- [Доступные регионы](#доступные-регионы)
- [Регион выхода](#регион-выхода)
- [Upstream](#upstream)
- [Управление по SSH](#управление-по-ssh)
- [Обновление](#обновление)
- [Структура репозитория](#структура-репозитория)
- [Модель безопасности](#модель-безопасности)
- [Удаление](#удаление)
- [Лицензия](#лицензия)

## Что делает regionhop

regionhop запускает несколько экземпляров
[`psiphon-tunnel-core`](https://github.com/psiphon-labs/psiphon-tunnel-core)
на одном сервере, каждый настроен на выход через свой регион Psiphon.
Каждый экземпляр открывает собственный SOCKS5-прокси — привязанный только к
`127.0.0.1`, то есть **недоступный снаружи сервера**. Небольшая веб-панель на
Go позволяет добавлять, удалять, запускать/останавливать локации и смотреть
логи, а также видеть, какая локация реально подключена; те же действия
доступны по SSH через команду `psictl`.

- 🌍 **Несколько регионов на одном сервере** — сколько угодно локаций, каждая в своём systemd-юните
- 🔒 **Изначально только локально** — SOCKS-порты привязаны к loopback в конфиге Psiphon *и* дополнительно блокируются файрволом; панель не может открыть их наружу
- 🖥 **Веб-панель** — пароль в виде bcrypt-хэша, подписанные сессионные cookie, блокировка после неудачных попыток входа, случайный порт при установке
- 📡 **Реальный статус подключения** — панель показывает *connecting* и *active* на основе собственных уведомлений туннеля, плюс регион выхода и флаг после подключения
- 🔀 **Upstream-прокси** — на серверах, где сеть блокирует Psiphon, весь трафик туннелей сначала идёт через SOCKS/HTTP-прокси или V2Ray-ссылку (VLESS/VMess/Trojan/Shadowsocks, запускается встроенным Xray) — см. [Upstream](#upstream)
- ⌨️ **Управление по SSH** — `psictl list|start|stop|restart|logs` для тех, кто предпочитает терминал
- ⚙️ **Полностью на systemd** — каждый туннель и сама панель — обычные systemd-сервисы: `systemctl status`, `journalctl`, автоперезапуск при сбое
- 🔄 **Самообновление** — одна команда подтягивает последний релиз и перезапускает панель; панель сама сообщает о наличии обновления

## Требования

- Сервер с systemd и одним из менеджеров пакетов `apt-get`, `dnf` или `pacman`, доступный по SSH как root (или пользователь с `sudo`). Должно работать на: Debian 11/12 и новее, Ubuntu 20.04/22.04/24.04 и новее, Fedora, Arch Linux (и производных вроде Manjaro). **Не поддерживаются** дистрибутивы без systemd (например, Alpine)
- `amd64`, `arm64` или `armv7` — быстрый путь (один готовый архив, Go не нужен); на других архитектурах Go устанавливается и всё собирается из исходников автоматически — остальная установка идёт так же
- Ваш собственный конфиг развёртывания Psiphon (см. [Перед добавлением локации](#перед-добавлением-локации))

## Установка

```bash
bash <(curl -Ls https://raw.githubusercontent.com/freeb5d/regionhop/master/install.sh)
```

Откроется интерактивное меню. При первом запуске выберите **1) Full setup** —
скрипт определит архитектуру сервера и скачает один архив из
[последнего релиза](https://github.com/freeb5d/regionhop/releases/latest), в
котором уже лежат и панель, и `ConsoleClient` из `psiphon-tunnel-core` (Go не
нужен на `amd64`/`arm64`/`armv7`; на остальных архитектурах Go устанавливается
и всё собирается из исходников), установит systemd-юниты, добавит правила
файрвола, блокирующие внешний доступ к SOCKS-порту каждой локации, и попросит
задать пароль администратора панели. В конце будет выведен адрес панели —
включая случайный префикс пути (например,
`http://1.2.3.4:34521/a1b2c3d4e5f6/`), а не только случайный порт. Сохраните
его целиком: без пути панель не откроется.

**Потеряли адрес?** Выполните на сервере `sudo psictl panel-url` или откройте
меню установщика и выберите **Show panel address** (пункт 11). Адрес
собирается из `PANEL_LISTEN` и `PANEL_PATH_PREFIX` в
`/opt/psi-panel/panel/panel.env`. (`psictl panel-url` — команда оболочки, а не
пункт меню: меню принимает только номера.)

Эту же команду можно запускать повторно в любой момент, чтобы снова открыть
меню (пересобрать ядро, сменить пароль, посмотреть статус, удалить установку
и т.д.) — она идемпотентна.

> И панель, и `ConsoleClient` (ядро туннеля Psiphon) поставляются вместе в
> одном архиве `regionhop-linux-<arch>.tar.gz` на каждый релиз — для `amd64`,
> `arm64` и `armv7`. Каждый релиз regionhop фиксирует версию `ConsoleClient`,
> и `psictl update` ставит именно этот архив, а не свежий исходный код Psiphon.

## Перед добавлением локации

Psiphon требует конфиг (`PropagationChannelId`, `SponsorId`, а обычно ещё
`RemoteServerListUrl` и открытые ключи подписи), выданный вам
[Psiphon Inc.](https://psiphon.ca) как зарегистрированному партнёру —
regionhop никак не может сгенерировать или получить это и не поставляется с
такими данными. Вставьте свой собственный конфиг в виде JSON-объекта на
странице панели **Psiphon config** (или через пункт меню установщика
**Set Psiphon PropagationChannelId/SponsorId**) перед добавлением первой
локации; все локации, добавленные после этого, подхватят конфиг
автоматически.

`EgressRegion`, `LocalSocksProxyPort`, `ListenInterface` и
`DataRootDirectory` всегда задаются самим regionhop и не могут быть
переопределены вставленным конфигом — именно это гарантирует, что каждый
SOCKS-прокси остаётся привязан к `127.0.0.1`, независимо от содержимого
вашего конфига.

## Использование

Откройте адрес панели, выведенный в конце установки, войдите и:

1. **Psiphon config** (в правом верхнем углу) — один раз вставьте свой конфиг развёртывания
2. **Add location** — имя и регион; SOCKS-порт назначается автоматически
3. Наблюдайте, как статус меняется с **connecting** на **active**, и как в столбце Flag появляется регион, куда реально подключился Psiphon
4. **Restart** / **Stop** / **Remove** / **Logs** для каждой локации по мере необходимости

## Доступные регионы

Эти регионы можно выбрать при добавлении локации, плюс **Any (no preference)** — Psiphon выбирает сам.

**Европа 🌍**

| | Регион | Код |
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

**Азия 🌏**

| | Регион | Код |
|---|---|---|
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1ee-1f1f3.svg" width="20" alt="IN"> | India | `IN` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1ee-1f1e9.svg" width="20" alt="ID"> | Indonesia | `ID` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1ef-1f1f5.svg" width="20" alt="JP"> | Japan | `JP` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1f8-1f1ec.svg" width="20" alt="SG"> | Singapore | `SG` |

**Северная Америка 🌎**

| | Регион | Код |
|---|---|---|
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1e8-1f1e6.svg" width="20" alt="CA"> | Canada | `CA` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1fa-1f1f8.svg" width="20" alt="US"> | United States | `US` |

**Океания 🌏**

| | Регион | Код |
|---|---|---|
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1e6-1f1fa.svg" width="20" alt="AU"> | Australia | `AU` |

Psiphon выбирает только страну выхода, а не конкретный город.

## Регион выхода

Как только локация показывает **active**, столбец Flag на панели отображает
регион сервера Psiphon, к которому реально произошло подключение, вместе с
флагом. Эта информация берётся из собственного уведомления
`ConnectedServerRegion` `psiphon-tunnel-core` в журнале — без внешних
запросов, без сторонних сервисов.

## Upstream

Если сеть сервера блокирует или замедляет Psiphon, задайте upstream на странице панели **Psiphon config**. Тогда каждая локация подключается к Psiphon через него, а работающие локации перезапускаются, чтобы применить изменение:

- **SOCKS / HTTP-прокси** — URL вида `socks5://127.0.0.1:1080` или `http://user:pass@host:3128` (передаётся в собственный параметр Psiphon `UpstreamProxyUrl`).
- **V2Ray** — вставьте ссылку `vless://`, `vmess://`, `trojan://` или `ss://` либо JSON outbound для Xray. regionhop запускает её встроенным Xray (`regionhop-upstream.service`) за SOCKS-входом только на `127.0.0.1:18999` и проверяет конфиг командой `xray run -test` перед применением.

## Управление по SSH

```bash
psictl list                 # список всех юнитов-локаций
psictl start de-1           # включить и запустить локацию
psictl stop de-1
psictl restart de-1
psictl logs de-1            # последние 200 строк журнала
psictl panel-url             # снова показать адрес панели (запускать от root)
psictl panel-restart
psictl panel-logs
psictl check-update          # сравнить установленную версию с последним релизом на GitHub
psictl update                # обновить панель до последнего релиза и перезапустить её
```

## Обновление

Панель показывает баннер (проверка раз в час, либо вручную кнопкой
**Check for updates** внизу страницы), когда выходит новый релиз, а рядом —
кнопка **Update now**: нажатие запускает обновление в фоне и автоматически
перезапускает панель.

Эта кнопка намеренно — единственное, что панель может выполнить от имени
root: она запускает один фиксированный скрипт, принадлежащий root, через
правило `sudoers` с NOPASSWD, ограниченное этим конкретным путём (см.
[Модель безопасности](#модель-безопасности)) — это не открытый доступ к
shell. Работает по тому же сценарию, что и:

```bash
psictl update
```

или без установленного `psictl`:

```bash
bash <(curl -Ls https://raw.githubusercontent.com/freeb5d/regionhop/master/install.sh) update
```

Любой из трёх способов скачивает последний релиз, обновляет панель (по
возможности — из готового бинарника), переустанавливает systemd-юниты и
перезапускает панель. Ни один из них не трогает уже собранный
`ConsoleClient` — используйте пункт меню установщика **Rebuild core only**,
если хотите пересобрать ядро Psiphon из актуальных исходников.

## Структура репозитория

| Путь | Назначение |
|---|---|
| `install.sh` | Интерактивный установщик/меню, безопасно перезапускать |
| `panel/` | Веб-панель на Go (авторизация, дашборд, настройки) |
| `systemd/psi-tunnel@.service` | Шаблон systemd-юнита, инстанцируется на каждый регион как `psi-tunnel@<name>` |
| `systemd/psi-panel.service` | Собственный systemd-юнит панели |

На сервере всё хранится в `/opt/psi-panel/`:

```
/opt/psi-panel/
├── core/ConsoleClient       # собранный бинарник Psiphon tunnel-core
├── configs/<name>.json      # сгенерированный конфиг Psiphon на каждую локацию
├── data/<name>/             # каталог данных Psiphon для каждой локации
├── data/tunnels.json        # реестр локаций панели
├── panel/psi-panel          # бинарник панели (скачан или собран)
├── panel/extra-config.json  # ваш вставленный конфиг развёртывания Psiphon
└── VERSION                  # текущая установленная версия regionhop
```

## Модель безопасности

- `LocalSocksProxyPort` и `ListenInterface: "lo"` каждой локации
  применяются *после* объединения с вашим вставленным конфигом Psiphon, так
  что ничто из вставленного не может сдвинуть SOCKS-порт с loopback — эта
  гарантия вообще не зависит от содержимого вашего конфига.
- Правила файрвола дополнительно блокируют внешний трафик к SOCKS-порту
  каждой локации как дополнительный уровень защиты, независимо от конфига.
  Они охватывают только порты, назначенные самим regionhop (в отдельной цепочке
  iptables `REGIONHOP-SOCKS`), поэтому другое ПО на том же сервере — например,
  панель или inbound 3x-ui на порту из диапазона 19000–19999 — никогда не
  блокируется. Новые локации также пропускают порты, на которых уже кто-то
  слушает.
- Сама панель: bcrypt-хэш пароля, HMAC-подписанные сессионные cookie,
  `HttpOnly`/`SameSite=Strict`, блокировка после 5 неудачных попыток входа
  с одного IP. Любой маршрут, изменяющий туннели, требует аутентифицированной
  сессии.
- Процесс панели работает от имени непривилегированного системного
  пользователя `psipanel`. У него нет постоянного доступа к root. Всё, что
  он может выполнить от root, ограничено одним точечным правилом `sudoers`:
  ровно `systemctl {enable --now|disable --now|restart}` для каждой локации по точному имени её юнита (перечислены явно, без шаблона `psi-tunnel@*`),
  `systemctl restart psi-panel` и запуск одного фиксированного скрипта
  `self-update.sh`, принадлежащего root (без аргументов, по точному пути) —
  для кнопки **Update now**; больше ничего на сервере, включая открытый
  доступ к shell, через это не достижимо.
- Каждый systemd-сервис локации дополнительно работает с `NoNewPrivileges`,
  `ProtectSystem=strict` и ограниченным `ReadWritePaths`.
- **Панель работает по обычному HTTP.** Пароль и cookie сессии передаются по сети без шифрования, и любой на пути между вами и сервером может их прочитать. Используйте её только из доверенной сети, либо поставьте перед ней HTTPS-обратный прокси, либо привяжите её к `127.0.0.1` (`PANEL_LISTEN` в `/opt/psi-panel/panel/panel.env`) и заходите через SSH-туннель.

## Удаление

Запустите установщик заново и выберите **Uninstall everything** — он
остановит и удалит все юниты, сотрёт `/opt/psi-panel`, удалит помощник
`psictl`, правило sudoers и системного пользователя `psipanel`.

## Лицензия

[MIT](LICENSE)
