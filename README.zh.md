<div align="center">

**[English](README.md) · [Русский](README.ru.md) · [فارسی](README.fa.md) · 中文**

# regionhop

**单台 Linux 服务器上的多地区 Psiphon 隧道管理器。**
Web 面板 + SSH 命令行。SOCKS 代理始终只在本机可访问。

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/panel-Go-00ADD8)](panel)
[![Platform](https://img.shields.io/badge/platform-Debian%2FUbuntu%20(systemd)-informational)](install.sh)
[![Release](https://img.shields.io/github/v/release/freeb5d/regionhop)](https://github.com/freeb5d/regionhop/releases/latest)

</div>

---

## 目录

- [功能介绍](#功能介绍)
- [环境要求](#环境要求)
- [安装](#安装)
- [添加线路前需要做的事](#添加线路前需要做的事)
- [使用方法](#使用方法)
- [可用地区](#可用地区)
- [出口地区](#出口地区)
- [Upstream](#upstream)
- [通过 SSH 管理](#通过-ssh-管理)
- [更新](#更新)
- [目录结构](#目录结构)
- [安全模型](#安全模型)
- [卸载](#卸载)
- [许可证](#许可证)

## 功能介绍

regionhop 在一台服务器上运行多个
[`psiphon-tunnel-core`](https://github.com/psiphon-labs/psiphon-tunnel-core)
实例，每个实例配置为从不同的 Psiphon 地区出口。每个实例都开放自己独立的
SOCKS5 代理——仅绑定在 `127.0.0.1`，因此**永远无法从服务器外部访问**。一个
用 Go 编写的轻量 Web 面板可以添加、删除、启动/停止各条线路、查看日志，
并实时查看哪条线路真正连接成功；同样的操作也可以通过 SSH 用 `psictl`
命令完成。

- 🌍 **一台服务器多地区** — 想开多少条线路都行，每条都运行在独立的 systemd 服务中
- 🔒 **默认只在本机可访问** — SOCKS 端口在 Psiphon 配置中绑定回环地址，防火墙层面再加一道封锁；面板本身无法把它们暴露到外部
- 🖥 **Web 面板** — 密码使用 bcrypt 哈希存储、签名会话 Cookie、登录失败次数超限自动锁定、安装时随机分配监听端口
- 📡 **真实连接状态** — 面板依据隧道自身发出的通知区分 *connecting*（连接中）与 *active*（已连接），连接成功后还会显示出口地区和对应国旗
- 🔀 **上游代理** — 在本机网络屏蔽 Psiphon 的服务器上，让所有隧道先经过 SOCKS/HTTP 代理或 V2Ray 链接（VLESS/VMess/Trojan/Shadowsocks，由内置的 Xray 运行）——见 [Upstream](#upstream)
- ⌨️ **SSH 端控制** — 喜欢命令行的话可以用 `psictl list|start|stop|restart|logs`
- ⚙️ **完全基于 systemd** — 每条隧道以及面板本身都是普通的 systemd 服务：`systemctl status`、`journalctl`、失败自动重启,一应俱全
- 🔄 **自我更新** — 一条命令即可拉取最新版本并重启面板；有新版本时面板会自动提示

## 环境要求

- 一台装有 systemd，并带有 `apt-get`、`dnf` 或 `pacman` 之一的服务器，可以用 root（或拥有 `sudo` 权限的用户）通过 SSH 登录。预期可在以下系统运行：Debian 11/12 及更新版本、Ubuntu 20.04/22.04/24.04 及更新版本、Fedora、Arch Linux（及 Manjaro 等衍生版）。**不支持**没有 systemd 的发行版（例如 Alpine）
- `amd64`、`arm64` 或 `armv7` 架构可走最快路径（一个预编译的整合包，完全不需要 Go）；其他架构会自动安装 Go 并从源码构建——安装流程其余部分完全相同
- 你自己的 Psiphon 部署配置（参见[添加线路前需要做的事](#添加线路前需要做的事)）

## 安装

```bash
bash <(curl -Ls https://raw.githubusercontent.com/freeb5d/regionhop/master/install.sh)
```

这会打开一个交互式菜单。首次运行请选择 **1) Full setup** ——它会检测
服务器架构，从[最新版本](https://github.com/freeb5d/regionhop/releases/latest)
下载一个整合包，其中已包含面板和 `psiphon-tunnel-core` 的 `ConsoleClient`
（`amd64`/`arm64`/`armv7` 无需安装 Go；其他架构会安装 Go 并从源码构建），
安装 systemd 服务，添加防火墙规则阻止外部访问每条线路的 SOCKS 端口，并引导你
设置面板管理员密码。安装结束时会打印出面板的访问地址——其中包含随机的路径
前缀（例如 `http://1.2.3.4:34521/a1b2c3d4e5f6/`），而不仅仅是随机端口。请保存
完整地址；缺少路径时面板无法打开。

**忘记地址了？** 在服务器上运行 `sudo psictl panel-url`，或打开安装菜单选择
**Show panel address**（第 11 项）。地址由 `/opt/psi-panel/panel/panel.env`
中的 `PANEL_LISTEN` 和 `PANEL_PATH_PREFIX` 组成。（`psictl panel-url` 是
shell 命令，不是在菜单里输入的内容——菜单只接受数字。）

同一条命令可以随时重新运行以再次打开菜单（重新构建核心、更换密码、查看
状态、卸载等）——它是幂等的，重复执行不会造成问题。

> 面板和 `ConsoleClient`（Psiphon 自己的隧道核心）每个版本都打包在同一个
> `regionhop-linux-<arch>.tar.gz` 中发布，支持 `amd64`、`arm64` 和 `armv7`。
> 每个 regionhop 版本都会固定它所带的 `ConsoleClient` 版本，`psictl update`
> 始终安装这个固定的整合包，而不是去拉取 Psiphon 的最新源码。

## 添加线路前需要做的事

Psiphon 需要一份由 [Psiphon Inc.](https://psiphon.ca) 作为注册合作伙伴
颁发给你的配置（`PropagationChannelId`、`SponsorId`，通常还包括
`RemoteServerListUrl`/签名公钥等）——regionhop 无法生成或获取这些内容，
也不会随程序附带。在添加第一条线路之前，请把你自己的配置以 JSON 对象的
形式粘贴到面板的 **Psiphon config** 页面（或通过安装脚本菜单中的
**Set Psiphon PropagationChannelId/SponsorId** 选项）；此后添加的每一条
线路都会自动使用这份配置。

`EgressRegion`、`LocalSocksProxyPort`、`ListenInterface` 和
`DataRootDirectory` 始终由 regionhop 自身设定，你粘贴的配置无法覆盖它们
——正是这一点确保了无论你的配置内容是什么，每个 SOCKS 代理都始终绑定在
`127.0.0.1`。

## 使用方法

打开安装结束时打印出的面板地址，登录后：

1. **Psiphon config**（右上角）——粘贴一次你的部署配置
2. **Add location** ——填写名称和地区；SOCKS 端口会自动分配
3. 观察状态徽标从 **connecting** 变为 **active**，Flag 一列会显示 Psiphon 实际落地的地区
4. 按需对每条线路进行 **Restart** / **Stop** / **Remove** / **Logs** 操作

## 可用地区

添加线路时可以选择这些地区，另外还有 **Any (no preference)**，由 Psiphon 自行选择。

**欧洲 🌍**

| | 地区 | 代码 |
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

**亚洲 🌏**

| | 地区 | 代码 |
|---|---|---|
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1ee-1f1f3.svg" width="20" alt="IN"> | India | `IN` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1ee-1f1e9.svg" width="20" alt="ID"> | Indonesia | `ID` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1ef-1f1f5.svg" width="20" alt="JP"> | Japan | `JP` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1f8-1f1ec.svg" width="20" alt="SG"> | Singapore | `SG` |

**北美洲 🌎**

| | 地区 | 代码 |
|---|---|---|
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1e8-1f1e6.svg" width="20" alt="CA"> | Canada | `CA` |
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1fa-1f1f8.svg" width="20" alt="US"> | United States | `US` |

**大洋洲 🌏**

| | 地区 | 代码 |
|---|---|---|
| <img src="https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/svg/1f1e6-1f1fa.svg" width="20" alt="AU"> | Australia | `AU` |

Psiphon 只能指定出口国家，不能指定具体城市。

## 出口地区

一旦某条线路显示 **active**，面板的 Flag 列就会显示 Psiphon 实际连接到
的服务器地区及对应国旗。这个信息来自 `psiphon-tunnel-core` 自身在日志中
发出的 `ConnectedServerRegion` 通知——不发起任何外部请求，不涉及任何第
三方服务。

## Upstream

如果服务器自身的网络屏蔽或限速 Psiphon，可以在面板的 **Psiphon config** 页面设置上游。设置后每条线路都会先经过它再连接 Psiphon，正在运行的线路会被重启以应用变更：

- **SOCKS / HTTP 代理** — 形如 `socks5://127.0.0.1:1080` 或 `http://user:pass@host:3128` 的地址（交给 Psiphon 自带的 `UpstreamProxyUrl`）。
- **V2Ray** — 粘贴 `vless://`、`vmess://`、`trojan://` 或 `ss://` 链接，或 Xray 出站 JSON。regionhop 用内置的 Xray（`regionhop-upstream.service`）运行它，SOCKS 入口仅监听 `127.0.0.1:18999`，并在应用前用 `xray run -test` 检查配置。

## 通过 SSH 管理

```bash
psictl list                 # 列出所有线路对应的 systemd 服务
psictl start de-1           # 启用并启动某条线路
psictl stop de-1
psictl restart de-1
psictl logs de-1            # 最近 200 行日志
psictl panel-url             # 再次显示面板地址（需 root 权限）
psictl panel-restart
psictl panel-logs
psictl check-update          # 比较当前版本与 GitHub 最新版本
psictl update                # 将面板更新到最新版本并重启
```

## 更新

有新版本发布时，面板会显示一条横幅提示（每小时自动检查一次，也可以点击
页面底部的 **Check for updates** 按钮手动检查），旁边就有一个
**Update now** 按钮——点击后会在后台执行更新并自动重启面板。

这个按钮是面板唯一能以 root 身份触发的操作：它通过一条限定了精确路径的
NOPASSWD `sudoers` 规则，运行一个固定的、由 root 所有的脚本（详见
[安全模型](#安全模型)）——这并不是开放的 shell 访问权限。它执行的流程和
下面这条命令完全相同：

```bash
psictl update
```

或者在未安装 `psictl` 的情况下：

```bash
bash <(curl -Ls https://raw.githubusercontent.com/freeb5d/regionhop/master/install.sh) update
```

以上三种方式都会拉取最新版本、更新面板（尽量使用预编译二进制文件）、重新
安装 systemd 服务并重启面板。它们都**不会**改动已经构建好的
`ConsoleClient`——如果还想用 Psiphon 上游最新源码重新构建核心，请使用
安装脚本菜单中的 **Rebuild core only** 选项。

## 目录结构

| 路径 | 作用 |
|---|---|
| `install.sh` | 交互式安装脚本/菜单，可安全地重复运行 |
| `panel/` | Go 编写的 Web 面板（鉴权、仪表盘、设置） |
| `systemd/psi-tunnel@.service` | systemd 服务模板，为每个地区实例化为 `psi-tunnel@<name>` |
| `systemd/psi-panel.service` | 面板自身的 systemd 服务 |

在服务器上，所有内容都存放在 `/opt/psi-panel/` 下：

```
/opt/psi-panel/
├── core/ConsoleClient       # 已构建的 Psiphon tunnel-core 二进制文件
├── configs/<name>.json      # 每条线路生成的 Psiphon 配置
├── data/<name>/             # 每条线路对应的 Psiphon 数据目录
├── data/tunnels.json        # 面板维护的线路清单
├── panel/psi-panel          # 面板二进制文件（下载或本地构建）
├── panel/extra-config.json  # 你粘贴的 Psiphon 部署配置
└── VERSION                  # 当前已安装的 regionhop 版本号
```

## 安全模型

- 每条线路的 `LocalSocksProxyPort` 和 `ListenInterface: "lo"` 是在与你
  粘贴的 Psiphon 配置合并**之后**才应用的，因此你粘贴的任何内容都无法把
  SOCKS 端口移出回环地址——这个保证与你的配置内容完全无关。
- 防火墙规则会额外、独立于配置内容地拦截所有指向每条线路自身 SOCKS 端口的
  外部流量，作为纵深防御的一层。规则只覆盖 regionhop 自己分配的端口（放在
  单独的 iptables 链 `REGIONHOP-SOCKS` 中），因此同一台服务器上的其他软件——
  例如恰好使用 19000–19999 范围内端口的 3x-ui 面板或入站——永远不会被拦截。
  新增线路时也会跳过已有程序正在监听的端口。
- 面板自身：密码使用 bcrypt 哈希，会话 Cookie 使用 HMAC 签名，带有
  `HttpOnly`/`SameSite=Strict` 属性，同一 IP 连续 5 次登录失败会被锁定。
  任何会改动线路状态的路由都必须先通过身份验证。
- 面板进程以无特权的系统用户 `psipanel` 运行，不具备任何常驻的 root
  权限。它能以 root 身份执行的操作被严格限定在一条 `sudoers` 规则内：
  仅有 `systemctl {enable --now|disable --now|restart}`，对象是每条线路自己的单元，按其确切名称显式列出（而不是 `psi-tunnel@*` 通配符）、
  `systemctl restart psi-panel`，以及为 **Update now** 按钮执行一个固定
  的、归 root 所有的 `self-update.sh` 脚本（不带任何参数，路径完全固定）
  ——服务器上没有任何其他东西、也没有任何开放的 shell 权限可以通过这条
  规则被触及。
- 每条线路自身的 systemd 服务还额外启用了 `NoNewPrivileges`、
  `ProtectSystem=strict`，以及受限的 `ReadWritePaths`。
- **面板通过明文 HTTP 提供服务。** 你的密码和会话 Cookie 会不加密地在网络上传输，你与服务器之间路径上的任何人都能读取。请只在可信网络中使用，或在前面加一层 HTTPS 反向代理，或让它只绑定到 `127.0.0.1`（`/opt/psi-panel/panel/panel.env` 中的 `PANEL_LISTEN`），再通过 SSH 隧道访问。

## 卸载

重新运行安装脚本并选择 **Uninstall everything**——它会停止并删除所有
服务、删除 `/opt/psi-panel`、移除 `psictl` 辅助命令、sudoers 规则，以及
`psipanel` 系统用户。

## 许可证

[MIT](LICENSE)
