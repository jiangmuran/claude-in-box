<p align="center">
  <img src="assets/banner.png" alt="claude-in-box —— 把 Claude Code 装进盒子,跑在真服务器上,任何设备都能连过来用" width="800">
</p>

<p align="center">
  <a href="README.md">English</a> &middot; <strong>简体中文</strong>
</p>

<p align="center">
  <em>在一台真服务器上跑 Claude Code,通过一个 Web 端口被浏览器、手机、IoT 板、甚至单片机驱动。</em>
</p>

<p align="center">
  <a href="#%E7%8A%B6%E6%80%81"><img src="https://img.shields.io/badge/status-early%20WIP-orange" alt="状态:早期 WIP"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-D97757" alt="MIT 协议"></a>
  <img src="https://img.shields.io/badge/docker-multi--arch-2496ED?logo=docker&logoColor=white" alt="docker 多架构">
  <img src="https://img.shields.io/badge/arch-amd64%20%7C%20arm64-success" alt="amd64 / arm64">
</p>

---

## 这是什么

`claude-in-box` 把一整套按需开发环境 + [Claude Code](https://www.anthropic.com/claude-code) 打包进一个 Docker 容器,通过**一个端口**对外暴露为 Web 服务。

你把它跑在一台真服务器上(云主机、独立服务器、家用大机)。容器里的 Claude Code **必须以完整的 interactive REPL 模式运行**—— 这是硬约束,因为只有 interactive 模式才会消耗 Anthropic 订阅配额;`--print` / 无头调用只接受 API key。然后你通过网络从任何地方驱动这个 interactive Claude Code。

你能得到:

- 一个沙箱化的 Linux 盒子,直接装好一整套真实开发环境 —— Node 20、Python 3(含 FastAPI + Uvicorn + Pydantic + httpx + rich + ipython)、Go 1.25、Rust,以及 `nginx`、`redis-server`、`postgresql`、Docker CLI/daemon —— 加上 Claude Code 本体;
- 常用工具直接可用:ripgrep、fd、bat、htop、tmux、vim、nano、openssh-client、less、file、tree、jq、curl、wget、build-essential、make;
- 内置服务默认不自启;在 `docker run` 时传 `CIB_SERVICES=redis,postgres,nginx`(可以是 `redis`、`postgres`、`nginx`、`docker` 的任意子集),entrypoint 会先把它们起好再起控制面;
- 在虚拟 TTY 里运行的一个或多个会话,默认 bypass-permission 模式(容器本身就是边界,逐工具弹权限只会成为噪音);
- 一个 Web UI,对同一个会话提供**三种并行视图** —— 原生虚拟终端、网页化的结构化 Claude 驱动、面向开发者的 API 检视器;
- 结构化事件流:文本增量、工具调用、todo 更新、token 用量、状态变化、停止原因、模型元数据,全部以 JSON 帧通过 WebSocket / SSE 推出,**绝不靠刮 TTY**;
- 会话生命周期:新建、attach、resume、kill、运行中切模型;
- 每个会话独立选择计费方式:Anthropic 订阅(在自己电脑上 `claude setup-token` 拿到长 token),或者 API key;
- 同一个端口上多种封装的 Web API:我们自己的帧 schema(REST + WS + SSE + AES 信封),以及计划中的 **Anthropic 兼容**(`/v1/messages`)和 **OpenAI 兼容**(`/openai/v1/chat/completions`)适配器,让已有 SDK 把 base URL 指过来就能用;
- 透明 SOCKS5 层,容器内所有出站流量经一个上游代理重定向,无需逐工具配置;
- 可编程 hooks,在所有生命周期事件触发;
- 单一多架构镜像(`linux/amd64`、`linux/arm64`),在 x86 与 Ampere 类 arm64 主机上都能直接跑。

简单说:别再把 Claude Code 锁死在一台开发机上。让它跑在你早就有的真服务器上,然后从任何设备用合适的协议与 API 形态连过去。

## 理想工作流

```
1.  选一个开发环境镜像:预制的 :latest,或者基于它的自定义版本。
2.  做一个端口转发(默认 8080)。
3.  docker run —— 容器在 :8080 上拉起控制面,把 Web UI + REST + WS +
    SSE + AES 信封全部多路复用到同一个端口。
4.  打开 Web 面板,用启动时生成的主 API key 登入。
5.  选择本次会话怎么计费:Anthropic 订阅(粘贴你在自己电脑上
    `claude setup-token` 拿到的长 OAuth token),或者 Anthropic API key。
    每个会话各自独立选择。
6.  仪表盘可见:活跃会话、token 消耗、累计工作时间、当前模型、hook 活动。
7.  新建会话。面板对同一会话提供三种并行视图:
       (a) 原生虚拟终端 —— xterm.js 绑定到 Claude Code 的 PTY,就是
           你在 iTerm 里看到的 CC TUI;
       (b) 网页化 Claude 驱动 —— chat 风格 transcript + todo 侧栏 +
           工具调用时间线 + token 仪表 + 模型选择器;
       (c) API 检视器 —— 所有帧、所有 API 请求/响应,devtools 风格。
    可以切换、也可以并排。
8.  与 Claude 对话;中途随时切模型;todo、工具调用、状态从类型化事件流
    实时渲染到结构化视图里,而不是刮终端。
9.  手机、平板、嵌入式 MCU 或别的 agent 都能连同一个会话,各自挑合适的
    传输 + API 形态 —— 浏览器走 REST/WS,ESP32 走 AES 信封,现成 SDK 走
    Anthropic / OpenAI 兼容 HTTP(规划中)。
```

这就是 README 后面这些章节要讲清楚的整个闭环。

## 能力

### 会话与 Claude Code

| 能力 | 说明 |
|------|------|
| 多会话 | PTY 驱动;spawn / attach / detach / kill / list。多个客户端可以同时 attach 同一个会话。 |
| 只跑 interactive REPL | Claude Code 永远以完整的 interactive 模式运行,**不会**用 `--print`。这是订阅配额计费的硬要求,也是结构化事件流(通过 hooks)的前提。 |
| Bypass-permission 模式 | 默认开启。Claude Code 以 `--dangerously-skip-permissions` 启动,因为容器才是安全边界,逐工具弹窗反而是干扰。可按会话关闭;hook `PermissionRequest` 事件仍会触发,可以二次拦截。 |
| Resume | 会话是 CC 自己的 —— transcript 在 `~/.claude/projects/<hash>/<session>.jsonl`。`POST /api/sessions { resume: <id> }` 用 `--resume` 重启即可。 |
| 模型切换 | `POST /api/sessions/:id/model { model }`,运行中往 PTY 注入 `/model <name>`,并 emit 一个 `meta` 帧。 |
| 输入模拟 | `POST /api/sessions/:id/input` 直接往会话 stdin 写字节(或文本帧)。人输入和自动化共用同一原语。 |
| 断线/无头 | 会话不依赖客户端连接;重连带 `?from=<seq>` 即可补齐丢失帧。 |

### Claude 鉴权(每会话独立)

| 模式 | 适用场景 | 方式 |
|------|----------|------|
| Anthropic 订阅(个人用户默认) | 已经付了 Claude Pro / Max,想走订阅。 | 在你自己电脑上跑 `claude setup-token` 拿到长 OAuth token;通过 `CLAUDE_CODE_OAUTH_TOKEN` env 或者 API 按会话注入容器。 |
| API key | 程序化、CI、按 token 计费,或者一台 box 给多个人各自带 key。 | 容器上设 `ANTHROPIC_API_KEY`,或按会话注入。 |
| 容器内 `claude /login` 交互登录 | 给不想折腾 `claude setup-token` 的用户。 | Roadmap (M3)。Web UI 会驱动一个 PTY 化的 `claude /login` + OAuth 回调流。 |

订阅计费能跑通,前提是 CC 在容器里保持 interactive REPL —— 见上一张表的对应条目。

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

Hooks 是一等公民。控制面在每个会话启动时安装自己的 `http` 类型 hook(HMAC 签名,指向内部路由),从而权威地捕获每个生命周期事件。用户 hook 在此之上合并:镜像级(`/etc/claude-in-box/hooks.json`)、用户级(`~/.claude/hooks.json`)、会话级声明。Hook 可改写、阻断、注入上下文或标注;结果以 `hook` 帧落到帧总线。

### Web API:一个端口,多种封装

容器只暴露一个 TCP 端口。所有能力通过 HTTP 路由多路复用到这一个端口上。同一能力还提供多种封装,让差异极大的客户端能各自挑顺眼的形态。

| 封装 | 路径前缀 | 适用客户端 | 加密 | 鉴权 | 状态 |
|------|----------|------------|------|------|------|
| 原生帧 REST + WS | `/api/*`、`/ws/*` | 浏览器、手机、服务器、我们自己的 Web UI | TLS(nginx 终止) | Bearer token(主/设备级) | M1 |
| 原生帧 SSE | `/sse/*` | 廉价单向客户端、日志拉取 | TLS(nginx 终止) | Bearer | M1 |
| HTTP + AES 信封 | `/aes/*` | 没 TLS 栈的裸机 MCU(ESP32 / STM32) | AES-256-GCM 设备级密钥 | API key + 单请求 nonce | M1 |
| Anthropic 兼容 API | `/v1/messages`、`/v1/messages?stream=true` | 已有 Claude SDK 客户端 —— base URL 指过来就能用 | TLS(nginx 终止) | Bearer / API key | M3(规划) |
| OpenAI 兼容 API | `/openai/v1/chat/completions` | 已有 OpenAI SDK 客户端 | TLS(nginx 终止) | Bearer / API key | M3(规划) |
| MQTT 桥 | — | IoT 总线接入 | TLS 或预共享 | 按主题 | Roadmap |
| 原始 TCP 帧 | — | 最小占用极限场景 | AES-GCM | API key | Roadmap |

Anthropic / OpenAI 兼容适配器是**同一会话总线上的格式适配器**,而不是平行的运行时。它们让已经会说这些 API 的工具把请求路过来,直接用上订阅配额支撑的 Claude。

HTTPS 部署提供 [nginx 配置模板](deploy/nginx.conf.template):终止 TLS、反代 REST、升级 WebSocket、放行 SSE 长连、转发客户端 IP。

嵌入式 HTTP 传输提供一份 [`docs/AES-TRANSPORT.md`](docs/AES-TRANSPORT.md) 协议规范,固件开发者用任一 AES-GCM 库,几百行就能跑起来。

### 控制面鉴权

- 容器启动时通过 `CIB_AUTH_TOKEN` 注入主 API key,控制面没有它会拒绝启动(本地调试可强制 override)。
- 通过 API 签发设备级 token,各有 label、scope 集合、可选 TTL,可单独吊销。
- WebSocket 鉴权放在 `Sec-WebSocket-Protocol` 子协议头里传输,避免出现在 URL 日志中。
- OIDC 计划通过外置反向代理 (oauth2-proxy / authelia) 接入,控制面认 `X-Forwarded-User`。

### 网络:透明 SOCKS5

启动时给 `CIB_PROXY_URL=socks5://user:pass@host:port`,容器里所有出站 TCP(以及 `tun2socks` 支持的 UDP)统统走该上游代理。Claude API、`npm install`、`pip install`、`apt`、`git push` —— 全都自动走代理,且无需在任何工具里改配置。底层用 `redsocks` + `nftables`。

### 嵌入式客户端(不是服务端)

服务端这边**不**针对嵌入式宿主缩水 —— interactive Claude Code 跑订阅配额需要一台真机器。真正"嵌入式友好"的是**客户端**这一头:

- AES 信封 HTTP 传输让一台只有 HTTP client + AES-GCM 实现的 ESP32 / STM32 / RP2040 也能当一等公民:给会话发输入、轮询结构化帧、对 todo/stop 做出反应。
- 参考 C 客户端放在 [`clients/c/`](clients/)(mbedtls,~300 LOC),旁边附带一个 ESP-IDF 例子。
- Rust / Python 参考客户端在 M3 跟上。

## 状态

非常早期。目前仓库里有项目名、logo、架构草图、nginx 模板和 AES 信封协议规范。实现按下方里程碑推进。想跟进可以 Star/Watch。

详细设计见 [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)。

## 计划架构

```
                                                       ┌────────────────────────────────────────────┐
                                                       │            claude-in-box 容器               │
                                                       │            (只跑在真服务器上)               │
   浏览器 / 手机 / iPad     ── /api  /ws  /sse  ───▶   │  ┌────────────┐    ┌──────────────────┐    │
   服务器 / CI / agent      ── /api  /ws  /sse  ───▶   │  │  控制面    │◀──▶│ session manager  │──┐ │
   已有 Claude SDK          ── /v1/messages*    ───▶   │  │ (单端口    │    │   (PTY 驱动,     │  │ │
   已有 OpenAI SDK          ── /openai/v1/chat* ───▶   │  │  :8080,    │    │   interactive,   │  │ │
   ESP32 / STM32 / MCU      ── /aes/...         ───▶   │  │   多形态   │    │   bypass-perm,   │  │ │
   看门狗 / 仪表盘          ── /sse             ───▶   │  │   封装)    │    │   可 resume)     │  │ │
                                                       │  │ + 鉴权层   │    └──────────────────┘  │ │
                                                       │  └────────────┘            ▲             ▼ │
                                                       │        ▲                   │     ┌──────────┐
                                                       │        │                   └────▶│  hooks   │
                                                       │        │  结构化帧               │ runtime  │
                                                       │        │  text.delta / tool.use  └──────────┘
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
                                                       │                │  (OAuth 长 token)          │
                                                       │                │     或 API key             │
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
  --cap-add NET_ADMIN \
  -e CIB_AUTH_TOKEN=$(openssl rand -hex 32) \
  -e CLAUDE_CODE_OAUTH_TOKEN=cclo_...      # 在你电脑上 `claude setup-token` 拿到
  -e CIB_PROXY_URL=socks5://user:pass@proxy.example:1080 \
  -e CIB_SERVICES=redis,postgres \         # 自启内置服务
  -v $(pwd)/workspace:/workspace \
  -v $(pwd)/sessions:/var/lib/claude-in-box/sessions \
  -v $(pwd)/claude-home:/home/coder/.claude \
  -v /var/run/docker.sock:/var/run/docker.sock \   # 可选:与宿主 Docker 通信
  ghcr.io/jiangmuran/claude-in-box:latest

open http://localhost:8080
```

API-only 模式(不在 `/` 提供 Web UI,只暴露 `/api/*` `/ws/*` `/sse/*` `/aes/*`)—— 同一个镜像,只是一个 runtime 开关:

```bash
docker run -d --restart unless-stopped \
  -p 8080:8080 \
  -e CIB_MODE=headless \
  -e CIB_AUTH_TOKEN=... \
  ghcr.io/jiangmuran/claude-in-box:latest
```

走 HTTPS / nginx 反代:见 [`deploy/nginx.conf.template`](deploy/nginx.conf.template)。

在单片机上实现 AES 信封传输:见 [`docs/AES-TRANSPORT.md`](docs/AES-TRANSPORT.md)。

## Roadmap

**M1 —— 完整无 UI 后端:**

- [ ] 基础 Docker 镜像(Debian-slim + Node + Python + Go + Rust + claude-code),单 tag,多架构(amd64 + arm64)
- [ ] Session manager:spawn / attach / detach / kill / resume,PTY 驱动,默认 bypass-permission,CC 仅 interactive
- [ ] 运行中切换模型(`/model`)
- [ ] Hooks runtime:镜像 / 用户 / 会话级合并,控制面 http hook 按会话注入
- [ ] 结构化事件流(见上表)
- [ ] Web API:bearer token、设备级 token、scope
- [ ] REST + WS + SSE 同一端口多路复用,`?from=<seq>` 断点续传
- [ ] 嵌入式客户端用的 AES 信封 HTTP 传输(`/aes/*`)
- [ ] 透明 SOCKS5(redsocks + nftables)
- [ ] `CIB_MODE=headless` runtime 开关,只暴露 API
- [ ] 多架构 CI 推送到 GHCR
- [ ] C 参考客户端 + ESP32-IDF 示例

**M2 —— Web UI:**

- [ ] 同会话三视图并行(原生终端 / 网页 Claude 驱动 / API 检视器)
- [ ] 会话侧栏、模型选择器、hook 编辑器、MCP server 配置 CRUD
- [ ] 移动端响应式

**M3 —— 格式适配器与鉴权延伸:**

- [ ] Anthropic 兼容 API(`/v1/messages`、流式)
- [ ] OpenAI 兼容 API(`/openai/v1/chat/completions`)
- [ ] 容器内交互式 `claude /login` OAuth 流
- [ ] 多用户 `~/.claude` 隔离
- [ ] OIDC(反代头信任)
- [ ] MQTT 桥、原始 TCP 帧传输
- [ ] AES 信封的 Rust / Python 参考客户端

## 贡献

设计还在收敛,暂未开放外部贡献。如果有目标客户端设备的限制需要考虑,欢迎开 issue 让我们提前对齐。

## 协议

[MIT](LICENSE)
