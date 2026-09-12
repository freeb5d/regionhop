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
- [出口地区](#出口地区)
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
- ⌨️ **SSH 端控制** — 喜欢命令行的话可以用 `psictl list|start|stop|restart|logs`
- ⚙️ **完全基于 systemd** — 每条隧道以及面板本身都是普通的 systemd 服务：`systemctl status`、`journalctl`、失败自动重启,一应俱全
- 🔄 **自我更新** — 一条命令即可拉取最新版本并重启面板；有新版本时面板会自动提示

## 环境要求

- 一台基于 Debian 或 Ubuntu、装有 systemd 的服务器，可以用 root（或拥有 `sudo` 权限的用户）通过 SSH 登录
- `amd64` 架构可走最快路径（直接下载预编译的面板二进制文件）；其他架构会自动从源码构建面板——安装流程其余部分完全相同
- 你自己的 Psiphon 部署配置（参见[添加线路前需要做的事](#添加线路前需要做的事)）

## 安装

```bash
bash <(curl -Ls https://raw.githubusercontent.com/freeb5d/regionhop/master/install.sh)
```

这会打开一个交互式菜单。首次运行请选择 **1) Full setup** ——它会安装
Go，从[最新版本](https://github.com/freeb5d/regionhop/releases/latest)
下载预编译的面板二进制文件（若不可用则自动从源码构建），从源码构建
`psiphon-tunnel-core` 的 `ConsoleClient`，安装 systemd 服务，添加防火墙
规则阻止外部访问 SOCKS 端口范围，并引导你设置面板管理员密码。安装结束时
会打印出面板的访问地址。

同一条命令可以随时重新运行以再次打开菜单（重新构建核心、更换密码、查看
状态、卸载等）——它是幂等的，重复执行不会造成问题。

> 每个版本发布时都会附带预编译好的 `linux/amd64` 面板二进制文件，因此
> 安装面板本质上就是一次下载。`ConsoleClient`（Psiphon 自己的隧道核心）
> 代码量大得多，安装/更新时始终会在你的服务器上从源码本地构建。

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

## 出口地区

一旦某条线路显示 **active**，面板的 Flag 列就会显示 Psiphon 实际连接到
的服务器地区及对应国旗。这个信息来自 `psiphon-tunnel-core` 自身在日志中
发出的 `ConnectedServerRegion` 通知——不发起任何外部请求，不涉及任何第
三方服务。

## 通过 SSH 管理

```bash
psictl list                 # 列出所有线路对应的 systemd 服务
psictl start de-1           # 启用并启动某条线路
psictl stop de-1
psictl restart de-1
psictl logs de-1            # 最近 200 行日志
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
- 防火墙规则会额外、独立于配置内容地拦截所有指向 SOCKS 端口范围
  （19000–19999）的外部流量，作为纵深防御的一层。
- 面板自身：密码使用 bcrypt 哈希，会话 Cookie 使用 HMAC 签名，带有
  `HttpOnly`/`SameSite=Strict` 属性，同一 IP 连续 5 次登录失败会被锁定。
  任何会改动线路状态的路由都必须先通过身份验证。
- 面板进程以无特权的系统用户 `psipanel` 运行，不具备任何常驻的 root
  权限。它能以 root 身份执行的操作被严格限定在一条 `sudoers` 规则内：
  仅有 `systemctl {enable --now|disable --now|restart} psi-tunnel@*`、
  `systemctl restart psi-panel`，以及为 **Update now** 按钮执行一个固定
  的、归 root 所有的 `self-update.sh` 脚本（不带任何参数，路径完全固定）
  ——服务器上没有任何其他东西、也没有任何开放的 shell 权限可以通过这条
  规则被触及。
- 每条线路自身的 systemd 服务还额外启用了 `NoNewPrivileges`、
  `ProtectSystem=strict`，以及受限的 `ReadWritePaths`。

## 卸载

重新运行安装脚本并选择 **Uninstall everything**——它会停止并删除所有
服务、删除 `/opt/psi-panel`、移除 `psictl` 辅助命令、sudoers 规则，以及
`psipanel` 系统用户。

## 许可证

[MIT](LICENSE)
