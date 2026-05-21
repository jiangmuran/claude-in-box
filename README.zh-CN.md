<p align="center">
  <img src="assets/banner.png" alt="claude-in-box —— 把 Claude Code 装进盒子带走" width="800">
</p>

<p align="center">
  <a href="README.md">English</a> · <strong>简体中文</strong>
</p>

<p align="center">
  <em>把 Claude Code 装进一个盒子,带到任何地方运行。沙箱化、多会话、可编程,任何设备都能连过来用 —— 甚至树莓派。</em>
</p>

<p align="center">
  <a href="#%E7%8A%B6%E6%80%81"><img src="https://img.shields.io/badge/status-early%20WIP-orange" alt="状态:早期 WIP"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-D97757" alt="MIT 协议"></a>
  <img src="https://img.shields.io/badge/docker-multi--arch-2496ED?logo=docker&logoColor=white" alt="docker 多架构">
  <img src="https://img.shields.io/badge/arch-amd64%20%7C%20arm64%20%7C%20armv7-success" alt="amd64 / arm64 / armv7">
</p>

---

## 这是什么

`claude-in-box` 把一整套按需开发环境 + [Claude Code](https://www.anthropic.com/claude-code) 打包进一个 Docker 容器,以 Web 服务的形式对外暴露。

你能得到:

- 📦 **沙箱化的 Linux 盒子**,预装常用语言、工具链和 Claude Code 本体;
- 🖥️ 在虚拟 TTY 里运行的**一个或多个会话**,每个会话都是一段活的 Claude Code 对话;
- 🪝 **自定义 hooks**,在每个生命周期事件(提交 prompt、工具调用、结束等)触发,可以拦截、记录、改写 I/O;
- 🌐 **全局透明 SOCKS5 代理**,一条环境变量配置完,所有出站 TCP/UDP(Claude API、`npm`、`pip`、`apt`、`git` …)自动走代理,无需逐个工具配置;
- 📡 **WebSocket 推流**,会话输出实时流出,带 sequence number,断线可断点续传;
- 🔌 **随时 attach**:`docker attach` 接整个控制面,或者 `claude-in-box attach <session>` 拎进单个会话;
- 🔐 **带鉴权的 Web API**:开箱 bearer token,可签发设备级 token,后续支持 OIDC;
- 🪶 **嵌入式友好**:多架构镜像(amd64 / arm64 / armv7),提供砍掉 UI 的 headless 模式,可以塞进树莓派、N100 软路由这种小盒子里跑。

简单说:别再把 Claude Code 锁死在一台开发机上。让它跑在家庭服务器、便宜 VPS,甚至板载机上,然后从任何设备连过去用。

## 为什么做这个

- **便携**。一个容器、一个镜像、一行命令。每个项目/分支/实验都能拉起完全一致的环境。
- **多会话**。并行驱动多个 Claude Code 对话,不用再开一堆终端 tab。
- **可编程**。Hooks 是一等公民:日志、权限闸、模型路由、输出改写,自己接。
- **远程优先**。从一开始就是按"通过网络驱动"设计的,不是本地工具勉强加个远程。
- **网络环境感知**。透明 SOCKS5 意味着在受限网络里也能跑,不用改 Claude Code 或上游工具。
- **可审计**。每个会话在磁盘上都有结构化 `transcript.jsonl`,输入、输出、hook 事件全留底。
- **嵌入式友好**。瞄准能塞进柜子里的小 ARM 盒子。

## 状态

非常早期。目前仓库里有项目名、logo 和架构草图。代码正在写。想跟进可以 Star/Watch。

详细系统设计见 [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)。

## 计划中的架构

```
                       ┌──────────────────────────────────────────┐
                       │           claude-in-box 容器              │
                       │                                          │
  浏览器 / curl  ──▶  ┌┴──────────┐    ┌──────────────────┐      │
  iPad / 手机    ──▶  │  web api  │◀──▶│ session manager  │──┐   │
  嵌入式 MCU    ──▶  │ + web ui  │    │   (PTY 驱动)     │  │   │
  其他 agent          │  (鉴权)   │    └──────────────────┘  │   │
                       └┬──────────┘            ▲           ▼   │
                       │       ▲                │     ┌──────────┐│
                       │       │                └────▶│  hooks   ││
                       │       │ ws 推流              │  runtime ││
                       │       │                      └──────────┘│
                       │       │                            │     │
                       │       │            ┌───────────────┴──┐  │
                       │       └────────────┤  会话文件         │  │
                       │                    │ + 结构化日志      │  │
                       │                    └──────────────────┘  │
                       │                                          │
                       │  Claude Code  ◀── pty ──  会话 N         │
                       │  Claude Code  ◀── pty ──  会话 2         │
                       │  Claude Code  ◀── pty ──  会话 1         │
                       │              ▲                            │
                       │              │ 所有出站流量               │
                       │     ┌────────┴──────────┐                │
                       │     │ 透明 SOCKS5 网关  │   ◀── 可选     │
                       │     │ (redsocks + nft)  │      PROXY_URL │
                       │     └────────────────────┘                │
                       └──────────────────────────────────────────┘
```

## 快速上手

```bash
# 待发布,命令形态预览:
docker run -d --name claude-box \
  -p 8080:8080 \
  -e ANTHROPIC_API_KEY=sk-... \
  -e CIB_AUTH_TOKEN=$(openssl rand -hex 32) \
  -e CIB_PROXY_URL=socks5://user:pass@proxy.example:1080 \
  -v $(pwd)/workspace:/workspace \
  -v $(pwd)/sessions:/var/lib/claude-in-box/sessions \
  ghcr.io/jiangmuran/claude-in-box:latest

# 打开 Web UI
open http://localhost:8080

# 或者从命令行 attach 进一个活动会话
claude-in-box attach session-abc123

# 或者 attach 整个容器(调试用)
docker attach claude-box
```

Headless / 嵌入式模式(无 Web UI,镜像更小,只暴露 API):

```bash
docker run -d -p 8080:8080 \
  -e CIB_MODE=headless \
  -e CIB_AUTH_TOKEN=... \
  ghcr.io/jiangmuran/claude-in-box:latest-headless
```

## Roadmap

- [ ] 基础 Docker 镜像(Debian-slim + Node + Python + Go + Claude Code)
- [ ] 多架构构建(`linux/amd64`、`linux/arm64`、`linux/arm/v7`)
- [ ] Session manager:spawn / attach / detach / kill,PTY 驱动
- [ ] 输入模拟器:程序化往会话 stdin 写入
- [ ] Hooks runtime:合并加载 `~/.claude/hooks.json` 与项目级 hooks,生命周期事件触发
- [ ] 透明 SOCKS5 代理(`redsocks` + nftables,一个环境变量全部出站重定向)
- [ ] 推流桥:WebSocket + SSE,带可恢复 sequence number
- [ ] Web API:bearer token 鉴权,设备级 token,可选 OIDC
- [ ] `claude-in-box` CLI:`attach`、`ls`、`new`、`kill`、`logs`、`exec`
- [ ] Web UI:终端面板、会话切换器、hook 编辑器,移动端响应式
- [ ] Headless / 嵌入式模式(只 API,不带 UI 资源)
- [ ] 容器重启后会话持久化
- [ ] 多用户 / 多租户

## 贡献

设计还在收敛,暂未开放外部贡献。有想法欢迎开 issue。

## 协议

[MIT](LICENSE)
