<p align="center">
  <img src="assets/banner.png" alt="claude-in-box —— 把 Claude Code 装进盒子带走" width="800">
</p>

<p align="center">
  <a href="README.md">English</a> &middot; <strong>简体中文</strong>
</p>

<p align="center">
  <em>把 Claude Code 装进一个容器,Web 管理,任何设备都能连过来用 —— 包括树莓派,甚至单片机。</em>
</p>

<p align="center">
  <a href="#%E7%8A%B6%E6%80%81"><img src="https://img.shields.io/badge/status-early%20WIP-orange" alt="状态:早期 WIP"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-D97757" alt="MIT 协议"></a>
  <img src="https://img.shields.io/badge/docker-multi--arch-2496ED?logo=docker&logoColor=white" alt="docker 多架构">
  <img src="https://img.shields.io/badge/arch-amd64%20%7C%20arm64%20%7C%20armv7-success" alt="amd64 / arm64 / armv7">
</p>

---

## 这是什么

`claude-in-box` 把一整套按需开发环境 + [Claude Code](https://www.anthropic.com/claude-code) 打包进一个 Docker 容器,然后以 Web 服务的形式对外暴露。

你能得到:

- 一个沙箱化的 Linux 盒子,预装常用语言、工具链和 Claude Code 本体;
- 在虚拟 TTY 里运行的一个或多个会话,默认以 bypass-permission 模式跑(容器本身就是边界,逐工具弹权限只会成为噪音);
- 结构化事件流:文本增量、工具调用、todo 更新、token 用量、状态变化、停止原因、模型元数据,统一以 JSON 帧通过 WebSocket / SSE 推出;
- Web UI 提供会话生命周期管理:新建、attach、resume、kill、运行中切模型;
- 两种 Claude 计费方式:订阅账号登录,或粘贴 API key;
- Web UI + REST/WebSocket API,都走 API key 鉴权;
- 面向差异化客户端的多套传输方案:HTTPS、WSS、HTTP + AES-GCM 信封加密、SSE、MQTT(规划中),各自配独立的鉴权与加密形态,目标是从手机浏览器一路覆盖到 STM32;
- 透明 SOCKS5 层:所有出站流量经一个上游代理重定向,无需逐工具配置;
- 可编程 hooks,在所有生命周期事件触发;
- 多架构镜像 + 砍掉 UI 的 headless 版本,小到可以塞进树莓派或 N100 软路由。

简单说:别再把 Claude Code 锁死在一台开发机上。让它跑在家庭服务器、便宜 VPS,甚至板载机上,然后从任何设备用合适的协议连过来。

## 理想工作流

```
1.  选一个开发环境镜像:预制的,或者你自己定制的。
2.  做端口转发(默认 8080)。
3.  docker run —— 容器拉起控制面,等待鉴权。
4.  打开 Web 面板,用启动时生成的主 API key 登入。
5.  选择如何与 Claude 通信:用你的 claude.ai 订阅账号登录,
    或粘贴 Anthropic API key。每个会话各自独立选择。
6.  仪表盘可见:活跃会话、token 消耗、累计工作时间、
    当前模型、hook 活动。
7.  新建会话 —— 直接进入一个跑着 Claude Code (bypass-permission)
    的虚拟 TTY 面板。也可以从历史 transcript 里 resume,
    接着上次停下的位置继续。
8.  与 Claude 对话,中途随时切模型;todo、工具调用、状态
    都从结构化事件流中渲染到侧栏,而不仅仅是终端原始输出。
9.  手机、平板、嵌入式 MCU 或别的 agent,可以走与设备匹配
    的传输协议,连同一份会话。
```

这就是 README 后面这些章节要讲清楚的整个闭环。

## 能力

### 会话与 Claude Code

| 能力 | 说明 |
|------|------|
| 多会话 | PTY 驱动;spawn / attach / detach / kill / list。多个客户端可以同时 attach 同一个会话。 |
| Bypass-permission 模式 | 默认开启。Claude Code 以 `--dangerously-skip-permissions` 启动,因为容器才是安全边界,逐工具弹窗反而是干扰。可按会话关闭。 |
| Resume | 每个会话有 append-only `transcript.jsonl`。`POST /api/sessions { resume: <id> }` 即可带完整历史重启。 |
| 模型切换 | `POST /api/sessions/:id/model { model }`,运行中切换,例如 `claude-opus-4-7`、`claude-sonnet-4-6`、`claude-haiku-4-5-20251001`。 |
| 输入模拟 | `POST /api/sessions/:id/input` 直接往会话 stdin 写字节(或文本帧)。人输入和自动化共用同一原语。 |
| 断线/无头 | 会话不依赖客户端连接;重连带 `?from=<seq>` 即可补齐丢失帧。 |

### Claude 鉴权(每会话独立)

| 模式 | 适用场景 | 方式 |
|------|----------|------|
| Anthropic 订阅 | 已经付了 Claude.ai Pro / Max,想走订阅。 | Web UI 引导你在容器内完成标准 `claude login` 流程,凭证存放在挂载卷的 `~/.claude/`。 |
| API key | 程序化、CI、多用户、按 token 计费。 | UI 粘贴 `sk-ant-...` 或容器启动时给 `ANTHROPIC_API_KEY`。 |
| 混用 | 两个会话两种计费。 | 创建会话时各自声明鉴权模式。 |

### 结构化事件流

推流桥不是单纯转发终端字节,而是把 Claude Code 的生命周期解析成类型化帧,客户端无需屏幕抓取就能渲染。每帧都带 `session`、`seq`、`ts`。

| 帧类型 | 触发时机 | 载荷字段 |
|--------|----------|----------|
| `text.delta` | 助手文本流式输出 | `text` |
| `thinking` | 扩展思考块 | `text`(可选,受配置门控) |
| `tool.use.start` | 工具调用开始 | `tool`、`input` |
| `tool.use.result` | 工具调用返回 | `tool`、`output`、`error?`、`duration_ms` |
| `todo.update` | TodoWrite / TodoUpdate 触发 | `items: [{ id, subject, status, activeForm? }]` |
| `ask.question` | 模型要求用户选择 | `prompt`、`options[]`、`multiSelect` |
| `usage` | 一轮结束 | `input`、`output`、`cache_read`、`cache_write` |
| `status` | 会话状态变化 | `state`:`idle / working / waiting_for_input / stopped`,`elapsed_ms` |
| `stop` | 一轮或会话结束 | `reason` |
| `meta` | 模型/配置变化 | `model`、`workdir`、… |
| `hook` | 用户 hook 触发 | `name`、`event`、`payload`、`result?` |
| `pty.raw` | 可选:原始 PTY 字节 | `data`(默认关,终端类客户端可打开) |

客户端按需选择关心哪些帧:手机仪表盘可能只要 `todo.update` / `usage` / `status` / `stop`;终端模拟器要 `pty.raw`;看门狗只要 `status` 和 `stop`。

### Hooks

Hooks 是一等公民。用户脚本可在上表所列任意生命周期事件触发,可改写、阻断、注解。配置按"镜像级 (`/etc/claude-in-box/hooks.json`) → 用户级 (`~/.claude/hooks.json`) → 会话级"合并。

### Web API 与传输协议

每种能力都跨多种传输实现,让差异极大的客户端能共用同一后端。按设备选合适的。

| 传输 | 适用客户端 | 加密 | 鉴权 | 流式 | 状态 |
|------|------------|------|------|------|------|
| HTTPS / WSS | 浏览器、手机、服务器 | TLS | Bearer token | 是 (WSS) | 规划中,首期目标 |
| HTTP + AES 信封 | 没 TLS 栈的裸机 MCU (ESP32 / STM32) | AES-256-GCM 设备级密钥 | API key + 单请求 nonce | 仅请求/响应 | 规划中 |
| SSE | 廉价的单向客户端、日志拉取 | TLS 或 AES 信封 | Bearer / API key | 是(单向) | 规划中 |
| WebSocket(无 TLS,内网) | 受信内网客户端、开发 | 可选 AES 信封 | Bearer | 是 | 规划中 |
| MQTT 桥 | IoT 总线接入 | TLS 或预共享 | 按主题 | 是 | Roadmap |
| 原始 TCP 帧 | 最小占用极限场景 | AES-GCM | API key | 是 | Roadmap |

HTTPS 部署提供 [nginx 配置模板](deploy/nginx.conf.template):终止 TLS、反代 REST、升级 WebSocket、转发客户端 IP。

嵌入式 HTTP 传输提供一份[`docs/AES-TRANSPORT.md`](docs/AES-TRANSPORT.md)协议规范,固件开发者用任一 AES-GCM 库,几百行就能跑起来。

### 控制面鉴权

- 容器启动时通过 `CIB_AUTH_TOKEN` 注入主 API key,控制面没有它会拒绝启动(本地调试可强制 override)。
- 通过 API 签发设备级 token,各有 label、scope 集合、可选 TTL,可单独吊销。
- WebSocket 鉴权放在 `Sec-WebSocket-Protocol` 子协议头里传输,避免出现在 URL 日志中。
- OIDC 计划通过外置反向代理 (oauth2-proxy / authelia) 接入,控制面认 `X-Forwarded-User`。

### 网络:透明 SOCKS5

启动时给 `CIB_PROXY_URL=socks5://user:pass@host:port`,容器里所有出站 TCP(以及 `tun2socks` 支持的 UDP)统统走该上游代理。Claude API、`npm install`、`pip install`、`apt`、`git push` —— 全都自动走代理,且无需在任何工具里改配置。底层用 `redsocks` + `nftables`。

### 嵌入式支持

- 多架构镜像:`linux/amd64`、`linux/arm64`、`linux/arm/v7`。
- Headless 版本(`:latest-headless`)砍掉 Web UI bundle,镜像小约 140 MB,只暴露 REST 与 WebSocket API。
- 默认基底是 Debian-slim;headless 版本进一步不预装语言运行时,保持镜像紧凑。
- 每会话内存/CPU 计量通过 API 暴露,4 GB 的小盒子上也能看清"谁吃资源"。

## 状态

非常早期。目前仓库里有项目名、logo、架构草图、nginx 模板和 AES 信封协议规范。实现进行中。想跟进可以 Star/Watch。

详细设计见 [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)。

## 计划架构

```
                                                       ┌────────────────────────────────────────────┐
                                                       │            claude-in-box 容器               │
                                                       │                                            │
   浏览器 / 手机 / iPad     ── HTTPS / WSS ─────────▶  │  ┌────────────┐    ┌──────────────────┐    │
   服务器 / CI / agent      ── HTTPS ────────────────▶ │  │  控制面    │◀──▶│ session manager  │──┐ │
   ESP32 / STM32 / MCU      ── HTTP + AES 信封 ──────▶ │  │ (REST +    │    │ (PTY 驱动,bypass │  │ │
   看门狗 / 仪表盘          ── SSE ──────────────────▶ │  │  WS +      │    │  permission,可   │  │ │
   IoT 总线                 ── MQTT (规划中) ────────▶ │  │  SSE +     │    │  resume)         │  │ │
                                                       │  │  AES ep)   │    └──────────────────┘  │ │
                                                       │  │            │            ▲             ▼ │
                                                       │  │ + 鉴权层   │            │     ┌──────────┐
                                                       │  └────────────┘            └────▶│  hooks   │
                                                       │        ▲                          │ runtime  │
                                                       │        │  结构化帧                └──────────┘
                                                       │        │  text.delta / tool.use         │   │
                                                       │        │  todo.update / usage           │   │
                                                       │        │  status / stop / meta          │   │
                                                       │        │                                │   │
                                                       │        │              ┌─────────────────┴──┐│
                                                       │        └──────────────┤ 会话文件 +         ││
                                                       │                       │ transcript.jsonl   ││
                                                       │                       └────────────────────┘│
                                                       │                                            │
                                                       │   Claude Code  ◀── pty ──  会话 N          │
                                                       │   Claude Code  ◀── pty ──  会话 2          │
                                                       │   Claude Code  ◀── pty ──  会话 1          │
                                                       │                ▲                            │
                                                       │                │  Anthropic 订阅            │
                                                       │                │       或 API key           │
                                                       │     ┌──────────┴────────┐                  │
                                                       │     │ 透明 SOCKS5 网关  │  ◀── 可选        │
                                                       │     │ (redsocks + nft)  │      PROXY_URL   │
                                                       │     └───────────────────┘                  │
                                                       └────────────────────────────────────────────┘
```

## 快速上手

```bash
# 待发布,命令形态预览,容器尚未发布。
docker run -d --name claude-box \
  -p 8080:8080 \
  -e CIB_AUTH_TOKEN=$(openssl rand -hex 32) \
  -e CIB_PROXY_URL=socks5://user:pass@proxy.example:1080 \
  -v $(pwd)/workspace:/workspace \
  -v $(pwd)/sessions:/var/lib/claude-in-box/sessions \
  -v $(pwd)/claude-home:/home/coder/.claude \
  ghcr.io/jiangmuran/claude-in-box:latest

open http://localhost:8080
```

嵌入式宿主用 headless 版本:

```bash
docker run -d --restart unless-stopped \
  --memory=2g --cpus=2 \
  -p 8080:8080 \
  -e CIB_MODE=headless \
  -e CIB_AUTH_TOKEN=... \
  ghcr.io/jiangmuran/claude-in-box:latest-headless
```

走 HTTPS / nginx 反代:见 [`deploy/nginx.conf.template`](deploy/nginx.conf.template)。

在单片机上实现 AES 信封传输:见 [`docs/AES-TRANSPORT.md`](docs/AES-TRANSPORT.md)。

## Roadmap

- [ ] 基础 Docker 镜像(Debian-slim + Node + Python + Go + Claude Code),多架构
- [ ] Session manager:spawn / attach / detach / kill / resume,PTY 驱动,默认 bypass-permission
- [ ] 运行中切换模型
- [ ] Hooks runtime:镜像 / 用户 / 会话级合并
- [ ] 结构化事件流(见上表)
- [ ] Web API:bearer token、设备级 token、scope、OIDC(后续)
- [ ] WebSocket 与 SSE 流式输出,`?from=<seq>` 断点续传
- [ ] 面向嵌入式客户端的 HTTP + AES 信封传输
- [ ] MQTT 与原始 TCP 帧传输
- [ ] Web UI:终端面板,todo / 工具调用 / 用量 / 状态侧栏,模型选择器,hook 编辑器,移动端响应式
- [ ] 容器内订阅登录流
- [ ] 透明 SOCKS5(redsocks + nftables)
- [ ] Headless 版本,多架构 CI
- [ ] 容器重启后会话持久化
- [ ] 会话级资源计量
- [ ] 多用户 / 多租户

## 贡献

设计还在收敛,暂未开放外部贡献。如果有目标设备的限制需要考虑,欢迎开 issue 让我们提前对齐。

## 协议

[MIT](LICENSE)
